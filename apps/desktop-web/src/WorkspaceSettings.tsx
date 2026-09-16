import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  WorkspaceBindingState,
  type AvailableWorkspaceSource,
  type WorkspaceBinding,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";

function WorkspaceSettingsBody({
  projectId,
  workosClients,
}: {
  projectId: string;
  workosClients: WorkOSClients;
}) {
  const [binding, setBinding] = useState<WorkspaceBinding>();
  const [sources, setSources] = useState<AvailableWorkspaceSource[]>([]);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const bindKey = useRef(crypto.randomUUID());
  const reload = useCallback(async () => {
    const [bindings, available] = await Promise.all([
      workosClients.projectWorkspaces.listProjectWorkspaces({ projectId }),
      workosClients.projectWorkspaces.listAvailableWorkspaces({ projectId }),
    ]);
    return {
      binding: bindings.bindings.find((item) => item.state === WorkspaceBindingState.ACTIVE),
      sources: available.sources,
    };
  }, [projectId, workosClients]);
  useEffect(() => {
    let active = true;
    void reload()
      .then((result) => {
        if (active) {
          setBinding(result.binding);
          setSources(result.sources);
        }
      })
      .catch(() => {
        if (active) setError("Workspace sources could not be loaded.");
      });
    return () => {
      active = false;
    };
  }, [reload]);
  const mutate = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      bindKey.current = crypto.randomUUID();
      const result = await reload();
      setBinding(result.binding);
      setSources(result.sources);
    } catch {
      setError(
        "Workspace update could not be confirmed. Reload its current access before retrying.",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="workspace-settings" aria-label="Project workspace">
      <h3>Project workspace</h3>
      <p>Agent tools, Files, Terminal and development apps use this same directory.</p>
      {error ? <p role="alert">{error}</p> : null}
      {binding ? (
        <>
          <p>
            <strong>{binding.displayName}</strong> ·{" "}
            {binding.readOnly ? "Read only" : "Read and write"}
          </p>
          <p>
            Changing access or disconnecting stops programs using the old revision. Inspect
            interrupted work before continuing.
          </p>
          <div className="preview-actions">
            <Button
              disabled={busy}
              onClick={() =>
                void mutate(() =>
                  workosClients.projectWorkspaces.updateWorkspaceAccess({
                    bindingId: binding.id,
                    expectedRevision: binding.revision,
                    readOnly: !binding.readOnly,
                  }),
                )
              }
            >
              {binding.readOnly ? "Allow writes" : "Make read only"}
            </Button>
            <Button
              disabled={busy}
              onClick={() =>
                void mutate(() =>
                  workosClients.projectWorkspaces.archiveWorkspace({
                    bindingId: binding.id,
                    expectedRevision: binding.revision,
                  }),
                )
              }
            >
              Disconnect workspace
            </Button>
          </div>
        </>
      ) : sources.length ? (
        <ul>
          {sources.map((source) => (
            <li key={source.id}>
              <span>
                {source.displayName} · {source.kind} {source.readOnly ? "· Read only" : ""}
              </span>
              <Button
                disabled={busy}
                onClick={() =>
                  void mutate(() =>
                    workosClients.projectWorkspaces.bindWorkspace({
                      projectId,
                      workspaceSourceId: source.id,
                      displayName: source.displayName,
                      idempotencyKey: bindKey.current,
                    }),
                  )
                }
              >
                Connect workspace
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <p>
          No authorized directory is registered for this project. Register a project directory on
          the Runtime host before starting development.
        </p>
      )}
    </section>
  );
}

export function WorkspaceSettings(props: { projectId: string; workosClients: WorkOSClients }) {
  return <WorkspaceSettingsBody key={props.projectId} {...props} />;
}
