// Project Mission Control (W6, expanded layout): project cards over the
// owner's real project facts plus the bounded per-project notification
// count, and the one documented creation path (CreateProject through the
// existing public service). No invented status, no fabricated alarms.
import { useState } from "react";

export interface MissionControlProject {
  id: string;
  name: string;
  revision: number | bigint;
  unreadNotifications: number;
  isActive: boolean;
}

export function MissionControl(props: {
  projects: MissionControlProject[];
  onSelect: (projectId: string) => void;
  onCreateProject: (name: string) => Promise<"ok" | "stale">;
}) {
  const { projects, onSelect, onCreateProject } = props;
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [verdict, setVerdict] = useState("");

  return (
    <div className="mission-control" data-testid="mission-control">
      <form
        className="mission-create"
        onSubmit={(event) => {
          event.preventDefault();
          const trimmed = name.trim();
          if (trimmed.length === 0 || busy) return;
          setBusy(true);
          setVerdict("");
          void onCreateProject(trimmed)
            .then((result) => {
              if (result === "stale") {
                setVerdict("Project creation is not available right now.");
              } else {
                setName("");
              }
            })
            .finally(() => {
              setBusy(false);
            });
        }}
      >
        <input
          aria-label="New project name"
          className="mission-create-input"
          placeholder="New project name"
          value={name}
          maxLength={80}
          onChange={(event) => {
            setName(event.target.value);
          }}
        />
        <button
          className="mission-create-button"
          type="submit"
          disabled={busy || name.trim().length === 0}
        >
          Create project
        </button>
      </form>
      {verdict ? (
        <p className="mission-verdict" role="status">
          {verdict}
        </p>
      ) : null}
      <ul className="mission-cards">
        {projects.map((project) => (
          <li key={project.id}>
            <button
              type="button"
              className={project.isActive ? "mission-card active" : "mission-card"}
              data-testid={`mission-card-${project.id}`}
              onClick={() => {
                onSelect(project.id);
              }}
            >
              <span className="mission-name">{project.name}</span>
              <span className="mission-facts">
                revision {String(project.revision)}
                {project.unreadNotifications > 0
                  ? ` · ${String(project.unreadNotifications)} unread`
                  : ""}
                {project.isActive ? " · active" : ""}
              </span>
            </button>
          </li>
        ))}
        {projects.length === 0 ? <li className="empty-state">No projects yet.</li> : null}
      </ul>
    </div>
  );
}
