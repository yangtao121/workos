import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import {
  activeWindow,
  orderedWindows,
  secondaryWindow,
  type DeviceLayout,
  type DeviceLayoutState,
} from "@workos/adaptive-shell";
import type { Project } from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import type { WindowState, WorkOSWindow } from "@workos/window-manager";

// The adaptive shell renders the same Project/Surface/Agent/Artifact state
// the expanded desktop renders, in the compact (phone), medium (tablet), and
// fold-separated modes (docs/structure.md 12.3–12.5). It never resizes the
// desktop down: one main surface at a time, a bottom nav on touch modes, a
// project sheet instead of the permanent rail, an Agent slide-over on
// medium, and two panes only when the fold posture really reports two
// segments.

export type SystemWindowId =
  | "agent-center"
  | "system-monitor"
  | "device-center"
  | "artifact-center";

export interface AdaptiveShellProps {
  layout: DeviceLayout;
  windows: WindowState;
  status: string;
  activeProject: Project | undefined;
  projects: Project[];
  // The durable per-project + device-class layout state (already loaded by
  // the Desktop). Undefined until the store answers; the shell then uses
  // the documented defaults (dual pane on fold).
  layoutState: DeviceLayoutState | undefined;
  onSwitchProject: (projectId: string) => void;
  onCreateProject: (name: string) => void;
  onOpenSystemWindow: (id: SystemWindowId) => void;
  onFocusWindow: (id: string) => void;
  onCloseWindow: (id: string) => void;
  onLayoutPreference: (preference: "single" | "dual") => void;
  // Opening a recent app instance re-runs the real CreateSurface flow when
  // no live window exists for it.
  onOpenAppInstance: (appInstanceId: string) => void;
  // The Desktop owns the window bodies (Agent Center, App Surface, System
  // Monitor, Device Center, Artifact Center/Viewer) and the App Library /
  // project settings panels; the shell decides where they render.
  renderWindowBody: (windowState: WorkOSWindow) => ReactNode;
  renderAppLibrary: () => ReactNode;
  renderProjectSettings: () => ReactNode;
  children?: ReactNode;
}

type ShellView = "home" | "window";

