import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { BridgeProtocolError } from "@workos/surface-sdk";
import { asBridgeProtocolError, bridgeCodeFromTransportError } from "./bridgeErrors.js";

function connectErrorWith(code: Code): ConnectError {
  return new ConnectError("synthetic detail that must never leak", code);
}

describe("bridgeCodeFromTransportError", () => {
  it("maps every relevant Connect code to its stable bridge code", () => {
    const expected: Array<[Code, string]> = [
      [Code.InvalidArgument, "invalid_argument"],
      [Code.Unauthenticated, "unauthenticated"],
      [Code.PermissionDenied, "permission_denied"],
      [Code.NotFound, "not_found"],
      [Code.Aborted, "aborted"],
      [Code.Unavailable, "unavailable"],
      [Code.DeadlineExceeded, "unavailable"],
      [Code.Canceled, "unavailable"],
      [Code.Internal, "internal"],
      [Code.DataLoss, "internal"],
      [Code.Unknown, "internal"],
    ];
    for (const [code, want] of expected) {
      expect(bridgeCodeFromTransportError(connectErrorWith(code))).toBe(want);
    }
  });

  it("passes typed bridge errors through and collapses unknown failures", () => {
    expect(bridgeCodeFromTransportError(new BridgeProtocolError("timeout"))).toBe("timeout");
    expect(bridgeCodeFromTransportError(new Error("pgx something"))).toBe("internal");
    expect(bridgeCodeFromTransportError(undefined)).toBe("internal");
  });

  it("never carries the raw message across the boundary", () => {
    const typed = asBridgeProtocolError(connectErrorWith(Code.PermissionDenied));
    expect(typed.code).toBe("permission_denied");
    expect(typed.message).not.toContain("synthetic detail");
    // The fixed safe message set is the only text the frame can see.
    expect(typed.message).toBe("This app does not have that capability.");
  });
});
