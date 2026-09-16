import type { WorkOSClients } from "@workos/agent-sdk";
import { SurfaceRenderer } from "@workos/protocol";

// The desktop owns the session independently of the responsive window body.
// The desktop retains it while the window exists, including hidden mobile panes.
// Standalone consumers release after unmount; same-commit remounts reuse the lease.
// Releasing also detaches a creation response arriving later.
//
// ADR-0031 (B08): closing the window or switching projects DETACHES — the
// program keeps running under its bounded policy — and only the explicit
// Stop affordance stops the workload. Re-opening discovers the live
// workload through ListProjectSurfaces and attaches the SAME instance
// instead of creating a second session.
export interface NativeSessionHandle {
  session: Promise<string>;
  // Whether this device currently holds the single-controller lease; input
  // is disabled until an explicit RequestSurfaceControl takes it over.
  controls: Promise<boolean>;
  controlGeneration: () => Promise<bigint>;
  requestControl: () => Promise<boolean>;
  stop: () => Promise<void>;
  release: () => void;
}

export class NativeSessionLease {
  constructor(private readonly keepAlive = false) {}

  dispose() {
    if (this.current) this.releaseLease(this.current);
  }
  private current:
    | {
        clients: WorkOSClients;
        projectId: string;
        session: Promise<string>;
        controls: Promise<boolean>;
        generation: Promise<bigint>;
        users: number;
        timer?: ReturnType<typeof setTimeout>;
        released: boolean;
      }
    | undefined;

  acquire(clients: WorkOSClients, projectId: string): NativeSessionHandle {
    if (
      this.current &&
      (this.current.clients !== clients || this.current.projectId !== projectId)
    ) {
      this.releaseLease(this.current);
    }
    if (!this.current) {
      // Attach to the project's live native display when one exists; only a
      // project with no running native workload creates a new session. A
      // continuity-unavailable host degrades honestly to the create path.
      const session = this.discover(clients, projectId);
      const controls = session.then((facts) => facts.controls).catch(() => false);
      this.current = {
        clients,
        projectId,
        session: session.then((facts) => facts.sessionId),
        controls,
        generation: session.then((facts) => facts.generation).catch(() => 0n),
        users: 0,
        released: false,
      };
    }
    const lease = this.current;
    clearTimeout(lease.timer);
    lease.users++;
    let released = false;
    return {
      session: lease.session,
      controls: lease.controls,
      controlGeneration: () => lease.generation,
      requestControl: () => this.requestControl(lease),
      stop: () => this.stop(lease),
      release: () => {
        if (released) return;
        released = true;
        lease.users--;
        if (lease.users === 0 && !this.keepAlive)
          lease.timer = setTimeout(() => {
            this.releaseLease(lease);
          }, 0);
      },
    };
  }

  private async discover(
    clients: WorkOSClients,
    projectId: string,
  ): Promise<{ sessionId: string; controls: boolean; generation: bigint }> {
    {
      const listed = await clients.surfaceContinuity.listProjectSurfaces({ projectId });
      const live = listed.workloads.find(
        (workload) =>
          workload.state === "running" &&
          workload.renderer === SurfaceRenderer.REMOTE_NATIVE &&
          workload.appInstanceId === "",
      );
      if (live) {
        const attached = await clients.surfaceContinuity.attachSurface({
          workloadId: live.workloadId,
          idempotencyKey: `desktop-native-attach-${crypto.randomUUID()}`,
        });
        const sessionId = attached.session?.id ?? live.workloadId;
        return {
          sessionId,
          controls: attached.attachment?.controls ?? false,
          generation: attached.attachment?.controlGeneration ?? 0n,
        };
      }
    }
    const created = await clients.nativeSessions.createNativeSession({
      idempotencyKey: `desktop-native-${crypto.randomUUID()}`,
      projectId,
      width: 800,
      height: 600,
    });
    if (!created.session?.id) throw new Error("missing native session");
    const attached = await clients.surfaceContinuity.attachSurface({
      workloadId: created.session.id,
      idempotencyKey: `desktop-native-attach-${crypto.randomUUID()}`,
    });
    return {
      sessionId: created.session.id,
      controls: attached.attachment?.controls ?? false,
      generation: attached.attachment?.controlGeneration ?? 0n,
    };
  }

  private async requestControl(
    lease: NonNullable<NativeSessionLease["current"]>,
  ): Promise<boolean> {
    const sessionId = await lease.session;
    const response = await lease.clients.surfaceContinuity.requestSurfaceControl({
      surfaceSessionId: sessionId,
    });
    lease.generation = Promise.resolve(response.attachment?.controlGeneration ?? 0n);
    lease.controls = Promise.resolve(response.attachment?.controls ?? false);
    return response.attachment?.controls ?? false;
  }

  private async stop(lease: NonNullable<NativeSessionLease["current"]>): Promise<void> {
    const sessionId = await lease.session;
    await lease.clients.surfaceContinuity.stopSurfaceWorkload({
      workloadId: sessionId,
      actionKey: `desktop-native-stop-${crypto.randomUUID()}`,
    });
  }

  private releaseLease(lease: NonNullable<NativeSessionLease["current"]>) {
    if (lease.released) return;
    lease.released = true;
    clearTimeout(lease.timer);
    if (this.current === lease) this.current = undefined;
    // Detach (never Close): window close keeps the program running under
    // its bounded policy; the explicit Stop affordance is the only stop.
    void lease.session
      .then((sessionId) => lease.clients.nativeSessions.detachNativeSession({ sessionId }))
      .catch(() => undefined);
  }
}
