// Artifact Center: one normal desktop window listing the active project's
// review artifacts. It keys/remounts on the project, isolates in-flight
// responses per generation so a late answer can never paint a project it no
// longer belongs to, and never displays content itself — selecting an entry
// asks the Desktop to open the authoritative read-only viewer window.
import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import { Button } from "@workos/ui-kit";

// The center only needs the artifact's identity and summary facts to open
// the viewer window; content is always re-fetched through the authoritative
// service. The generated list item satisfies this shape structurally.
export interface ArtifactReference {
  id: string;
  projectId: string;
  title: string;
  type: string;
  createdAt?: { seconds: bigint; nanos: number } | undefined;
}

interface ArtifactCenterProps {
  projectId: string;
  workosClients: WorkOSClients;
  onOpenArtifact: (artifact: ArtifactReference) => void;
}

const PAGE_SIZE = 20;

// artifactCenterErrorMessage projects the fixed server error matrix onto
// user-facing copy. Unknown/foreign projects and artifacts read the same
// "not available" so nothing leaks existence.
function artifactCenterErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "Artifact list could not be loaded.";
  switch (reason.code) {
    case Code.NotFound:
      return "This project's artifacts are not available.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "Artifact store is temporarily unavailable.";
    case Code.Unauthenticated:
      return "Your device session has ended. Sign in again, then retry.";
    default:
      return "Artifact list could not be loaded.";
  }
}

interface ArtifactListState {
  items: ArtifactReference[];
  nextPageToken: string;
  loading: boolean;
  loadingMore: boolean;
  error?: string | undefined;
}

export function ArtifactCenter({ projectId, workosClients, onOpenArtifact }: ArtifactCenterProps) {
  const [state, setState] = useState<ArtifactListState>({
    items: [],
    nextPageToken: "",
    loading: true,
    loadingMore: false,
  });
  // A response that resolves after a project switch, a newer page load, or
  // unmount must never paint: every fetch captures the generation it
  // started under and re-checks before touching state.
  const generationRef = useRef(0);
  const projectIdRef = useRef(projectId);
  projectIdRef.current = projectId;

  const isLive = (generation: number) =>
    generationRef.current === generation && projectIdRef.current === projectId;

  const load = useCallback(
    async (cursor: string) => {
      const generation = ++generationRef.current;
      setState((current) =>
        cursor === ""
          ? { items: [], nextPageToken: "", loading: true, loadingMore: false }
          : { ...current, loadingMore: true, error: undefined },
      );
      try {
        const response = await workosClients.artifacts.listArtifacts({
          projectId,
          page: { pageSize: PAGE_SIZE, pageToken: cursor },
        });
        if (!isLive(generation)) return;
        setState((current) => ({
          items: cursor === "" ? response.artifacts : [...current.items, ...response.artifacts],
          nextPageToken: response.page?.nextPageToken ?? "",
          loading: false,
          loadingMore: false,
        }));
      } catch (reason) {
        if (!isLive(generation)) return;
        setState((current) => ({
          items: cursor === "" ? [] : current.items,
          nextPageToken: "",
          loading: false,
          loadingMore: false,
          error: artifactCenterErrorMessage(reason),
        }));
      }
    },
    [projectId, workosClients],
  );

  useEffect(() => {
    void load("");
    // Unmount invalidates in-flight fetches exactly once.
    return () => {
      generationRef.current += 1;
    };
  }, [load]);

  if (state.loading) {
    return <p className="empty-state">Loading artifacts…</p>;
  }
  if (state.error) {
    return (
      <div className="artifact-center-body">
        <p className="empty-state" role="alert">
          {state.error}
        </p>
        <Button type="button" onClick={() => void load("")}>
          Retry
        </Button>
      </div>
    );
  }
  if (state.items.length === 0) {
    return (
      <p className="empty-state">
        No review artifacts yet. Run a task that requests a markdown document or a unified diff.
      </p>
    );
  }
  return (
    <div className="artifact-center-body" aria-label="Artifact Center">
      <ul className="artifact-list">
        {state.items.map((artifact) => (
          <li key={artifact.id}>
            <button
              className="artifact-row"
              data-testid="artifact-row"
              type="button"
              onClick={() => {
                onOpenArtifact(artifact);
              }}
            >
              <strong>{artifact.title}</strong>
              <span>{artifact.type === "document.markdown.v1" ? "Markdown" : "Unified diff"}</span>
              <small>
                {artifact.createdAt
                  ? `${new Date(Number(artifact.createdAt.seconds) * 1000)
                      .toISOString()
                      .slice(0, 16)
                      .replace("T", " ")} UTC`
                  : "just now"}
              </small>
            </button>
          </li>
        ))}
      </ul>
      {state.nextPageToken !== "" ? (
        <Button
          type="button"
          disabled={state.loadingMore}
          onClick={() => void load(state.nextPageToken)}
        >
          {state.loadingMore ? "Loading…" : "Load more"}
        </Button>
      ) : null}
    </div>
  );
}
