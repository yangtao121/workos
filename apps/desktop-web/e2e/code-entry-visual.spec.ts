import { expect, test } from "@playwright/test";
import { sharedDesktopFixture } from "./shared-desktop-fixture.js";

// Run explicitly with WORKOS_CODE_ENTRY_CAPTURE_DIR pointing to before/ or after/.
// Every RPC response and the clock are fixed. No live account or Code pixels
// appear in this documentation capture.
const directory = process.env.WORKOS_CODE_ENTRY_CAPTURE_DIR;
const captureDir = directory ?? "/captures";
const expectNative = process.env.WORKOS_CODE_ENTRY_EXPECT_NATIVE === "1";
const sessionId = "01999999-9999-7999-8999-000000000021";
const projectId = "01999999-9999-7999-8999-000000000001";

test.use({ deviceScaleFactor: 1, locale: "en-US", timezoneId: "UTC" });

test("captures Code's Home entry and opened window at 1440x900", async ({ page }) => {
  test.skip(!directory, "explicit deterministic Code entry capture");
  await page.setViewportSize({ width: 1440, height: 900 });
  const fixture = await sharedDesktopFixture();
  await fixture.install(page);
  let nativeStarts = 0;
  await page.route("**/workos.*/**", async (route) => {
    const method = new URL(route.request().url()).pathname.split("/").at(-1);
    if (method === "CreateNativeSession") nativeStarts++;
    const responses: Record<string, unknown> = {
      CreateNativeSession: { session: { id: sessionId, projectId } },
      GetSurfaceWorkload: {
        workload: {
          workloadId: sessionId,
          projectId,
          state: "running",
          generation: "1",
          renderer: "SURFACE_RENDERER_REMOTE_NATIVE",
        },
      },
      AttachSurface: {
        session: {
          id: sessionId,
          projectId,
          workloadId: sessionId,
          workloadGeneration: "1",
          renderer: "SURFACE_RENDERER_REMOTE_NATIVE",
        },
        attachment: {
          id: "01999999-9999-7999-8999-000000000022",
          workloadId: sessionId,
          surfaceSessionId: sessionId,
          controls: false,
          controlGeneration: "1",
        },
      },
    };
    if (method === "GetNativeSession") {
      await route.fulfill({
        status: 404,
        json: { code: "not_found", message: "fixture display unavailable" },
      });
      return;
    }
    if (method && method in responses) {
      await route.fulfill({ json: responses[method] });
      return;
    }
    await route.fallback();
  });

  try {
    await page.goto("/");
    await expect(page.locator(".project-switcher")).toContainText("Studio");
    const home = page.getByTestId("home-app");
    await expect(home.getByTestId("home-entry-code")).toBeVisible();
    await page.screenshot({
      path: `${captureDir}/home--launchpad--1440x900.png`,
      animations: "disabled",
    });
    await home.getByTestId("home-entry-code").click();
    if (expectNative) {
      const viewer = page.getByTestId("native-app");
      await expect(viewer).toBeVisible();
      await expect(viewer.getByTestId("native-status")).toHaveText("unavailable");
      await expect(page.getByTestId("code-app")).toHaveCount(0);
      await expect(page.getByTestId("open-code")).toHaveClass(/running/);
      expect(nativeStarts).toBe(1);
      await page.getByTestId("open-code").click();
      expect(nativeStarts).toBe(1);
    } else {
      await expect(page.getByTestId("code-app")).toBeVisible();
      expect(nativeStarts).toBe(0);
    }
    await page.screenshot({
      path: `${captureDir}/code-entry--opened--1440x900.png`,
      animations: "disabled",
    });
  } finally {
    await fixture.close();
  }
});
