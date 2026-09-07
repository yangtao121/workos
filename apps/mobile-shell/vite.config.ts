import { defineConfig } from "vite";

// Library scaffold. The wrapper gate requires a real HTML entry and native projects.
export default defineConfig({
  build: {
    lib: {
      entry: "src/main.ts",
      formats: ["es"],
      fileName: "main",
    },
  },
});
