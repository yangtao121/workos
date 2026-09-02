// Knowledge Center: one normal desktop window with bounded lexical search
// over the active project's indexed review artifacts (ADR-0013). The window
// is generation-guarded per project and per query, renders excerpts as
// inert text only, and never injects anything into an Agent task by itself —
// pinning a hit goes through the Desktop's existing canonical context chips.
import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import { Button } from "@workos/ui-kit";

const PAGE_SIZE = 20;

// Response boundary validation: any malformed server fact fails the whole
// page closed instead of reaching the DOM or the context state.
const UUID_V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const SHA256 = /^sha256:[0-9a-f]{64}$/;
const REVIEW_TYPES = new Set(["document.markdown.v1", "code.unified-diff.v1"]);
const MAX_EXCERPT_CODE_POINTS = 200;
const PAGE_TOKEN = /^[A-Za-z0-9_-]{1,4096}$/;

function isSafePlainText(value: string, maxCodePoints: number, allowEmpty: boolean): boolean {
  let codePoints = 0;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) return false;
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false;
    }
    codePoints += 1;
    if (codePoints > maxCodePoints) return false;
  }
  return allowEmpty || codePoints > 0;
}

interface KnowledgeHit {
  artifactId: string;
  digest: string;
  artifactType: string;
  title: string;
  excerpt: string;
  score: number;
}

function validateHit(
  hit: NonNullable<Awaited<ReturnType<WorkOSClients["index"]["search"]>>["hits"]>[number],
): KnowledgeHit | null {
  const artifactId = hit.artifactId;
  const digest = hit.digest;
  if (!UUID_V7.test(artifactId) || !SHA256.test(digest)) return null;
  if (!REVIEW_TYPES.has(hit.artifactType)) return null;
  if (!Number.isFinite(hit.score) || hit.score < 0 || hit.score > 3) return null;
  const title = hit.title;
  if (!isSafePlainText(title, 200, false)) return null;
  const excerpt = hit.excerpt;
  if (!isSafePlainText(excerpt, MAX_EXCERPT_CODE_POINTS, false)) return null;
  // The typed ref and the legacy projection must agree — a drifting pair is
  // corruption, not a display problem.
  if (
    hit.sourceRef?.type !== "artifact.review.v1" ||
    hit.sourceRef.id !== artifactId ||
    hit.sourceRef.revision !== digest ||
    hit.contextRef !== `artifact.review.v1:${artifactId}:${digest}`
  ) {
    return null;
  }
  return {
    artifactId,
    digest,
    artifactType: hit.artifactType,
    title,
    excerpt,
    score: hit.score,
  };
}

function knowledgeErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "Knowledge search could not be loaded.";
  switch (reason.code) {
    case Code.InvalidArgument:
      return "That search query is not valid.";
    case Code.NotFound:
      return "Knowledge search is not available for this project.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "Knowledge index is temporarily unavailable.";
    case Code.Unauthenticated:
      return "Your device session has ended. Sign in again, then retry.";
    default:
      return "Knowledge search could not be loaded.";
  }
}

type KnowledgeStatus = "idle" | "searching" | "results" | "empty" | "unavailable";

interface KnowledgeCenterProps {
  projectId: string;
  workosClients: WorkOSClients;
  selectedContextIds?: ReadonlySet<string> | undefined;
  onUseAsContext: (hit: KnowledgeHit) => void;
  onOpenArtifact: (artifactId: string) => void;
}

