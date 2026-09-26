import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkOSClients } from "@workos/agent-sdk";
import { GreenfieldDisplayState, type GreenfieldWindowSnapshot } from "@workos/protocol";
import type { GreenfieldAttachment } from "./greenfieldWindowClient.js";

export type GreenfieldWindowConnection =
  | "connecting"
  | "connected"
  | "reconnecting"
  | "unavailable";

export interface GreenfieldWindowProjectionState {
  connection: GreenfieldWindowConnection;
  epoch: number;
  snapshot?: GreenfieldWindowSnapshot;
}

function validSnapshot(
  snapshot: GreenfieldWindowSnapshot,
  attachment: GreenfieldAttachment,
): boolean {
  if (
    snapshot.sessionId !== attachment.sessionId ||
    snapshot.workloadGeneration !== attachment.workloadGeneration ||
    snapshot.windows.length > 128
  )
    return false;
  const ids = new Set<string>();
  for (const item of snapshot.windows) {
    if (
      !item.id ||
      ids.has(item.id) ||
      !item.visualRect ||
      !item.contentRect ||
      item.visualRect.width < 1 ||
      item.visualRect.height < 1 ||
      item.contentRect.width < 1 ||
      item.contentRect.height < 1
    )
      return false;
    ids.add(item.id);
  }
  return snapshot.windows.every(
    (item) =>
      !item.parentWindowId || (item.parentWindowId !== item.id && ids.has(item.parentWindowId)),
  );
}

// WatchWindows sends whole snapshots. A new stream epoch invalidates every
// frame subscription; a stale cached graph is never emitted as connected.
export class GreenfieldWindowProjection {
  private state: GreenfieldWindowProjectionState = { connection: "connecting", epoch: 0 };
  private listeners = new Set<(state: GreenfieldWindowProjectionState) => void>();
  private controller?: AbortController;
  private retry?: ReturnType<typeof setTimeout>;
  private running = false;

  constructor(
    private readonly client: WorkOSClients["greenfieldWindows"],
    private readonly attachment: GreenfieldAttachment,
  ) {}

  subscribe(listener: (state: GreenfieldWindowProjectionState) => void) {
    this.listeners.add(listener);
    listener(this.state);
    return () => this.listeners.delete(listener);
  }

  private emit(state: GreenfieldWindowProjectionState) {
    this.state = state;
    for (const listener of this.listeners) listener(state);
  }

  start() {
    if (this.running) return;
    this.running = true;
    this.connect();
  }

  private connect = () => {
    if (!this.running) return;
    this.controller?.abort();
    clearTimeout(this.retry);
    const controller = new AbortController();
    this.controller = controller;
    const epoch = this.state.epoch + 1;
    this.emit({
      ...this.state,
      epoch,
      connection: this.state.snapshot ? "reconnecting" : "connecting",
    });
    void this.watch(epoch, controller);
  };

  private async watch(epoch: number, controller: AbortController) {
    let first = true;
    const live = () => this.running && !controller.signal.aborted && this.state.epoch === epoch;
    try {
      for await (const response of this.client.watchGreenfieldWindows(
        {
          sessionId: this.attachment.sessionId,
          attachmentId: this.attachment.attachmentId,
          expectedWorkloadGeneration: this.attachment.workloadGeneration,
        },
        { signal: controller.signal },
      )) {
        if (!live()) return;
        const snapshot = response.snapshot;
        if (!snapshot || !validSnapshot(snapshot, this.attachment))
          throw new Error("invalid native window snapshot");
        if (snapshot.state !== GreenfieldDisplayState.RUNNING) {
          this.emit({ connection: "unavailable", epoch });
          return;
        }
        if (!first && snapshot.revision < (this.state.snapshot?.revision ?? 0n))
          throw new Error("native window revision regressed");
        first = false;
        this.emit({ connection: "connected", epoch, snapshot });
      }
      if (!live()) return;
    } catch (error) {
      if (!live()) return;
      if (
        error instanceof ConnectError &&
        (error.code === Code.PermissionDenied || error.code === Code.Unauthenticated)
      ) {
        this.emit({ connection: "unavailable", epoch });
        return;
      }
    }
    this.emit({ ...this.state, connection: "reconnecting" });
    this.retry = setTimeout(this.connect, 1500);
  }

  stop() {
    this.running = false;
    this.controller?.abort();
    clearTimeout(this.retry);
    this.emit({ connection: "unavailable", epoch: this.state.epoch });
  }
}
