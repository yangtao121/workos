// The trusted parent-side App Bridge host. Running inside the Desktop (never
// inside the iframe), it owns the versioned MessageChannel handshake with the
// exact sandboxed iframe window and dispatches bounded bridge requests to the
// injected transport. This package is a protocol adapter only: it holds no
// authorization truth of its own — every request is re-validated server-side,
// and the bridge token stays in the caller's memory and travels only as RPC
// metadata, never through this port.
import {
  APP_BRIDGE_VERSION,
  BridgeProtocolError,
  REQUEST_TIMEOUT_MS,
  isFrameEnvelope,
  postBridgeHello,
  postBridgeMessage,
  type BridgeAck,
  type BridgeMethod,
  type BridgeRequest,
  type BridgeRunPayload,
  type BridgeStreamPayload,
} from "@workos/surface-sdk";
import type { AgentEvent } from "@workos/protocol";

export {
  APP_BRIDGE_VERSION,
  BridgeProtocolError,
  type BridgeErrorCode,
  type BridgeMethod,
} from "@workos/surface-sdk";

/** The result of one bridge run, projected from the canonical RPC response. */
export interface AppBridgeRunResult {
  taskId: string;
  state: string;
  lastEventSequence: string;
}

/**
 * The transport the Desktop wires in: the public AppBridgeService calls with
 * the surface's bridge token already attached as metadata. The host never
 * sees the token itself.
 */
export interface AppBridgeTransport {
  runAgentTask(input: BridgeRunPayload): Promise<AppBridgeRunResult>;
  /**
   * Watches persisted task events until the task is terminal or abort is
   * triggered. Ending the stream never cancels the durable task.
   */
  watchAgentTaskEvents(
    input: BridgeStreamPayload,
    onEvent: (event: AgentEvent) => void,
    signal: AbortSignal,
  ): Promise<void>;
}

export interface AppBridgeHostOptions {
  /** The exact iframe window the handshake targets; never a global lookup. */
  frameWindow: Window;
  /** Effective capabilities of this surface, from the CreateSurface session. */
  capabilities: readonly string[];
  transport: AppBridgeTransport;
  timeoutMs?: number;
  nonceGenerator?: () => string;
  /** Test seam: defaults to a real MessageChannel. */
  channelFactory?: () => MessageChannel;
  onHandshakeComplete?: () => void;
  onHandshakeFailed?: (error: Error) => void;
}

export interface AppBridgeHost {
  /** Tears down the port, rejects pending requests, stops listening. */
  close(): void;
  /** Resolves once the frame acked the nonce; rejects on timeout/failure. */
  readonly ready: Promise<void>;
}

/** Capability → bridge method mapping: only granted AND implemented methods
 * are ever offered to the iframe. */
const capabilityMethods: Record<string, BridgeMethod> = {
  "agent.task.run": "agent.run",
  "agent.event.watch": "agent.stream",
};

/**
 * openAppBridgeHost performs the parent half of the handshake: generate a
 * one-time nonce, create a fresh MessageChannel, post the versioned hello to
 * the exact frame window (targetOrigin `*` is mandatory for opaque origins —
 * security comes from the exact window reference, not from origin strings),
 * transfer port2, and accept at most one correctly acked nonce reply on the
 * port. Every later RPC flows through the port alone; any other source,
 * version, nonce, or a double ack fails the handshake closed.
 */
