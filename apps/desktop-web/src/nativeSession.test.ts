import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkOSClients } from "@workos/agent-sdk";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NativeSessionLease } from "./nativeSession.js";

function expiresIn(milliseconds: number) {
  const expires = Date.now() + milliseconds;
  return {
    seconds: BigInt(Math.floor(expires / 1000)),
    nanos: (expires % 1000) * 1_000_000,
  };
}

function fixture() {
  const attachSurface = vi.fn().mockImplementation(() =>
    Promise.resolve({
      session: { id: "session-1", workloadGeneration: 3n },
      attachment: {
        id: "attachment-1",
        controls: true,
        controlGeneration: 7n,
        controlExpiresAt: expiresIn(60_000),
      },
    }),
  );
  const renewSurfaceControl = vi.fn().mockImplementation(() =>
    Promise.resolve({
      attachment: {
        id: "attachment-1",
        controls: true,
        controlGeneration: 7n,
        controlExpiresAt: expiresIn(60_000),
      },
    }),
  );
  const requestSurfaceControl = vi.fn().mockImplementation(() =>
    Promise.resolve({
      attachment: {
        id: "attachment-1",
        controls: true,
        controlGeneration: 8n,
        controlExpiresAt: expiresIn(60_000),
      },
    }),
  );
  const clients = {
    surfaceContinuity: {
      attachSurface,
      renewSurfaceControl,
      requestSurfaceControl,
      detachSurface: vi.fn().mockResolvedValue({}),
    },
  } as unknown as WorkOSClients;
  return { clients, attachSurface, renewSurfaceControl, requestSurfaceControl };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("NativeSessionLease controller renewal", () => {
  it("renews only the current attachment and generation before expiry", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-26T10:00:00Z"));
    const f = fixture();
    const lease = new NativeSessionLease(true);
    const handle = lease.acquire(f.clients, "project-1", "session-1", 3n);
    await handle.session;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(f.renewSurfaceControl).toHaveBeenCalledExactlyOnceWith({
      surfaceSessionId: "session-1",
      attachmentId: "attachment-1",
      expectedControlGeneration: 7n,
      expectedWorkloadGeneration: 3n,
    });
    expect(f.requestSurfaceControl).not.toHaveBeenCalled();
    expect(await handle.controls).toBe(true);
    lease.dispose();
  });

  it("fails closed on renewal denial and waits for an explicit takeover", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-26T10:00:00Z"));
    const f = fixture();
    f.renewSurfaceControl.mockRejectedValue(
      new ConnectError("stale control", Code.PermissionDenied),
    );
    const lease = new NativeSessionLease(true);
    const handle = lease.acquire(f.clients, "project-1", "session-1", 3n);
    const changes = vi.fn();
    handle.onControlChange(changes);
    await handle.session;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(await handle.controls).toBe(false);
    expect(changes).toHaveBeenCalledWith(false);
    expect(f.requestSurfaceControl).not.toHaveBeenCalled();
    await handle.requestControl();
    expect(f.requestSurfaceControl).toHaveBeenCalledTimes(1);
    expect(await handle.controls).toBe(true);
    expect(await handle.controlGeneration()).toBe(8n);
    lease.dispose();
  });

  it("reattaches only after explicit takeover when the old attachment was swept", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-26T10:00:00Z"));
    const f = fixture();
    f.renewSurfaceControl.mockRejectedValue(new ConnectError("expired", Code.PermissionDenied));
    f.requestSurfaceControl
      .mockRejectedValueOnce(new ConnectError("attachment gone", Code.NotFound))
      .mockImplementationOnce(() =>
        Promise.resolve({
          attachment: {
            id: "attachment-2",
            controls: true,
            controlGeneration: 9n,
            controlExpiresAt: expiresIn(60_000),
          },
        }),
      );
    f.attachSurface
      .mockImplementationOnce(() =>
        Promise.resolve({
          session: { id: "session-1", workloadGeneration: 3n },
          attachment: {
            id: "attachment-1",
            controls: true,
            controlGeneration: 7n,
            controlExpiresAt: expiresIn(60_000),
          },
        }),
      )
      .mockImplementationOnce(() =>
        Promise.resolve({
          session: { id: "session-1", workloadGeneration: 3n },
          attachment: { id: "attachment-2", controls: false, controlGeneration: 8n },
        }),
      );
    const lease = new NativeSessionLease(true);
    const handle = lease.acquire(f.clients, "project-1", "session-1", 3n);
    await handle.session;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(f.attachSurface).toHaveBeenCalledTimes(1);
    await handle.requestControl();
    expect(f.attachSurface).toHaveBeenCalledTimes(2);
    expect(await handle.attachmentId()).toBe("attachment-2");
    expect(f.attachSurface).toHaveBeenLastCalledWith(
      expect.objectContaining({
        workloadId: "session-1",
        expectedWorkloadGeneration: 3n,
      }),
    );
    lease.dispose();
  });
});
