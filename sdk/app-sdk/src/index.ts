// The iframe-side WorkOS App SDK. A sandboxed web bundle app calls this at
// startup to receive its App Bridge over a versioned MessageChannel
// handshake from the trusted parent. The SDK only ever talks to
// `window.parent` through the transferred port; it never sees credentials,
// WorkOS cookies, Connect clients, or the network — the port itself is the
// capability handle, and every request is re-authorized server-side.
import {
  APP_BRIDGE_VERSION,
  BridgeProtocolError,
  MAX_INFLIGHT_REQUESTS,
  REQUEST_TIMEOUT_MS,
  isParentEnvelope,
  postBridgeMessage,
  type BridgeMethod,
  type BridgeRunPayload,
  type BridgeKnowledgeSearchPayload,
  type BridgeKnowledgeSearchResult,
  type BridgeRunResult,
  type BridgeStreamPayload,
} from "@workos/surface-sdk";
import type { AgentEvent } from "@workos/protocol";

/**
 * The canonical capability vocabulary. These names mirror the registry
 * manifest vocabulary exactly; a manifest asking for anything else is
 * rejected at registration, and only `agent.task.run`/`agent.event.watch`
 * have bridge executors today.
 */
export type Capability =
  | "agent.task.run"
  | "agent.event.watch"
  | "artifact.read"
  | "artifact.write"
  | "knowledge.read"
  | "project.read";

export interface AppAgentRunInput {
  idempotencyKey: string;
  role?: string | undefined;
  goal: string;
}

export interface AppAgentRunResult {
  taskId: string;
  state: string;
  lastEventSequence: string;
}

export interface WorkOSAppBridge {
  agent: {
    /** Runs one project-scoped agent task (requires agent.task.run). */
    run(input: AppAgentRunInput): Promise<AppAgentRunResult>;
    /**
     * Streams one task's persisted canonical events from afterSequence
     * (requires agent.event.watch). Ending the iteration early cancels only
     * the local/server stream — the durable task keeps running.
     */
    stream(taskId: string, afterSequence?: string): AsyncIterable<AgentEvent>;
  };
  knowledge: {
    /**
     * One bounded, read-only lexical search over THIS app's project knowledge
     * (requires knowledge.read). The payload can never carry owner, project,
     * or source scope: the runtime derives both from the validated surface
     * session and re-verifies the exact grant revision with Core on every
     * call (ADR-0013). Excerpts are bounded plain text — render inert.
     */
    search(input: AppKnowledgeSearchInput): Promise<AppKnowledgeSearchResult>;
  };
}

export interface AppKnowledgeSearchInput {
  query: string;
  pageSize?: number | undefined;
  pageToken?: string | undefined;
}

export interface AppKnowledgeHit {
  artifactId: string;
  digest: string;
  artifactType: string;
  title: string;
  excerpt: string;
  score: number;
}

export interface AppKnowledgeSearchResult {
  hits: AppKnowledgeHit[];
  nextPageToken: string;
}

export interface ConnectAppBridgeOptions {
  timeoutMs?: number;
  /** Test seam: defaults to window.parent. */
  parent?: Window;
}

/**
 * connectWorkOSAppBridge performs the handshake: it waits for the parent's
 * versioned hello (only ever accepted from `window.parent`, carrying exactly
 * one MessagePort), answers with the matching nonce on that port, and from
 * then on speaks only over the port. It rejects on timeout or any protocol
 * violation — never falling back to a fake bridge.
 */
