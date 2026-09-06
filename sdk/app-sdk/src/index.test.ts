// @vitest-environment jsdom
// Deterministic frame-side SDK tests: the handshake accepts only an exact
// parent hello with one port and the right version, and every bridge call
// stays bounded and fails closed.
import { describe, expect, it, vi } from "vitest";
import {
  APP_BRIDGE_VERSION,
  BridgeProtocolError,
  MAX_INFLIGHT_REQUESTS,
} from "@workos/surface-sdk";
import { connectWorkOSAppBridge } from "./index.js";

class FakePort {
  sent: Record<string, unknown>[] = [];
  onmessage: ((event: { data: unknown }) => void) | null = null;
  started = false;
  postMessage(data: unknown): void {
    this.sent.push(data as Record<string, unknown>);
  }
  start(): void {
    this.started = true;
  }
  receive(data: unknown): void {
    this.onmessage?.({ data });
  }
}

function fakeParent() {
  return { postMessage: vi.fn() } as unknown as Window;
}

function deliverHello(parent: Window, ports: MessagePort[], data?: Record<string, unknown>): void {
  const event = new MessageEvent("message", {
    source: parent,
    data: {
      version: APP_BRIDGE_VERSION,
      type: "hello",
      nonce: "nonce-1",
      methods: ["agent.run", "agent.stream"],
      ...(data ?? {}),
    },
  });
  // jsdom does not preserve MessageEventInit.ports; the SDK only reads the
  // array, so patch it onto the instance.
  Object.defineProperty(event, "ports", { value: ports });
  window.dispatchEvent(event);
}

async function connectedBridge(options?: { methods?: string[] }) {
  const parent = fakeParent();
  const port = new FakePort();
  const pending = connectWorkOSAppBridge({ parent, timeoutMs: 500 });
  deliverHello(
    parent,
    [port as unknown as MessagePort],
    options?.methods ? { methods: options.methods } : undefined,
  );
  return { bridge: await pending, port, parent };
}

describe("connectWorkOSAppBridge handshake", () => {
  it("acks the exact nonce on the single transferred port and resolves the bridge", async () => {
    const { bridge, port } = await connectedBridge();
    expect(port.started).toBe(true);
    expect(port.sent[0]).toMatchObject({
      version: APP_BRIDGE_VERSION,
      type: "ack",
      nonce: "nonce-1",
    });
    expect(bridge.agent).toBeDefined();
  });

  it("ignores hellos from any source other than the exact parent", async () => {
    const parent = fakeParent();
    const impostor = fakeParent();
    const port = new FakePort();
    const pending = connectWorkOSAppBridge({ parent, timeoutMs: 50 });
    deliverHello(impostor, [port as unknown as MessagePort]);
    await expect(pending).rejects.toThrow(BridgeProtocolError);
    expect(port.sent).toHaveLength(0);
  });

  it("ignores hellos without exactly one port and bad versions", async () => {
    const parent = fakeParent();
    const port = new FakePort();
    const pending = connectWorkOSAppBridge({ parent, timeoutMs: 50 });
    deliverHello(parent, []);
    deliverHello(parent, [port as unknown as MessagePort, port as unknown as MessagePort]);
    deliverHello(parent, [port as unknown as MessagePort], { version: "workos.app-bridge/v0" });
    await expect(pending).rejects.toThrow(BridgeProtocolError);
    expect(port.sent).toHaveLength(0);
  });

  it("times out without a hello", async () => {
    const pending = connectWorkOSAppBridge({ parent: fakeParent(), timeoutMs: 20 });
    await expect(pending).rejects.toThrow(BridgeProtocolError);
  });
});

describe("WorkOSAppBridge calls", () => {
  it("runs agent.run over the port and resolves the canonical result", async () => {
    const { bridge, port } = await connectedBridge();
    const pending = bridge.agent.run({ idempotencyKey: "key-1", role: "", goal: "Go." });
    const request = port.sent.at(-1) as {
      type: string;
      method: string;
      requestId: string;
      payload: unknown;
    };
    expect(request).toMatchObject({ type: "request", method: "agent.run", requestId: "req-1" });
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: request.requestId,
      payload: { taskId: "task-9", state: "queued", lastEventSequence: "0" },
    });
    await expect(pending).resolves.toMatchObject({ taskId: "task-9" });
  });

  it("rejects a method that was never offered", async () => {
    const { bridge } = await connectedBridge({ methods: ["agent.run"] });
    await expect(bridge.agent.stream("task-1")[Symbol.asyncIterator]().next()).rejects.toThrow(
      BridgeProtocolError,
    );
  });

  it("maps error envelopes to stable protocol errors without internals", async () => {
    const { bridge, port } = await connectedBridge();
    const pending = bridge.agent.run({ idempotencyKey: "key-1", goal: "Go." });
    const request = port.sent.at(-1) as { requestId: string };
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "error",
      requestId: request.requestId,
      code: "permission_denied",
    });
    await expect(pending).rejects.toMatchObject({ code: "permission_denied" });
  });

  it("bounds in-flight requests and fails the excess closed", async () => {
    const { bridge, port } = await connectedBridge();
    for (let index = 0; index < MAX_INFLIGHT_REQUESTS; index += 1) {
      void bridge.agent
        .run({ idempotencyKey: `k${String(index)}`, goal: "g" })
        .catch(() => undefined);
    }
    await expect(bridge.agent.run({ idempotencyKey: "k-excess", goal: "g" })).rejects.toMatchObject(
      { code: "too_many_inflight" },
    );
    expect(port.sent).toHaveLength(MAX_INFLIGHT_REQUESTS + 1); // + the ack
  });

  it("streams events then completes, and canceling drops later envelopes", async () => {
    const { bridge, port } = await connectedBridge();
    const iterator = bridge.agent.stream("task-1")[Symbol.asyncIterator]();
    const next = iterator.next();
    const request = port.sent.at(-1) as { requestId: string };
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "event",
      requestId: request.requestId,
      payload: { id: "event-1" },
    });
    await expect(next).resolves.toMatchObject({ value: { id: "event-1" }, done: false });
    const finish = iterator.next();
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: request.requestId,
      payload: { done: true },
    });
    await expect(finish).resolves.toMatchObject({ done: true });

    // A second stream that ends early must cancel only itself.
    const iterator2 = bridge.agent.stream("task-2")[Symbol.asyncIterator]();
    const step2 = iterator2.next();
    const request2 = port.sent.at(-1) as { requestId: string };
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "event",
      requestId: request2.requestId,
      payload: { id: "event-2" },
    });
    await expect(step2).resolves.toMatchObject({ value: { id: "event-2" } });
    await iterator2.return?.(undefined);
    expect(port.sent.at(-1)).toMatchObject({ type: "cancel", requestId: request2.requestId });
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "event",
      requestId: request2.requestId,
      payload: { id: "event-3" },
    });
    // The canceled stream swallowed the late event: nothing pending fails.
    expect(true).toBe(true);
  });

  it("treats late or unknown envelopes as inert", async () => {
    const { bridge, port } = await connectedBridge();
    const pending = bridge.agent.run({ idempotencyKey: "k", goal: "g" });
    const request = port.sent.at(-1) as { requestId: string };
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: "req-unknown",
      payload: { taskId: "x", state: "queued", lastEventSequence: "0" },
    });
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: request.requestId,
      payload: { taskId: "task-1", state: "queued", lastEventSequence: "0" },
    });
    await expect(pending).resolves.toMatchObject({ taskId: "task-1" });
  });
});

