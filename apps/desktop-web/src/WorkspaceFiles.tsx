import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { WorkspaceFileEntry } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

function WorkspaceFilesBody({
  projectId,
  workosClients,
}: {
  projectId: string;
  workosClients: WorkOSClients;
}) {
  const [path, setPath] = useState(".");
  const [entries, setEntries] = useState<WorkspaceFileEntry[]>([]);
  const [file, setFile] = useState<{
    path: string;
    content: string;
    etag: string;
    revision: bigint;
    readOnly: boolean;
  }>();
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const load = useCallback(async () => {
    const current = ++generation.current;
    try {
      const result = await workosClients.projectWorkspaces.listWorkspaceFiles({ projectId, path });
      if (current === generation.current) {
        setEntries(result.entries);
        setError(undefined);
      }
    } catch {
      if (current === generation.current)
        setError(
          "The project directory is unavailable. Check its workspace binding in Project settings.",
        );
    }
  }, [path, projectId, workosClients]);
  useEffect(() => {
    void load();
    return () => {
      generation.current++;
    };
  }, [load]);
  const open = async (entry: WorkspaceFileEntry) => {
    if (entry.kind === "directory") {
      setPath(entry.path);
      setFile(undefined);
      return;
    }
    if (entry.kind !== "file") return;
    const current = ++generation.current;
    setBusy(true);
    setError(undefined);
    try {
      const result = await workosClients.projectWorkspaces.readWorkspaceFile({
        projectId,
        path: entry.path,
      });
      if (current !== generation.current) return;
      setFile({
        path: entry.path,
        content: result.content,
        etag: result.etag,
        revision: result.workspaceRevision,
        readOnly: result.readOnly,
      });
      setDraft(result.content);
    } catch {
      if (current === generation.current)
        setError(
          "This file cannot be opened. Text files are limited to 256 KiB; links and binary files are unavailable in this editor.",
        );
    } finally {
      if (current === generation.current) setBusy(false);
    }
  };
  const save = async () => {
    if (!file || busy) return;
    const current = generation.current;
    setBusy(true);
    setError(undefined);
    try {
      const result = await workosClients.projectWorkspaces.writeWorkspaceFile({
        projectId,
        path: file.path,
        content: draft,
        expectedEtag: file.etag,
        workspaceRevision: file.revision,
      });
      if (current === generation.current) setFile({ ...file, content: draft, etag: result.etag });
    } catch {
      if (current === generation.current)
        setError(
          "Save was not confirmed. The file or workspace access may have changed. Reopen the file and compare your draft before saving again.",
        );
    } finally {
      if (current === generation.current) setBusy(false);
    }
  };
  return (
    <section className="workspace-files" data-testid="workspace-files">
      <header>
        <h2>Project files</h2>
        <p>Current files shared with Agent, Terminal and development apps.</p>
      </header>
      {error ? <p role="alert">{error}</p> : null}
      <div className="preview-actions">
        <Button
          disabled={path === "." || busy}
          onClick={() => {
            setFile(undefined);
            setPath(path.split("/").slice(0, -1).join("/") || ".");
          }}
        >
          Up
        </Button>
        <Button disabled={busy} onClick={() => void load()}>
          Refresh directory
        </Button>
        <span>{path}</span>
      </div>
      {file ? (
        <div className="workspace-file-editor">
          <h3>{file.path}</h3>
          <p>{file.readOnly ? "Read only" : "Read and write"}</p>
          <textarea
            aria-label="File content"
            readOnly={file.readOnly || busy}
            spellCheck={false}
            value={draft}
            onChange={(event) => {
              setDraft(event.target.value);
            }}
          />
          <div className="preview-actions">
            <Button
              onClick={() => {
                setFile(undefined);
              }}
            >
              Close file
            </Button>
            <Button
              disabled={busy || file.readOnly || draft === file.content}
              onClick={() => void save()}
            >
              Save file
            </Button>
          </div>
        </div>
      ) : (
        <ul>
          {entries.map((entry) => (
            <li key={entry.path}>
              <Button
                disabled={busy || (entry.kind !== "directory" && entry.kind !== "file")}
                onClick={() => void open(entry)}
              >
                {entry.path.split("/").at(-1)}
                {entry.kind === "directory" ? "/" : ""}
              </Button>
              <span>
                {entry.kind} · {String(entry.size)} bytes
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function WorkspaceFiles(props: { projectId: string; workosClients: WorkOSClients }) {
  return <WorkspaceFilesBody key={props.projectId} {...props} />;
}
