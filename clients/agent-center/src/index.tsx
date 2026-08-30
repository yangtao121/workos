import { AppAgentApprovalDecision, type AgentEvent } from "@workos/protocol";

export function describeAgentEvent(event: AgentEvent): string {
  switch (event.event.case) {
    case "runStarted":
      return `Run started · ${event.event.value.providerId}`;
    case "assistantDelta":
      return event.event.value.text;
    case "assistantMessage":
      return event.event.value.text;
    case "usageRecorded":
      return `Usage · ${event.event.value.inputTokens.toString()} in / ${event.event.value.outputTokens.toString()} out`;
    case "runCompleted":
      return event.event.value.summary;
    case "runFailed":
      return `Failed · ${event.event.value.reason}`;
    case "runCancelled":
      return `Cancelled · ${event.event.value.reason}`;
    case "runWaiting":
      return `Waiting · ${event.event.value.reason}`;
    case "approvalRequired":
      return `Approval required · waiting for user decision · ${event.event.value.title}`;
    case "approvalDecided":
      return `Approval ${event.event.value.decision === AppAgentApprovalDecision.APPROVE ? "approved" : "rejected"}`;
    case "approvalExpired":
      return "Approval expired · policy changed before a decision";
    case "artifactCreated":
      return `Artifact · ${event.event.value.artifactType}`;
    case "toolCallStarted":
      return `Tool started · ${event.event.value.toolName}`;
    case "toolCallCompleted":
      return `Tool ${event.event.value.success ? "completed" : "failed"}`;
    default:
      return "Agent event";
  }
}

export function AgentTimeline({
  events,
  onOpenArtifact,
}: {
  events: AgentEvent[];
  // When provided, Core-minted artifact events become accessible buttons.
  // The handler re-fetches authoritative content through ArtifactService —
  // the event reference itself is provenance, never the content source.
  onOpenArtifact?: (artifactId: string) => void;
}) {
  if (events.length === 0) {
    return <p className="empty-state">No task events yet.</p>;
  }
  return (
    <ol className="agent-timeline" aria-label="Agent task events">
      {events.map((event) => {
        // Narrow once outside the click closure: the artifact id travels as
        // provenance only, and the handler re-fetches authoritative content.
        const artifactId =
          event.event.case === "artifactCreated" ? event.event.value.artifactId : undefined;
        const handler = artifactId !== "" && artifactId !== undefined ? onOpenArtifact : undefined;
        return (
          <li key={event.id || event.sequence.toString()} data-event={event.event.case}>
            <span>{event.sequence.toString().padStart(2, "0")}</span>
            {handler && artifactId !== undefined && artifactId !== "" ? (
              <button
                className="timeline-artifact"
                type="button"
                onClick={() => {
                  handler(artifactId);
                }}
              >
                {describeAgentEvent(event)}
                <small>Open review</small>
              </button>
            ) : (
              <p>{describeAgentEvent(event)}</p>
            )}
          </li>
        );
      })}
    </ol>
  );
}