export function openAppBridgeHost(options: AppBridgeHostOptions): AppBridgeHost {
  const timeoutMs = options.timeoutMs ?? REQUEST_TIMEOUT_MS;
  const nonce =
    options.nonceGenerator?.() ??
    (typeof crypto.randomUUID === "function" ? crypto.randomUUID() : String(Math.random()));

  const methods = Object.entries(capabilityMethods)
    .filter(([capability]) => options.capabilities.includes(capability))
    .map(([, method]) => method);

  const channel = options.channelFactory?.() ?? new MessageChannel();
  let closed = false;
  let handshaked = false;
  const pending = new Map<
    string,
    { reject: (error: BridgeProtocolError) => void; timer: number }
  >();
  const streams = new Map<string, AbortController>();

  const failAll = (code: ConstructorParameters<typeof BridgeProtocolError>[0]) => {
    for (const entry of pending.values()) {
      window.clearTimeout(entry.timer);
      entry.reject(new BridgeProtocolError(code));
    }
    pending.clear();
    for (const controller of streams.values()) {
      controller.abort();
    }
    streams.clear();
  };

  // The port listener below settles the handshake exactly once.
  let settleHandshake: ((error?: BridgeProtocolError) => void) | undefined;

  const ready = new Promise<void>((resolve, reject) => {
    const finishHandshake = (error?: BridgeProtocolError) => {
      settleHandshake = undefined;
      window.clearTimeout(timer);
      if (error !== undefined) {
        reject(error);
        options.onHandshakeFailed?.(error);
        return;
      }
      handshaked = true;
      resolve();
      options.onHandshakeComplete?.();
    };
    const timer = window.setTimeout(() => {
      finishHandshake(new BridgeProtocolError("timeout"));
    }, timeoutMs);
    settleHandshake = finishHandshake;
  });

  // The ack and every later request arrive on the port; messages from any
  // other window/port can never reach this listener.
  channel.port1.onmessage = (event: MessageEvent) => {
    if (closed) return;
    const envelope: unknown = event.data;
    if (!isFrameEnvelope(envelope)) return;
    if (!handshaked) {
      // Exactly one ack with the exact nonce starts the session; anything
      // else (wrong nonce, wrong version, double ack, early request) fails.
      const ack = envelope as Partial<BridgeAck>;
      if (ack.type === "ack" && ack.nonce === nonce) {
        settleHandshake?.();
      } else {
        settleHandshake?.(new BridgeProtocolError("bridge_closed"));
      }
      return;
    }
    if (envelope.type === "request") {
      void dispatch(envelope);
      return;
    }
    if (envelope.type === "cancel") {
      const controller = streams.get(envelope.requestId);
      if (controller) {
        controller.abort();
        streams.delete(envelope.requestId);
      }
    }
  };

  const respondError = (
    requestId: string,
    code: ConstructorParameters<typeof BridgeProtocolError>[0],
  ) => {
    if (closed || !handshaked) return;
    postBridgeMessage(channel.port1, {
      version: APP_BRIDGE_VERSION,
      type: "error",
      requestId,
      code,
    });
  };

  const dispatch = async (request: BridgeRequest) => {
    const timer = window.setTimeout(() => {
      if (pending.delete(request.requestId)) {
        respondError(request.requestId, "timeout");
      }
    }, timeoutMs);

    if (!methods.includes(request.method)) {
      // Fail closed even if the frame asks for a method this surface was
      // never offered — the advertisement is not the only gate.
      window.clearTimeout(timer);
      respondError(request.requestId, "permission_denied");
      return;
    }
    if (request.method === "agent.run") {
      const payload: unknown = request.payload;
      const runPayload = payload as Partial<BridgeRunPayload>;
      if (typeof runPayload.idempotencyKey !== "string" || typeof runPayload.goal !== "string") {
        window.clearTimeout(timer);
        respondError(request.requestId, "invalid_argument");
        return;
      }
      let result: AppBridgeRunResult;
      try {
        result = await options.transport.runAgentTask(runPayload as BridgeRunPayload);
      } catch {
        // A stable internal code — the transport failure detail never
        // crosses the port.
        window.clearTimeout(timer);
        respondError(request.requestId, "internal");
        return;
      }
      window.clearTimeout(timer);
      postBridgeMessage(channel.port1, {
        version: APP_BRIDGE_VERSION,
        type: "response",
        requestId: request.requestId,
        payload: result,
      });
      return;
    }

    // agent.stream: acknowledge immediately, then forward canonical events
    // until the server stream completes; a frame cancel aborts only the
    // local/server stream, never the durable task.
    const payload: unknown = request.payload;
    const streamPayload = payload as Partial<BridgeStreamPayload>;
    if (typeof streamPayload.taskId !== "string" || streamPayload.taskId === "") {
      window.clearTimeout(timer);
      respondError(request.requestId, "invalid_argument");
      return;
    }
    const controller = new AbortController();
    streams.set(request.requestId, controller);
    window.clearTimeout(timer);
    try {
      await options.transport.watchAgentTaskEvents(
        streamPayload as BridgeStreamPayload,
        (event) => {
          if (closed || controller.signal.aborted) {
            return;
          }
          postBridgeMessage(channel.port1, {
            version: APP_BRIDGE_VERSION,
            type: "event",
            requestId: request.requestId,
            payload: event,
          });
        },
        controller.signal,
      );
      if (!controller.signal.aborted && !closed) {
        postBridgeMessage(channel.port1, {
          version: APP_BRIDGE_VERSION,
          type: "response",
          requestId: request.requestId,
          payload: { done: true },
        });
      }
    } catch {
      if (!controller.signal.aborted && !closed) {
        respondError(request.requestId, "unavailable");
      }
    } finally {
      streams.delete(request.requestId);
    }
  };

  // Hello: only to the exact contentWindow of the iframe's current document,
  // transferring the frame's end of the channel.
  postBridgeHello(
    options.frameWindow,
    { version: APP_BRIDGE_VERSION, type: "hello", nonce, methods },
    channel.port2,
  );

  return {
    close() {
      if (closed) return;
      closed = true;
      failAll("bridge_closed");
      channel.port1.close();
    },
    ready,
  };
}
