// The shared shell's mobile mount: device-posture layout through the
// adaptive-shell contract, the durable device key in the wrapper's storage
// tier, and the honest gateway state machine (connecting / unavailable /
// paired via the deployment's own auth screens).
import { useCallback, useEffect, useRef, useState } from "react";
import { classifyDevice } from "@workos/adaptive-shell";
import type { DeviceKeyStore } from "./native.js";

type ConnectionState = "connecting" | "unavailable" | "online";

interface ShellProject {
  id: string;
  name: string;
  revision: string;
}

export function MobileShell(props: { origin: string; keyStore: DeviceKeyStore }) {
  const [posture, setPosture] = useState(() => detectPosture());
  const [connection, setConnection] = useState<ConnectionState>("connecting");
  const [projects, setProjects] = useState<ShellProject[]>([]);
  const [keyStatus, setKeyStatus] = useState("");
  const origin = props.origin;

  useEffect(() => {
    const update = () => {
      setPosture(detectPosture());
    };
    window.addEventListener("resize", update);
    return () => {
      window.removeEventListener("resize", update);
    };
  }, []);

  useEffect(() => {
    const cancellation = { current: false };
    void (async () => {
      const status = await props.keyStore.status();
      if (cancellation.current) return;
      setKeyStatus(
        status.secure ? "Device key: native secure storage" : `Device key: ${status.reason}`,
      );
    })();
    return () => {
      cancellation.current = true;
    };
  }, [props.keyStore]);

  const loadProjects = useCallback(async () => {
    setConnection("connecting");
    try {
      const response = await fetch(
        new URL("/workos.project.v1.ProjectService/ListProjects", origin),
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: "{}",
        },
      );
      if (!response.ok) throw new Error(`status ${String(response.status)}`);
      const body = (await response.json()) as { projects?: ShellProject[] };
      if (cancelledRef.current) return;
      setProjects(body.projects ?? []);
      setConnection("online");
    } catch {
      if (cancelledRef.current) return;
      setProjects([]);
      setConnection("unavailable");
    }
  }, [origin]);

  const cancelledRef = useRef(false);
  useEffect(() => {
    cancelledRef.current = false;
    void loadProjects();
    const timer = window.setInterval(() => void loadProjects(), 30_000);
    return () => {
      cancelledRef.current = true;
      window.clearInterval(timer);
    };
  }, [loadProjects]);

  return (
    <div className={`workos-mobile posture-${posture}`} data-testid="workos-mobile">
      <header className="mobile-topbar">
        <span className="mobile-brand">WorkOS</span>
        <span
          className={`mobile-connection connection-${connection}`}
          data-testid="mobile-connection"
        >
          {connection === "online"
            ? "Connected"
            : connection === "connecting"
              ? "Connecting…"
              : "Gateway unavailable"}
        </span>
      </header>
      <main className="mobile-content">
        {connection === "unavailable" ? (
          <section className="mobile-card" data-testid="mobile-unavailable">
            <h1>No gateway</h1>
            <p>
              The WorkOS deployment at <code>{origin}</code> is not reachable. Pair the device
              inside the deployment LAN or configure the origin and try again.
            </p>
            <button type="button" className="mobile-button" onClick={() => void loadProjects()}>
              Retry
            </button>
          </section>
        ) : projects.length === 0 && connection === "online" ? (
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
        <span data-testid="mobile-key-status">{keyStatus}</span>
      </footer>
    </div>
  );
}

function detectPosture(): string {
  return classifyDevice(window.innerWidth);
}
