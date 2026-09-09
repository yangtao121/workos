// Dock system applications (W6): Home launchpad, Files over the indexed
// workspace projection, Docs and Code over the project's review artifacts,
// and the sandboxed Browser window. Everything is read-only and bounded;
// the Browser renders external pages inside a sandboxed iframe whose
// missing allow-popups/allow-top-navigation flags are the _blank and
// top-navigation interception boundary.
import { IndexedDocumentPreview } from "./IndexedDocumentPreview.js";
import { Icon, Button, type IconName } from "@workos/ui-kit";
import { useEffect, useCallback, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { ArtifactReference } from "./ArtifactCenter.js";

export interface HomeAppEntry {
  id: string;
  icon: IconName;
  label: string;
  hint: string;
  available: boolean;
  open: () => void;
}

export function HomeApp(props: { apps: HomeAppEntry[] }) {
  const { apps } = props;
  return (
    <div className="home-app system-app" data-testid="home-app">
      <header className="app-heading">
        <p>YOUR WORKSPACE</p>
        <h1>Your workspace</h1>
        <span>Open a tool to get started.</span>
      </header>
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
              <span className="home-icon">
                <Icon name={app.icon} size={24} />
              </span>
              <span className="home-label">{app.label}</span>
              <span className="home-hint">{app.available ? app.hint : "unavailable"}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

type IndexedHit = Awaited<ReturnType<WorkOSClients["index"]["searchHybrid"]>>["hits"][number];

export function FilesApp({
  projectId,
  workosClients,
}: {
  projectId: string;
  workosClients: WorkOSClients;
}) {
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<IndexedHit[]>([]);
  const [cursor, setCursor] = useState("");
  const [verdict, setVerdict] = useState("");
  const [busy, setBusy] = useState(false);
  const [searched, setSearched] = useState(false);
  const [preview, setPreview] = useState<IndexedHit>();
  const generation = useRef(0);
  const running = useRef(false);
  const submittedQuery = useRef("");
  useEffect(
    () => () => {
      generation.current++;
    },
    [projectId],
  );
  const search = async (pageToken = "") => {
    const text = pageToken ? submittedQuery.current : query.trim();
    if (!text || running.current) return;
    const operation = ++generation.current;
    running.current = true;
    setBusy(true);
    setVerdict("");
    if (!pageToken) {
      setHits([]);
      setCursor("");
      submittedQuery.current = text;
    }
    try {
      const result = await workosClients.index.searchHybrid({
        projectId,
        query: text,
        sourceType: "workspace.file.v1",
        page: { pageSize: 20, pageToken },
      });
      if (operation !== generation.current) return;
      if (
        result.hits.some(
          (hit) =>
            hit.sourceRef?.type !== "workspace.file.v1" ||
            hit.sourceRef.id !== hit.artifactId ||
            hit.sourceRef.revision !== hit.digest,
        )
      )
        throw new Error("invalid file result");
      setHits((current) =>
        pageToken
          ? [
              ...current,
              ...result.hits.filter(
                (hit) => !current.some((old) => old.artifactId === hit.artifactId),
              ),
            ]
          : result.hits,
      );
      setCursor(result.page?.nextPageToken ?? "");
      setSearched(true);
    } catch {
      if (operation === generation.current)
        setVerdict("Workspace search could not be loaded. Try again.");
    } finally {
      if (operation === generation.current) {
        running.current = false;
        setBusy(false);
      }
    }
  };
  if (preview?.sourceRef)
    return (
      <IndexedDocumentPreview
        projectId={projectId}
        source={preview.sourceRef}
        workosClients={workosClients}
        onClose={() => {
          setPreview(undefined);
        }}
      />
    );
  return (
    <div className="files-app" data-testid="files-app">
      <header className="app-heading">
        <p>WORKSPACE</p>
        <h1>Files</h1>
        <span>Search and preview indexed project files.</span>
      </header>
      <form
        className="files-search"
        onSubmit={(event) => {
          event.preventDefault();
          void search();
        }}
      >
        <input
          aria-label="Search workspace files"
          placeholder="Search files…"
          className="files-query"
          value={query}
          maxLength={256}
          onChange={(event) => {
            setQuery(event.target.value);
          }}
        />
        <Button type="submit" disabled={busy || !query.trim()}>
          Search
        </Button>
      </form>
      {busy ? <p role="status">Searching…</p> : null}
      {verdict ? (
        <p role="alert" className="files-verdict">
          {verdict}
        </p>
      ) : null}
      <ul className="files-results">
        {hits.map((hit) => (
          <li key={hit.artifactId}>
            <button
              className="files-hit"
              type="button"
              onClick={() => {
                setPreview(hit);
              }}
            >
              <span className="files-title">{hit.title}</span>
              <span className="files-excerpt">{hit.excerpt}</span>
            </button>
          </li>
        ))}
      </ul>
      {!busy && !verdict && searched && !hits.length ? (
        <p className="empty-state">No indexed files match this search.</p>
      ) : null}
      {cursor ? (
        <Button type="button" disabled={busy} onClick={() => void search(cursor)}>
          Load more files
        </Button>
      ) : null}
      {!searched ? (
        <p className="files-note">
          Files appear here after a workspace has been connected and indexed.
        </p>
      ) : null}
    </div>
  );
}

function ArtifactList({
  projectId,
  workosClients,
  onOpenArtifact,
  type,
  label,
}: {
  projectId: string;
  workosClients: WorkOSClients;
  onOpenArtifact: (artifact: ArtifactReference) => void;
  type: string;
  label: "Docs" | "Code";
}) {
  const [artifacts, setArtifacts] = useState<ArtifactReference[]>([]);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const running = useRef(false);
  const load = useCallback(
    async (pageToken = "") => {
      if (running.current) return;
      const operation = generation.current;
      running.current = true;
      setBusy(true);
      setError("");
      try {
        const result = await workosClients.artifacts.listArtifacts({
          projectId,
          page: { pageSize: 50, pageToken },
        });
        if (operation !== generation.current) return;
        const items = result.artifacts
          .filter((item) => item.type === type)
          .map((item) => ({
            id: item.id,
            projectId,
            title: item.title,
            type: item.type,
            digest: item.digest,
          }));
        setArtifacts((current) =>
          pageToken
            ? [...current, ...items.filter((item) => !current.some((old) => old.id === item.id))]
            : items,
        );
        setCursor(result.page?.nextPageToken ?? "");
      } catch {
        if (operation === generation.current) setError("Could not load this list. Try again.");
      } finally {
        if (operation === generation.current) {
          running.current = false;
          setBusy(false);
        }
      }
    },
    [projectId, type, workosClients],
  );
  useEffect(() => {
    generation.current++;
    running.current = false;
    setArtifacts([]);
    setCursor("");
    void load();
    return () => {
      generation.current++;
      running.current = false;
    };
  }, [load]);
  const kind = label.toLowerCase();
  return (
    <div className={`${kind}-app`} data-testid={`${kind}-app`}>
      <header className="app-heading">
        <p>PROJECT OUTPUTS</p>
        <h1>{label === "Docs" ? "Documents" : "Proposed changes"}</h1>
        <span>
          {label === "Docs"
            ? "Read and review project documents."
            : "Inspect patches before taking the next step."}
        </span>
      </header>
      {busy ? <p role="status">Loading…</p> : null}
      {error ? (
        <div role="alert">
          <p>{error}</p>
          <Button type="button" onClick={() => void load(cursor)}>
            Retry
          </Button>
        </div>
      ) : null}
      <ul className={`${kind}-list`}>
        {artifacts.map((artifact) => (
          <li key={artifact.id}>
            <button
              type="button"
              className={`${kind}-entry`}
              onClick={() => {
                onOpenArtifact(artifact);
              }}
            >
              <span className={`${kind}-title`}>{artifact.title}</span>
              <Icon name="arrow" size={16} />
            </button>
          </li>
        ))}
      </ul>
      {!busy && !error && !artifacts.length ? (
        <p className="empty-state">
          {cursor
            ? "More project outputs are available on the next page."
            : label === "Docs"
              ? "No markdown documents in this project yet."
              : "No proposed patches in this project yet."}
        </p>
      ) : null}
      {cursor && !error ? (
        <Button type="button" disabled={busy} onClick={() => void load(cursor)}>
          Load more
        </Button>
      ) : null}
    </div>
  );
}

type ArtifactAppProps = {
  projectId: string;
  workosClients: WorkOSClients;
  onOpenArtifact: (artifact: ArtifactReference) => void;
};
export function DocsApp(props: ArtifactAppProps) {
  return <ArtifactList {...props} type="document.markdown.v1" label="Docs" />;
}
export function CodeApp(props: ArtifactAppProps) {
  return <ArtifactList {...props} type="code.unified-diff.v1" label="Code" />;
}

// BrowserApp keeps external web content inside the WorkOS window with a
// opaque sandbox: even a same-origin page cannot access desktop storage or DOM.
// Popups and top navigation remain disabled.
// Only http(s) URLs are accepted; everything else is a fixed verdict.
export function BrowserApp(props: {
  initialUrl?: string;
  workosClients?: WorkOSClients;
  activeProjectId?: string;
}) {
  const [url, setUrl] = useState(props.initialUrl ?? "");
  const [target, setTarget] = useState("");
  const [verdict, setVerdict] = useState("");
  const [poolNotice, setPoolNotice] = useState("");
  const [frame, setFrame] = useState<{ data: Uint8Array; width: number; height: number } | null>(
    null,
  );
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const sessionIdRef = useRef<string>("");
  const [sessionReady, setSessionReady] = useState(false);
  const clients = props.workosClients;
  const projectId = props.activeProjectId ?? "";

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
    void (async () => {
      if (!clients || !projectId) {
        setPoolNotice("browser pool unavailable — sandboxed view");
        setTarget(parsed.toString());
        return;
      }
      setPoolNotice("");
      try {
        if (sessionIdRef.current) {
          await clients.browserSessions.navigateBrowserSession({
            sessionId: sessionIdRef.current,
            url: parsed.toString(),
          });
          return;
        }
        const created = await clients.browserSessions.createBrowserSession({
          idempotencyKey: `desktop-browser-${crypto.randomUUID()}`,
          projectId,
          initialUrl: parsed.toString(),
        });
        sessionIdRef.current = created.session?.id ?? "";
        setSessionReady(Boolean(created.session?.id));
      } catch {
        setPoolNotice("browser pool unavailable — sandboxed view");
        setTarget(parsed.toString());
      }
    })();
  };

  useEffect(() => {
    if (props.initialUrl) {
      navigate(props.initialUrl);
    }
    // navigate is a stable local closure over the current clients/project.
  }, [props.initialUrl]);

  useEffect(() => {
    if (!clients || !sessionReady || !sessionIdRef.current) return;
    const abort = new AbortController();
    const paint = async () => {
      try {
        for await (const event of clients.browserSessions.watchBrowserSession(
          { sessionId: sessionIdRef.current },
          { signal: abort.signal },
        )) {
          if (event.kind.case === "frame" && event.kind.value.jpeg.length > 0) {
            const frame = event.kind.value;
            setFrame({ data: frame.jpeg, width: frame.width || 1280, height: frame.height || 800 });
          }
        }
      } catch {
        setPoolNotice("browser pool unavailable — sandboxed view");
      }
    };
    void paint();
    return () => {
      abort.abort();
      const session = sessionIdRef.current;
      if (session) {
        void clients.browserSessions
          .closeBrowserSession({ sessionId: session })
          .catch(() => undefined);
      }
    };
  }, [clients, sessionReady]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas || !frame) return;
    const blob = new Blob([frame.data as unknown as BlobPart], { type: "image/jpeg" });
    const bitmapUrl = URL.createObjectURL(blob);
    const image = new Image();
    image.onload = () => {
      canvas.width = image.width;
      canvas.height = image.height;
      canvas.getContext("2d")?.drawImage(image, 0, 0);
      URL.revokeObjectURL(bitmapUrl);
    };
    image.src = bitmapUrl;
  }, [frame]);

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
      {poolNotice ? (
        <p className="browser-verdict" role="status" data-testid="browser-pool-notice">
          {poolNotice}
        </p>
      ) : null}
      {sessionReady ? (
        <canvas
          className="browser-frame"
          data-testid="browser-canvas"
          ref={canvasRef}
          title="Remote browser"
        />
      ) : null}
      {!sessionReady && props.initialUrl && poolNotice ? (
        <iframe
          className="browser-frame"
          data-testid="browser-frame"
          src={props.initialUrl}
          // Fixed boundary: popups (_blank) and top navigation are
          // intercepted by the sandbox itself; the WorkOS window is never
          // navigated away and no browser tab is opened.
          sandbox="allow-scripts allow-forms"
          title="Embedded browser"
        />
      ) : null}
      {!sessionReady && !target ? (
        <p className="empty-state">Enter an address to browse inside WorkOS.</p>
      ) : null}
    </div>
  );
}
