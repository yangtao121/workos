import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  outputDir: process.env.WORKOS_E2E_OUTPUT_DIR ?? "test-results",
  timeout: 30_000,
  use: { baseURL: process.env.WORKOS_E2E_URL ?? "http://127.0.0.1:8080" },
  reporter: "list",
});
