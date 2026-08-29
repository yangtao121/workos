// The single App Bridge wire protocol shared by the trusted parent
// (clients/app-host) and the sandboxed iframe (sdk/app-sdk). Business
// payloads reuse the generated canonical types from @workos/protocol — this
// module defines only the bounded envelopes, versions, limits, and stable
// error codes, never a second DTO for agent data.
import type { AgentEvent } from "@workos/protocol";

/** The one protocol version; a mismatch fails the handshake closed. */
export const APP_BRIDGE_VERSION = "workos.app-bridge/v1" as const;

/** Hard wire limits: one bounded message, bounded in-flight, bounded wait. */
export const MAX_SINGLE_MESSAGE_BYTES = 64 * 1024;
export const MAX_INFLIGHT_REQUESTS = 32;
export const REQUEST_TIMEOUT_MS = 15_000;

/** The only bridge methods that exist; anything else fails closed. */
export const BRIDGE_METHODS = ["agent.run", "agent.stream"] as const;
export type BridgeMethod = (typeof BRIDGE_METHODS)[number];

export interface BridgeHello {
  version: typeof APP_BRIDGE_VERSION;
  type: "hello";
  /** One-time nonce echoed by the ack on the transferred port. */
  nonce: string;
  /** The method names this surface may call (implemented AND granted). */
  methods: readonly BridgeMethod[];
}

export interface BridgeAck {
  version: typeof APP_BRIDGE_VERSION;
  type: "ack";
  /** The hello nonce this ack answers; any other value rejects the reply. */
  nonce: string;
}

/** agent.run payload: the bounded canonical run input. */
export interface BridgeRunPayload {
  idempotencyKey: string;
  role?: string | undefined;
  goal: string;
}

/** agent.run result, projected from workos.bridge.v1.RunAgentTaskResponse. */
export interface BridgeRunResult {
  taskId: string;
  state: string;
  lastEventSequence: string;
}

/** agent.stream payload: resume cursor semantics match the canonical RPC. */
export interface BridgeStreamPayload {
  taskId: string;
  afterSequence: string;
}

export interface BridgeRequest {
  version: typeof APP_BRIDGE_VERSION;
  type: "request";
  requestId: string;
  method: BridgeMethod;
  payload: BridgeRunPayload | BridgeStreamPayload;
}

export interface BridgeResponse {
  version: typeof APP_BRIDGE_VERSION;
  type: "response";
  requestId: string;
  payload: BridgeRunResult | { done: true };
}

/** One streamed canonical event for an accepted agent.stream request. */
export interface BridgeEvent {
  version: typeof APP_BRIDGE_VERSION;
  type: "event";
  requestId: string;
  payload: AgentEvent;
}

export interface BridgeError {
  version: typeof APP_BRIDGE_VERSION;
  type: "error";
  requestId: string;
  code: BridgeErrorCode;
}

/** Frame-side early stream end: cancels only that local/server stream. */
export interface BridgeCancel {
  version: typeof APP_BRIDGE_VERSION;
  type: "cancel";
  requestId: string;
}

export type BridgeErrorCode =
  | "invalid_argument"
  | "unauthenticated"
  | "permission_denied"
  | "not_found"
  | "aborted"
  | "unavailable"
  | "internal"
  | "timeout"
  | "unknown_method"
  | "oversize"
  | "too_many_inflight"
  | "bridge_closed";

export const BRIDGE_ERROR_MESSAGES: Record<BridgeErrorCode, string> = {
  invalid_argument: "The bridge request was invalid.",
  unauthenticated: "The bridge credential is not valid.",
  permission_denied: "This app does not have that capability.",
  not_found: "The task is not available to this app.",
  aborted: "The idempotency key was already used for a different request.",
  unavailable: "The bridge is temporarily unavailable.",
  internal: "The bridge failed unexpectedly.",
  timeout: "The bridge request timed out.",
  unknown_method: "That bridge method does not exist.",
  oversize: "The bridge message was too large.",
  too_many_inflight: "Too many bridge requests are in flight.",
  bridge_closed: "The bridge is closed.",
};

export type ParentEnvelope = BridgeHello | BridgeResponse | BridgeEvent | BridgeError;
export type FrameEnvelope = BridgeAck | BridgeRequest | BridgeCancel;
export type BridgeEnvelope = ParentEnvelope | FrameEnvelope;

/** Structural guard for incoming frame-side messages (never trusts shape). */
export function isFrameEnvelope(value: unknown): value is FrameEnvelope {
  return (
    isEnvelope(value) &&
    (value.type === "ack" || value.type === "request" || value.type === "cancel")
  );
}

/** Structural guard for incoming parent-side messages (never trusts shape). */
export function isParentEnvelope(value: unknown): value is ParentEnvelope {
  return (
    isEnvelope(value) &&
    (value.type === "hello" ||
      value.type === "response" ||
      value.type === "event" ||
      value.type === "error")
  );
}

export function isEnvelope(value: unknown): value is BridgeEnvelope {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    candidate["version"] === APP_BRIDGE_VERSION &&
    typeof candidate["type"] === "string" &&
    ["hello", "ack", "request", "response", "event", "error", "cancel"].includes(candidate["type"])
  );
}

/**
 * Serializes one envelope and enforces the single-message bound measured in
 * UTF-8 bytes — the wire quantity, not a UTF-16 `string.length` that
 * multibyte payloads can hide behind. Canonical proto payloads may carry
 * bigint int64 fields, which JSON.stringify cannot serialize — they are
 * stringified only for the measurement; postMessage transports the original
 * structured-clone-safe object.
 */
export function encodeBridgeMessage(envelope: BridgeEnvelope): string {
  const encoded = JSON.stringify(envelope, (_key, value: unknown) =>
    typeof value === "bigint" ? value.toString() : value,
  );
  if (new TextEncoder().encode(encoded).length > MAX_SINGLE_MESSAGE_BYTES) {
    throw new BridgeProtocolError("oversize");
  }
  return encoded;
}

/**
 * Posts one bounded envelope over a MessagePort. Both sides use this so the
 * single-message bound is enforced symmetrically.
 */
export function postBridgeMessage(target: MessagePort, envelope: BridgeEnvelope): void {
  encodeBridgeMessage(envelope);
  target.postMessage(envelope);
}

/**
 * Posts the bounded versioned hello to the exact frame window, transferring
 * the frame's port end. This is the only window-targeted message in the
 * protocol; opaque origins require targetOrigin "*" and the security comes
 * from the exact window reference, never the origin string.
 */
export function postBridgeHello(
  frameWindow: Window,
  hello: BridgeHello,
  framePort: MessagePort,
): void {
  encodeBridgeMessage(hello);
  frameWindow.postMessage(hello, "*", [framePort]);
}

export class BridgeProtocolError extends Error {
  readonly code: BridgeErrorCode;
  constructor(code: BridgeErrorCode) {
    super(BRIDGE_ERROR_MESSAGES[code]);
    this.name = "BridgeProtocolError";
    this.code = code;
  }
}

export type SurfaceLifecycle =
  | "create"
  | "attach"
  | "ready"
  | "resize"
  | "background"
  | "suspend"
  | "resume"
  | "detach"
  | "close";
