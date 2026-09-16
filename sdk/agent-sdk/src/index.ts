import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  AgentApprovalService,
  AgentAppPolicyService,
  AgentAppUsageService,
  AgentSessionService,
  AgentInteractionService,
  AgentTaskService,
  AppBridgeService,
  AppInstallationService,
  AppRegistryService,
  ArtifactService,
  BrowserSessionService,
  PtySessionService,
  NativeSessionService,
  HarnessCatalogService,
  IncidentService,
  IndexService,
  NotificationService,
  ProjectHarnessBindingService,
  ProjectService,
  ProjectWorkspaceService,
  SurfaceService,
  SurfaceContinuityService,
  WorkspacePreviewService,
} from "@workos/protocol";

export interface WorkOSClients {
  projects: Client<typeof ProjectService>;
  projectWorkspaces: Client<typeof ProjectWorkspaceService>;
  projectHarnessBindings: Client<typeof ProjectHarnessBindingService>;
  harnessCatalog: Client<typeof HarnessCatalogService>;
  agentTasks: Client<typeof AgentTaskService>;
  agentSessions: Client<typeof AgentSessionService>;
  agentInteractions: Client<typeof AgentInteractionService>;
  appPolicies: Client<typeof AgentAppPolicyService>;
  approvals: Client<typeof AgentApprovalService>;
  appUsage: Client<typeof AgentAppUsageService>;
  appRegistry: Client<typeof AppRegistryService>;
  appInstallations: Client<typeof AppInstallationService>;
  artifacts: Client<typeof ArtifactService>;
  surfaces: Client<typeof SurfaceService>;
  surfaceContinuity: Client<typeof SurfaceContinuityService>;
  workspacePreviews: Client<typeof WorkspacePreviewService>;
  browserSessions: Client<typeof BrowserSessionService>;
  ptySessions: Client<typeof PtySessionService>;
  nativeSessions: Client<typeof NativeSessionService>;
  appBridge: Client<typeof AppBridgeService>;
  incidents: Client<typeof IncidentService>;
  index: Client<typeof IndexService>;
  notifications: Client<typeof NotificationService>;
}

export function createWorkOSClients(baseUrl: string, transport?: Transport): WorkOSClients {
  const activeTransport = transport ?? createConnectTransport({ baseUrl });
  return {
    projects: createClient(ProjectService, activeTransport),
    projectWorkspaces: createClient(ProjectWorkspaceService, activeTransport),
    projectHarnessBindings: createClient(ProjectHarnessBindingService, activeTransport),
    harnessCatalog: createClient(HarnessCatalogService, activeTransport),
    agentTasks: createClient(AgentTaskService, activeTransport),
    agentSessions: createClient(AgentSessionService, activeTransport),
    agentInteractions: createClient(AgentInteractionService, activeTransport),
    appPolicies: createClient(AgentAppPolicyService, activeTransport),
    approvals: createClient(AgentApprovalService, activeTransport),
    appUsage: createClient(AgentAppUsageService, activeTransport),
    appRegistry: createClient(AppRegistryService, activeTransport),
    appInstallations: createClient(AppInstallationService, activeTransport),
    artifacts: createClient(ArtifactService, activeTransport),
    surfaces: createClient(SurfaceService, activeTransport),
    surfaceContinuity: createClient(SurfaceContinuityService, activeTransport),
    workspacePreviews: createClient(WorkspacePreviewService, activeTransport),
    browserSessions: createClient(BrowserSessionService, activeTransport),
    ptySessions: createClient(PtySessionService, activeTransport),
    nativeSessions: createClient(NativeSessionService, activeTransport),
    appBridge: createClient(AppBridgeService, activeTransport),
    incidents: createClient(IncidentService, activeTransport),
    index: createClient(IndexService, activeTransport),
    notifications: createClient(NotificationService, activeTransport),
  };
}
export * from "./notifications.js";
