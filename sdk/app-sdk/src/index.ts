import type { AgentEvent, AgentTaskInput, Project } from "@workos/protocol";

export interface WorkOSAppBridge {
  readonly identity: { appId: string; projectId?: string };
  project: { current(): Promise<Project> };
  agent: {
    run(input: AgentTaskInput): Promise<{ taskId: string }>;
    stream(taskId: string, afterSequence?: bigint): AsyncIterable<AgentEvent>;
  };
  window: {
    setTitle(title: string): void;
    setBadge(count?: number): void;
    maximize(): void;
    minimize(): void;
    close(): void;
  };
}

export type Capability =
  | "project.read"
  | "project.artifacts.read"
  | "agent.project.invoke"
  | "agent.global.invoke"
  | "notifications.create";