export function connectWorkOSAppBridge(
  options: ConnectAppBridgeOptions = {},
): Promise<WorkOSAppBridge> {
  const parent = options.parent ?? window.parent;
  const timeoutMs = options.timeoutMs ?? REQUEST_TIMEOUT_MS;
  return new Promise<WorkOSAppBridge>((resolve, reject) => {
    let settled = false;
    let port: MessagePort | undefined;
    const finish = (error?: unknown, bridge?: WorkOSAppBridge) => {
      if (settled) return;
      settled = true;
      window.clearTimeout(timer);
      window.removeEventListener("message", onHello);
      if (error !== undefined) {
        port?.close();
        reject(error instanceof Error ? error : new BridgeProtocolError("bridge_closed"));
        return;
      }
      resolve(bridge as WorkOSAppBridge);
    };
    const timer = window.setTimeout(() => {
      finish(new BridgeProtocolError("timeout"));
    }, timeoutMs);

    const onHello = (event: MessageEvent) => {
      // The opaque origin makes event.origin useless as identity: trust is
      // the exact parent window reference plus the protocol contract.
      if (event.source !== parent) return;
      const hello: unknown = event.data;
      const parsed = hello as {
        version?: unknown;
        type?: unknown;
        nonce?: unknown;
        methods?: unknown;
      } | null;
      if (!parsed || parsed.version !== APP_BRIDGE_VERSION || parsed.type !== "hello") return;
      if (typeof parsed.nonce !== "string" || parsed.nonce === "") return;
      if (!Array.isArray(event.ports) || event.ports.length !== 1) return;
      const rawMethods: unknown[] = Array.isArray(parsed.methods) ? parsed.methods : [];
      const methods = rawMethods.filter(
        (method): method is BridgeMethod => typeof method === "string",
      );
      const framePort: MessagePort = event.ports[0] as unknown as MessagePort;
      port = framePort;
      framePort.start();
      window.removeEventListener("message", onHello);
      // The ack answers the nonce on the port; the parent proceeds only for
      // an exact match and only once.
      framePort.postMessage({ version: APP_BRIDGE_VERSION, type: "ack", nonce: parsed.nonce });
      finish(undefined, createBridge(framePort, methods, timeoutMs));
    };
    window.addEventListener("message", onHello);
  });
}

type Pending = {
  resolve: (value: BridgeRunResult | BridgeKnowledgeSearchResult | { done: true }) => void;
  reject: (error: BridgeProtocolError) => void;
  onEvent?: ((event: AgentEvent) => void) | undefined;
  canceled?: boolean | undefined;
  timer: number;
};

