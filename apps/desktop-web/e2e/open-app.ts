import { expect, type Page } from "@playwright/test";

const names: Record<string, string> = {
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
  await page.keyboard.press("ControlOrMeta+k");
  const input = page.getByLabel("Search commands");
  await expect(input).toBeVisible();
  await input.fill(`Open ${names[id] ?? id}`);
  await page.keyboard.press("Enter");
  await expect(page.getByTestId("command-palette")).toHaveCount(0);
}
