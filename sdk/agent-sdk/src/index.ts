import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  AgentTaskService,
  AppInstallationService,
  AppRegistryService,
  HarnessCatalogService,
  ProjectHarnessBindingService,
  ProjectService,
} from "@workos/protocol";

export interface WorkOSClients {
  projects: Client<typeof ProjectService>;
  projectHarnessBindings: Client<typeof ProjectHarnessBindingService>;
  harnessCatalog: Client<typeof HarnessCatalogService>;
  agentTasks: Client<typeof AgentTaskService>;
  appRegistry: Client<typeof AppRegistryService>;
  appInstallations: Client<typeof AppInstallationService>;
}

export function createWorkOSClients(baseUrl: string, transport?: Transport): WorkOSClients {
  const activeTransport = transport ?? createConnectTransport({ baseUrl });
  return {
    projects: createClient(ProjectService, activeTransport),
    projectHarnessBindings: createClient(ProjectHarnessBindingService, activeTransport),
    harnessCatalog: createClient(HarnessCatalogService, activeTransport),
    agentTasks: createClient(AgentTaskService, activeTransport),
    appRegistry: createClient(AppRegistryService, activeTransport),
    appInstallations: createClient(AppInstallationService, activeTransport),
  };
}
