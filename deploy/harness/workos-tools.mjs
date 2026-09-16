// Task-lease scoped system tools. Native tool dispatch owns validation and
// execution; the parent supplies the authenticated Core transport.
export const name = "workos-tools";
export const inject = ["tools"];
export function apply(ctx) {
  const register = (name, operation, description, properties = {}, required = []) =>
    ctx.tools.register({
      name,
      description,
      parameters: { type: "object", properties, required, additionalProperties: false },
      timeoutMs: 30000,
      output: { schema: { type: "string" }, render: (_args, text) => [{ type: "text", text }] },
      async execute(args) {
        const result = await globalThis.__workosCall(operation, args);
        const text = JSON.stringify(result);
        if (Buffer.byteLength(text) > 262144) throw new Error("WorkOS tool output exceeds limit");
        return text;
      },
    });
  register(
    "workos_start_preview",
    "preview.start",
    "Start a persistent development server in this project. The server must listen on PORT; use WORKOS_PREVIEW_BASE for asset URLs. Reuse outputKey on retry. Runs for at most 30 minutes.",
    {
      command: { type: "string" },
      port: { type: "integer", minimum: 1024, maximum: 65535 },
      outputKey: { type: "string" },
    },
    ["command", "port", "outputKey"],
  );
  register(
    "workos_list_previews",
    "preview.list",
    "Discover running development servers in the current project.",
  );
  register(
    "workos_stop_preview",
    "preview.stop",
    "Stop one development server in the current project.",
    { previewId: { type: "string" }, outputKey: { type: "string" } },
    ["previewId", "outputKey"],
  );
  register(
    "workos_list_apps",
    "app.list",
    "List applications installed and authorized in the current project.",
  );
  register("workos_project_info", "project.info", "Read the current authorized WorkOS project.");
  register(
    "workos_workspace_info",
    "workspace.info",
    "Read the pinned workspace identity, revision and access mode.",
  );
  register(
    "workos_list_artifacts",
    "artifact.list",
    "List reviewable artifacts of the current project.",
  );
  register(
    "workos_read_artifact",
    "artifact.read",
    "Read one review artifact in the current project.",
    { artifactId: { type: "string" } },
    ["artifactId"],
  );
  register(
    "workos_create_artifact",
    "artifact.create",
    "Publish a review artifact with task provenance. Reuse outputKey for an identical retry.",
    {
      outputKey: { type: "string" },
      title: { type: "string" },
      type: { type: "string", enum: ["document.markdown.v1", "code.unified-diff.v1"] },
      content: { type: "string" },
    },
    ["outputKey", "title", "type", "content"],
  );
}