export function KnowledgeCenter({
  projectId,
  workosClients,
  selectedContextIds,
  onUseAsContext,
  onOpenArtifact,
}: KnowledgeCenterProps) {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<KnowledgeStatus>("idle");
  const [hits, setHits] = useState<KnowledgeHit[]>([]);
  const [nextPageToken, setNextPageToken] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  const [catchingUp, setCatchingUp] = useState(false);
  // Generation guard: a late response from a previous project, a previous
  // query, or an unmounted window must never paint or pin anything.
  const generationRef = useRef(0);
  const projectIdRef = useRef(projectId);
  projectIdRef.current = projectId;

  useEffect(
    () => () => {
      generationRef.current += 1;
    },
    [],
  );

  const isLive = (generation: number) =>
    generationRef.current === generation && projectIdRef.current === projectId;

  const runSearch = useCallback(
    async (cursor: string) => {
      const trimmed = query.trim();
      if (trimmed === "" && cursor === "") {
        // An empty query never reaches the server.
        setStatus("idle");
        setHits([]);
        setNextPageToken("");
        return;
      }
      const generation = ++generationRef.current;
      if (cursor === "") {
        setStatus("searching");
        setHits([]);
        setNextPageToken("");
        setNotice("");
      } else {
        setLoadingMore(true);
      }
      setError("");
      try {
        const response = await workosClients.index.search({
          projectId,
          query: trimmed,
          page: { pageSize: PAGE_SIZE, pageToken: cursor },
        });
        if (!isLive(generation)) return;
        const validated: KnowledgeHit[] = [];
        for (const hit of response.hits) {
          const safe = validateHit(hit);
          if (safe === null) {
            // One malformed hit fails the page closed (ADR-0013 D1).
            throw new Error("knowledge hit failed response validation");
          }
          validated.push(safe);
        }
        const responseToken = response.page?.nextPageToken ?? "";
        if (responseToken !== "" && !PAGE_TOKEN.test(responseToken)) {
          throw new Error("knowledge page token failed response validation");
        }
        setHits((current) => (cursor === "" ? validated : [...current, ...validated]));
        setNextPageToken(responseToken);
        setCatchingUp(!(response.freshness?.caughtUp ?? true));
        setStatus(validated.length === 0 && cursor === "" ? "empty" : "results");
      } catch (reason) {
        if (!isLive(generation)) return;
        setHits([]);
        setNextPageToken("");
        setStatus("unavailable");
        setError(knowledgeErrorMessage(reason));
      } finally {
        if (isLive(generation)) setLoadingMore(false);
      }
    },
    [projectId, query, workosClients],
  );

  const submit = () => {
    void runSearch("");
  };

  return (
    <div className="knowledge-center-body" aria-label="Knowledge Center">
      <form
        className="knowledge-search-form"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <input
          data-testid="knowledge-search-input"
          aria-label="Search project knowledge"
          placeholder="Search this project's reviews"
          value={query}
          onChange={(event) => {
            generationRef.current += 1;
            setQuery(event.target.value);
            setHits([]);
            setNextPageToken("");
            setError("");
            setNotice("");
            setLoadingMore(false);
            setCatchingUp(false);
            setStatus("idle");
          }}
        />
        <Button
          data-testid="knowledge-search-submit"
          type="submit"
          disabled={status === "searching"}
        >
          {status === "searching" ? "Searching…" : "Search"}
        </Button>
        <Button
          type="button"
          data-testid="knowledge-search-clear"
          onClick={() => {
            generationRef.current += 1;
            setQuery("");
            setHits([]);
            setNextPageToken("");
            setError("");
            setNotice("");
            setStatus("idle");
          }}
        >
          Clear
        </Button>
      </form>
      {catchingUp && status === "results" ? (
        <p className="knowledge-freshness" data-testid="knowledge-catching-up">
          Index is catching up — newest documents may be missing.
        </p>
      ) : null}
      {status === "searching" ? <p className="empty-state">Searching…</p> : null}
      {status === "idle" ? (
        <p className="empty-state">Search the review documents this project has produced.</p>
      ) : null}
      {status === "empty" ? (
        <p className="empty-state" data-testid="knowledge-empty">
          No knowledge matched that query.
        </p>
      ) : null}
      {status === "unavailable" ? (
        <div>
          <p className="empty-state" role="alert" data-testid="knowledge-unavailable">
            {error}
          </p>
          <Button type="button" onClick={submit}>
            Retry
          </Button>
        </div>
      ) : null}
      {status === "results" ? (
        <>
          <ul className="knowledge-results" aria-label="Knowledge results">
            {hits.map((hit) => (
              <li key={hit.artifactId} data-testid="knowledge-result">
                <button
                  className="knowledge-hit"
                  type="button"
                  onClick={() => {
                    onOpenArtifact(hit.artifactId);
                  }}
                >
                  <strong>{hit.title}</strong>
                  <span>
                    {hit.artifactType === "document.markdown.v1" ? "Markdown" : "Unified diff"}
                  </span>
                  <p className="knowledge-excerpt">{hit.excerpt}</p>
                </button>
                {selectedContextIds?.has(hit.artifactId) ? (
                  <p className="context-selected-note" data-testid="knowledge-context-selected">
                    Pinned as Agent context.
                  </p>
                ) : (
                  <Button
                    data-testid="knowledge-use-as-context"
                    onClick={() => {
                      onUseAsContext(hit);
                    }}
                  >
                    Use as Agent context
                  </Button>
                )}
              </li>
            ))}
          </ul>
          {nextPageToken !== "" ? (
            <Button
              type="button"
              data-testid="knowledge-load-more"
              disabled={loadingMore}
              onClick={() => void runSearch(nextPageToken)}
            >
              {loadingMore ? "Loading…" : "Load more"}
            </Button>
          ) : null}
        </>
      ) : null}
      {notice !== "" ? <p className="empty-state">{notice}</p> : null}
    </div>
  );
}

export type { KnowledgeHit };
