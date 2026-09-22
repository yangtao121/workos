import { create, fromJsonString, toJsonString, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  DesktopStateSchema,
  type DesktopState,
  type DesktopInitialization,
  type ApplyDesktopOperationRequestSchema,
} from "@workos/protocol";

export type DesktopOperation = NonNullable<
  MessageInitShape<typeof ApplyDesktopOperationRequestSchema>["operation"]
>;
export type DesktopConnection = "connecting" | "connected" | "reconnecting" | "unavailable";
export type DesktopProjection = { state?: DesktopState; connection: DesktopConnection };
const CACHE_KEY = "workos.desktop-projection.v1";
const REFRESH_INTERVAL_MS = 5000;

// Only canonical references and a cursor are cached. A cached projection is never
// painted or sent as an operation before a fresh authenticated server read.
export function clearDesktopProjection() {
  try {
    localStorage.removeItem(CACHE_KEY);
  } catch {
    /* storage may be disabled */
  }
}
function persist(state: DesktopState) {
  try {
    localStorage.setItem(CACHE_KEY, toJsonString(DesktopStateSchema, state));
  } catch {
    /* optional cache */
  }
}
export function readDesktopProjection(): DesktopState | undefined {
  try {
    const value = localStorage.getItem(CACHE_KEY);
    return value ? fromJsonString(DesktopStateSchema, value) : undefined;
  } catch {
    return undefined;
  }
}

export class SharedDesktop {
  private snapshot: DesktopProjection = { connection: "connecting" };
  private listeners = new Set<(snapshot: DesktopProjection) => void>();
  private controller?: AbortController;
  private retry?: ReturnType<typeof setTimeout>;
  private refresh?: ReturnType<typeof setTimeout>;
  private generation = 0;
  private resetEpoch = 0;
  private running = false;
  private initialize?: DesktopInitialization;
  constructor(private readonly client: WorkOSClients["desktop"]) {}
  get current() {
    return this.snapshot;
  }
  subscribe(listener: (snapshot: DesktopProjection) => void) {
    this.listeners.add(listener);
    listener(this.snapshot);
    return () => {
      this.listeners.delete(listener);
    };
  }
  private emit(connection: DesktopConnection, state = this.snapshot.state) {
    this.snapshot = state ? { state, connection } : { connection };
    for (const listener of this.listeners) listener(this.snapshot);
  }
  private accept(state: DesktopState | undefined, reset = false) {
    if (!state || (!reset && this.snapshot.state && state.revision <= this.snapshot.state.revision))
      return;
    if (reset) ++this.resetEpoch;
    persist(state);
    this.emit("connected", state);
  }
  start(initial: DesktopInitialization) {
    this.initialize = initial;
    if (this.running) return;
    this.running = true;
    window.addEventListener("online", this.reconnect);
    document.addEventListener("visibilitychange", this.visible);
    this.reconnect();
  }
  private visible = () => {
    if (document.visibilityState === "visible") this.reconnect();
  };
  private reconnect = () => {
    if (!this.running) return;
    this.controller?.abort();
    clearTimeout(this.retry);
    clearTimeout(this.refresh);
    const generation = ++this.generation;
    const controller = new AbortController();
    this.controller = controller;
    this.emit(this.snapshot.state ? "reconnecting" : "connecting");
    void this.readAndWatch(generation, controller);
  };
  private async readAndWatch(generation: number, controller: AbortController) {
    const live = () => this.running && generation === this.generation && !controller.signal.aborted;
    try {
      const response = await this.client.getDesktop({}, { signal: controller.signal });
      if (!live()) return;
      let state = response.state ?? create(DesktopStateSchema);
      if (state.revision === 0n && this.initialize) {
        const initialized = await this.client.applyDesktopOperation(
          {
            idempotencyKey: crypto.randomUUID(),
            operation: { case: "initialize", value: this.initialize },
          },
          { signal: controller.signal },
        );
        if (!live()) return;
        state = initialized.state ?? state;
      }
      this.accept(state, true);
      this.emit("connected");
      this.scheduleRefresh(generation, controller);
      for await (const event of this.client.watchDesktop(
        { afterRevision: state.revision },
        { signal: controller.signal },
      )) {
        if (!live()) return;
        this.accept(event.state, event.resetRequired);
        this.emit("connected");
      }
      if (live()) this.scheduleReconnect();
    } catch (error) {
      if (!live()) return;
      if (
        error instanceof ConnectError &&
        (error.code === Code.Unauthenticated || error.code === Code.PermissionDenied)
      ) {
        this.revoke();
        return;
      }
      this.scheduleReconnect();
    }
  }
  private scheduleRefresh(generation: number, controller: AbortController) {
    this.refresh = setTimeout(() => {
      void this.readFallback(generation, controller);
    }, REFRESH_INTERVAL_MS);
  }
  // A quiet or delayed stream must not prevent convergence. Schedule the next
  // read after this one settles, so a slow read is neither overlapped nor starved.
  private async readFallback(generation: number, controller: AbortController) {
    const live = () => this.running && generation === this.generation && !controller.signal.aborted;
    if (!live()) return;
    const resetEpoch = this.resetEpoch;
    try {
      const response = await this.client.getDesktop({}, { signal: controller.signal });
      if (!live() || resetEpoch !== this.resetEpoch) return;
      this.accept(response.state);
      this.emit("connected");
    } catch (error) {
      if (!live()) return;
      if (
        error instanceof ConnectError &&
        (error.code === Code.Unauthenticated || error.code === Code.PermissionDenied)
      ) {
        this.revoke();
        return;
      }
      this.emit("reconnecting");
    } finally {
      if (live()) this.scheduleRefresh(generation, controller);
    }
  }
  private revoke() {
    this.stop();
    clearDesktopProjection();
    this.snapshot = { connection: "unavailable" };
    this.emit("unavailable");
  }
  private scheduleReconnect() {
    this.controller?.abort();
    clearTimeout(this.refresh);
    this.emit("reconnecting");
    this.retry = setTimeout(this.reconnect, 1500);
  }
  // No queue: a disconnected device cannot replay a stale desktop intent later.
  async apply(operation: DesktopOperation): Promise<void> {
    if (!this.running || this.snapshot.connection !== "connected" || !navigator.onLine)
      throw new Error("Desktop is reconnecting. Try again when connected.");
    const generation = this.generation;
    const isCurrent = () => this.running && generation === this.generation;
    try {
      const response = await this.client.applyDesktopOperation({
        idempotencyKey: crypto.randomUUID(),
        operation,
      });
      if (isCurrent()) this.accept(response.state);
    } catch (error) {
      // A response may have been lost after commit. Read authority; do not retry
      // an old intent after another device has since changed the desktop.
      this.reconnect();
      throw error;
    }
  }
  stop() {
    this.running = false;
    ++this.generation;
    this.controller?.abort();
    clearTimeout(this.retry);
    clearTimeout(this.refresh);
    window.removeEventListener("online", this.reconnect);
    document.removeEventListener("visibilitychange", this.visible);
  }
}
