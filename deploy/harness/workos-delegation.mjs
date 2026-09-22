import { AsyncLocalStorage } from "node:async_hooks";

// Scope and resource policy around the official in-process provider. Native
// agents, inboxes, model loops, child sessions and settlement remain upstream.
export function attachDelegation(ctx, rootSession, enabled, notify, flush) {
  const scope = new AsyncLocalStorage();
  const agents = new WeakMap();
  const sessions = new Map();
  let running = 0;
  const withAgent = (agent, next) => scope.run(agents.get(agent) || { delegationId: "" }, next);
  ctx.on("agent/created", ({ agent }) => {
    const current = scope.getStore();
    if (!current?.delegationId) return;
    agents.set(agent, current);
    sessions.set(String(agent.session.id), current.delegationId);
    notify("session.delegation", {
      sessionId: String(agent.session.id),
      delegationId: current.delegationId,
    });
  });
  ctx.subagents.registerProvider({
    name: "workos-spawn",
    capabilities: { outputSchema: true, depthLimit: true, toolFilter: true, persona: true },
    inheritsParentContext: false,
    async start(request) {
      if (
        !enabled() ||
        String(request.parent.session.id) !== rootSession() ||
        agents.has(request.parent) ||
        running >= 2
      )
        throw new Error("Delegation limit reached");
      const native = ctx.subagents.getProvider("workos-native-spawn");
      if (!native || request.signal.aborted) throw new Error("Native delegation unavailable");
      running++;
      let child;
      let run;
      let released = false;
      const release = () => {
        if (!released) {
          released = true;
          running--;
        }
      };
      try {
        child = await globalThis.__workosCall("delegation.acquire", {
          requestKey: crypto.randomUUID(),
          title: (request.label || "Delegated task").slice(0, 128),
        });
        if (request.signal.aborted) throw new Error("Delegation cancelled");
        run = await scope.run({ delegationId: child.delegationId }, () =>
          native.start({ ...request, maxDepth: 1 }),
        );
        const result = run.result
          .then(async (result) => {
            const state =
              result.stopReason === "completed"
                ? "completed"
                : result.stopReason === "aborted"
                  ? "cancelled"
                  : "failed";
            const summary = result.output
              .filter((block) => block.type === "text")
              .map((block) => block.text)
              .join("")
              .slice(0, 512);
            await flush();
            await scope.run({ delegationId: child.delegationId }, () =>
              globalThis.__workosCall("delegation.finish", { state, summary }),
            );
            return result;
          })
          .finally(release);
        return {
          id: run.id,
          localAgent: run.localAgent,
          result,
          async dispose() {
            try {
              await run.dispose();
              await result;
            } finally {
              release();
            }
          },
        };
      } catch (error) {
        if (run) await run.dispose();
        if (child) {
          try {
            await scope.run({ delegationId: child.delegationId }, () =>
              globalThis.__workosCall("delegation.finish", {
                state: "failed",
                summary: "Native child did not start.",
              }),
            );
          } catch {
            /* Core retains the durable grant for interruption review. */
          }
        }
        release();
        throw error;
      }
    },
  });
  return {
    withAgent,
    current: () => scope.getStore()?.delegationId || "",
    session: (id) => sessions.get(String(id)),
    hasChildren: () => running !== 0,
  };
}
