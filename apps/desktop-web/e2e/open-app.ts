import { expect, type Page } from "@playwright/test";

const names: Record<string, string> = {
  "agent-center": "Agent Center",
  "app-library": "App Library",
  settings: "Project settings",
  "system-monitor": "System Monitor",
  "device-center": "Device Center",
  "artifact-center": "Artifact Center",
  "knowledge-center": "Knowledge Center",
  "mission-control": "Mission Control",
  docs: "Docs",
  code: "Code",
  browser: "Browser",
};
export async function openDesktopApp(page: Page, id: string) {
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toBeVisible();
  await page.keyboard.press("ControlOrMeta+k");
  const input = page.getByLabel("Search commands");
  await expect(input).toBeVisible();
  await input.fill(`Open ${names[id] ?? id}`);
  await expect(
    page.getByRole("option").filter({ hasText: new RegExp(`^Open ${names[id] ?? id}`, "i") }),
  ).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page.getByTestId("command-palette")).toHaveCount(0);
}

export async function createDesktopProject(page: Page, name: string) {
  await page.getByRole("button", { name: "Switch project", exact: true }).click();
  const mission = page.getByTestId("mission-control");
  await mission.getByLabel("New project name").fill(name);
  const [response] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/CreateProject") && response.request().method() === "POST",
    ),
    mission.getByRole("button", { name: "Create project", exact: true }).click(),
  ]);
  expect(response.ok()).toBe(true);
  const body = (await response.json()) as { project: { id: string } };
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    name,
  );
  await page.getByRole("button", { name: "Close Mission Control", exact: true }).click();
  return body.project.id;
}

export async function expectProjectRevision(page: Page, projectId: string, revision: string) {
  const response = await page.request.post("/workos.project.v1.ProjectService/GetProject", {
    data: { projectId },
  });
  expect(response.ok()).toBe(true);
  expect(((await response.json()) as { project: { revision: string } }).project.revision).toBe(
    revision,
  );
}
