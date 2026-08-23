export interface AppHostSession {
  surfaceSessionId: string;
  appInstanceId: string;
  projectId: string;
  bridgeOrigin: string;
}

export const APP_BRIDGE_VERSION = "workos.surface/v1" as const;
