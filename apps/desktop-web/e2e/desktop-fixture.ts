import type { Page } from "@playwright/test";
const projectId = "01999999-9999-7999-8999-000000000001";
const projects = [
  { id: projectId, name: "Studio", icon: "S", revision: "3", installedAppIds: [] },
  {
    id: "01999999-9999-7999-8999-000000000002",
    name: "Field notes",
    icon: "F",
    revision: "1",
    installedAppIds: [],
  },
];

export async function desktopFixture(page: Page) {
  await page.clock.setFixedTime(new Date("2026-09-06T09:00:00Z"));
  await page.route("**/workos.*/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.includes("workos.auth.")) {
      await route.fulfill({
        status: 404,
        contentType: "application/json",
        body: JSON.stringify({ code: "unimplemented", message: "fixture auth bypass" }),
      });
      return;
    }
    const method = path.split("/").at(-1);
    const responses: Record<string, unknown> = {
      ListProjects: { projects },
      GetProject: { project: projects[0] },
      GetHarnessCatalog: { providers: [], defaultProviderId: "" },
      ListInstalledApps: { installations: [] },
      ListApps: { apps: [] },
      ListArtifacts: { artifacts: [] },
      SearchHybrid: { hits: [], freshness: { caughtUp: true } },
      ListNotifications: {
        notifications: [],
        unreadCount: "0",
        snapshotRevision: "0",
        incidentSourceReady: true,
      },
      GetCapabilities: { capabilities: [] },
      ListIncidents: { incidents: [] },
      GetTelemetrySummary: { services: [] },
      GetPushPreferences: {
        preferences: { quietEnabled: false, quietStartUtc: "22:00", quietEndUtc: "07:00" },
        webPushUnavailableReason: "Web Push is not configured on this WorkOS host.",
      },
    };
    if (method === "WatchNotifications") {
      await route.abort();
      return;
    }
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify(responses[method ?? ""] ?? {}),
    });
  });
}
