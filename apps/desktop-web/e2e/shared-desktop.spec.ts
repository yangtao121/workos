import { expect, test } from "@playwright/test";
import { sharedDesktopFixture } from "./shared-desktop-fixture.js";
import { openDesktopApp } from "./open-app.js";
const projectId = "01999999-9999-7999-8999-000000000001";
const secondProject = "01999999-9999-7999-8999-000000000002";

test("three independent screen sizes share project, windows, focus and reload authority", async ({
  browser,
}) => {
  const fixture = await sharedDesktopFixture();
  const contexts = await Promise.all(
    [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
      { width: 820, height: 1180 },
    ].map((viewport) => browser.newContext({ viewport })),
  );
  try {
    const pages = await Promise.all(contexts.map((context) => context.newPage()));
    for (const page of pages) {
      await fixture.install(page);
      await page.goto("/");
      await expect(page.getByTestId("home-app")).toBeVisible();
    }
    const [desktop, phone, tablet] = pages;
    if (!desktop || !phone || !tablet) throw new Error("fixture pages missing");
    await openDesktopApp(desktop, "files");
    for (const page of pages) await expect(page.getByTestId("workspace-files")).toBeVisible();
    await phone.getByTestId("nav-agent").click();
    for (const page of pages)
      await expect(
        page.getByRole("heading", { name: "Agent Sessions", exact: true }),
      ).toBeVisible();
    await tablet.getByRole("button", { name: "Close Agent Sessions", exact: true }).click();
    for (const page of pages)
      await expect(page.getByRole("heading", { name: "Agent Sessions", exact: true })).toHaveCount(
        0,
      );
    await phone.reload();
    await expect(phone.getByTestId("workspace-files")).toBeVisible();
    await desktop.keyboard.press("ControlOrMeta+k");
    await desktop.getByLabel("Search commands").fill("Switch to project: Field notes");
    await desktop.keyboard.press("Enter");
    for (const page of pages)
      await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
        "Field notes",
      );
    expect(fixture.state().activeProjectId).toBe(secondProject);
    expect(
      fixture
        .state()
        .windows.some(
          (item) => item.target.kind === "files" && item.target.projectId === projectId,
        ),
    ).toBe(true);
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
    await fixture.close();
  }
});

for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`shared desktop visual ${String(width)}x${String(height)}`, async ({
    page,
    browserName,
  }) => {
    const directory = process.env.WORKOS_CAPTURE_DIR;
    test.skip(!directory, "explicit deterministic visual capture");
    const fixture = await sharedDesktopFixture();
    try {
      await page.setViewportSize({ width, height });
      await fixture.install(page);
      await page.goto("/");
      await expect(page.getByTestId("home-app")).toBeVisible();
      await expect(page.getByText("No live app sessions in this project.")).toBeVisible();
      await page.screenshot({
        path: `${directory ?? ""}/${browserName}/home--launchpad--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
    } finally {
      await page.goto("about:blank");
      await fixture.close();
    }
  });
}
