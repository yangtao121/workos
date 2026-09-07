import { readFile, writeFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { createDesktopProject } from "./open-app.js";

const directory = process.env.WORKOS_PUSH_WAKE_DIR;
const relayURL = process.env.WORKOS_PUSH_WAKE_RELAY;
const service = "/workos.notification.v1.NotificationService/";
test.use({ channel: "chromium", viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });

test("durable Core retry delivers encrypted wakes and resumes authoritative notifications", async ({
  page,
  context,
  baseURL,
}) => {
  test.skip(!directory || !relayURL, "requires the isolated push-wake stack");
  if (!directory || !relayURL || !baseURL)
    throw new Error("push-wake fixture configuration missing");
  test.setTimeout(150_000);
  const subscription = JSON.parse(
    await readFile(`${directory}/public/subscription.json`, "utf8"),
  ) as {
    endpoint: string;
    p256dh: string;
    authSecret: string;
    publicKey: string;
  };
  // Hold the notification stream as a stalled connection. No successful API response is mocked,
  // and no reconnect timer can fetch the new facts before the wake triggers refresh.
  let releaseStream: (() => void) | undefined;
  const stalled = new Promise<void>((resolve) => {
    releaseStream = resolve;
  });
  await page.route("**/WatchNotificationEvents", async (route) => {
    await stalled;
    await route.abort().catch(() => undefined);
  });
  try {
    await context.grantPermissions(["notifications"], { origin: baseURL });
    const cdp = await context.newCDPSession(page);
    let registrationId = "";
    cdp.on(
      "ServiceWorker.workerRegistrationUpdated",
      (event: { registrations: { registrationId: string; scopeURL: string }[] }) => {
        registrationId =
          event.registrations.find((entry) => entry.scopeURL === `${baseURL}/`)?.registrationId ??
          registrationId;
      },
    );
    await cdp.send("ServiceWorker.enable");
    const initial = page.waitForResponse(
      (response) => response.url().endsWith("/ListNotifications") && response.ok(),
    );
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Switch project" })).toBeVisible();
    await page.evaluate(async () => {
      await navigator.serviceWorker.register("/push-worker.js", { scope: "/" });
      await navigator.serviceWorker.ready;
    });
    await expect.poll(() => registrationId).not.toBe("");
    await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
    expect(
      ((await (await initial).json()) as { notifications?: unknown[] }).notifications ?? [],
    ).toHaveLength(0);
    await expect(page.getByTestId("open-notifications")).toHaveAttribute(
      "aria-label",
      "Notifications",
    );
    const preferences = await page.request.post(`${service}GetPushPreferences`, { data: {} });
    expect(((await preferences.json()) as { webPushPublicKey: string }).webPushPublicKey).toBe(
      subscription.publicKey,
    );
    const subscribed = await page.request.post(`${service}SubscribePush`, {
      data: {
        deviceId: "01999999-9999-7999-8999-000000000a02",
        platform: "web-push",
        endpoint: subscription.endpoint,
        p256dh: subscription.p256dh,
        authSecret: subscription.authSecret,
      },
    });
    expect(subscribed.ok()).toBe(true);
    const projectId = await createDesktopProject(page, "Encrypted wake fixture");
    await page
      .getByLabel("Agent goal")
      .fill("Create a synthetic review for the push wake acceptance gate.");
    await page.getByRole("checkbox", { name: "Markdown document" }).check();
    await page.getByRole("button", { name: "Run task" }).click();
    await expect(page.getByText(/completed by fake harness/)).toBeVisible({ timeout: 30_000 });
    const listed = await page.request.post(`${service}ListNotifications`, {
      data: { projectId, pageSize: 100 },
    });
    expect(listed.ok()).toBe(true);
    const facts = ((await listed.json()) as { notifications: { id: string; title: string }[] })
      .notifications;
    expect(facts.map((fact) => fact.title).sort()).toEqual([
      "Review artifact ready",
      "Task completed",
    ]);
    await expect(page.getByTestId("open-notifications")).toHaveAttribute(
      "aria-label",
      "Notifications",
    );
    await cdp.send("Page.setWebLifecycleState", { state: "frozen" });
    await writeFile(`${directory}/browser-ready`, "ready\n");
    await expect
      .poll(async () => readFile(`${directory}/core-restarted`, "utf8").catch(() => ""), {
        timeout: 60_000,
      })
      .toBe("restarted\n");
    interface RelayState {
      invalid: number;
      deliveries: Record<string, { payload: string; attempts: number; ciphertexts: number }>;
      accepted: Record<string, string>;
    }
    const state = async (): Promise<RelayState> => {
      const response = await page.request.get(`${relayURL}/state`);
      expect(response.ok()).toBe(true);
      return (await response.json()) as RelayState;
    };
    const expectedIDs = facts.map((fact) => fact.id).sort();
    await expect
      .poll(async () => Object.keys((await state()).accepted).sort(), { timeout: 60_000 })
      .toEqual(expectedIDs);
    const received = await state();
    expect(received.invalid).toBe(0);
    expect(
      Object.values(received.deliveries).some(
        (entry) => entry.attempts >= 2 && entry.ciphertexts === entry.attempts,
      ),
    ).toBe(true);
    const refresh = page.waitForResponse(
      (response) => response.url().endsWith("/ListNotifications") && response.ok(),
    );
    for (const id of expectedIDs) {
      const payload = received.accepted[id];
      if (!payload) throw new Error("relay omitted a notification payload");
      expect(payload).toBe(JSON.stringify({ notificationId: id }));
      await cdp.send("ServiceWorker.deliverPushMessage", {
        origin: baseURL,
        registrationId,
        data: payload,
      });
    }
    await cdp.send("Page.setWebLifecycleState", { state: "active" });
    const refreshed = (await (await refresh).json()) as { notifications: { id: string }[] };
    expect(refreshed.notifications.map((fact) => fact.id).sort()).toEqual(expectedIDs);
    const displayed = () =>
      page.evaluate(async () => {
        const worker = await navigator.serviceWorker.ready;
        return (await worker.getNotifications())
          .map((item) => ({ title: item.title, body: item.body, tag: item.tag }))
          .sort((a, b) => a.tag.localeCompare(b.tag));
      });
    await expect.poll(displayed).toEqual(
      expectedIDs.map((id) => ({
        title: "WorkOS",
        body: "You have a new workspace notification.",
        tag: `workos-${id}`,
      })),
    );
    await page.getByTestId("open-notifications").click();
    await expect(page.getByTestId("notification-item")).toHaveCount(2);
    for (const fact of facts)
      await expect(
        page.getByTestId("notification-center").getByText(fact.title, { exact: true }),
      ).toBeVisible();
    const duplicate = Object.values(received.accepted)[0];
    if (!duplicate) throw new Error("relay returned no payloads");
    await cdp.send("ServiceWorker.deliverPushMessage", {
      origin: baseURL,
      registrationId,
      data: duplicate,
    });
    await expect.poll(displayed).toHaveLength(2);
    const unsubscribed = await page.request.post(`${service}UnsubscribePush`, {
      data: { deviceId: "01999999-9999-7999-8999-000000000a02", platform: "web-push" },
    });
    expect(unsubscribed.ok()).toBe(true);
  } finally {
    releaseStream?.();
  }
});