function createBridge(
  port: MessagePort,
  methods: readonly BridgeMethod[],
  timeoutMs: number,
): WorkOSAppBridge {
  let nextRequestId = 0;
  const pending = new Map<string, Pending>();

  port.onmessage = (event: MessageEvent) => {
    const envelope: unknown = event.data;
    if (!isParentEnvelope(envelope) || envelope.type === "hello") return;
    const entry = pending.get(envelope.requestId);
    if (!entry) return; // late or unknown responses are inert
    if (entry.canceled) return; // canceled streams drop everything further
    if (envelope.type === "response") {
      window.clearTimeout(entry.timer);
      pending.delete(envelope.requestId);
      entry.resolve(envelope.payload);
      return;
    }
    if (envelope.type === "event") {
      window.clearTimeout(entry.timer);
      entry.timer = window.setTimeout(() => {
        pending.delete(envelope.requestId);
        entry.reject(new BridgeProtocolError("timeout"));
      }, timeoutMs);
      entry.onEvent?.(envelope.payload);
      return;
    }
    window.clearTimeout(entry.timer);
    pending.delete(envelope.requestId);
    entry.reject(new BridgeProtocolError(envelope.code));
  };

  const call = (
    method: BridgeMethod,
    payload: BridgeRunPayload | BridgeStreamPayload | BridgeKnowledgeSearchPayload,
    onEvent?: (event: AgentEvent) => void,
    onRegistered?: (requestId: string) => void,
  ): Promise<BridgeRunResult | BridgeKnowledgeSearchResult | { done: true }> => {
    return new Promise((resolve, reject) => {
      if (!methods.includes(method)) {
        reject(new BridgeProtocolError("permission_denied"));
        return;
      }
      if (pending.size >= MAX_INFLIGHT_REQUESTS) {
        reject(new BridgeProtocolError("too_many_inflight"));
        return;
      }
      const requestId = `req-${String(++nextRequestId)}`;
      const timer = window.setTimeout(() => {
        pending.delete(requestId);
        reject(new BridgeProtocolError("timeout"));
      }, timeoutMs);
      pending.set(requestId, { resolve, reject, onEvent, timer });
      onRegistered?.(requestId);
      try {
        // Outbound traffic uses the same bounded helper as the trusted host:
        // the single-message bound is enforced on this side too, not only in
        // tests of a helper nothing calls.
        postBridgeMessage(port, {
          version: APP_BRIDGE_VERSION,
          type: "request",
          requestId,
          method,
          payload,
        });
      } catch (error) {
        window.clearTimeout(timer);
        pending.delete(requestId);
        reject(
          error instanceof BridgeProtocolError ? error : new BridgeProtocolError("bridge_closed"),
        );
      }
    });
  };

  return {
    agent: {
      async run(input: AppAgentRunInput): Promise<AppAgentRunResult> {
        const payload: BridgeRunPayload = {
          idempotencyKey: input.idempotencyKey,
          role: input.role,
          goal: input.goal,
        };
        const result = await call("agent.run", payload);
        if ("done" in result || !("taskId" in result)) {
          throw new BridgeProtocolError("internal");
        }
        return result;
      },
      stream(taskId: string, afterSequence = "0"): AsyncIterable<AgentEvent> {
        const queue: AgentEvent[] = [];
        let finished = false;
        let canceled = false;
        let failure: BridgeProtocolError | undefined;
        let wake: (() => void) | undefined;
        let streamRequestId: string | undefined;
        const notify = () => {
          const pendingWake = wake;
          wake = undefined;
          pendingWake?.();
        };

        void call(
          "agent.stream",
          { taskId, afterSequence } satisfies BridgeStreamPayload,
          (event) => {
            if (canceled) return;
            queue.push(event);
            notify();
          },
          (requestId) => {
            streamRequestId = requestId;
          },
        ).then(
          (result) => {
            if ("done" in result) {
              finished = true;
            } else {
              failure = new BridgeProtocolError("internal");
            }
            notify();
          },
          (error: unknown) => {
            failure =
              error instanceof BridgeProtocolError
                ? error
                : new BridgeProtocolError("bridge_closed");
            notify();
          },
        );

        return {
          [Symbol.asyncIterator]() {
            return {
              async next(): Promise<IteratorResult<AgentEvent>> {
                for (;;) {
                  const event = queue.shift();
                  if (event) return { value: event, done: false };
                  if (failure) throw failure;
                  if (finished) return { value: undefined, done: true };
                  await new Promise<void>((resolve) => {
                    wake = resolve;
                  });
                }
              },
              return(): Promise<IteratorResult<AgentEvent>> {
                // Early exit: cancel only this stream. The durable task and
                // its persisted events are untouched; later envelopes for
                // this request are dropped as canceled.
                canceled = true;
                queue.length = 0;
                finished = true;
                if (streamRequestId !== undefined) {
                  const entry = pending.get(streamRequestId);
                  if (entry) {
                    entry.canceled = true;
                    window.clearTimeout(entry.timer);
                    pending.delete(streamRequestId);
                  }
                  try {
                    postBridgeMessage(port, {
                      version: APP_BRIDGE_VERSION,
                      type: "cancel",
                      requestId: streamRequestId,
                    });
                  } catch {
                    // The port already died; nothing to cancel.
                  }
                }
                return Promise.resolve({ value: undefined, done: true });
              },
            };
          },
        };
      },
    },
    knowledge: {
      async search(input: AppKnowledgeSearchInput): Promise<AppKnowledgeSearchResult> {
        const payload: BridgeKnowledgeSearchPayload = {
          query: input.query,
          pageSize: input.pageSize,
          pageToken: input.pageToken,
        };
        const result = await call("knowledge.search", payload);
        if ("done" in result || !("hits" in result)) {
          throw new BridgeProtocolError("internal");
        }
        return result;
      },
    },
  };
}
