// Versioned WorkOS transport for the pinned official Harness. This plugin
// only adapts lifecycle, framing and execution policy. agents.create/resume,
// followup, tools, history reconstruction and persistence remain upstream.
import { attachDelegation } from "./workos-delegation.mjs";

export const name = "workos-session";
export const inject = [
  "agents",
  "sessionPersistence",
  "llm",
  "userQuestions",
  "goals",
  "subagents",
];
const FRAME_LIMIT = 1024 * 1024;
const OUTPUT_LIMIT = 4 * FRAME_LIMIT;

function frozen(value) {
  if (value && typeof value === "object") {
    for (const child of Object.values(value)) frozen(child);
    Object.freeze(value);
  }
  return value;
}

export function apply(ctx) {
  let initialized;
  let handle;
  let sessionID;
  let active = false;
  let closing = false;
  let remaining = 0;
  let requested = false;
  const reservations = new Map();
  let budgetUncertain = false;
  let bytes = "";
  let requests = Promise.resolve();
  let notifications = Promise.resolve();
  const usage = new Map();
  const toolCalls = new Map();
  let nextToolID = -1;
  const delegation = attachDelegation(
    ctx,
    () => sessionID,
    () => active && !closing,
    (method, params) => enqueueNotification(() => notify(method, params)),
    () => notifications,
  );
  globalThis.__workosCall = (operation, args) =>
    new Promise((resolve, reject) => {
      if (!initialized || closing || toolCalls.size >= 64)
        return reject(new Error("Tool bridge unavailable"));
      const id = nextToolID--;
      toolCalls.set(id, { resolve, reject });
      send({
        jsonrpc: "2.0",
        id,
        method: "workos/tool",
        params: { operation, arguments: args, delegationId: delegation.current() },
      });
    });

  ctx.userQuestions.registerProvider({
    async ask(request) {
      if (
        !active ||
        !request.agent ||
        String(request.agent.session.id) !== sessionID ||
        request.signal?.aborted
      )
        throw new Error("User question unavailable");
      const result = await globalThis.__workosCall("interaction.ask", {
        requestKey: crypto.randomUUID(),
        questions: request.questions.map((question) => ({
          id: question.id,
          text: question.question,
          detail: question.detail || "",
          choices: (question.options || []).map((option) => ({
            label: option.label,
            description: option.description || "",
          })),
          multiple: !!question.multiSelect,
        })),
      });
      if (request.signal?.aborted || result.state !== "answered")
        throw new Error("User question rejected or expired");
      return {
        answers: result.answers.map((answer) => ({
          id: answer.questionId,
          selected: answer.selected || [],
          ...(answer.text ? { custom: answer.text } : {}),
        })),
      };
    },
  });
  // Docker does not implement wider sandbox grants. The official approval
  // seam receives a truthful unavailable answer; a human cannot bypass it.
  ctx.on("approval/request", () => Promise.resolve("unavailable"));

  function send(value) {
    const line = JSON.stringify(value) + "\n";
    if (Buffer.byteLength(line) > FRAME_LIMIT || process.stdout.writableLength > OUTPUT_LIMIT) {
      throw new Error("WorkOS session transport output limit");
    }
    process.stdout.write(line);
  }
  const notify = (method, params) => send({ jsonrpc: "2.0", method, params });
  function fatal() {
    // Never print a vendor error: it may contain a prompt or credential.
    process.stderr.write("WorkOS session bridge failed\n");
    process.exit(1);
  }
  function enqueueNotification(fn) {
    notifications = notifications.then(fn);
    notifications.catch(fatal);
  }

  ctx.on("session/event", (session, event) => {
    if (!active || (String(session.id) !== sessionID && !delegation.session(session.id))) return;
    const data = event.data;
    const value =
      event.type === "assistant/chunk" && data.chunk.type === "usage"
        ? data.chunk.usage
        : event.type === "assistant/message"
          ? data.usage
          : undefined;
    if (value) {
      const output = value.outputTokens;
      if (!Number.isSafeInteger(output) || output < 0) throw new Error("Invalid model usage");
      const key = `${session.id}:${data.turn}:${data.step}`;
      const previous = usage.get(key) ?? 0;
      if (output < previous) throw new Error("Model usage regressed");
      const reservation = reservations.get(key);
      if (!reservation && output !== previous) throw new Error("Unreserved model usage");
      if (reservation) {
        if (output > reservation.limit) throw new Error("Model exceeded reserved budget");
        reservation.used = output;
        reservation.observed = true;
      }
      remaining -= output - previous;
      usage.set(key, output);
      if (remaining < 0) throw new Error("Model exceeded execution output budget");
    }
    if (event.type === "assistant/message") {
      const key = `${session.id}:${data.turn}:${data.step}`;
      if (!reservations.get(key)?.observed) {
        budgetUncertain = true;
        throw new Error("Model usage unavailable");
      }
      reservations.delete(key);
    }
    if (
      event.type === "turn/end" &&
      [...reservations.keys()].some((key) => key.startsWith(String(session.id) + ":"))
    )
      budgetUncertain = true;
    enqueueNotification(() => notify("session.event", { sessionId: String(session.id), event }));
    if (event.type === "user/message" && String(session.id) === sessionID)
      publishGoal(handle?.agent);
  });
  ctx.on("agent/request", (request, next) =>
    delegation.withAgent(request.agent, async () => {
      if (!active || closing || budgetUncertain || remaining <= 0)
        throw new Error("Execution output budget exhausted or uncertain");
      const id = String(request.agent.session.id);
      if (id !== sessionID && !delegation.session(id)) throw new Error("Unknown agent scope");
      if ([...reservations.keys()].some((key) => key.startsWith(id + ":")))
        throw new Error("Previous request usage unavailable; retry refused");
      const available =
        remaining -
        [...reservations.values()].reduce((total, item) => total + item.limit - item.used, 0);
      if (available < 1) throw new Error("Execution output budget reserved");
      const limit =
        id === sessionID
          ? available
          : Math.max(1, Math.floor(available / (reservations.size === 0 ? 2 : 1)));
      const key = `${id}:${request.turn}:${request.step}`;
      const reservation = { limit, used: 0, observed: false };
      reservations.set(key, reservation);
      requested = true;
      // Keep an unobserved reservation on failure. Retrying an ambiguous paid
      // request cannot recover or double-spend its reserved output allowance.
      const config = await next();
      reservation.limit = Math.min(config.maxTokens ?? limit, limit);
      return { ...config, maxTokens: reservation.limit };
    }),
  );

  function publishGoal(agent) {
    if (!active || !agent || String(agent.session.id) !== sessionID) return;
    const goal = ctx.goals.get(agent);
    if (!goal) return;
    if (goal.maxGoalRounds > 32) throw new Error("Goal round limit exceeded");
    const view = {
      ref: goal.id,
      revision: String(goal.revision),
      objective: goal.objective,
      phase: goal.phase,
      roundsStarted: goal.roundsStarted,
      maxRounds: goal.maxGoalRounds,
      blockedReason: goal.blockedReason?.message || "",
      armed: goal.activation === "armed",
    };
    enqueueNotification(() => notify("session.goal", { sessionId: sessionID, goal: view }));
  }
  async function settle(agent) {
    if (!active || agent.status !== "idle" || delegation.hasChildren()) return;
    const goal = ctx.goals.get(agent);
    if (goal?.phase === "active" && goal.activation === "armed") return;
    // Loading balances a native persistence flush. Recheck after the await:
    // the upstream driver or a wrap-up tool may have queued a new turn.
    await ctx.sessionPersistence.load(sessionID);
    if (!active || agent.status !== "idle" || delegation.hasChildren()) return;
    const latest = ctx.goals.get(agent);
    if (latest?.phase === "active" && latest.activation === "armed") return;
    publishGoal(agent);
    enqueueNotification(() =>
      notify(requested ? "session.status" : "session.controlComplete", {
        sessionId: sessionID,
        status: "idle",
      }),
    );
    active = false;
  }
  ctx.on("goal/changed", ({ agent }) => {
    publishGoal(agent);
    if (active && String(agent.session.id) === sessionID) settle(agent).catch(fatal);
  });
  ctx.on("agent/pre-step", ({ agent }, next) =>
    delegation.withAgent(agent, async () => {
      if (
        active &&
        String(agent.session.id) === sessionID &&
        ctx.goals.get(agent)?.phase === "active"
      ) {
        const control = await globalThis.__workosCall("session.control", {});
        const goal = ctx.goals.get(agent);
        if (goal && control.pauseGoalRef === goal.id) {
          ctx.goals.pause(agent, { id: goal.id, revision: goal.revision });
          return { kind: "reject" };
        }
      }
      return next();
    }),
  );
  ctx.on("tools/execute", (execution, next) =>
    delegation.withAgent(execution.agent, async () => {
      if (
        (execution.name === "create_goal" || execution.name === "update_goal") &&
        execution.arguments?.max_goal_rounds !== undefined &&
        (!Number.isSafeInteger(execution.arguments.max_goal_rounds) ||
          execution.arguments.max_goal_rounds > 32)
      )
        throw new Error("Goal round limit must not exceed 32");
      return next();
    }),
  );
  ctx.on("agent/status", ({ agent, status }) => {
    if (!active || String(agent.session.id) !== sessionID) return;
    if (status === "idle") settle(agent).catch(fatal);
    else enqueueNotification(() => notify("session.status", { sessionId: sessionID, status }));
  });

  async function dispatch(method, params) {
    if (method === "initialize") {
      if (initialized || !params || typeof params.cwd !== "string" || !params.cwd.startsWith("/")) {
        throw new Error("Invalid session initialization");
      }
      await ctx.get("loader")?.await();
      initialized = { cwd: params.cwd, provider: params.provider, model: params.model };
      return { serverInfo: { name: "workos-deepseek-session", version: "1" } };
    }
    if (!initialized || closing) throw new Error("Session is not initialized");
    if (method === "session/prompt") {
      if (
        active ||
        !params ||
        typeof params.sessionId !== "string" ||
        !/^[a-zA-Z0-9_-]{1,128}$/.test(params.sessionId) ||
        !Number.isSafeInteger(params.maxTokens) ||
        params.maxTokens < 1 ||
        params.maxTokens > 384000 ||
        !Array.isArray(params.contentBlocks) ||
        params.contentBlocks.length !== 1 ||
        params.contentBlocks[0]?.type !== "text" ||
        typeof params.contentBlocks[0].text !== "string"
      ) {
        throw new Error("Invalid session turn");
      }
      if (sessionID && sessionID !== params.sessionId) throw new Error("Session identity mismatch");
      sessionID = params.sessionId;
      remaining = params.maxTokens;
      usage.clear();
      requested = false;
      reservations.clear();
      budgetUncertain = false;
      if (!handle) {
        const records = await ctx.sessionPersistence.list();
        const exists = records.some((record) => String(record.id) === sessionID);
        const agentOptions = {
          provider: initialized.provider,
          model: initialized.model,
          maxTokens: remaining,
        };
        handle = exists
          ? await ctx.agents.resume({ resumeSessionId: sessionID, agentOptions })
          : await ctx.agents.create({
              sessionId: sessionID,
              meta: { cwd: initialized.cwd },
              agentOptions,
            });
        if (handle.agent.session.header.cwd !== initialized.cwd)
          throw new Error("Session workspace changed");
      }
      active = true;
      const message = frozen({
        id: params.messageId,
        role: "user",
        content: params.contentBlocks,
        source: { kind: "user" },
      });
      if (typeof message.id !== "string" || !message.id || message.id.length > 128)
        throw new Error("Invalid message identity");
      const directive = params.directive;
      if (directive) {
        const ref = {
          id: directive.goal_ref || directive.goalRef,
          revision: Number(directive.expected_revision || directive.expectedRevision),
        };
        // Go's adapter carries the canonical enum as its numeric wire value.
        if (directive.kind === 1) {
          const rounds = directive.max_rounds || directive.maxRounds;
          if (!Number.isSafeInteger(rounds) || rounds < 1 || rounds > 32)
            throw new Error("Invalid goal round limit");
          ctx.goals.create(handle.agent, { objective: directive.objective, maxGoalRounds: rounds });
        } else if (directive.kind === 2) ctx.goals.resume(handle.agent, ref);
        else if (directive.kind === 3) {
          ctx.goals.pause(handle.agent, ref);
          await settle(handle.agent);
        } else throw new Error("Invalid goal directive");
      } else {
        publishGoal(handle.agent);
        handle.agent.followup(message);
      }
      return { messageId: message.id };
    }
    if (method === "shutdown") {
      closing = true;
      if (handle) await handle.dispose();
      await notifications;
      return {};
    }
    throw new Error("Unsupported session operation");
  }

  async function receive(line) {
    let request;
    try {
      request = JSON.parse(line);
      if (Number.isSafeInteger(request.id) && request.id < 0 && !request.method) {
        const pending = toolCalls.get(request.id);
        if (!pending) throw new Error("Unknown tool response");
        toolCalls.delete(request.id);
        if (request.error) pending.reject(new Error("WorkOS operation failed"));
        else pending.resolve(request.result);
        return;
      }
      if (
        request.jsonrpc !== "2.0" ||
        !Number.isSafeInteger(request.id) ||
        typeof request.method !== "string"
      ) {
        throw new Error("Invalid session frame");
      }
      const result = await dispatch(request.method, request.params);
      send({ jsonrpc: "2.0", id: request.id, result });
      if (request.method === "shutdown") {
        process.stdin.pause();
        await ctx.root.fiber.dispose();
        process.stdout.write("", () => process.exit(0));
      }
    } catch {
      if (!Number.isSafeInteger(request?.id)) return fatal();
      send({
        jsonrpc: "2.0",
        id: request.id,
        error: { code: -32000, message: "Session operation failed" },
      });
    }
  }
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", (chunk) => {
    bytes += chunk;
    let end;
    while ((end = bytes.indexOf("\n")) >= 0) {
      const line = bytes.slice(0, end);
      bytes = bytes.slice(end + 1);
      if (Buffer.byteLength(line) > FRAME_LIMIT) return fatal();
      // Backend responses must bypass the command queue: create/resume may
      // itself await filesystem reads before session/prompt can return.
      let frame;
      try {
        frame = JSON.parse(line);
      } catch {
        return fatal();
      }
      if (Number.isSafeInteger(frame.id) && frame.id < 0 && !frame.method) {
        receive(line).catch(fatal);
        continue;
      }
      requests = requests.then(() => receive(line));
      requests.catch(fatal);
    }
    if (Buffer.byteLength(bytes) > FRAME_LIMIT) fatal();
  });
  process.stdin.on("end", () => {
    requests
      .then(async () => {
        if (handle) await handle.dispose();
        await ctx.root.fiber.dispose();
        process.exit(0);
      })
      .catch(fatal);
  });
}
