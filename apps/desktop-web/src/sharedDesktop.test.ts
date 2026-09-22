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
afterEach(() => {
  instances.forEach((instance) => {
    instance.stop();
  });
  instances.length = 0;
  localStorage.clear();
  vi.restoreAllMocks();
});
function fixture(
  initial = create(DesktopStateSchema, { revision: 1n, activeProjectId: "project-a" }),
) {
  let state = initial;
  const watchers = new Set<(event: WatchDesktopResponse) => void>();
  const get = vi.fn(() => Promise.resolve({ state }));
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
});
