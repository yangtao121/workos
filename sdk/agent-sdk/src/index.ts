import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AgentTaskService, ProjectService } from "@workos/protocol";

export interface WorkOSClients {
  projects: Client<typeof ProjectService>;
  agentTasks: Client<typeof AgentTaskService>;
}

export function createWorkOSClients(baseUrl: string, transport?: Transport): WorkOSClients {
  const activeTransport = transport ?? createConnectTransport({ baseUrl });
  return {
    projects: createClient(ProjectService, activeTransport),
    agentTasks: createClient(AgentTaskService, activeTransport),
  };
}
