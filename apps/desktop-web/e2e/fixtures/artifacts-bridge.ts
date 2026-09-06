import { connectWorkOSAppBridge } from "../../../../sdk/app-sdk/src/index.js";
async function main() {
  const output = document.querySelector<HTMLOutputElement>("output");
  if (!output) throw new Error("fixture output missing");
  const bridge = await connectWorkOSAppBridge();
  output.textContent = "ready";
  let artifactId = "";
  const actions: Record<string, () => Promise<string>> = {
    async create() {
      const artifact = await bridge.artifacts.create({
        idempotencyKey: "release-notes",
        type: "document.markdown.v1",
        title: "Release notes",
        content:
          "# Release notes\n\nCreated by the project app.\n\n- Changes saved\n- Ready for review\n",
      });
      artifactId = artifact.id;
      output.dataset.artifactId = artifactId;
      return "saved";
    },
    async conflict() {
      await bridge.artifacts.create({
        idempotencyKey: "release-notes",
        type: "document.markdown.v1",
        title: "Release notes",
        content: "different",
      });
      return "unexpected";
    },
    async open() {
      await bridge.artifacts.open(artifactId);
      return "opened";
    },
    async foreign() {
      await bridge.artifacts.open(document.body.dataset.foreignId ?? "");
      return "unexpected";
    },
  };
  for (const [name, action] of Object.entries(actions)) {
    document.getElementById(name)?.addEventListener("click", () => {
      void action().then(
        (result) => {
          output.textContent = `${name}:${result}`;
        },
        (error: unknown) => {
          output.textContent = `${name}:error:${(error as { code?: string }).code ?? "internal"}`;
        },
      );
    });
  }
}
void main();
