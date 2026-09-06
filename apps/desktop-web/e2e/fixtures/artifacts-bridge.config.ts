export default {
  publicDir: false,
  build: {
    outDir: "../../tmp/workos-artifacts-fixture",
    emptyOutDir: true,
    lib: {
      entry: new URL("./artifacts-bridge.ts", import.meta.url).pathname,
      name: "WorkOSArtifactsFixture",
      formats: ["iife"],
      fileName: () => "app.js",
    },
  },
};
