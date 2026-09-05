// Dock system applications (W6): Home launchpad, Files over the indexed
// workspace projection, Docs and Code over the project's review artifacts,
// and the sandboxed Browser window. Everything is read-only and bounded;
// the Browser renders external pages inside a sandboxed iframe whose
// missing allow-popups/allow-top-navigation flags are the _blank and
// top-navigation interception boundary.
import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useMemo, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { ArtifactReference } from "./ArtifactCenter.js";

export interface HomeAppEntry {
  id: string;
  label: string;
  hint: string;
  available: boolean;
  open: () => void;
}

export function HomeApp(props: { apps: HomeAppEntry[] }) {
  const { apps } = props;
  return (
    <div className="home-app" data-testid="home-app">
      <ul className="home-grid">
        {apps.map((app) => (
          <li key={app.id}>
            <button
              type="button"
              className="home-entry"
              data-testid={`home-entry-${app.id}`}
              disabled={!app.available}
              title={app.available ? app.hint : `${app.label} is unavailable: ${app.hint}`}
              onClick={app.open}
            >
              <span className="home-label">{app.label}</span>
              <span className="home-hint">{app.available ? app.hint : "unavailable"}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

// FilesApp is the bounded read view over the indexed workspace projection:
// an explicit bounded query (the search grammar requires one), results
// filtered to workspace.file.v1 provenance. There is no full-directory
// listing RPC, and this surface never pretends otherwise.
export function FilesApp(props: { projectId: string; workosClients: WorkOSClients }) {
  const { projectId, workosClients } = props;
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<
    Array<{ title: string; excerpt: string; artifactId: string }>
  >([]);
  const [verdict, setVerdict] = useState("");
  const [busy, setBusy] = useState(false);

  const search = async () => {
    const trimmed = query.trim();
    if (trimmed.length === 0 || busy) return;
    setBusy(true);
    setVerdict("");
    try {
      const response = await workosClients.index.searchHybrid({
        projectId,
        query: trimmed,
        page: { pageSize: 20 },
      });
      const files = response.hits.filter((hit) => hit.sourceRef?.type === "workspace.file.v1");
      setResults(
        files.map((hit) => ({
          title: hit.title,
          excerpt: hit.excerpt,
          artifactId: hit.artifactId,
        })),
      );
      if (files.length === 0) {
        setVerdict("No indexed workspace file matches this query.");
      }
    } catch (reason) {
      setVerdict(
        reason instanceof ConnectError && reason.code === Code.InvalidArgument
          ? "The query is invalid."
          : "Workspace search is temporarily unavailable.",
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="files-app" data-testid="files-app">
      <form
        className="files-search"
        onSubmit={(event) => {
          event.preventDefault();
          void search();
        }}
      >
        <input
          aria-label="Search workspace files"
          className="files-query"
          placeholder="Search indexed workspace files…"
          value={query}
          maxLength={256}
          onChange={(event) => {
            setQuery(event.target.value);
          }}
        />
        <button
          className="files-search-button"
          type="submit"
          disabled={busy || query.trim().length === 0}
        >
          Search
        </button>
      </form>
      {verdict ? (
        <p className="files-verdict" role="status">
          {verdict}
        </p>
      ) : null}
      <ul className="files-results">
        {results.map((result) => (
          <li key={result.artifactId} className="files-hit">
            <span className="files-title">{result.title}</span>
            <span className="files-excerpt">{result.excerpt}</span>
          </li>
        ))}
      </ul>
      <p className="files-note">
        Files are indexed from the project&apos;s bound workspace source. Register one with{" "}
        <code>workosctl index workspace register</code>.
      </p>
    </div>
  );
}

// useProjectArtifacts lists the project's review artifacts through the
// authoritative service, remounting per project. The list is bounded at one
// page; content is never shown here — selection opens the read-only viewer.
function useProjectArtifacts(projectId: string, workosClients: WorkOSClients) {
  const [artifacts, setArtifacts] = useState<ArtifactReference[]>([]);
  const [verdict, setVerdict] = useState("");
  useEffect(() => {
    let live = true;
    setArtifacts([]);
    setVerdict("");
    workosClients.artifacts
      .listArtifacts({ projectId, page: { pageSize: 50 } })
      .then((response) => {
        if (!live) return;
        setArtifacts(
          response.artifacts.map((artifact) => ({
            id: artifact.id,
            projectId,
            title: artifact.title,
            type: artifact.type,
            digest: artifact.digest,
          })),
        );
      })
      .catch((reason: unknown) => {
        if (!live) return;
        setVerdict(
          reason instanceof ConnectError && reason.code === Code.NotFound
            ? "This project's artifacts are not available."
            : "Artifact list is temporarily unavailable.",
        );
      });
    return () => {
      live = false;
    };
  }, [projectId, workosClients]);
  return { artifacts, verdict };
}

export function DocsApp(props: {
  projectId: string;
  workosClients: WorkOSClients;
  onOpenArtifact: (artifact: ArtifactReference) => void;
}) {
  const { artifacts, verdict } = useProjectArtifacts(props.projectId, props.workosClients);
  const docs = useMemo(
    () => artifacts.filter((artifact) => artifact.type === "document.markdown.v1"),
    [artifacts],
  );
  return (
    <div className="docs-app" data-testid="docs-app">
      {verdict ? (
        <p className="docs-verdict" role="status">
          {verdict}
        </p>
      ) : null}
      <ul className="docs-list">
        {docs.map((artifact) => (
          <li key={artifact.id}>
            <button
              type="button"
              className="docs-entry"
              onClick={() => {
                props.onOpenArtifact(artifact);
              }}
            >
              <span className="docs-title">{artifact.title}</span>
              <span className="docs-meta">{(artifact.digest ?? "").slice(0, 18)}…</span>
            </button>
          </li>
        ))}
        {docs.length === 0 && verdict === "" ? (
          <li className="empty-state">No markdown documents in this project yet.</li>
        ) : null}
      </ul>
    </div>
  );
}

export function CodeApp(props: {
  projectId: string;
  workosClients: WorkOSClients;
  onOpenArtifact: (artifact: ArtifactReference) => void;
}) {
  const { artifacts, verdict } = useProjectArtifacts(props.projectId, props.workosClients);
  const patches = useMemo(
    () => artifacts.filter((artifact) => artifact.type === "code.unified-diff.v1"),
    [artifacts],
  );
  return (
    <div className="code-app" data-testid="code-app">
      {verdict ? (
        <p className="code-verdict" role="status">
          {verdict}
        </p>
      ) : null}
      <ul className="code-list">
        {patches.map((artifact) => (
          <li key={artifact.id}>
            <button
              type="button"
              className="code-entry"
              onClick={() => {
                props.onOpenArtifact(artifact);
              }}
            >
              <span className="code-title">{artifact.title}</span>
              <span className="code-meta">read-only diff</span>
            </button>
          </li>
        ))}
        {patches.length === 0 && verdict === "" ? (
          <li className="empty-state">No proposed patches in this project yet.</li>
        ) : null}
      </ul>
    </div>
  );
}

// BrowserApp keeps external web content inside the WorkOS window with a
// fixed sandbox: no allow-popups (window.open/_blank is intercepted by the
// sandbox) and no allow-top-navigation (the embedder can never be replaced).
// Only http(s) URLs are accepted; everything else is a fixed verdict.
export function BrowserApp(props: { initialUrl?: string }) {
  const [url, setUrl] = useState(props.initialUrl ?? "");
  const [target, setTarget] = useState("");
  const [verdict, setVerdict] = useState("");

  const navigate = (raw: string) => {
    const trimmed = raw.trim();
    if (trimmed.length === 0) return;
    let parsed: URL;
    try {
      parsed = new URL(trimmed);
    } catch {
      setVerdict("Enter a valid http(s) URL.");
      return;
    }
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      setVerdict("Only http(s) pages can be shown here.");
      return;
    }
    setVerdict("");
    setTarget(parsed.toString());
  };

  useEffect(() => {
    if (props.initialUrl) {
      navigate(props.initialUrl);
    }
  }, [props.initialUrl]);

  return (
    <div className="browser-app" data-testid="browser-app">
      <form
        className="browser-bar"
        onSubmit={(event) => {
          event.preventDefault();
          navigate(url);
        }}
      >
        <input
          aria-label="Browser address"
          className="browser-address"
          placeholder="https://…"
          value={url}
          maxLength={2048}
          onChange={(event) => {
            setUrl(event.target.value);
          }}
        />
        <button className="browser-go" type="submit">
          Go
        </button>
      </form>
      {verdict ? (
        <p className="browser-verdict" role="status">
          {verdict}
        </p>
      ) : null}
      {target ? (
        <iframe
          className="browser-frame"
          data-testid="browser-frame"
          src={target}
          // Fixed boundary: popups (_blank) and top navigation are
          // intercepted by the sandbox itself; the WorkOS window is never
          // navigated away and no browser tab is opened.
          sandbox="allow-scripts allow-forms allow-same-origin"
          title="Embedded browser"
        />
      ) : (
        <p className="empty-state">Enter an address to browse inside WorkOS.</p>
      )}
    </div>
  );
}
