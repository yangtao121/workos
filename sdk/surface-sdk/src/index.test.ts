// @vitest-environment node
// Wire-protocol unit tests: envelope guards and the symmetric message bound.
import { describe, expect, it } from "vitest";
import {
  APP_BRIDGE_VERSION,
  BridgeProtocolError,
  encodeBridgeMessage,
  isEnvelope,
  isFrameEnvelope,
  isParentEnvelope,
  postBridgeMessage,
} from "./index.js";

describe("envelope guards", () => {
  it("accepts well-formed envelopes and rejects everything else", () => {
    expect(
      isEnvelope({
        version: APP_BRIDGE_VERSION,
        type: "request",
        requestId: "r",
        method: "agent.run",
        payload: {},
      }),
    ).toBe(true);
    expect(isEnvelope({ version: "workos.app-bridge/v0", type: "request" })).toBe(false);
    expect(isEnvelope({ version: APP_BRIDGE_VERSION, type: "unknown" })).toBe(false);
    expect(isEnvelope(null)).toBe(false);
    expect(isEnvelope("hello")).toBe(false);
  });

  it("splits parent and frame envelopes", () => {
    expect(isFrameEnvelope({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "n" })).toBe(true);
    expect(isFrameEnvelope({ version: APP_BRIDGE_VERSION, type: "cancel", requestId: "r" })).toBe(
      true,
    );
    expect(isFrameEnvelope({ version: APP_BRIDGE_VERSION, type: "hello", nonce: "n" })).toBe(false);
    expect(
      isParentEnvelope({ version: APP_BRIDGE_VERSION, type: "hello", nonce: "n", methods: [] }),
    ).toBe(true);
    expect(
      isParentEnvelope({
        version: APP_BRIDGE_VERSION,
        type: "error",
        requestId: "r",
        code: "internal",
      }),
    ).toBe(true);
    expect(isParentEnvelope({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "n" })).toBe(false);
  });
});

describe("message bounds", () => {
  it("rejects oversize envelopes before they hit the wire", () => {
    const huge = "x".repeat(64 * 1024);
    expect(() =>
      encodeBridgeMessage({
        version: APP_BRIDGE_VERSION,
        type: "error",
        requestId: huge,
        code: "internal",
      }),
    ).toThrow(BridgeProtocolError);
  });

  it("posts small envelopes and refuses oversize ones", () => {
    const sent: unknown[] = [];
    const port = {
      postMessage: (value: unknown): void => {
        sent.push(value);
      },
    };
    postBridgeMessage(port as unknown as MessagePort, {
      version: APP_BRIDGE_VERSION,
      type: "ack",
      nonce: "n",
    });
    expect(sent).toHaveLength(1);
    const huge = "x".repeat(64 * 1024);
    const postOversize = (): void => {
      postBridgeMessage(port as unknown as MessagePort, {
        version: APP_BRIDGE_VERSION,
        type: "error",
        requestId: huge,
        code: "internal",
      });
    };
    expect(postOversize).toThrow(BridgeProtocolError);
    expect(sent).toHaveLength(1);
  });

  it("converts unserializable envelopes into the stable invalid_argument error", () => {
    const request: {
      version: typeof APP_BRIDGE_VERSION;
      type: "request";
      requestId: string;
      method: "agent.run";
      payload: unknown;
    } = {
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "r",
      method: "agent.run",
      payload: { idempotencyKey: "k", goal: "g" },
    };
    const cyclic: Record<string, unknown> = { value: 1 };
    cyclic["self"] = cyclic;
    request.payload = cyclic;
    expect(() => encodeBridgeMessage(request as never)).toThrow(BridgeProtocolError);
    try {
      encodeBridgeMessage(request as never);
    } catch (error: unknown) {
      // A cyclic payload is a caller bug with a stable code — never a raw
      // TypeError leaking out of JSON.stringify.
      expect((error as BridgeProtocolError).code).toBe("invalid_argument");
    }
  });
});
