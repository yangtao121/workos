import type { WorkOSClients } from "@workos/agent-sdk";

// The desktop owns the session independently of the responsive window body.
// The desktop retains it while the window exists, including hidden mobile panes.
// Standalone consumers release after unmount; same-commit remounts reuse the lease.
// Disposal also closes a creation response arriving later.
export class NativeSessionLease {
  constructor(private readonly keepAlive = false) {}

  dispose() {
    if (this.current) this.close(this.current);
  }
  private current:
    | {
        clients: WorkOSClients;
        projectId: string;
        session: Promise<string>;
        users: number;
        timer?: ReturnType<typeof setTimeout>;
        closed: boolean;
      }
    | undefined;

  acquire(clients: WorkOSClients, projectId: string) {
    if (
      this.current &&
      (this.current.clients !== clients || this.current.projectId !== projectId)
    ) {
      this.close(this.current);
    }
    if (!this.current) {
      const session = clients.nativeSessions
        .createNativeSession({
          idempotencyKey: `desktop-native-${crypto.randomUUID()}`,
          projectId,
          width: 800,
          height: 600,
        })
        .then((result) => {
          if (!result.session?.id) throw new Error("missing native session");
          return result.session.id;
        });
      this.current = { clients, projectId, session, users: 0, closed: false };
    }
    const lease = this.current;
    clearTimeout(lease.timer);
    lease.users++;
    let released = false;
    return {
      session: lease.session,
      release: () => {
        if (released) return;
        released = true;
        lease.users--;
        if (lease.users === 0 && !this.keepAlive)
          lease.timer = setTimeout(() => {
            this.close(lease);
          }, 0);
      },
    };
  }

  private close(lease: NonNullable<NativeSessionLease["current"]>) {
    if (lease.closed) return;
    lease.closed = true;
    clearTimeout(lease.timer);
    if (this.current === lease) this.current = undefined;
    void lease.session
      .then((sessionId) => lease.clients.nativeSessions.closeNativeSession({ sessionId }))
      .catch(() => undefined);
  }
}
