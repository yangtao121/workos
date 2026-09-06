export default {
  publicDir: false,
  build: {
    outDir: "../../tmp/workos-files-fixture",
    emptyOutDir: true,
    lib: {
      entry: new URL("./files-bridge.ts", import.meta.url).pathname,
      name: "WorkOSFilesFixture",
      formats: ["iife"],
      fileName: () => "app.js",
    },
  },
};
