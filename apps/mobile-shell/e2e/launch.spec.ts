import { mkdir } from "node:fs/promises";
import { expect, test, type Route } from "@playwright/test";

// The launchable mobile entry (ADR-0019 W5): the built app mounts the shared
// shell with posture detection, the canonical pairing/session phases, the
// paired project and notification projections, and the honest gateway state
// machine. The gateway RPC surface is routed at the page level: the shell's
// own client logic (auth controller, connect transport, projections) runs
// for real against those deterministic responses.
test.setTimeout(60_000);

const CAPTURE = "../../docs/ui/mobile-shell/changes/20260914-merge-review/after";

await mkdir(CAPTURE, { recursive: true });

function json(body: Record<string, unknown>): {
  status: number;
  contentType: string;
  body: string;
} {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

test("mounted shell renders posture and honest gateway state", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/workos.auth.v1.DeviceService/GetCurrentDevice", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );
  await page.goto("/");
  const root = page.getByTestId("workos-mobile");
  await expect(root).toBeVisible();
  await expect(page.getByTestId("mobile-connection")).toContainText(
    /Connecting|Gateway unavailable/,
    { timeout: 30_000 },
  );
  await expect(page.getByTestId("mobile-unavailable")).toBeVisible();
  const posture = await page.getByTestId("mobile-posture").textContent();
  expect(["phone", "tablet", "foldable", "desktop"]).toContain(posture ?? "");
  await expect(page.getByTestId("mobile-key-status")).toContainText(/Device key:/);
  await page.screenshot({
    path: `${CAPTURE}/mobile-shell--unavailable--390x844.png`,
    animations: "disabled",
  });
});

test("unpaired deployment shows the pairing screen without projections", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/workos.auth.v1.DeviceService/GetCurrentDevice", (route) =>
    route.fulfill({ status: 401, contentType: "application/json", body: "{}" }),
  );
  await page.goto("/");
  await expect(page.getByTestId("mobile-unpaired")).toBeVisible({ timeout: 30_000 });
  // The pairing hint names the exact remote-push boundary.
  await expect(page.getByTestId("mobile-unpaired")).toContainText("APNs/FCM");
  await page.screenshot({
    path: `${CAPTURE}/mobile-shell--unpaired--390x844.png`,
    animations: "disabled",
  });
});

test("paired session lists projects and notifications and marks them read", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/workos.auth.v1.DeviceService/GetCurrentDevice", (route) =>
    route.fulfill(
      json({
        device: {
          deviceId: "01999999-9999-7999-8999-000000000d01",
          name: "Pixel",
          deviceClass: 4,
          revision: "1",
          isCurrent: true,
        },
      }),
    ),
  );
  await page.route("**/workos.project.v1.ProjectService/ListProjects", (route) =>
    route.fulfill(
      json({
        projects: [
          { id: "01999999-9999-7999-8999-00000000aa01", name: "Atlas", revision: "4" },
          { id: "01999999-9999-7999-8999-00000000aa02", name: "Nadir", revision: "2" },
        ],
      }),
    ),
  );
  await page.route("**/workos.notification.v1.NotificationService/ListNotifications", (route) =>
    route.fulfill(
      json({
        notifications: [
          {
            id: "01999999-9999-7999-8999-00000000bb01",
            title: "Task completed",
            createdAt: "2026-09-14T00:00:00Z",
          },
        ],
        unreadCount: "1",
      }),
    ),
  );
  await page.route("**/workos.notification.v1.NotificationService/MarkNotificationRead", (route) =>
    route.fulfill(json({ notification: {} })),
  );
  await page.route("**/workos.auth.v1.DeviceService/Logout", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ code: "unavailable", message: "fixture outage" }),
    }),
  );
  await page.goto("/");
  await expect(page.getByTestId("mobile-projects")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("mobile-projects")).toContainText("Atlas");
  await expect(page.getByTestId("mobile-projects")).toContainText("Nadir");
  // The alerts tab carries the unread count; marking read clears it.
  await page.getByTestId("mobile-tabs").getByText("Alerts").click();
  const notification = page.getByTestId("mobile-notification");
  await expect(notification).toBeVisible();
  await expect(notification).toContainText("Task completed");
  await notification.getByText("Mark read").click();
  await expect(notification.getByText("Mark read")).toHaveCount(0);
  await expect(page.getByTestId("mobile-tabs").getByText("Alerts", { exact: true })).toBeVisible();
  await page.screenshot({
    path: `${CAPTURE}/mobile-shell--paired-alerts--390x844.png`,
    animations: "disabled",
  });
  await page.getByRole("button", { name: "Forget device" }).click();
  await expect(page.getByRole("alert")).toHaveText("Forget device failed. Try again.");
  await expect(page.getByTestId("mobile-notifications")).toBeVisible();
  await page.screenshot({
    path: `${CAPTURE}/mobile-shell--forget-failed--390x844.png`,
    animations: "disabled",
  });
});

test("Forget prevents a late projection failure from restarting authentication", async ({
  page,
}) => {
  let restores = 0;
  let projectRoute: Route | undefined;
  let logoutRoute: Route | undefined;
  await page.route("**/workos.auth.v1.DeviceService/GetCurrentDevice", (route) => {
    restores++;
    // A second restore remains pending: the regression must detect its admission.
    if (restores > 1) return;
    return route.fulfill(
      json({
        device: {
          deviceId: "01999999-9999-7999-8999-000000000d01",
          name: "Pixel",
          deviceClass: 4,
          revision: "1",
          isCurrent: true,
        },
      }),
    );
  });
  await page.route("**/workos.project.v1.ProjectService/ListProjects", (route) => {
    projectRoute = route;
  });
  await page.route("**/workos.auth.v1.DeviceService/Logout", (route) => {
    logoutRoute = route;
  });
  await page.goto("/");
  await expect.poll(() => projectRoute !== undefined).toBe(true);
  await page.getByRole("button", { name: "Forget device" }).click();
  await expect.poll(() => logoutRoute !== undefined).toBe(true);
  if (!projectRoute || !logoutRoute) throw new Error("fixture requests did not arrive");
  const failure = page.waitForResponse("**/workos.project.v1.ProjectService/ListProjects");
  await projectRoute.fulfill({ status: 401, contentType: "application/json", body: "{}" });
  await (await failure).finished();
  // Let the response promise and React commit finish while Logout is still pending.
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() =>
          requestAnimationFrame(() => {
            resolve();
          }),
        );
      }),
  );
  expect(restores).toBe(1);
  await logoutRoute.fulfill(json({}));
  await expect(page.getByTestId("mobile-unpaired")).toBeVisible();
  await expect(page.getByTestId("mobile-projects")).toHaveCount(0);
});
