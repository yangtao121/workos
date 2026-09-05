import { defineConfig } from "vite";

// The mobile wrapper builds a minimal entry bundle (the shared adaptive
// shell mount) into dist/ for the Capacitor webDir. Native sync/build needs
// Xcode or the Android SDK and is gated separately (test-mobile-wrappers).
export default defineConfig({
  build: {
    lib: {
      entry: "src/main.ts",
      formats: ["es"],
      fileName: "main",
    },
  },
});