export function AdaptiveShell({
  layout,
  windows,
  status,
  activeProject,
  projects,
  layoutState,
  onSwitchProject,
  onCreateProject,
  onOpenSystemWindow,
  onFocusWindow,
  onCloseWindow,
  onLayoutPreference,
  onOpenAppInstance,
  renderWindowBody,
  renderAppLibrary,
  renderProjectSettings,
  children,
}: AdaptiveShellProps) {
  // A separated fold starts on its panes (that posture exists to show two
  // surfaces); every other adaptive mode starts on the home view.
  const [view, setView] = useState<ShellView>(() =>
    layout.mode === "fold-separated" ? "window" : "home",
  );
  const [sheetOpen, setSheetOpen] = useState(false);
  const [appsOpen, setAppsOpen] = useState(false);
  const [slideOverOpen, setSlideOverOpen] = useState(false);
  const [dockRevealed, setDockRevealed] = useState(false);
  const dualPane = layout.dualPane && (layoutState?.layoutPreference ?? "dual") === "dual";
  const main = activeWindow(windows);
  const secondary = dualPane ? secondaryWindow(windows, main?.id) : undefined;
  const medium = layout.mode === "medium";
  const fold = layout.mode === "fold-separated";

  // Opening an overlay collapses the others so the shell never stacks two
  // competing modals.
  useEffect(() => {
    if (sheetOpen) {
      setAppsOpen(false);
      setSlideOverOpen(false);
    }
  }, [sheetOpen]);
  useEffect(() => {
    if (appsOpen) setSheetOpen(false);
  }, [appsOpen]);
  useEffect(() => {
    if (slideOverOpen) {
      setSheetOpen(false);
      setAppsOpen(false);
    }
  }, [slideOverOpen]);

  // Escape closes the topmost overlay and returns focus to the shell.
  useEffect(() => {
    if (!sheetOpen && !appsOpen && !slideOverOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.stopPropagation();
      if (slideOverOpen) setSlideOverOpen(false);
      else if (appsOpen) setAppsOpen(false);
      else setSheetOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [sheetOpen, appsOpen, slideOverOpen]);

  const openSystemWindow = (id: SystemWindowId) => {
    setView("window");
    setAppsOpen(false);
    setDockRevealed(false);
    onOpenSystemWindow(id);
  };

  const openAgent = () => {
    setView("window");
    setAppsOpen(false);
    onOpenSystemWindow("agent-center");
  };

  const paneFor = (windowState: WorkOSWindow | undefined, testid: string | undefined) => {
    if (!windowState) {
      return (
        <div className="adaptive-pane adaptive-pane-empty" data-testid={testid}>
          <p className="empty-state">Nothing is open. Pick a destination from the navigation.</p>
        </div>
      );
    }
    return (
      <div
        className="adaptive-pane"
        data-testid={testid}
        onClick={() => {
          onFocusWindow(windowState.id);
        }}
      >
        <header className="adaptive-pane-header">
          <div className="traffic-lights">
            <button
              aria-label={`Close ${windowState.title}`}
              className="traffic-close"
              onClick={(event) => {
                event.stopPropagation();
                onCloseWindow(windowState.id);
              }}
              type="button"
            />
          </div>
          <strong>{windowState.title}</strong>
          <span>{activeProject?.name ?? "No project"}</span>
        </header>
        <div className="adaptive-pane-body">{renderWindowBody(windowState)}</div>
      </div>
    );
  };

  const hingeGap = layout.hinge?.gap ?? 24;
  const firstSegment = layout.segments[0];
  const secondSegment = layout.segments[1];
  const foldGridStyle: CSSProperties | undefined = fold
    ? layout.hinge?.horizontal
      ? {
          gridTemplateColumns: "minmax(0, 1fr)",
          gridTemplateRows: `${String(firstSegment?.height ?? 1)}fr ${String(hingeGap)}px ${String(secondSegment?.height ?? 1)}fr`,
        }
      : {
          gridTemplateColumns: `${String(firstSegment?.width ?? 1)}fr ${String(hingeGap)}px ${String(secondSegment?.width ?? 1)}fr`,
          gridTemplateRows: "minmax(0, 1fr)",
        }
    : undefined;

  return (
    <main className="adaptive-shell" data-mode={layout.mode}>
      <header className="system-bar adaptive-bar">
        <strong>◈ WorkOS</strong>
        <button
          aria-expanded={sheetOpen}
          className="project-switcher"
          data-testid="nav-projects"
          onClick={() => {
            setSheetOpen((current) => !current);
          }}
          type="button"
        >
          {activeProject?.name ?? "Global Space"} <span>⌄</span>
        </button>
        <span className="adaptive-bar-actions">
          <span className="agent-status">
            <i /> Agent · {status}
          </span>
          {medium ? (
            <button
              aria-expanded={slideOverOpen}
              className="adaptive-bar-button"
              data-testid="open-agent-slideover"
              onClick={() => {
                setSlideOverOpen((current) => !current);
              }}
              type="button"
            >
              Agent
            </button>
          ) : null}
          {fold ? (
            <button
              className="adaptive-bar-button"
              data-testid="toggle-panes"
              onClick={() => {
                onLayoutPreference(dualPane ? "single" : "dual");
              }}
              type="button"
            >
              {dualPane ? "Single pane" : "Dual pane"}
            </button>
          ) : null}
          {medium ? (
            <button
              aria-expanded={dockRevealed}
              className="adaptive-bar-button"
              data-testid="toggle-dock"
              onClick={() => {
                setDockRevealed((current) => !current);
              }}
              type="button"
            >
              Dock
            </button>
          ) : null}
        </span>
      </header>

      <section className="adaptive-stage">
        {view === "home" ? (
          <div className="adaptive-home">
            <p>PROJECT SPACES</p>
            <h1>{activeProject?.name ?? "Global Space"}</h1>
            <div className="adaptive-quick">
              <Button
                onClick={() => {
                  openSystemWindow("system-monitor");
                }}
                type="button"
              >
                System Monitor
              </Button>
              <Button
                onClick={() => {
                  openSystemWindow("device-center");
                }}
                type="button"
              >
                Device Center
              </Button>
              <Button
                disabled={!activeProject}
                onClick={() => {
                  openSystemWindow("artifact-center");
                }}
                type="button"
              >
                Artifact Center
              </Button>
              <Button
                disabled={!activeProject}
                onClick={() => {
                  setAppsOpen(true);
                }}
                type="button"
              >
                App Library
              </Button>
            </div>
            {layoutState && layoutState.recentAppInstanceIds.length > 0 ? (
              <>
                <p className="adaptive-home-section">Recent apps</p>
                <ul className="adaptive-recent" aria-label="Recent apps">
                  {layoutState.recentAppInstanceIds.map((instanceId) => (
                    <li key={instanceId}>
                      <Button
                        onClick={() => {
                          // A live window just comes to the front; an
                          // instance without one re-runs the real surface
                          // open flow.
                          const live = windows.windows.find(
                            (candidate) =>
                              candidate.kind === "app-surface" && candidate.appId === instanceId,
                          );
                          if (live) {
                            setView("window");
                            onFocusWindow(live.id);
                          } else {
                            onOpenAppInstance(instanceId);
                          }
                        }}
                        type="button"
                      >
                        {instanceId}
                      </Button>
                    </li>
                  ))}
                </ul>
              </>
            ) : null}
          </div>
        ) : fold && dualPane ? (
          <div
            className="fold-panes"
            data-hinge-orientation={layout.hinge?.horizontal ? "horizontal" : "vertical"}
            style={foldGridStyle}
          >
            {paneFor(main, "fold-pane-main")}
            <div className="fold-hinge" aria-hidden="true" />
            {paneFor(secondary, "fold-pane-secondary")}
          </div>
        ) : (
          paneFor(main, "adaptive-pane-main")
        )}
        {children}
      </section>

      {medium && dockRevealed ? (
        <nav aria-label="WorkOS Dock" className="adaptive-dock" data-testid="adaptive-dock">
          <Button
            onClick={() => {
              openAgent();
              setDockRevealed(false);
            }}
            type="button"
          >
            Agent Center
          </Button>
          <Button
            disabled={!activeProject}
            onClick={() => {
              setDockRevealed(false);
              setAppsOpen(true);
            }}
            type="button"
          >
            App Library
          </Button>
          <Button
            onClick={() => {
              openSystemWindow("system-monitor");
            }}
            type="button"
          >
            System Monitor
          </Button>
          <Button
            onClick={() => {
              openSystemWindow("device-center");
            }}
            type="button"
          >
            Device Center
          </Button>
        </nav>
      ) : null}

      {medium || fold ? null : (
        <nav
          aria-label="WorkOS navigation"
          className="adaptive-nav"
          data-testid="adaptive-bottom-nav"
        >
          <button
            aria-label="Home"
            className={view === "home" ? "adaptive-nav-item active" : "adaptive-nav-item"}
            data-testid="nav-home"
            onClick={() => {
              setView("home");
              setAppsOpen(false);
            }}
            type="button"
          >
            ⌂
          </button>
          <button
            aria-label="Agent Center"
            className={
              view === "window" && main?.kind === "agent-center"
                ? "adaptive-nav-item active"
                : "adaptive-nav-item"
            }
            data-testid="nav-agent"
            onClick={openAgent}
            type="button"
          >
            A
          </button>
          <button
            aria-label="App Library"
            className="adaptive-nav-item"
            data-testid="nav-apps"
            disabled={!activeProject}
            onClick={() => {
              setAppsOpen(true);
            }}
            type="button"
          >
            ▦
          </button>
          <button
            aria-label="System Monitor"
            className={
              view === "window" && main?.kind === "system-monitor"
                ? "adaptive-nav-item active"
                : "adaptive-nav-item"
            }
            data-testid="nav-monitor"
            onClick={() => {
              openSystemWindow("system-monitor");
            }}
            type="button"
          >
            ⏻
          </button>
        </nav>
      )}

      {sheetOpen ? (
        <div className="adaptive-backdrop" role="presentation">
          <div
            aria-labelledby="adaptive-projects-title"
            aria-modal="true"
            className="adaptive-sheet"
            role="dialog"
          >
            <header>
              <h2 id="adaptive-projects-title">Projects</h2>
              <Button
                autoFocus
                onClick={() => {
                  setSheetOpen(false);
                }}
                type="button"
              >
                Close
              </Button>
            </header>
            <div className="project-grid">
              {projects.map((project) => (
                <button
                  className={
                    project.id === activeProject?.id ? "project-card active" : "project-card"
                  }
                  key={project.id}
                  onClick={() => {
                    onSwitchProject(project.id);
                    setSheetOpen(false);
                  }}
                  type="button"
                >
                  <span>{project.icon || "◌"}</span>
                  <strong>{project.name}</strong>
                  <small>revision {project.revision.toString()}</small>
                </button>
              ))}
            </div>
            <form
              className="adaptive-new-project"
              onSubmit={(event) => {
                event.preventDefault();
                const form = new FormData(event.currentTarget);
                const name = form.get("name");
                if (typeof name === "string" && name.trim()) {
                  onCreateProject(name.trim());
                  event.currentTarget.reset();
                }
              }}
            >
              <input
                aria-label="Project name"
                maxLength={120}
                name="name"
                placeholder="New project"
              />
              <Button type="submit">Create space</Button>
            </form>
            {activeProject ? renderProjectSettings() : null}
          </div>
        </div>
      ) : null}

      {appsOpen ? (
        <div className="adaptive-overlay" data-testid="adaptive-apps-overlay">
          <header className="adaptive-overlay-header">
            <h2>App Library</h2>
            <Button
              autoFocus
              onClick={() => {
                setAppsOpen(false);
              }}
              type="button"
            >
              Close
            </Button>
          </header>
          <div className="adaptive-overlay-body">{renderAppLibrary()}</div>
        </div>
      ) : null}

      {slideOverOpen ? (
        <aside
          aria-label="Agent Center"
          className="adaptive-slideover"
          data-testid="agent-slideover"
        >
          <header className="adaptive-pane-header">
            <strong>Agent Center</strong>
            <Button
              autoFocus
              onClick={() => {
                setSlideOverOpen(false);
              }}
              type="button"
            >
              Close
            </Button>
          </header>
          <div className="adaptive-pane-body">
            {slideOverAgent(windows, renderWindowBody, () => {
              openAgent();
            })}
          </div>
        </aside>
      ) : null}
    </main>
  );
}

function slideOverAgent(
  windows: WindowState,
  renderWindowBody: (windowState: WorkOSWindow) => ReactNode,
  reopen: () => void,
): ReactNode {
  const agent = orderedWindows(windows).find((window) => window.kind === "agent-center");
  if (!agent) {
    return (
      <div className="empty-state">
        <p>The Agent Center window is closed.</p>
        <Button onClick={reopen} type="button">
          Reopen Agent Center
        </Button>
      </div>
    );
  }
  return renderWindowBody(agent);
}
