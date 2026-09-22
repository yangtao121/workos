import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import { LifecycleMode, type WorkspacePreview } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

interface WorkspacePreviewsProps {
  projectId: string;
  workosClients: WorkOSClients;
  selectedPreviewId?: string | undefined;
  onSelectPreview?: ((id: string | undefined) => void) | undefined;
}

function WorkspacePreviewsBody(props: WorkspacePreviewsProps) {
  const { projectId, workosClients } = props;
  const [previews, setPreviews] = useState<WorkspacePreview[]>([]);
  const [localSelected, setLocalSelected] = useState<string>();
  const selected = props.onSelectPreview ? props.selectedPreviewId : localSelected;
  const setSelected = props.onSelectPreview ?? setLocalSelected;
  const [command, setCommand] = useState(
    'npm run dev -- --host 127.0.0.1 --port "$PORT" --base "$WORKOS_PREVIEW_BASE"',
  );
  const [port, setPort] = useState("3000");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  const intent = useRef<{ signature: string; key: string } | undefined>(undefined);
  const reload = useCallback(async () => {
    const result = await workosClients.workspacePreviews.listProjectWorkspacePreviews({
      projectId,
    });
    if (mounted.current) setPreviews(result.previews);
  }, [projectId, workosClients]);
  useEffect(() => {
    mounted.current = true;
    const load = () =>
      void reload().catch(() => {
        if (mounted.current)
          setError(
            "Development previews are unavailable. Check the workspace binding and Runtime.",
          );
      });
    load();
    const timer = window.setInterval(load, 5000);
    // Closing the view detaches it. Only an explicit Stop ends the server.
    return () => {
      mounted.current = false;
      window.clearInterval(timer);
    };
  }, [reload]);
  const run = async (signature: string, action: (key: string) => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError(undefined);
    if (intent.current?.signature !== signature)
      intent.current = { signature, key: crypto.randomUUID() };
    try {
      await action(intent.current.key);
      intent.current = undefined;
      await reload();
    } catch {
      if (mounted.current)
        setError(
          "The request could not be confirmed. Retry to recover the same operation; inspect the current server state before changing the request.",
        );
    } finally {
      if (mounted.current) setBusy(false);
    }
  };
  const active = previews.find((preview) => preview.id === selected);
  const safeURL =
    active?.url.startsWith(`/previews/${active.id}/`) && !active.url.includes("?")
      ? active.url
      : undefined;
  return (
    <section className="workspace-previews" data-testid="workspace-previews">
      <header>
        <h2>Development previews</h2>
        <p>
          New servers keep running until you stop them. Restart starts a new generation. Saved
          project files remain.
        </p>
      </header>
      {error ? <p role="alert">{error}</p> : null}
      {selected && !active ? (
        <p role="status">
          This preview is unavailable.{" "}
          <Button
            onClick={() => {
              setSelected(undefined);
            }}
          >
            All previews
          </Button>
        </p>
      ) : active ? (
        <>
          <div className="preview-actions">
            <Button
              onClick={() => {
                setSelected(undefined);
              }}
            >
              All previews
            </Button>
            <span>
              {active.state} · generation {String(active.generation)}
            </span>
          </div>
          {active.state === "running" && safeURL ? (
            <iframe
              key={`${active.id}-${String(active.generation)}`}
              title="Project development preview"
              src={safeURL}
              sandbox="allow-scripts allow-forms"
              referrerPolicy="no-referrer"
            />
          ) : (
            <p role="status">This server is {active.state}. Return to the list to restart it.</p>
          )}
        </>
      ) : (
        <>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void run(`start:${command}:${port}`, async (key) => {
                const result = await workosClients.workspacePreviews.startWorkspacePreview({
                  projectId,
                  command,
                  port: Number(port),
                  idempotencyKey: key,
                  lifecycleMode: LifecycleMode.MANUAL_STOP,
                });
                if (mounted.current && result.preview) setSelected(result.preview.id);
              });
            }}
          >
            <label>
              Start command
              <textarea
                value={command}
                onChange={(event) => {
                  setCommand(event.target.value);
                }}
                required
                maxLength={4096}
              />
            </label>
            <label>
              Port
              <input
                type="number"
                min="1024"
                max="65535"
                value={port}
                onChange={(event) => {
                  setPort(event.target.value);
                }}
                required
              />
            </label>
            <Button type="submit" disabled={busy || !command.trim()}>
              Start preview
            </Button>
          </form>
          <p className="preview-note">
            The command runs in this project's workspace. Use PORT and WORKOS_PREVIEW_BASE for the
            server port and asset base path. Dependencies must already be available; live reload
            over WebSocket is unavailable.
          </p>
          <ul>
            {previews.map((preview) => (
              <li key={preview.id}>
                <div>
                  <strong>{preview.command}</strong>
                  <span>
                    {preview.state} · generation {String(preview.generation)} · port {preview.port}
                  </span>
                </div>
                <div className="preview-actions">
                  <Button
                    disabled={preview.state !== "running"}
                    onClick={() => {
                      setSelected(preview.id);
                    }}
                  >
                    Open
                  </Button>
                  <Button
                    disabled={busy}
                    onClick={() =>
                      void run(`restart:${preview.id}`, async (key) => {
                        await workosClients.workspacePreviews.restartWorkspacePreview({
                          previewId: preview.id,
                          actionKey: key,
                          lifecycleMode: LifecycleMode.MANUAL_STOP,
                        });
                      })
                    }
                  >
                    Restart
                  </Button>
                  <Button
                    disabled={busy || preview.state !== "running"}
                    onClick={() =>
                      void run(`stop:${preview.id}`, async (key) => {
                        await workosClients.workspacePreviews.stopWorkspacePreview({
                          previewId: preview.id,
                          actionKey: key,
                        });
                      })
                    }
                  >
                    Stop
                  </Button>
                </div>
              </li>
            ))}
          </ul>
          {previews.length === 0 ? <p>No development servers in this project yet.</p> : null}
        </>
      )}
    </section>
  );
}

export function WorkspacePreviews(props: WorkspacePreviewsProps) {
  return <WorkspacePreviewsBody key={props.projectId} {...props} />;
}
