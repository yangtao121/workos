import type { WorkOSClients } from "@workos/agent-sdk";
import { Code, ConnectError } from "@connectrpc/connect";
import { LifecycleMode, SurfaceRenderer } from "@workos/protocol";
import {
  GreenfieldWindowInputClient,
  type GreenfieldAttachment,
} from "./greenfieldWindowClient.js";

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
  attachmentId: () => Promise<string>;
  workloadGeneration: () => Promise<bigint>;
  // Whether this device currently holds the single-controller lease; input
  // is disabled until an explicit RequestSurfaceControl takes it over.
  controls: Promise<boolean>;
  controlGeneration: () => Promise<bigint>;
  onControlChange: (listener: (controls: boolean) => void) => () => void;
  requestControl: () => Promise<boolean>;
  stop: () => Promise<void>;
  release: () => void;
}

export class NativeSessionLease {
  constructor(private readonly keepAlive = false) {}

  // Responsive shell remounts retain the same live attachment. Its input
  // sequence belongs to that attachment, not to the React window body.
  private residentInput:
    | {
        client: WorkOSClients["greenfieldWindows"];
        attachmentId: string;
        sessionId: string;
        workloadGeneration: bigint;
        input: GreenfieldWindowInputClient;
      }
    | undefined;

  greenfieldInputClient(
    client: WorkOSClients["greenfieldWindows"],
    attachment: GreenfieldAttachment,
  ): GreenfieldWindowInputClient {
    const current = this.residentInput;
    if (
      current?.client === client &&
      current.attachmentId === attachment.attachmentId &&
      current.sessionId === attachment.sessionId &&
      current.workloadGeneration === attachment.workloadGeneration
    ) {
      current.input.setControl(attachment.controls, attachment.controlGeneration);
      return current.input;
    }
    const input = new GreenfieldWindowInputClient(client, attachment, () => {
      if (this.current?.residentAttachmentId === attachment.attachmentId)
        this.loseControl(this.current);
    });
    this.residentInput = {
      client,
      attachmentId: attachment.attachmentId,
      sessionId: attachment.sessionId,
      workloadGeneration: attachment.workloadGeneration,
      input,
    };
    return input;
  }

  dispose() {
    if (this.current) this.releaseLease(this.current);
  }
  private current:
    | {
        clients: WorkOSClients;
        projectId: string;
        workloadId?: string | undefined;
        expectedWorkloadGeneration: bigint;
        session: Promise<string>;
        attachmentId: Promise<string>;
        workloadGeneration: Promise<bigint>;
        controls: Promise<boolean>;
        generation: Promise<bigint>;
        residentAttachmentId: string;
        expiresAt: number;
        controlRevision: number;
        listeners: Set<(controls: boolean) => void>;
        renewTimer?: ReturnType<typeof setTimeout>;
        expiryTimer?: ReturnType<typeof setTimeout>;
        users: number;
        timer?: ReturnType<typeof setTimeout>;
        released: boolean;
      }
    | undefined;

