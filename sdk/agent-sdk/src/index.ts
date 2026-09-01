import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  AgentApprovalService,
  AgentAppPolicyService,
  AgentAppUsageService,
  AgentTaskService,
  AppBridgeService,
  AppInstallationService,
  AppRegistryService,
  ArtifactService,
  HarnessCatalogService,
  IncidentService,
  IndexService,
  ProjectHarnessBindingService,
  ProjectService,
  SurfaceService,
} from "@workos/protocol";

export interface WorkOSClients {
  projects: Client<typeof ProjectService>;
  projectHarnessBindings: Client<typeof ProjectHarnessBindingService>;
  harnessCatalog: Client<typeof HarnessCatalogService>;
  agentTasks: Client<typeof AgentTaskService>;
  appPolicies: Client<typeof AgentAppPolicyService>;
  approvals: Client<typeof AgentApprovalService>;
  appUsage: Client<typeof AgentAppUsageService>;
  appRegistry: Client<typeof AppRegistryService>;
  appInstallations: Client<typeof AppInstallationService>;
  artifacts: Client<typeof ArtifactService>;
  surfaces: Client<typeof SurfaceService>;
  appBridge: Client<typeof AppBridgeService>;
  incidents: Client<typeof IncidentService>;
  index: Client<typeof IndexService>;
}

export function createWorkOSClients(baseUrl: string, transport?: Transport): WorkOSClients {
  const activeTransport = transport ?? createConnectTransport({ baseUrl });
  return {
    projects: createClient(ProjectService, activeTransport),
    projectHarnessBindings: createClient(ProjectHarnessBindingService, activeTransport),
    harnessCatalog: createClient(HarnessCatalogService, activeTransport),
    agentTasks: createClient(AgentTaskService, activeTransport),
    appPolicies: createClient(AgentAppPolicyService, activeTransport),
    approvals: createClient(AgentApprovalService, activeTransport),
    appUsage: createClient(AgentAppUsageService, activeTransport),
    appRegistry: createClient(AppRegistryService, activeTransport),
    appInstallations: createClient(AppInstallationService, activeTransport),
    artifacts: createClient(ArtifactService, activeTransport),
    surfaces: createClient(SurfaceService, activeTransport),
    appBridge: createClient(AppBridgeService, activeTransport),
    incidents: createClient(IncidentService, activeTransport),
    index: createClient(IndexService, activeTransport),
  };
}