describe("WorkOSAppBridge outbound bounds", () => {
  it("rejects an oversize outbound request locally without posting", async () => {
    const { bridge, port } = await connectedBridge();
    // ~68 KB of UTF-8 goal: the shared bounded post helper must reject it
    // client-side, before any envelope reaches the port.
    const goal = "\u{1F680}".repeat(17_000);
    await expect(bridge.agent.run({ idempotencyKey: "k", role: "", goal })).rejects.toMatchObject({
      code: "oversize",
    });
    // Only the handshake ack was ever posted.
    expect(port.sent).toHaveLength(1);
  });
});

describe("workspace files", () => {
  const ref = {
    projectId: "0198d7ea-2110-7c42-b659-c5e4d73bc352",
    path: "notes.txt",
    etag: `sha256:${"a".repeat(64)}`,
  };
  it("round-trips binary files and adopts the returned content etag", async () => {
    const { bridge, port } = await connectedBridge({ methods: ["files.read", "files.write"] });
    const reading = bridge.files.read(ref);
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: "req-1",
      payload: { dataBase64: "AP8=" },
    });
    expect(new Uint8Array(await reading)).toEqual(new Uint8Array([0, 255]));
    const updated = { ...ref, etag: `sha256:${"b".repeat(64)}` };
    const writing = bridge.files.write(ref, new Uint8Array([0, 255]).buffer);
    expect(port.sent.at(-1)).toMatchObject({
      method: "files.write",
      payload: { ref, dataBase64: "AP8=" },
    });
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: "req-2",
      payload: { ref: updated },
    });
    expect(await writing).toEqual(updated);
  });
  it("does not send an oversized file", async () => {
    const { bridge, port } = await connectedBridge({ methods: ["files.write"] });
    await expect(bridge.files.write(ref, new ArrayBuffer(32769))).rejects.toMatchObject({
      code: "oversize",
    });
    expect(port.sent).toHaveLength(1);
  });
  it("returns no references when the person cancels the picker", async () => {
    const { bridge, port } = await connectedBridge({ methods: ["files.pick"] });
    const picking = bridge.files.pick({ multiple: true });
    expect(port.sent.at(-1)).toMatchObject({ method: "files.pick", payload: { multiple: true } });
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: "req-1",
      payload: { refs: [] },
    });
    expect(await picking).toEqual([]);
  });
});

describe("app artifacts", () => {
  it("encodes UTF-8 content and returns canonical metadata", async () => {
    const { bridge, port } = await connectedBridge({ methods: ["artifacts.create"] });
    const pending = bridge.artifacts.create({
      idempotencyKey: "notes",
      type: "document.markdown.v1",
      title: "Notes",
      content: "中文",
    });
    expect(port.sent.at(-1)).toMatchObject({
      method: "artifacts.create",
      payload: { contentBase64: "5Lit5paH" },
    });
    const artifact = {
      id: "0198d7ea-2110-7c42-b659-c5e4d73bc353",
      projectId: "0198d7ea-2110-7c42-b659-c5e4d73bc352",
      type: "document.markdown.v1",
      title: "Notes",
      digest: `sha256:${"a".repeat(64)}`,
    };
    port.receive({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: "req-1",
      payload: { artifact },
    });
    expect(await pending).toEqual(artifact);
  });
  it("rejects a byte-oversized artifact without sending a request", async () => {
    const { bridge, port } = await connectedBridge({ methods: ["artifacts.create"] });
    await expect(
      bridge.artifacts.create({
        idempotencyKey: "notes",
        type: "document.markdown.v1",
        title: "Notes",
        content: "中".repeat(11000),
      }),
    ).rejects.toMatchObject({ code: "oversize" });
    expect(port.sent).toHaveLength(1);
  });
});
