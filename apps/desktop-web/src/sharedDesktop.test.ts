// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  DesktopInitializationSchema,
  DesktopStateSchema,
  WatchDesktopResponseSchema,
  type DesktopState,
  type WatchDesktopResponse,
} from "@workos/protocol";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SharedDesktop, readDesktopProjection } from "./sharedDesktop.js";

const instances: SharedDesktop[] = [];
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((complete) => {
    resolve = complete;
  });
  return { promise, resolve };
}
afterEach(() => {
  instances.forEach((instance) => {
    instance.stop();
  });
  instances.length = 0;
  localStorage.clear();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
function fixture(
  initial = create(DesktopStateSchema, { revision: 1n, activeProjectId: "project-a" }),
) {
  let state = initial;
  const watchers = new Set<(event: WatchDesktopResponse) => void>();
  const get = vi.fn<
    (request: unknown, options: { signal: AbortSignal }) => Promise<{ state: DesktopState }>
  >(() => Promise.resolve({ state }));
  const apply = vi.fn(() => Promise.resolve({ state }));
  const watch = vi.fn(async function* (_request: unknown, options: { signal: AbortSignal }) {
    let resolve: ((event: WatchDesktopResponse | undefined) => void) | undefined;
    const queue: WatchDesktopResponse[] = [];
    const receive = (event: WatchDesktopResponse) => {
      if (resolve) {
        const next = resolve;
        resolve = undefined;
        next(event);
      } else queue.push(event);
    };
    watchers.add(receive);
    const stop = () => {
      resolve?.(undefined);
    };
    options.signal.addEventListener("abort", stop);
    try {
      while (!options.signal.aborted) {
        const event =
          queue.shift() ??
          (await new Promise<WatchDesktopResponse | undefined>((next) => {
            resolve = next;
          }));
        if (!event) break;
        yield event;
      }
    } finally {
      watchers.delete(receive);
      options.signal.removeEventListener("abort", stop);
    }
  });
  const client = {
    getDesktop: get,
    applyDesktopOperation: apply,
    watchDesktop: watch,
  } as unknown as WorkOSClients["desktop"];
  return {
    get,
    apply,
    watch,
    setState(next: DesktopState) {
      state = next;
    },
    publish(next: DesktopState, resetRequired = false) {
      state = next;
      for (const receive of watchers)
        receive(create(WatchDesktopResponseSchema, { state, resetRequired }));
    },
    instance() {
      const instance = new SharedDesktop(client);
      instances.push(instance);
      instance.start(create(DesktopInitializationSchema, { windows: [{ kind: "home" }] }));
      return instance;
    },
  };
}

describe("Shared desktop projection", () => {
  it("two devices receive canonical state, persist the cursor, and never broadcast incoming changes", async () => {
    const backend = fixture();
    const a = backend.instance(),
      b = backend.instance();
    await vi.waitFor(() => {
      expect(backend.watch).toHaveBeenCalledTimes(2);
    });
    const latest = create(DesktopStateSchema, {
      revision: 4n,
      activeProjectId: "project-b",
      windows: [
        {
          id: "window",
          target: {
            kind: "agent-sessions",
            projectId: "project-b",
            resource: { case: "sessionId", value: "session" },
          },
        },
      ],
      focusedWindowId: "window",
    });
    backend.publish(latest);
    await vi.waitFor(() => {
      expect(a.current.state?.revision).toBe(4n);
    });
    expect(b.current.state).toEqual(latest);
    expect(readDesktopProjection()).toEqual(latest);
    expect(backend.apply).not.toHaveBeenCalled();
    backend.publish(create(DesktopStateSchema, { revision: 2n, activeProjectId: "project-a" }));
    await Promise.resolve();
    expect(a.current.state?.revision).toBe(4n);
  });
  it("an explicit reset replaces a future cursor and resumes from the reset projection", async () => {
    const backend = fixture(create(DesktopStateSchema, { revision: 99n, activeProjectId: "old" }));
    const device = backend.instance();
    await vi.waitFor(() => {
      expect(device.current.connection).toBe("connected");
    });
    backend.publish(create(DesktopStateSchema, { revision: 3n, activeProjectId: "reset" }), true);
    await vi.waitFor(() => {
      expect(device.current.state?.revision).toBe(3n);
    });
    expect(readDesktopProjection()?.activeProjectId).toBe("reset");
  });
  it("rejects offline commands without queueing them and reads fresh authority on return", async () => {
    const backend = fixture();
    const device = backend.instance();
    await vi.waitFor(() => {
      expect(device.current.connection).toBe("connected");
    });
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    await expect(
      device.apply({ case: "switchProject", value: { projectId: "stale" } }),
    ).rejects.toThrow("reconnecting");
    expect(backend.apply).not.toHaveBeenCalled();
    backend.publish(create(DesktopStateSchema, { revision: 3n, activeProjectId: "remote" }));
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(true);
    window.dispatchEvent(new Event("online"));
    await vi.waitFor(() => {
      expect(device.current.state?.activeProjectId).toBe("remote");
    });
    expect(backend.apply).not.toHaveBeenCalled();
  });
  it("does not replay an operation with a lost response", async () => {
    const backend = fixture();
    const device = backend.instance();
    await vi.waitFor(() => {
      expect(device.current.connection).toBe("connected");
    });
    backend.apply.mockImplementationOnce(() => {
      backend.publish(create(DesktopStateSchema, { revision: 2n, activeProjectId: "committed" }));
      return Promise.reject(new ConnectError("connection lost", Code.Unavailable));
    });
    await expect(
      device.apply({ case: "switchProject", value: { projectId: "committed" } }),
    ).rejects.toThrow();
    await vi.waitFor(() => {
      expect(device.current.connection).toBe("connected");
    });
    expect(device.current.state?.activeProjectId).toBe("committed");
    expect(backend.apply).toHaveBeenCalledOnce();
  });
  it("clears references on loss of authentication, never paints a previous cached desktop", async () => {
    const previous = fixture().instance();
    await vi.waitFor(() => {
      expect(previous.current.connection).toBe("connected");
    });
    previous.stop();
    const backend = fixture();
    backend.get.mockRejectedValue(new ConnectError("revoked", Code.Unauthenticated));
    const device = backend.instance();
    expect(device.current.state).toBeUndefined();
    await vi.waitFor(() => {
      expect(device.current.connection).toBe("unavailable");
    });
    expect(readDesktopProjection()).toBeUndefined();
  });
  it("advances through a silent stream without overwriting newer streamed revisions", async () => {
    vi.useFakeTimers();
    const backend = fixture();
    const device = backend.instance();
    await vi.advanceTimersByTimeAsync(0);
    backend.setState(create(DesktopStateSchema, { revision: 7n, activeProjectId: "remote" }));
    await vi.advanceTimersByTimeAsync(5000);
    expect(device.current.state?.revision).toBe(7n);
    expect(readDesktopProjection()?.activeProjectId).toBe("remote");
    backend.publish(create(DesktopStateSchema, { revision: 9n }));
    await vi.advanceTimersByTimeAsync(0);
    backend.setState(create(DesktopStateSchema, { revision: 8n }));
    await vi.advanceTimersByTimeAsync(5000);
    expect(device.current.state?.revision).toBe(9n);
    expect(backend.get).toHaveBeenCalledTimes(3);
    expect(backend.watch).toHaveBeenCalledOnce();
    expect(backend.apply).not.toHaveBeenCalled();
  });
  it("lets a slow authoritative read finish before scheduling another read", async () => {
    vi.useFakeTimers();
    const backend = fixture();
    const device = backend.instance();
    await vi.advanceTimersByTimeAsync(0);
    const pending = deferred<{ state: DesktopState }>();
    backend.get.mockReturnValueOnce(pending.promise);
    await vi.advanceTimersByTimeAsync(5000);
    const signal = backend.get.mock.calls[1]?.[1].signal;
    await vi.advanceTimersByTimeAsync(15000);
    expect(backend.get).toHaveBeenCalledTimes(2);
    expect(signal?.aborted).toBe(false);
    expect(backend.watch).toHaveBeenCalledOnce();
    pending.resolve({ state: create(DesktopStateSchema, { revision: 5n }) });
    await vi.advanceTimersByTimeAsync(0);
    expect(device.current.state?.revision).toBe(5n);
    await vi.advanceTimersByTimeAsync(4999);
    expect(backend.get).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(backend.get).toHaveBeenCalledTimes(3);
  });
  it("discards a late read from before a reconnect even when its transport ignores abort", async () => {
    vi.useFakeTimers();
    const backend = fixture();
    const device = backend.instance();
    await vi.advanceTimersByTimeAsync(0);
    const pending = deferred<{ state: DesktopState }>();
    backend.get.mockReturnValueOnce(pending.promise);
    await vi.advanceTimersByTimeAsync(5000);
    const signal = backend.get.mock.calls[1]?.[1].signal;
    backend.setState(create(DesktopStateSchema, { revision: 10n }));
    window.dispatchEvent(new Event("online"));
    await vi.advanceTimersByTimeAsync(0);
    expect(signal?.aborted).toBe(true);
    expect(device.current.state?.revision).toBe(10n);
    pending.resolve({ state: create(DesktopStateSchema, { revision: 99n }) });
    await vi.advanceTimersByTimeAsync(0);
    expect(device.current.state?.revision).toBe(10n);
    await vi.advanceTimersByTimeAsync(5000);
    expect(backend.get).toHaveBeenCalledTimes(4);
  });
  it("discards reads after stop and never schedules another poll", async () => {
    vi.useFakeTimers();
    const backend = fixture();
    const device = backend.instance();
    await vi.advanceTimersByTimeAsync(0);
    const pending = deferred<{ state: DesktopState }>();
    backend.get.mockReturnValueOnce(pending.promise);
    await vi.advanceTimersByTimeAsync(5000);
    device.stop();
    pending.resolve({ state: create(DesktopStateSchema, { revision: 99n }) });
    await vi.advanceTimersByTimeAsync(20000);
    expect(device.current.state?.revision).toBe(1n);
    expect(backend.get).toHaveBeenCalledTimes(2);
    expect(backend.watch.mock.calls[0]?.[1].signal.aborted).toBe(true);
  });
  it("does not resurrect a pre-reset snapshot when a slow read finishes", async () => {
    vi.useFakeTimers();
    const backend = fixture(create(DesktopStateSchema, { revision: 99n }));
    const device = backend.instance();
    await vi.advanceTimersByTimeAsync(0);
    const pending = deferred<{ state: DesktopState }>();
    backend.get.mockReturnValueOnce(pending.promise);
    await vi.advanceTimersByTimeAsync(5000);
    backend.publish(create(DesktopStateSchema, { revision: 3n }), true);
    await vi.advanceTimersByTimeAsync(0);
    pending.resolve({ state: create(DesktopStateSchema, { revision: 100n }) });
    await vi.advanceTimersByTimeAsync(0);
    expect(device.current.state?.revision).toBe(3n);
    await vi.advanceTimersByTimeAsync(5000);
    expect(backend.get).toHaveBeenCalledTimes(3);
  });
  it("keeps the watch alive through a transient read failure and recovers on the next read", async () => {
    vi.useFakeTimers();
    const backend = fixture();
    const device = backend.instance();
    await vi.advanceTimersByTimeAsync(0);
    backend.get.mockRejectedValueOnce(
      new ConnectError("temporarily unavailable", Code.Unavailable),
    );
    await vi.advanceTimersByTimeAsync(5000);
    expect(device.current.connection).toBe("reconnecting");
    expect(backend.watch.mock.calls[0]?.[1].signal.aborted).toBe(false);
    backend.setState(create(DesktopStateSchema, { revision: 7n }));
    await vi.advanceTimersByTimeAsync(5000);
    expect(device.current.connection).toBe("connected");
    expect(device.current.state?.revision).toBe(7n);
    expect(backend.watch).toHaveBeenCalledOnce();
  });
  it.each([Code.Unauthenticated, Code.PermissionDenied])(
    "clears the projection and stops all synchronization after read authorization failure %s",
    async (code) => {
      vi.useFakeTimers();
      const backend = fixture();
      const device = backend.instance();
      await vi.advanceTimersByTimeAsync(0);
      backend.get.mockRejectedValueOnce(new ConnectError("revoked", code));
      await vi.advanceTimersByTimeAsync(5000);
      expect(device.current).toEqual({ connection: "unavailable" });
      expect(readDesktopProjection()).toBeUndefined();
      expect(backend.watch.mock.calls[0]?.[1].signal.aborted).toBe(true);
      window.dispatchEvent(new Event("online"));
      await vi.advanceTimersByTimeAsync(20000);
      expect(backend.get).toHaveBeenCalledTimes(2);
      expect(backend.watch).toHaveBeenCalledOnce();
    },
  );
});
