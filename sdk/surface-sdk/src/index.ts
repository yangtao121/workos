export type SurfaceLifecycle =
  | "create"
  | "attach"
  | "ready"
  | "resize"
  | "background"
  | "suspend"
  | "resume"
  | "detach"
  | "close";

export interface SurfaceBridgeEnvelope<T = unknown> {
  version: "workos.surface/v1";
  requestId: string;
  capability: string;
  payload: T;
}
