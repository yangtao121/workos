// The shared shell's mobile mount: device-posture layout through the
// adaptive-shell contract, canonical device pairing/session phases, the
// paired project and notification projections, and the honest gateway state
// machine (connecting / unavailable / unpaired / paired).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { classifyDevice } from "@workos/adaptive-shell";
import { createWorkOSClients } from "@workos/agent-sdk";
import { createMobileAuth, type MobileAuthPhase } from "./auth.js";
import { type MobileVault } from "./native.js";

interface ShellProject {
  id: string;
  name: string;
  revision: string;
}

interface ShellNotification {
  id: string;
  title: string;
  createdAt: string;
  read: boolean;
}

type View = "projects" | "notifications";

export function MobileShell(props: {
  origin: string;
  secureVaultReady: boolean;
  vault: MobileVault;
}) {
  const [posture, setPosture] = useState(() => detectPosture());
  const [keyboardInset, setKeyboardInset] = useState(0);
  const [authPhase, setAuthPhase] = useState<MobileAuthPhase>({ phase: "connecting" });
  const [projects, setProjects] = useState<ShellProject[]>([]);
  const [notifications, setNotifications] = useState<ShellNotification[]>([]);
  const [unread, setUnread] = useState(0n);
  const [connection, setConnection] = useState<"connecting" | "online" | "unavailable">(
    "connecting",
  );
  const [view, setView] = useState<View>("projects");
  const [vaultReason, setVaultReason] = useState("");
  const origin = props.origin;
  const pairedClientsRef = useRef(false);
  const projectionEpoch = useRef(0);
  const authEpoch = useRef(0);
  const pendingReads = useRef(new Set<string>());
  const [actionError, setActionError] = useState("");
  const pairingFragment = useRef(
    window.location.hash.length > 1 ? window.location.hash : undefined,
  );

  const auth = useMemo(() => {
    try {
      return createMobileAuth(origin, props.secureVaultReady ? props.vault.vault : undefined);
    } catch {
      return undefined;
    }
  }, [origin, props.secureVaultReady, props.vault]);

  useEffect(() => {
    const update = () => {
      setPosture(detectPosture());
    };
    window.addEventListener("resize", update);
    // visualViewport is the honest keyboard signal on mobile: the layout
    // viewport keeps its height while the visual viewport shrinks.
    const viewport = window.visualViewport;
    if (viewport) {
      const track = () => {
        const inset = Math.max(0, window.innerHeight - viewport.height - viewport.offsetTop);
        setKeyboardInset(inset > 48 ? inset : 0);
      };
      viewport.addEventListener("resize", track);
      return () => {
        window.removeEventListener("resize", update);
        viewport.removeEventListener("resize", track);
      };
    }
    return () => {
      window.removeEventListener("resize", update);
    };
  }, []);

  useEffect(() => {
    void (async () => {
      const status = await props.vault.status();
      setVaultReason(
        status.secure ? "Device key: native secure storage" : `Device key: ${status.reason}`,
      );
    })();
  }, [props.vault]);

  const begin = useCallback(async () => {
    if (!auth) {
      setAuthPhase({ phase: "unavailable" });
      return;
    }
    const epoch = ++authEpoch.current;
    setAuthPhase({ phase: "connecting" });
    const fragment = pairingFragment.current;
    if (window.location.hash)
      window.history.replaceState(null, "", window.location.pathname + window.location.search);
    const phase = await auth.begin(fragment);
    if (epoch !== authEpoch.current) return;
    setAuthPhase(phase);
    if (phase.phase === "paired" || phase.phase === "unpaired") pairingFragment.current = undefined;
    if (phase.phase === "paired" && fragment) {
      // Scrub the one-time pairing secret after a successful pairing.
      window.history.replaceState(null, "", window.location.pathname + window.location.search);
    }
  }, [auth]);

  useEffect(() => {
    void begin();
    return () => {
      projectionEpoch.current++;
      authEpoch.current++;
    };
  }, [begin]);

  const loadProjections = useCallback(async () => {
    if (authPhase.phase !== "paired" || !auth) return;
    const epoch = ++projectionEpoch.current;
    setConnection("connecting");
    const clients = createWorkOSClients(origin, auth.transport);
    try {
      const projectPage = await clients.projects.listProjects({});
      const notificationPage = await clients.notifications.listNotifications({ pageSize: 20 });
      if (epoch !== projectionEpoch.current) return;
      setProjects(
        projectPage.projects.map((project) => ({
          id: project.id,
          name: project.name,
          revision: String(project.revision),
        })),
      );
      setNotifications(
        notificationPage.notifications.map((notification) => ({
          id: notification.id,
          title: notification.title,
          createdAt: notification.createdAt
            ? new Date(Number(notification.createdAt.seconds) * 1000).toISOString()
            : "",
          read: notification.readAt !== undefined,
        })),
      );
      setUnread(notificationPage.unreadCount);
      setConnection("online");
    } catch (error) {
      if (epoch !== projectionEpoch.current) return;
      if (
        error instanceof ConnectError &&
        (error.code === Code.Unauthenticated || error.code === Code.PermissionDenied)
      ) {
        setProjects([]);
        setNotifications([]);
        setUnread(0n);
        setAuthPhase({ phase: "connecting" });
        const epoch = ++authEpoch.current;
        const phase = await auth.begin(undefined);
        if (epoch !== authEpoch.current) return;
        setAuthPhase(phase);
        return;
      }
      setConnection("unavailable");
    }
  }, [authPhase.phase, auth, origin]);

  useEffect(() => {
    if (authPhase.phase !== "paired") {
      pairedClientsRef.current = false;
      projectionEpoch.current++;
      return;
    }
    if (pairedClientsRef.current) return;
    pairedClientsRef.current = true;
    void loadProjections();
    const timer = window.setInterval(() => void loadProjections(), 30_000);
    return () => {
      pairedClientsRef.current = false;
      window.clearInterval(timer);
    };
  }, [authPhase.phase, loadProjections]);

  const markRead = useCallback(
    async (notificationId: string) => {
      if (!auth || pendingReads.current.has(notificationId)) return;
      pendingReads.current.add(notificationId);
      const epoch = authEpoch.current;
      const clients = createWorkOSClients(origin, auth.transport);
      try {
        await clients.notifications.markNotificationRead({ notificationId });
        if (epoch !== authEpoch.current) return;
        projectionEpoch.current++;
        setNotifications((current) =>
          current.map((item) => (item.id === notificationId ? { ...item, read: true } : item)),
        );
        setUnread((count) => (count > 0n ? count - 1n : 0n));
      } catch {
        // The next projection refresh reconciles the authoritative state.
      } finally {
        pendingReads.current.delete(notificationId);
      }
    },
    [auth, origin],
  );

  const paired = authPhase.phase === "paired";
  const authRequired = authPhase.phase === "unpaired" || authPhase.phase === "pairing";

  return (
    <div
      className={`workos-mobile posture-${posture}`}
      data-testid="workos-mobile"
      style={{ "--mobile-keyboard-inset": `${String(keyboardInset)}px` } as React.CSSProperties}
    >
      <header className="mobile-topbar">
        <span className="mobile-brand">WorkOS</span>
        {paired ? (
          <nav className="mobile-tabs" data-testid="mobile-tabs">
            <button
              type="button"
              className={`mobile-tab ${view === "projects" ? "active" : ""}`}
              onClick={() => {
                setView("projects");
              }}
            >
              Projects
            </button>
            <button
              type="button"
              className={`mobile-tab ${view === "notifications" ? "active" : ""}`}
              onClick={() => {
                setView("notifications");
              }}
            >
              Alerts{unread > 0n ? ` (${unread.toString()})` : ""}
            </button>
          </nav>
        ) : (
          <span
            className={`mobile-connection connection-${connection}`}
            data-testid="mobile-connection"
          >
            {authPhase.phase === "unavailable"
              ? "Gateway unavailable"
              : authPhase.phase === "connecting"
                ? "Connecting…"
                : "Not paired"}
          </span>
        )}
      </header>
      <main className="mobile-content">
        {actionError ? <p role="alert">{actionError}</p> : null}
        {authPhase.phase === "unavailable" ? (
          <section className="mobile-card" data-testid="mobile-unavailable">
            <h1>No gateway</h1>
            <p>
              The WorkOS deployment at <code>{origin}</code> is not reachable. Check the network or
              try again from the deployment LAN.
            </p>
            <button type="button" className="mobile-button" onClick={() => void begin()}>
              Retry
            </button>
          </section>
        ) : authRequired ? (
          <section className="mobile-card" data-testid="mobile-unpaired">
            <h1>Pair this device</h1>
            <p>
              Scan the pairing QR code shown in Device Center on a paired desktop, or open the
              pairing link on this device. The ticket binds this device key to your deployment.
            </p>
            <p className="mobile-hint">
              Remote push delivery needs the native push services (APNs/FCM); without those
              credentials notifications arrive while the app is open.
            </p>
          </section>
        ) : view === "notifications" ? (
          <section data-testid="mobile-notifications">
            {notifications.length === 0 ? (
              <p className="mobile-empty" data-testid="mobile-notifications-empty">
                No notifications yet.
              </p>
            ) : (
              <ul className="mobile-notification-list">
                {notifications.map((notification) => (
                  <li
                    key={notification.id}
                    className={`mobile-notification ${notification.read ? "" : "unread"}`}
                    data-testid="mobile-notification"
                  >
                    <span className="mobile-notification-title">{notification.title}</span>
                    {!notification.read ? (
                      <button
                        type="button"
                        className="mobile-mark-read"
                        onClick={() => void markRead(notification.id)}
                      >
                        Mark read
                      </button>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </section>
        ) : connection === "unavailable" ? (
          <section className="mobile-card" data-testid="mobile-unavailable">
            <h1>Projection unavailable</h1>
            <p>The gateway is reachable for the session but the projection read failed.</p>
            <button type="button" className="mobile-button" onClick={() => void loadProjections()}>
              Retry
            </button>
          </section>
        ) : projects.length === 0 ? (
          <section className="mobile-card" data-testid="mobile-empty">
            <h1>No projects yet</h1>
            <p>Create your first project from a paired desktop, then open WorkOS here.</p>
          </section>
        ) : (
          <ul className="mobile-project-list" data-testid="mobile-projects">
            {projects.map((project) => (
              <li className="mobile-project" key={project.id}>
                <span className="mobile-project-name">{project.name}</span>
                <span className="mobile-project-revision">rev {project.revision}</span>
              </li>
            ))}
          </ul>
        )}
      </main>
      <footer className="mobile-footer">
        <span data-testid="mobile-posture">{posture}</span>
        <span data-testid="mobile-key-status">{vaultReason}</span>
        {paired ? (
          <button
            type="button"
            className="mobile-logout"
            onClick={() =>
              void (async () => {
                if (!auth) return;
                const epoch = ++authEpoch.current;
                projectionEpoch.current++;
                setActionError("");
                const phase = await auth.logout();
                if (epoch !== authEpoch.current) return;
                if (phase.phase === "unavailable") {
                  setActionError("Forget device failed. Try again.");
                  return;
                }
                projectionEpoch.current++;
                setProjects([]);
                setNotifications([]);
                setUnread(0n);
                setAuthPhase(phase);
              })()
            }
          >
            Forget device
          </button>
        ) : null}
      </footer>
    </div>
  );
}

function detectPosture(): string {
  // A real separated fold (two window segments) wins over width, matching
  // the adaptive-shell contract used everywhere else.
  const screenLike = window.screen as Screen & { segments?: unknown[] };
  const segments = screenLike.segments;
  const separatedFold = Array.isArray(segments) && segments.length > 1;
  return classifyDevice(window.innerWidth, separatedFold);
}
