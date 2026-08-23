import { AgentTaskState } from "@workos/protocol";

export function taskStatus(state: AgentTaskState): string {
  return AgentTaskState[state].replace("AGENT_TASK_STATE_", "").toLowerCase();
}
