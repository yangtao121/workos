import { useEffect, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { ContextRef } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

export function IndexedDocumentPreview({
  projectId,
  source,
  workosClients,
  onClose,
}: {
  projectId: string;
  source: Pick<ContextRef, "type" | "id" | "revision">;
  workosClients: WorkOSClients;
  onClose: () => void;
}) {
  const [document, setDocument] = useState<{ title: string; content: string }>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const { type, id, revision } = source;
  useEffect(() => {
    let live = true;
    setDocument(undefined);
    setError("");
    void workosClients.index
      .readDocument({ projectId, source: { type, id, revision } })
      .then((result) => {
        if (!live) return;
        if (
          result.source?.id !== id ||
          result.source.type !== type ||
          result.source.revision !== revision ||
          new TextEncoder().encode(result.content).length > 512 * 1024
        )
          throw new Error("invalid preview");
        setDocument({ title: result.title, content: result.content });
      })
      .catch(() => {
        if (live)
          setError("This snapshot is no longer available. Refresh the search or try again.");
      });
    return () => {
      live = false;
    };
  }, [projectId, type, id, revision, workosClients, attempt]);
  return (
    <section className="indexed-preview" aria-label="Indexed document preview">
      <header>
        <div>
          <strong>{document?.title ?? "Document preview"}</strong>
          <p>Indexed snapshot · read only</p>
        </div>
        <Button type="button" onClick={onClose}>
          Back to results
        </Button>
      </header>
      {error ? (
        <div role="alert">
          <p>{error}</p>
          <Button
            type="button"
            onClick={() => {
              setAttempt((value) => value + 1);
            }}
          >
            Retry
          </Button>
        </div>
      ) : document ? (
        <pre>{document.content}</pre>
      ) : (
        <p role="status">Loading document…</p>
      )}
    </section>
  );
}
