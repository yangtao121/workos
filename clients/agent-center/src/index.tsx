import type { AgentEvent } from "@workos/protocol";

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
      return `Approval · ${event.event.value.title}`;
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

export function AgentTimeline({ events }: { events: AgentEvent[] }) {
  if (events.length === 0) {
    return <p className="empty-state">No task events yet.</p>;
  }
  return (
    <ol className="agent-timeline" aria-label="Agent task events">
      {events.map((event) => (
        <li key={event.id || event.sequence.toString()} data-event={event.event.case}>
          <span>{event.sequence.toString().padStart(2, "0")}</span>
          <p>{describeAgentEvent(event)}</p>
        </li>
      ))}
    </ol>
  );
}
