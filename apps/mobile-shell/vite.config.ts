import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// App build (ADR-0019 W5): the launchable mobile entry with the shared
// shell; the library exports stay for workspace consumers.
export default defineConfig({
  plugins: [react()],
  envPrefix: ["VITE_", "WORKOS_MOBILE_"],
  build: {
    rollupOptions: {
      input: "index.html",
    },
  },
});
