// Stable projection of Desktop transport failures onto the App Bridge error
// codes. The trusted host converts these to fixed, short, safe Messages on
// the MessagePort: no raw Connect message, SQL, DSN, token, goal/event text,
// stack, or internal address ever crosses to the untrusted frame. Run and
// stream share the same mapping so one server verdict cannot change category
// with the RPC shape.
import { Code, ConnectError } from "@connectrpc/connect";
import { BridgeProtocolError, type BridgeErrorCode } from "@workos/surface-sdk";

/** Maps any thrown transport failure to its stable bridge error code. */
export function bridgeCodeFromTransportError(reason: unknown): BridgeErrorCode {
  if (reason instanceof BridgeProtocolError) return reason.code;
  if (reason instanceof ConnectError) {
    const reasonCode = reason.code;
    switch (reasonCode) {
      case Code.InvalidArgument:
        return "invalid_argument";
      case Code.Unauthenticated:
        return "unauthenticated";
      case Code.PermissionDenied:
        return "permission_denied";
      case Code.NotFound:
        return "not_found";
      case Code.Aborted:
        return "aborted";
      case Code.FailedPrecondition:
        return "failed_precondition";
      case Code.ResourceExhausted:
        return "resource_exhausted";
      case Code.Unavailable:
      case Code.DeadlineExceeded:
      case Code.Canceled:
        return "unavailable";
      default:
        return "internal";
    }
  }
  return "internal";
}

/** Wraps a transport failure as the typed error the AppHost understands. */
export function asBridgeProtocolError(reason: unknown): BridgeProtocolError {
  return reason instanceof BridgeProtocolError
    ? reason
    : new BridgeProtocolError(bridgeCodeFromTransportError(reason));
}
