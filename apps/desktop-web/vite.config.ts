import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// The published compositor imports relative files without a .js suffix.
function greenfieldExtensions(): Plugin {
  return {
    name: "greenfield-extensions",
    enforce: "pre",
    async resolveId(source, importer, options) {
      if (!importer?.includes("@gfld/") || !source.startsWith(".")) return null;
      if (/\.(js|mjs|ts|json|css|wasm)$/.test(source)) return null;
      return this.resolve(source + ".js", importer, { skipSelf: true, ...options });
    },
  };
}

export default defineConfig({
  plugins: [greenfieldExtensions(), react()],
  server: {
    proxy: {
      "/workos.": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/native/greenfield": { target: "http://127.0.0.1:8080", changeOrigin: false, ws: true },
    },
  },
});
