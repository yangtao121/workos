import { connectWorkOSAppBridge } from "../../../../sdk/app-sdk/src/index.js";
import type { BridgeFileRef } from "../../../../sdk/surface-sdk/src/index.js";
async function main() {
  const status = document.getElementById("status");
  if (!status) throw new Error("fixture status missing");
  let ref: BridgeFileRef | undefined;
  let previous: BridgeFileRef | undefined;
  const bridge = await connectWorkOSAppBridge();
  status.textContent = "ready";
  const actions: Record<string, () => Promise<string>> = {
    async pick() {
      const refs = await bridge.files.pick();
      ref = refs[0];
      return ref ? ref.path : "cancelled";
    },
    async read() {
      if (!ref) throw new Error("no file");
      return new TextDecoder().decode(await bridge.files.read(ref));
    },
    async write() {
      if (!ref) throw new Error("no file");
      previous = ref;
      ref = await bridge.files.write(ref, new TextEncoder().encode("Saved in WorkOS").buffer);
      return "saved";
    },
    async stale() {
      if (!previous) throw new Error("no file");
      await bridge.files.write(previous, new TextEncoder().encode("stale content").buffer);
      return "unexpected";
    },
    async escape() {
      if (!ref) throw new Error("no file");
      await bridge.files.read({ ...ref, path: "../outside.txt" });
      return "unexpected";
    },
    async symlink() {
      if (!ref) throw new Error("no file");
      await bridge.files.read({ ...ref, path: "outside-link" });
      return "unexpected";
    },
  };
  for (const [name, action] of Object.entries(actions)) {
    const button = document.getElementById(name);
    if (!button) throw new Error("fixture button missing");
    button.addEventListener("click", () => {
      void action().then(
        (result) => {
          status.textContent = `${name}:${result}`;
        },
        (error: unknown) => {
          status.textContent = `${name}:error:${(error as { code?: string }).code ?? "internal"}`;
        },
      );
    });
  }
}
void main();
