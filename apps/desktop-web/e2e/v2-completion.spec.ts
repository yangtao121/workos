import { expect, test } from "@playwright/test";
import { openDesktopApp } from "./open-app.js";

test.skip(process.env.WORKOS_V2_E2E !== "true", "requires isolated v2-completion fixture");
test.setTimeout(180_000);
const projectId = process.env.WORKOS_V2_PROJECT_ID ?? "";
test("shared files, resumed native display and real development preview", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    "Development fixture",
  );
  await openDesktopApp(page, "files");
  await page.getByRole("button", { name: "calculate.cjs", exact: true }).click();
  await expect(page.getByLabel("File content")).toContainText("n * 3");
  await openDesktopApp(page, "agent-sessions");
  await expect(page.getByTestId("agent-sessions-app")).toContainText("V2_DEVELOP_1");
  await page
    .getByTestId("agent-sessions-app")
    .getByRole("button")
    .filter({ hasText: "V2_DEVELOP_1" })
    .click();
  await expect(page.getByTestId("agent-session-view")).toContainText("V2_DEVELOP_2");
  await page.reload();
  await openDesktopApp(page, "agent-sessions");
  await expect(page.getByTestId("agent-sessions-app")).toContainText("V2_DEVELOP_1");
  await openDesktopApp(page, "Development previews");
  await page
    .getByTestId("workspace-previews")
    .getByRole("button", { name: "Restart", exact: true })
    .click();
  await expect(page.getByTestId("workspace-previews")).toContainText("running");
  await page
    .getByTestId("workspace-previews")
    .getByRole("button", { name: "Open", exact: true })
    .click();
  await expect(
    page.frameLocator('iframe[title="Project development preview"]').getByRole("heading"),
  ).toHaveText("Development preview");

  await openDesktopApp(page, "home");
  await page.getByTestId("home-entry-native").click();
  await expect(page.getByTestId("native-status")).toHaveText("streaming", { timeout: 60_000 });
  const readFile = async () => {
    const response = await page.request.post(
      "/workos.project.v1.ProjectWorkspaceService/ReadWorkspaceFile",
      { data: { projectId, path: "native-proof.txt" } },
    );
    if (!response.ok()) return "";
    const result = (await response.json()) as { content: string };
    return result.content;
  };
  const marker = `native-${String(Date.now())}`;
  await page.getByTestId("native-stage").click();
  await page.keyboard.type(
    `export V2_NATIVE_MEMORY=preserved; printf ${marker} > native-proof.txt`,
    { delay: 20 },
  );
  await page.keyboard.press("Enter");
  await expect.poll(readFile).toBe(marker);
  const workloads = async () => {
    const response = await page.request.post(
      "/workos.surface.v1.SurfaceContinuityService/ListProjectSurfaces",
      { data: { projectId } },
    );
    return (await response.json()) as {
      workloads: { workloadId: string; generation: string; renderer: string; state: string }[];
    };
  };
  const before = (await workloads()).workloads.find(
    (w) => w.renderer === "SURFACE_RENDERER_REMOTE_NATIVE" && w.state === "running",
  );
  expect(before).toBeTruthy();
  await page.reload();
  await openDesktopApp(page, "home");
  await page.getByTestId("home-entry-native").click();
  await expect(page.getByTestId("native-status")).toHaveText(/streaming|unavailable/, {
    timeout: 60_000,
  });
  if (await page.getByTestId("native-take-control").isVisible())
    await page.getByTestId("native-take-control").click();
  await expect(page.getByTestId("native-status")).toHaveText("streaming", { timeout: 60_000 });
  await page.getByTestId("native-stage").click();
  await page.keyboard.type('printf "%s" "$V2_NATIVE_MEMORY" > native-proof.txt', { delay: 20 });
  await page.keyboard.press("Enter");
  await expect.poll(readFile).toBe("preserved");
  const after = (await workloads()).workloads.find((w) => w.workloadId === before?.workloadId);
  expect(after?.generation).toBe(before?.generation);
  await page.getByTestId("native-stop").click();
});
