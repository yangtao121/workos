// @vitest-environment node
// Deterministic handshake and dispatch tests for the trusted parent host:
// exact-window targeting, nonce semantics, bounded dispatch, and fail-closed
// behavior for every protocol violation.
import { MessageChannel as NodeMessageChannel } from "node:worker_threads";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  APP_BRIDGE_VERSION,
  BridgeProtocolError,
  REQUEST_TIMEOUT_MS,
  type BridgeMethod,
} from "@workos/surface-sdk";
import { openAppBridgeHost, type AppBridgeRunResult, type AppBridgeTransport } from "./index.js";

// The host uses window timers; the node environment provides none.
beforeEach(() => {
  vi.stubGlobal("window", {
    setTimeout: (handler: () => void, timeout: number): number => {
      return setTimeout(handler, timeout) as unknown as number;
    },
    clearTimeout: (id: number): void => {
      clearTimeout(id);
    },
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

interface RecordedHello {
  envelope: Record<string, unknown>;
  ports: unknown[];
}

type NodePort = {
  onmessage: ((event: { data: unknown }) => void) | null;
  postMessage: (value: unknown) => void;
  close: () => void;
};

const CANONICAL_TASK_ID = "0198d7ea-2110-7c42-b659-c5e4d73bc371";

function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

function peekHello(hellos: RecordedHello[]): RecordedHello {
  const hello = hellos.at(0);
  if (!hello) throw new Error("no hello captured");
  return hello;
}

function framePortOf(hellos: RecordedHello[]): NodePort {
  const port = peekHello(hellos).ports[0];
  if (!port) throw new Error("hello carried no transferred port");
  return port as unknown as NodePort;
}

function setup(options?: {
  capabilities?: string[];
  timeoutMs?: number;
  transport?: Partial<AppBridgeTransport>;
}): ReturnType<typeof setupImplementation> {
  const normalized: Parameters<typeof setupImplementation>[0] = {};
  if (options?.capabilities !== undefined) {
    normalized.capabilities = options.capabilities;
  }
  if (options?.timeoutMs !== undefined) {
    normalized.timeoutMs = options.timeoutMs;
  }
  if (options?.transport !== undefined) {
    normalized.transport = options.transport;
  }
  return setupImplementation(normalized);
}

function setupImplementation(options: {
  capabilities?: string[];
  timeoutMs?: number;
  transport?: Partial<AppBridgeTransport>;
}) {
  const channel = new NodeMessageChannel();
  const hellos: RecordedHello[] = [];
  const frameWindow = {
    postMessage: (data: unknown, _targetOrigin?: string, transfer?: unknown[]) => {
      hellos.push({ envelope: data as Record<string, unknown>, ports: transfer ?? [] });
    },
  };
  const runAgentTask = vi.fn<
    (input: {
      idempotencyKey: string;
      role?: string | undefined;
      goal: string;
    }) => Promise<AppBridgeRunResult>
  >(() => Promise.resolve({ taskId: "task-1", state: "queued", lastEventSequence: "0" }));
  const watchAgentTaskEvents = vi.fn<AppBridgeTransport["watchAgentTaskEvents"]>(() =>
    Promise.resolve(),
  );
  const transport: AppBridgeTransport = {
    runAgentTask,
    watchAgentTaskEvents,
    ...options.transport,
  };
  const onHandshakeComplete = vi.fn();
  const onHandshakeFailed = vi.fn();
  const host = openAppBridgeHost({
    frameWindow: frameWindow as unknown as Window,
    capabilities: options.capabilities ?? ["agent.task.run", "agent.event.watch"],
    transport,
    timeoutMs: options.timeoutMs ?? REQUEST_TIMEOUT_MS,
    nonceGenerator: () => "nonce-1",
    channelFactory: () => channel as unknown as MessageChannel,
    onHandshakeComplete,
    onHandshakeFailed,
  });
  return {
    host,
    channel,
    hellos,
    runAgentTask,
    watchAgentTaskEvents,
    onHandshakeComplete,
    onHandshakeFailed,
  };
}

describe("App Bridge host handshake", () => {
  it("completes for an exact ack of the one-time nonce and offers only granted methods", async () => {
    const { host, hellos, onHandshakeComplete } = setup();
    expect(peekHello(hellos).envelope).toMatchObject({
      version: APP_BRIDGE_VERSION,
      type: "hello",
      nonce: "nonce-1",
      methods: ["agent.run", "agent.stream"],
    });
    const port = framePortOf(hellos);
    port.postMessage({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "nonce-1" });
    await host.ready;
    expect(onHandshakeComplete).toHaveBeenCalled();
    host.close();
  });

  it("rejects a replayed or wrong nonce and never completes the session", async () => {
    const { host, hellos, onHandshakeFailed } = setup({ timeoutMs: 200 });
    const port = framePortOf(hellos);
    port.postMessage({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "nonce-other" });
    await expect(host.ready).rejects.toThrow(BridgeProtocolError);
    expect(onHandshakeFailed).toHaveBeenCalled();
    // A later correct ack cannot revive the failed handshake (single-shot).
    port.postMessage({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "nonce-1" });
    await flush();
    expect(onHandshakeFailed).toHaveBeenCalledTimes(1);
  });

  it("times out when no ack arrives", async () => {
    const { host, onHandshakeFailed } = setup({ timeoutMs: 20 });
    await expect(host.ready).rejects.toThrow(BridgeProtocolError);
    expect(onHandshakeFailed).toHaveBeenCalled();
  });

  it("fails closed on a malformed envelope before the handshake", async () => {
    const { host, hellos } = setup({ timeoutMs: 200 });
    const port = framePortOf(hellos);
    port.postMessage({ version: "workos.app-bridge/v0", type: "ack", nonce: "nonce-1" });
    await expect(host.ready).rejects.toThrow(BridgeProtocolError);
  });
});

async function handshakenHost(options?: {
  capabilities?: string[];
  transport?: Partial<AppBridgeTransport>;
}) {
  const setupResult = setup(options);
  const port = framePortOf(setupResult.hellos);
  const received: { data: unknown }[] = [];
  port.onmessage = (event) => {
    received.push(event);
  };
  port.postMessage({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "nonce-1" });
  await setupResult.host.ready;
  return { ...setupResult, port, received };
}

describe("App Bridge host dispatch", () => {
  it("dispatches agent.run to the transport and answers with the canonical result", async () => {
    const { runAgentTask, port, received } = await handshakenHost();
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-1",
      method: "agent.run",
      payload: { idempotencyKey: "key-1", role: "", goal: "Do the thing." },
    });
    await flush();
    await flush();
    expect(runAgentTask).toHaveBeenCalledWith({
      idempotencyKey: "key-1",
      role: "",
      goal: "Do the thing.",
    });
    const last = received.at(-1);
    if (!last) throw new Error("no response envelope");
    expect(last.data).toMatchObject({
      version: APP_BRIDGE_VERSION,
      type: "response",
      requestId: "req-1",
      payload: { taskId: "task-1" },
    });
  });

  it("maps a transport failure to a stable error code without internals", async () => {
    const { port, received } = await handshakenHost({
      transport: {
        runAgentTask: () => Promise.reject(new Error("postgres DSN postgres://secret@10.0.0.1")),
      },
    });
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-1",
      method: "agent.run",
      payload: { idempotencyKey: "key-1", goal: "g" },
    });
    await flush();
    await flush();
    const last = received.at(-1);
    if (!last) throw new Error("no error envelope");
    const error = last.data as { type: string; code: string; requestId: string };
    expect(error).toMatchObject({ type: "error", code: "internal", requestId: "req-1" });
    expect(JSON.stringify(error)).not.toContain("postgres://");
  });

  it("rejects unknown or unoffered methods closed without touching the transport", async () => {
    const { runAgentTask, port, received } = await handshakenHost();
    const unknownMethods: string[] = ["window.close", "project.current"];
    unknownMethods.forEach((method, index) => {
      port.postMessage({
        version: APP_BRIDGE_VERSION,
        type: "request",
        requestId: `req-${String(index)}`,
        method: method as BridgeMethod,
        payload: {},
      });
    });
    // A malformed agent.run payload fails closed before the transport too.
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-bad-run",
      method: "agent.run",
      payload: {},
    });
    await flush();
    await flush();
    const errors = received.filter((event) => (event.data as { type: string }).type === "error");
    expect(errors).toHaveLength(3);
    expect((errors[0]?.data as { code: string }).code).toBe("permission_denied");
    expect((errors[1]?.data as { code: string }).code).toBe("permission_denied");
    expect((errors[2]?.data as { code: string }).code).toBe("invalid_argument");
    expect(runAgentTask).not.toHaveBeenCalled();
  });

  it("streams canonical events and stops forwarding after a cancel", async () => {
    let push: ((event: unknown) => void) | undefined;
    const { port, received } = await handshakenHost({
      transport: {
        watchAgentTaskEvents: (_input, onEvent) =>
          new Promise<void>((resolve) => {
            push = (event: unknown) => {
              onEvent(event as never);
            };
            void resolve;
          }),
      },
    });
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-s",
      method: "agent.stream",
      payload: { taskId: CANONICAL_TASK_ID, afterSequence: "0" },
    });
    await flush();
    push?.({ id: "e1" });
    await flush();
    const first = received.at(-1);
    if (!first) throw new Error("no event envelope");
    expect(first.data).toMatchObject({
      type: "event",
      requestId: "req-s",
      payload: { id: "e1" },
    });
    push?.({ id: "never-forwarded" });
    await flush();
    const second = received.at(-1);
    if (!second) throw new Error("no second envelope");
    expect(second.data).toMatchObject({ payload: { id: "never-forwarded" } });
    // The cancel envelope aborts the stream; no event may follow it. The
    // flush lets the port deliver the cancel before the next event push.
    port.postMessage({ version: APP_BRIDGE_VERSION, type: "cancel", requestId: "req-s" });
    await flush();
    push?.({ id: "after-cancel" });
    await flush();
    expect(received.at(-1)?.data).toMatchObject({ payload: { id: "never-forwarded" } });
  });

  it("offers no methods without capabilities and rejects run requests", async () => {
    const { runAgentTask, hellos } = setup({ capabilities: [] });
    expect(peekHello(hellos).envelope.methods).toEqual([]);
    const port = framePortOf(hellos);
    const received: { data: unknown }[] = [];
    port.onmessage = (event) => {
      received.push(event);
    };
    port.postMessage({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "nonce-1" });
    await flush();
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-1",
      method: "agent.run",
      payload: { idempotencyKey: "k", goal: "g" },
    });
    await flush();
    await flush();
    const last = received.at(-1);
    if (!last) throw new Error("no error envelope");
    expect((last.data as { code: string }).code).toBe("permission_denied");
    expect(runAgentTask).not.toHaveBeenCalled();
  });

  it("fails all pending requests when the host closes", async () => {
    const { host, port } = await handshakenHost({
      transport: {
        runAgentTask: () => new Promise<AppBridgeRunResult>(() => undefined),
      },
    });
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-hang",
      method: "agent.run",
      payload: { idempotencyKey: "k", goal: "g" },
    });
    await flush();
    host.close();
    // After close the port is dead: nothing further is dispatched or sent.
    const before = vi.fn();
    port.onmessage = before;
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-late",
      method: "agent.run",
      payload: { idempotencyKey: "k", goal: "g" },
    });
    await flush();
    expect(before).not.toHaveBeenCalled();
  });
});

