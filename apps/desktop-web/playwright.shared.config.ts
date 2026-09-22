import { defineConfig } from "@playwright/test";

// Bounded cross-browser gate; ordinary suites retain their existing runner.
export default defineConfig({
  testDir: "./e2e",
  testMatch: /(shared-desktop(-live)?|session-continuity-visual|session-forget)\.spec\.ts/,
  outputDir: process.env.WORKOS_E2E_OUTPUT_DIR ?? "test-results",
  workers: 1,
  timeout: 120_000,
  use: { baseURL: process.env.WORKOS_E2E_URL ?? "http://127.0.0.1:8080" },
  projects: [
    { name: "chromium", use: { browserName: "chromium" } },
    { name: "webkit", use: { browserName: "webkit" } },
  ],
  reporter: "list",
});
