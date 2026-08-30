// One artifact viewer window: fetches the authoritative typed content for
// exactly one artifact through ArtifactService and renders it inertly. The
// server re-checks owner/project on every read; this component only projects
// the sanitized verdicts (loading / unavailable / content) and never caches
// stale bodies — a failed or switched fetch clears whatever was shown.
import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { Artifact } from "@workos/protocol";
import { ArtifactViewer } from "@workos/artifact-viewer";

interface ViewerState {
  loading: boolean;
  error?: string | undefined;
  artifact?: Artifact | undefined;
  content?: Uint8Array | undefined;
}

const LOADING: ViewerState = { loading: true };
const UNAVAILABLE: ViewerState = { loading: false, error: "unavailable" };

function viewerError(reason: unknown): ViewerState {
  if (reason instanceof ConnectError) {
    switch (reason.code) {
      case Code.NotFound:
      case Code.Unimplemented:
      case Code.PermissionDenied:
        return UNAVAILABLE;
      case Code.Unavailable:
      case Code.DeadlineExceeded:
        return { loading: false, error: "unavailable" };
      case Code.Unauthenticated:
        return { loading: false, error: "unavailable" };
      default:
        return UNAVAILABLE;
    }
  }
  return UNAVAILABLE;
}

export function ArtifactViewerWindow({
  artifactId,
  workosClients,
}: {
  artifactId: string;
  workosClients: WorkOSClients;
}) {
  const [state, setState] = useState<ViewerState>(LOADING);
  const generationRef = useRef(0);
  const artifactIdRef = useRef(artifactId);
  artifactIdRef.current = artifactId;

  useEffect(() => {
    const generation = ++generationRef.current;
    setState(LOADING);
    void (async () => {
      try {
        const response = await workosClients.artifacts.getReviewArtifact({ artifactId });
        if (generationRef.current !== generation) return;
        const content = response.content?.content;
        let bytes: Uint8Array | undefined;
        if (content?.case === "markdown" || content?.case === "unifiedDiff") {
          bytes = content.value.content;
        }
        if (!response.artifact || !bytes) {
          setState(UNAVAILABLE);
          return;
        }
        setState({ loading: false, artifact: response.artifact, content: bytes });
      } catch (reason) {
        if (generationRef.current !== generation) return;
        setState(viewerError(reason));
      }
    })();
    return () => {
      generationRef.current += 1;
    };
  }, [artifactId, workosClients]);

  return (
    <div className="artifact-viewer-body">
      <ArtifactViewer
        artifact={state.artifact}
        content={state.content}
        loading={state.loading}
        error={state.error}
      />
    </div>
  );
}
