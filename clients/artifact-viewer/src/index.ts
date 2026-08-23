export type SupportedArtifact = "markdown" | "diff" | "chart" | "image" | "json";

export interface ArtifactViewerState {
  artifactId: string;
  type: SupportedArtifact;
  loading: boolean;
  error?: string;
}