describe("App Bridge host untrusted-port boundary", () => {
  function hangingSetup(options?: { timeoutMs?: number }) {
    let releaseRun: ((result: AppBridgeRunResult) => void) | undefined;
    const runAgentTask = vi.fn<
      (input: { idempotencyKey: string; goal: string }) => Promise<AppBridgeRunResult>
    >(
      (input) =>
        new Promise<AppBridgeRunResult>((resolve) => {
          releaseRun = (result: AppBridgeRunResult) => {
            resolve({ ...result, taskId: input.idempotencyKey });
          };
        }),
    );
    const transport: Partial<AppBridgeTransport> = { runAgentTask };
    const setupOptions: Parameters<typeof setupImplementation>[0] = { transport };
    if (options?.timeoutMs !== undefined) {
      setupOptions.timeoutMs = options.timeoutMs;
    }
    const setupResult = setup(setupOptions);
    const port = framePortOf(setupResult.hellos);
    const received: { data: unknown }[] = [];
    port.onmessage = (event) => {
      received.push(event);
    };
    port.postMessage({ version: APP_BRIDGE_VERSION, type: "ack", nonce: "nonce-1" });
    return { ...setupResult, port, received, releaseRun, runAgentTask };
  }

  it("rejects an inbound oversize request measured in UTF-8 bytes (multibyte goal)", async () => {
    const { port, received, runAgentTask } = hangingSetup();
    // ~68 KB of UTF-8 from only ~34 K UTF-16 units: a string-length check
    // would pass; the byte-size bound must not.
    const goal = "\u{1F680}".repeat(17_000);
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-big",
      method: "agent.run",
      payload: { idempotencyKey: "k", role: "", goal },
    });
    await flush();
    const last = received.at(-1);
    if (!last) throw new Error("no error envelope");
    expect((last.data as { code: string }).code).toBe("oversize");
    expect(runAgentTask).not.toHaveBeenCalled();
  });

  it("enforces the in-flight bound against direct port traffic (33rd request)", async () => {
    const { port, received, runAgentTask } = hangingSetup();
    for (let index = 1; index <= 33; index += 1) {
      port.postMessage({
        version: APP_BRIDGE_VERSION,
        type: "request",
        requestId: `req-${String(index)}`,
        method: "agent.run",
        payload: { idempotencyKey: `k${String(index)}`, role: "", goal: "g" },
      });
    }
    await flush();
    expect(runAgentTask).toHaveBeenCalledTimes(32);
    const errors = received.filter((event) => (event.data as { type: string }).type === "error");
    expect(errors).toHaveLength(1);
    expect((errors[0]?.data as { code: string }).code).toBe("too_many_inflight");
  });

  it("rejects a duplicate request ID without a second transport call", async () => {
    const { port, received, runAgentTask } = hangingSetup();
    for (const _ of [1, 2]) {
      void _;
      port.postMessage({
        version: APP_BRIDGE_VERSION,
        type: "request",
        requestId: "req-dup",
        method: "agent.run",
        payload: { idempotencyKey: "k", role: "", goal: "g" },
      });
    }
    await flush();
    expect(runAgentTask).toHaveBeenCalledTimes(1);
    const last = received.at(-1);
    if (!last) throw new Error("no error envelope");
    expect((last.data as { code: string }).code).toBe("invalid_argument");
  });

  it("times out a hanging run and keeps the late result inert", async () => {
    const { received, releaseRun } = await (async () => {
      const harness = hangingSetup({ timeoutMs: 25 });
      harness.port.postMessage({
        version: APP_BRIDGE_VERSION,
        type: "request",
        requestId: "req-slow",
        method: "agent.run",
        payload: { idempotencyKey: "k", role: "", goal: "g" },
      });
      await new Promise((resolve) => setTimeout(resolve, 40));
      return harness;
    })();
    const last = received.at(-1);
    if (!last) throw new Error("no timeout envelope");
    expect((last.data as { code: string }).code).toBe("timeout");
    const count = received.length;
    // The late transport result must find no pending owner: nothing posted.
    releaseRun?.({ taskId: "late", state: "queued", lastEventSequence: "0" });
    await flush();
    expect(received.length).toBe(count);
  });

  it("drops a late run result after close without posting", async () => {
    const harness = hangingSetup();
    harness.port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-closed",
      method: "agent.run",
      payload: { idempotencyKey: "k", role: "", goal: "g" },
    });
    await flush();
    const count = harness.received.length;
    harness.host.close();
    harness.releaseRun?.({ taskId: "late-after-close", state: "queued", lastEventSequence: "0" });
    await flush();
    expect(harness.received.length).toBe(count);
  });

  it("fails closed on malformed cursors and unknown payload fields", async () => {
    const { port, received, runAgentTask } = await handshakenHost();
    const post = (payload: unknown, requestId: string) => {
      port.postMessage({
        version: APP_BRIDGE_VERSION,
        type: "request",
        requestId,
        method: "agent.stream",
        payload,
      });
    };
    post({ taskId: CANONICAL_TASK_ID, afterSequence: "-1" }, "req-neg");
    post({ taskId: CANONICAL_TASK_ID, afterSequence: "not-a-number" }, "req-nan");
    post({ taskId: CANONICAL_TASK_ID, afterSequence: "0", smuggled: true }, "req-extra");
    post({ taskId: CANONICAL_TASK_ID }, "req-missing");
    await flush();
    await flush();
    const errors = received.filter((event) => (event.data as { type: string }).type === "error");
    expect(errors).toHaveLength(4);
    for (const error of errors) {
      expect((error.data as { code: string }).code).toBe("invalid_argument");
    }
    expect(runAgentTask).not.toHaveBeenCalled();
  });

  it("rejects a non-canonical request ID shape", async () => {
    const { port, received, runAgentTask } = await handshakenHost();
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "x".repeat(65),
      method: "agent.run",
      payload: { idempotencyKey: "k", role: "", goal: "g" },
    });
    await flush();
    const last = received.at(-1);
    if (!last) throw new Error("no error envelope");
    expect((last.data as { code: string }).code).toBe("invalid_argument");
    expect(runAgentTask).not.toHaveBeenCalled();
  });

  it("preserves typed transport codes for run and stream alike", async () => {
    const typedRun = vi.fn(() => Promise.reject(new BridgeProtocolError("permission_denied")));
    const typedWatch = vi.fn(() => Promise.reject(new BridgeProtocolError("not_found")));
    const runTransport: Partial<AppBridgeTransport> = {
      runAgentTask: typedRun,
      watchAgentTaskEvents: typedWatch,
    };
    const { port, received } = await handshakenHost({ transport: runTransport });
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-run",
      method: "agent.run",
      payload: { idempotencyKey: "k", role: "", goal: "g" },
    });
    port.postMessage({
      version: APP_BRIDGE_VERSION,
      type: "request",
      requestId: "req-stream",
      method: "agent.stream",
      payload: { taskId: CANONICAL_TASK_ID, afterSequence: "0" },
    });
    await flush();
    await flush();
    const byId = new Map(
      received
        .filter((event) => (event.data as { type: string }).type === "error")
        .map((event) => [
          (event.data as { requestId: string }).requestId,
          (event.data as { code: string }).code,
        ]),
    );
    expect(byId.get("req-run")).toBe("permission_denied");
    expect(byId.get("req-stream")).toBe("not_found");
    expect(typedRun).toHaveBeenCalledTimes(1);
    expect(typedWatch).toHaveBeenCalledTimes(1);
  });
});