  acquire(
    clients: WorkOSClients,
    projectId: string,
    workloadId?: string,
    expectedWorkloadGeneration = 0n,
  ): NativeSessionHandle {
    if (
      this.current &&
      (this.current.clients !== clients ||
        this.current.projectId !== projectId ||
        this.current.workloadId !== workloadId ||
        this.current.expectedWorkloadGeneration !== expectedWorkloadGeneration)
    ) {
      this.releaseLease(this.current);
    }
    if (!this.current) {
      // Attach to the project's live native display when one exists; only a
      // project with no running native workload creates a new session. A
      // continuity-unavailable host degrades honestly to the create path.
      const session = this.discover(clients, projectId, workloadId, expectedWorkloadGeneration);
      const controls = session.then((facts) => facts.controls).catch(() => false);
      this.current = {
        clients,
        projectId,
        workloadId,
        expectedWorkloadGeneration,
        session: session.then((facts) => facts.sessionId),
        attachmentId: session.then((facts) => facts.attachmentId).catch(() => ""),
        workloadGeneration: session.then((facts) => facts.workloadGeneration).catch(() => 0n),
        controls,
        generation: session.then((facts) => facts.generation).catch(() => 0n),
        residentAttachmentId: "",
        expiresAt: 0,
        controlRevision: 0,
        listeners: new Set(),
        users: 0,
        released: false,
      };
      const lease = this.current;
      void session
        .then((facts) => {
          if (lease.released) return;
          lease.residentAttachmentId = facts.attachmentId;
          if (facts.controls) this.scheduleRenewal(lease, facts.expiresAt);
        })
        .catch(() => undefined);
    }
    const lease = this.current;
    clearTimeout(lease.timer);
    lease.users++;
    let released = false;
    return {
      session: lease.session,
      attachmentId: () => lease.attachmentId,
      workloadGeneration: () => lease.workloadGeneration,
      get controls() {
        return lease.controls;
      },
      controlGeneration: () => lease.generation,
      onControlChange: (listener) => {
        lease.listeners.add(listener);
        return () => lease.listeners.delete(listener);
      },
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
    workloadId?: string,
    expectedWorkloadGeneration = 0n,
  ): Promise<{
    sessionId: string;
    attachmentId: string;
    workloadGeneration: bigint;
    controls: boolean;
    generation: bigint;
    expiresAt: number;
  }> {
    if (workloadId) {
      const attached = await clients.surfaceContinuity.attachSurface({
        workloadId,
        expectedWorkloadGeneration,
        idempotencyKey: `desktop-native-attach-${crypto.randomUUID()}`,
      });
      return {
        sessionId: attached.session?.id ?? workloadId,
        attachmentId: attached.attachment?.id ?? "",
        workloadGeneration: attached.session?.workloadGeneration ?? expectedWorkloadGeneration,
        controls: attached.attachment?.controls ?? false,
        generation: attached.attachment?.controlGeneration ?? 0n,
        expiresAt: timestampMillis(attached.attachment?.controlExpiresAt),
      };
    }
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
          attachmentId: attached.attachment?.id ?? "",
          workloadGeneration: attached.session?.workloadGeneration ?? live.generation,
          controls: attached.attachment?.controls ?? false,
          generation: attached.attachment?.controlGeneration ?? 0n,
          expiresAt: timestampMillis(attached.attachment?.controlExpiresAt),
        };
      }
    }
    const created = await clients.nativeSessions.createNativeSession({
      idempotencyKey: `desktop-native-${crypto.randomUUID()}`,
      projectId,
      width: 800,
      height: 600,
      lifecycleMode: LifecycleMode.MANUAL_STOP,
    });
    if (!created.session?.id) throw new Error("missing native session");
    const attached = await clients.surfaceContinuity.attachSurface({
      workloadId: created.session.id,
      idempotencyKey: `desktop-native-attach-${crypto.randomUUID()}`,
    });
    return {
      sessionId: created.session.id,
      attachmentId: attached.attachment?.id ?? "",
      workloadGeneration: attached.session?.workloadGeneration ?? expectedWorkloadGeneration,
      controls: attached.attachment?.controls ?? false,
      generation: attached.attachment?.controlGeneration ?? 0n,
      expiresAt: timestampMillis(attached.attachment?.controlExpiresAt),
    };
  }

  private clearControlTimers(lease: NonNullable<NativeSessionLease["current"]>) {
    clearTimeout(lease.renewTimer);
    clearTimeout(lease.expiryTimer);
  }

  // A lease may be disposed while an RPC is suspended. Read the current
  // state again after each await before changing controller state.
  private isReleased(lease: NonNullable<NativeSessionLease["current"]>) {
    return lease.released;
  }

  private loseControl(lease: NonNullable<NativeSessionLease["current"]>) {
    if (lease.released) return;
    lease.controlRevision++;
    this.clearControlTimers(lease);
    lease.expiresAt = 0;
    lease.controls = Promise.resolve(false);
    this.residentInput?.input.setControl(false, 0n);
    for (const listener of lease.listeners) listener(false);
  }

  private scheduleRenewal(lease: NonNullable<NativeSessionLease["current"]>, expiresAt: number) {
    this.clearControlTimers(lease);
    if (lease.released) return;
    if (!Number.isFinite(expiresAt) || expiresAt <= Date.now()) {
      this.loseControl(lease);
      return;
    }
    lease.expiresAt = expiresAt;
    const revision = ++lease.controlRevision;
    // Renew with enough time for one LAN round trip. A suspended tab wakes
    // after expiry in read-only mode, even if its pending RPC is still open.
    const remaining = expiresAt - Date.now();
    lease.renewTimer = setTimeout(
      () => {
        void this.renewControl(lease, revision);
      },
      Math.max(0, remaining - Math.min(30_000, remaining / 2)),
    );
    lease.expiryTimer = setTimeout(() => {
      if (lease.controlRevision === revision) this.loseControl(lease);
    }, remaining);
  }

  private async renewControl(lease: NonNullable<NativeSessionLease["current"]>, revision: number) {
    if (lease.released || lease.controlRevision !== revision) return;
    try {
      const [sessionId, attachmentId, generation, workloadGeneration] = await Promise.all([
        lease.session,
        lease.attachmentId,
        lease.generation,
        lease.workloadGeneration,
      ]);
      if (this.isReleased(lease) || lease.controlRevision !== revision) return;
      const response = await lease.clients.surfaceContinuity.renewSurfaceControl({
        surfaceSessionId: sessionId,
        attachmentId,
        expectedControlGeneration: generation,
        expectedWorkloadGeneration: workloadGeneration,
      });
      if (this.isReleased(lease) || lease.controlRevision !== revision) return;
      const attachment = response.attachment;
      if (
        !attachment?.controls ||
        attachment.id !== attachmentId ||
        attachment.controlGeneration !== generation
      ) {
        this.loseControl(lease);
        return;
      }
      this.scheduleRenewal(lease, timestampMillis(attachment.controlExpiresAt));
    } catch {
      if (!this.isReleased(lease) && lease.controlRevision === revision) this.loseControl(lease);
    }
  }

  private async reattach(lease: NonNullable<NativeSessionLease["current"]>) {
    if (this.isReleased(lease)) throw new Error("native attachment unavailable");
    const sessionId = await lease.session;
    const workloadGeneration = await lease.workloadGeneration;
    if (this.isReleased(lease)) throw new Error("native attachment unavailable");
    const attached = await lease.clients.surfaceContinuity.attachSurface({
      workloadId: sessionId,
      expectedWorkloadGeneration: workloadGeneration,
      idempotencyKey: `desktop-native-recover-${crypto.randomUUID()}`,
    });
    if (!attached.attachment?.id || attached.session?.workloadGeneration !== workloadGeneration)
      throw new Error("native attachment recovery failed");
    if (this.isReleased(lease)) {
      void lease.clients.surfaceContinuity.detachSurface({ surfaceSessionId: sessionId });
      throw new Error("native attachment unavailable");
    }
    lease.attachmentId = Promise.resolve(attached.attachment.id);
    lease.residentAttachmentId = attached.attachment.id;
    lease.generation = Promise.resolve(attached.attachment.controlGeneration);
    lease.controls = Promise.resolve(attached.attachment.controls);
    this.residentInput = undefined;
    return attached.attachment;
  }

  private async requestControl(
    lease: NonNullable<NativeSessionLease["current"]>,
  ): Promise<boolean> {
    if (this.isReleased(lease)) throw new Error("native attachment unavailable");
    lease.controlRevision++;
    this.clearControlTimers(lease);
    const sessionId = await lease.session;
    if (this.isReleased(lease)) throw new Error("native attachment unavailable");
    try {
      if (this.residentInput?.input.needsFreshAttachment) await this.reattach(lease);
      let response;
      try {
        if (this.isReleased(lease)) throw new Error("native attachment unavailable");
        response = await lease.clients.surfaceContinuity.requestSurfaceControl({
          surfaceSessionId: sessionId,
        });
      } catch (error) {
        // The old attachment may have been expired by the Surface sweeper.
        // Only this explicit button action may attach again and take control.
        if (
          !(error instanceof ConnectError) ||
          (error.code !== Code.NotFound && error.code !== Code.FailedPrecondition)
        )
          throw error;
        await this.reattach(lease);
        if (this.isReleased(lease)) throw new Error("native attachment unavailable");
        response = await lease.clients.surfaceContinuity.requestSurfaceControl({
          surfaceSessionId: sessionId,
        });
      }
      if (this.isReleased(lease)) throw new Error("native attachment unavailable");
      const attachment = response.attachment;
      const attachmentId = await lease.attachmentId;
      if (
        !attachment?.controls ||
        attachment.id !== attachmentId ||
        attachment.controlGeneration < 1n
      )
        throw new Error("native control was not granted");
      lease.generation = Promise.resolve(attachment.controlGeneration);
      lease.controls = Promise.resolve(true);
      this.scheduleRenewal(lease, timestampMillis(attachment.controlExpiresAt));
      if (await lease.controls) {
        for (const listener of lease.listeners) listener(true);
        return true;
      }
      return false;
    } catch (error) {
      this.loseControl(lease);
      throw error;
    }
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
    this.clearControlTimers(lease);
    if (this.current === lease) this.current = undefined;
    this.residentInput = undefined;
    // Detach (never Close): window close keeps the program running under
    // its bounded policy; the explicit Stop affordance is the only stop.
    void lease.session
      .then((sessionId) =>
        lease.clients.surfaceContinuity.detachSurface({ surfaceSessionId: sessionId }),
      )
      .catch(() => undefined);
  }
}

function timestampMillis(value?: { seconds: bigint; nanos: number }): number {
  if (!value) return 0;
  return Number(value.seconds) * 1000 + Math.floor(value.nanos / 1_000_000);
}
