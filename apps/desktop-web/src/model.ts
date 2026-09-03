import {
  AgentTaskState,
  HealthState,
  type HarnessCapabilities,
  type Project,
} from "@workos/protocol";

export type HarnessSelection = { kind: "global" } | { kind: "provider"; providerId: string };

export type HarnessCapabilityID =
  | "credentialKind"
  | "streaming"
  | "persistentSessions"
  | "resume"
  | "steerDuringRun"
  | "approvals"
  | "toolRegistration"
  | "mcp"
  | "subagents"
  | "workspaceMount"
  | "structuredArtifacts"
  | "usageReporting"
  | "hardTokenBudget"
  | "hardRuntimeDeadline";

export interface CapabilityEntry {
  id: HarnessCapabilityID;
  label: string;
  available: boolean;
  // detail carries the exact credential kind for the credentialKind entry.
  detail?: string;
}

export function taskStatus(state: AgentTaskState): string {
  return AgentTaskState[state].replace("AGENT_TASK_STATE_", "").toLowerCase();
}

export function selectionFromProject(project: Project): HarnessSelection {
  const providerId = project.harnessBinding?.providerId.trim();
  return providerId ? { kind: "provider", providerId } : { kind: "global" };
}

export function sameSelection(left: HarnessSelection, right: HarnessSelection): boolean {
  return (
    left.kind === right.kind &&
    (left.kind === "global" || (right.kind === "provider" && left.providerId === right.providerId))
  );
}

export function healthLabel(health: HealthState): string {
  switch (health) {
    case HealthState.STARTING:
      return "starting";
    case HealthState.HEALTHY:
      return "healthy";
    case HealthState.DEGRADED:
      return "degraded";
    case HealthState.UNAVAILABLE:
      return "unavailable";
    default:
      return "unknown";
  }
}

export function providerSelectable(health: HealthState): boolean {
  return health === HealthState.HEALTHY || health === HealthState.DEGRADED;
}

export function capabilityEntries(capabilities?: HarnessCapabilities): CapabilityEntry[] {
  const definitions: Array<[Exclude<HarnessCapabilityID, "credentialKind">, string]> = [
    ["streaming", "Streaming"],
    ["persistentSessions", "Persistent sessions"],
    ["resume", "Resume"],
    ["steerDuringRun", "Steer during run"],
    ["approvals", "Approvals"],
    ["toolRegistration", "Tool registration"],
    ["mcp", "MCP"],
    ["subagents", "Subagents"],
    ["workspaceMount", "Workspace mount"],
    ["structuredArtifacts", "Structured artifacts"],
    ["usageReporting", "Usage reporting"],
    ["hardTokenBudget", "Hard token budget"],
    ["hardRuntimeDeadline", "Hard runtime deadline"],
  ];
  const entries: CapabilityEntry[] = definitions.map(([id, label]) => ({
    id,
    label,
    available: capabilities?.[id] === true,
  }));
  const purpose = capabilities?.requiresTaskCredentialLease
    ? (capabilities.requiredCredentialPurpose || "").trim()
    : "";
  entries.push({
    id: "credentialKind",
    label: "Lease purpose",
    available: capabilities?.requiresTaskCredentialLease === true && purpose !== "",
    detail: capabilities?.requiresTaskCredentialLease === true && purpose !== "" ? purpose : "none",
  });
  return entries;
}
