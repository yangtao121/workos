import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkOSClients } from "@workos/agent-sdk";
import { GreenfieldDisplayState, GreenfieldWindowSnapshotSchema } from "@workos/protocol";
import { describe, expect, it, vi } from "vitest";
import type { GreenfieldAttachment } from "./greenfieldWindowClient.js";
import {
  GreenfieldWindowProjection,
  type GreenfieldWindowProjectionState,
} from "./greenfieldWindowProjection.js";

const attachment: GreenfieldAttachment = {
  sessionId: "session-1",
  attachmentId: "attachment-1",
  workloadGeneration: 3n,
  controlGeneration: 8n,
  controls: false,
};

const snapshot = create(GreenfieldWindowSnapshotSchema, {
  sessionId: attachment.sessionId,
  workloadGeneration: attachment.workloadGeneration,
  revision: 1n,
  state: GreenfieldDisplayState.RUNNING,
  windows: [],
});

describe("resident Greenfield window projection", () => {
  it("marks a disconnected snapshot stale before retrying the same live attachment", async () => {
    let disconnect: (() => void) | undefined;
    const disconnected = new Promise<void>((resolve) => {
      disconnect = resolve;
    });
    const watchGreenfieldWindows = vi.fn(async function* () {
      yield { snapshot };
      await disconnected;
    });
    const projection = new GreenfieldWindowProjection(
      { watchGreenfieldWindows } as unknown as WorkOSClients["greenfieldWindows"],
      attachment,
    );
    const states: GreenfieldWindowProjectionState[] = [];
    projection.subscribe((state) => {
      states.push(state);
    });
    projection.start();
    await vi.waitFor(() => {
      expect(states.at(-1)?.connection).toBe("connected");
    });
    expect(watchGreenfieldWindows).toHaveBeenCalledWith(
      expect.objectContaining({ attachmentId: attachment.attachmentId }),
      expect.anything(),
    );
    disconnect?.();
    await vi.waitFor(() => {
      expect(states.at(-1)?.connection).toBe("reconnecting");
    });
    expect(states.at(-1)?.snapshot).toEqual(snapshot);
    projection.stop();
    expect(states.at(-1)?.connection).toBe("unavailable");
    expect(states.at(-1)?.snapshot).toBeUndefined();
  });

  it("removes native windows on a definitive attachment denial", async () => {
    const watchGreenfieldWindows = vi.fn(async function* () {
      yield { snapshot };
      await Promise.resolve();
      throw new ConnectError("revoked", Code.PermissionDenied);
    });
    const projection = new GreenfieldWindowProjection(
      { watchGreenfieldWindows } as unknown as WorkOSClients["greenfieldWindows"],
      attachment,
    );
    const states: GreenfieldWindowProjectionState[] = [];
    projection.subscribe((state) => {
      states.push(state);
    });
    projection.start();
    await vi.waitFor(() => {
      expect(states.at(-1)?.connection).toBe("unavailable");
    });
    expect(states.some((state) => state.connection === "connected")).toBe(true);
    expect(states.at(-1)?.snapshot).toBeUndefined();
    projection.stop();
  });
});
