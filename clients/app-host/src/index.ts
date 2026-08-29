// The trusted parent-side App Bridge host. Running inside the Desktop (never
// inside the iframe), it owns the versioned MessageChannel handshake with the
// exact sandboxed iframe window and dispatches bounded bridge requests to the
// injected transport. This package is a protocol adapter only: it holds no
// authorization truth of its own — every request is re-validated server-side,
// and the bridge token stays in the caller's memory and travels only as RPC
// metadata, never through this port.
//
// The iframe is an UNTRUSTED peer: it can bypass @workos/app-sdk and post
// arbitrary structured-clone payloads straight at the port. This module
// therefore re-validates every inbound envelope itself — protocol version,
// UTF-8 byte size, bounded canonical request IDs, allow-listed methods,
// exact payload shapes and field bounds, in-flight concurrency, duplicate
// IDs — and fails closed with stable error codes before any transport call.
import {
  APP_BRIDGE_VERSION,
  BridgeProtocolError,
  MAX_INFLIGHT_REQUESTS,
  MAX_SINGLE_MESSAGE_BYTES,
  REQUEST_TIMEOUT_MS,
  isFrameEnvelope,
  postBridgeHello,
  postBridgeMessage,
  type BridgeAck,
  type BridgeErrorCode,
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
 * sees the token itself. Implementations signal stable client-facing codes by
 * throwing BridgeProtocolError; any other thrown value is collapsed to
 * `internal` and its details never cross the port.
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

// Request-boundary grammar enforced on the untrusted inbound stream. These
// mirror the server contract: the runtime re-validates everything again.
const MAX_IDEMPOTENCY_KEY_CHARS = 128;
const MAX_ROLE_CHARS = 64;
const MAX_GOAL_BYTES = 16 * 1024;
const requestIdPattern = /^[A-Za-z0-9-]{1,64}$/;
const taskIdPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const cursorPattern = /^[0-9]{1,20}$/;

/** UTF-8 byte size of an envelope — the wire bound, not a UTF-16 length. */
function envelopeByteSize(envelope: unknown): number {
  return new TextEncoder().encode(JSON.stringify(envelope)).length;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Exact-key shape check: unknown and missing fields both fail closed. */
function hasExactKeys(payload: Record<string, unknown>, keys: readonly string[]): boolean {
  const actual = Object.keys(payload);
  return (
    actual.length === keys.length &&
    keys.every((key) => Object.prototype.hasOwnProperty.call(payload, key))
  );
}

function validRunPayload(payload: unknown): payload is BridgeRunPayload {
  if (!isPlainObject(payload)) return false;
  // role is optional in the protocol: exactly the 2-key or 3-key shape.
  const withRole = hasExactKeys(payload, ["goal", "idempotencyKey", "role"]);
  const withoutRole = hasExactKeys(payload, ["goal", "idempotencyKey"]);
  if (!withRole && !withoutRole) return false;
  const { goal, idempotencyKey, role } = payload;
  return (
    typeof idempotencyKey === "string" &&
    idempotencyKey.length >= 1 &&
    idempotencyKey.length <= MAX_IDEMPOTENCY_KEY_CHARS &&
    (role === undefined || (typeof role === "string" && role.length <= MAX_ROLE_CHARS)) &&
    typeof goal === "string" &&
    goal.length >= 1 &&
    envelopeByteSize(goal) <= MAX_GOAL_BYTES
  );
}

function validStreamPayload(payload: unknown): payload is BridgeStreamPayload {
  if (!isPlainObject(payload) || !hasExactKeys(payload, ["afterSequence", "taskId"])) {
    return false;
  }
  const { afterSequence, taskId } = payload;
  return (
    typeof taskId === "string" &&
    taskIdPattern.test(taskId) &&
    typeof afterSequence === "string" &&
    cursorPattern.test(afterSequence)
  );
}

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
  // Pending runs and live streams both count against the in-flight bound;
  // timers are owned by their entries so teardown and timeouts always clean
  // up the exact operation that timed out.
  const pending = new Map<string, { timer: number }>();
  const streams = new Map<string, { controller: AbortController; timer: number }>();

  const clearPending = (requestId: string): void => {
    const entry = pending.get(requestId);
    if (!entry) return;
    window.clearTimeout(entry.timer);
    pending.delete(requestId);
  };

  const failAll = (): void => {
    for (const entry of pending.values()) {
      window.clearTimeout(entry.timer);
    }
    pending.clear();
    for (const { controller, timer } of streams.values()) {
      window.clearTimeout(timer);
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

  const respondError = (requestId: string, code: BridgeErrorCode): void => {
    if (closed || !handshaked) return;
    postBridgeMessage(channel.port1, {
      version: APP_BRIDGE_VERSION,
      type: "error",
      requestId,
      code,
    });
  };

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
      dispatch(envelope);
      return;
    }
    if (envelope.type === "cancel") {
      const entry = streams.get(envelope.requestId);
      if (entry) {
        window.clearTimeout(entry.timer);
        entry.controller.abort();
        streams.delete(envelope.requestId);
      }
    }
  };

  /** Validates one untrusted inbound request; returns the stable failure code
   * or null when the request may proceed to the transport. */
  const validateRequest = (request: BridgeRequest): BridgeErrorCode | null => {
    if (typeof request.requestId !== "string" || !requestIdPattern.test(request.requestId)) {
      return "invalid_argument";
    }
    if (pending.has(request.requestId) || streams.has(request.requestId)) {
      // Duplicate ID: the first operation still owns it.
      return "invalid_argument";
    }
    if (!methods.includes(request.method)) {
      // Fail closed even if the frame asks for a method this surface was
      // never offered — the advertisement is not the only gate.
      return "permission_denied";
    }
    if (pending.size + streams.size >= MAX_INFLIGHT_REQUESTS) {
      return "too_many_inflight";
    }
    if (envelopeByteSize(request) > MAX_SINGLE_MESSAGE_BYTES) {
      return "oversize";
    }
    if (request.method === "agent.run") {
      return validRunPayload(request.payload) ? null : "invalid_argument";
    }
    return validStreamPayload(request.payload) ? null : "invalid_argument";
  };

  const dispatch = (request: BridgeRequest): void => {
    const failure = validateRequest(request);
    if (failure !== null) {
      respondError(request.requestId, failure);
      return;
    }
    if (request.method === "agent.run") {
      const payload = request.payload as BridgeRunPayload;
      const entry = {
        timer: window.setTimeout(() => {
          // The timeout is the linearization of cleanup: the pending entry
          // is removed first so a late transport result finds no owner and
          // stays inert.
          if (pending.delete(request.requestId)) {
            respondError(request.requestId, "timeout");
          }
        }, timeoutMs),
      };
      pending.set(request.requestId, entry);
      void options.transport
        .runAgentTask(payload)
        .then((result) => {
          if (!pending.has(request.requestId)) return; // timed out or closed: inert
          clearPending(request.requestId);
          if (closed || !handshaked) return;
          postBridgeMessage(channel.port1, {
            version: APP_BRIDGE_VERSION,
            type: "response",
            requestId: request.requestId,
            payload: result,
          });
        })
        .catch((reason: unknown) => {
          if (!pending.has(request.requestId)) return;
          clearPending(request.requestId);
          const code: BridgeErrorCode =
            reason instanceof BridgeProtocolError ? reason.code : "internal";
          respondError(request.requestId, code);
        });
      return;
    }

    // agent.stream: acknowledge implicitly by streaming; forward canonical
    // events until the server stream completes. The in-flight timer aborts
    // the local/server stream if it never completes; a frame cancel aborts
    // only this stream, never the durable task.
    const payload = request.payload as BridgeStreamPayload;
    const controller = new AbortController();
    const entry = {
      controller,
      timer: window.setTimeout(() => {
        if (streams.delete(request.requestId)) {
          controller.abort();
          respondError(request.requestId, "timeout");
        }
      }, timeoutMs),
    };
    streams.set(request.requestId, entry);
    const settle = (failure: BridgeErrorCode | null): void => {
      const live = streams.get(request.requestId);
      if (!live || live.controller !== controller) return; // canceled/closed: inert
      window.clearTimeout(live.timer);
      streams.delete(request.requestId);
      if (failure !== null) {
        respondError(request.requestId, failure);
        return;
      }
      if (!controller.signal.aborted && !closed) {
        postBridgeMessage(channel.port1, {
          version: APP_BRIDGE_VERSION,
          type: "response",
          requestId: request.requestId,
          payload: { done: true },
        });
      }
    };
    void options.transport
      .watchAgentTaskEvents(
        payload,
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
      )
      .then(
        () => {
          settle(null);
        },
        (reason: unknown) => {
          const code: BridgeErrorCode =
            reason instanceof BridgeProtocolError ? reason.code : "unavailable";
          settle(code);
        },
      );
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
      failAll();
      channel.port1.close();
    },
    ready,
  };
}
