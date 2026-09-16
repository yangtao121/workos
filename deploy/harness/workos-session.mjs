// Versioned WorkOS transport for the pinned official Harness. This plugin
// only adapts lifecycle, framing and execution policy. agents.create/resume,
// followup, tools, history reconstruction and persistence remain upstream.
export const name = "workos-session";
export const inject = ["agents", "sessionPersistence", "llm", "userQuestions"];
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
  let usageObserved = false;
  let bytes = "";
  let requests = Promise.resolve();
  let notifications = Promise.resolve();
  const usage = new Map();
  const toolCalls = new Map();
  let nextToolID = -1;
  globalThis.__workosCall = (operation, args) =>
    new Promise((resolve, reject) => {
      if (!initialized || closing || toolCalls.size >= 64)
        return reject(new Error("Tool bridge unavailable"));
      const id = nextToolID--;
      toolCalls.set(id, { resolve, reject });
      send({ jsonrpc: "2.0", id, method: "workos/tool", params: { operation, arguments: args } });
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
    if (!active || String(session.id) !== sessionID) return;
    const data = event.data;
    const value =
      event.type === "assistant/chunk" && data.chunk.type === "usage"
        ? data.chunk.usage
        : event.type === "assistant/message"
          ? data.usage
          : undefined;
    if (value) {
      usageObserved = true;
      const output = value.outputTokens;
      if (!Number.isSafeInteger(output) || output < 0) throw new Error("Invalid model usage");
      const key = `${data.turn}:${data.step}`;
      const previous = usage.get(key) ?? 0;
      if (output < previous) throw new Error("Model usage regressed");
      remaining -= output - previous;
      usage.set(key, output);
      if (remaining < 0) throw new Error("Model exceeded execution output budget");
    }
    if (event.type === "assistant/message" && !usageObserved)
      throw new Error("Model usage unavailable");
    enqueueNotification(() => notify("session.event", { sessionId: sessionID, event }));
  });
  ctx.on("agent/request", async (_request, next) => {
    if (!active || closing || remaining <= 0) throw new Error("Execution output budget exhausted");
    if (requested && !usageObserved)
      throw new Error("Previous request usage unavailable; retry refused");
    requested = true;
    usageObserved = false;
    const config = await next();
    return { ...config, maxTokens: Math.min(config.maxTokens ?? remaining, remaining) };
  });
  ctx.on("agent/status", ({ agent, status }) => {
    if (!active || String(agent.session.id) !== sessionID) return;
    enqueueNotification(async () => {
      if (status === "idle") {
        // A live, balanced load flushes the native log before completion can
        // reach Core. A failed flush must not produce a successful turn.
        await ctx.sessionPersistence.load(sessionID);
        active = false;
      }
      notify("session.status", { sessionId: sessionID, status });
    });
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
      usageObserved = false;
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
      handle.agent.followup(message);
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
