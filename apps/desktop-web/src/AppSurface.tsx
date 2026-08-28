import { useEffect, useRef, useState } from "react";
import type { AppSurfaceRef } from "@workos/window-manager";

export type SurfaceWindowState = "loading" | "ready" | "closed";

interface AppSurfaceProps {
  surface: AppSurfaceRef;
}

// AppSurface renders one installed app inside a sandboxed iframe. The iframe
// gets scripts only — no same-origin, no forms, no popups, no storage — and
// never receives WorkOS clients or credentials. The URL is the same-origin
// relative path the SurfaceService returned.
export function AppSurface({ surface }: AppSurfaceProps) {
  const [state, setState] = useState<SurfaceWindowState>("loading");
  const frameRef = useRef<HTMLIFrameElement>(null);
  useEffect(() => {
    setState("loading");
  }, [surface.url]);

  return (
    <div className="app-surface-body">
      {state === "loading" ? (
        <p className="surface-state" role="status">
          Opening app surface…
        </p>
      ) : null}
      <iframe
        ref={frameRef}
        className="app-surface-frame"
        data-testid="app-surface-frame"
        onLoad={() => {
          setState((current) => (current === "closed" ? current : "ready"));
        }}
        referrerPolicy="no-referrer"
        sandbox="allow-scripts"
        src={surface.url}
        title={`App surface ${surface.surfaceSessionId}`}
      />
    </div>
  );
}
