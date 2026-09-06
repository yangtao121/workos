import { expect, test } from "@playwright/test";
test.use({ channel: "chromium" });

// The browser receives decrypted Push API data via Chromium's push driver.
// Encryption/VAPID is separately verified against the RFC vector and TLS relay.
test("background push displays only fixed copy and resumes the notification projection", async ({
  page,
  context,
  baseURL,
}) => {
  if (!baseURL) throw new Error("E2E origin required");
  await context.grantPermissions(["notifications"], { origin: baseURL });
  await page.goto("/");
  const cdp = await context.newCDPSession(page);
  let registrationId = "";
  cdp.on(
    "ServiceWorker.workerRegistrationUpdated",
    (event: { registrations: { registrationId: string; scopeURL: string }[] }) => {
      registrationId =
        event.registrations.find((item) => item.scopeURL === `${baseURL}/`)?.registrationId ??
        registrationId;
    },
  );
  await cdp.send("ServiceWorker.enable");
  await page.evaluate(async () => {
    await navigator.serviceWorker.register("/push-worker.js", { scope: "/" });
    await navigator.serviceWorker.ready;
  });
  await expect.poll(() => registrationId).not.toBe("");
  const initial = page.waitForResponse((response) => response.url().includes("/ListNotifications"));
  await page.reload();
  await initial;
  const notificationId = "01999999-9999-7999-8999-000000000991";
  const refresh = page.waitForRequest((request) => request.url().includes("/ListNotifications"));
  await cdp.send("ServiceWorker.deliverPushMessage", {
    origin: baseURL,
    registrationId,
    data: JSON.stringify({ notificationId }),
  });
  await refresh;
  const displayed = () =>
    page.evaluate(async () => {
      const worker = await navigator.serviceWorker.ready;
      return (await worker.getNotifications()).map((item) => ({
        title: item.title,
        body: item.body,
        tag: item.tag,
      }));
    });
  await expect.poll(displayed).toEqual([
    {
      title: "WorkOS",
      body: "You have a new workspace notification.",
      tag: `workos-${notificationId}`,
    },
  ]);
  await cdp.send("ServiceWorker.deliverPushMessage", {
    origin: baseURL,
    registrationId,
    data: JSON.stringify({ notificationId }),
  });
  await expect.poll(displayed).toHaveLength(1);
  await cdp.send("ServiceWorker.deliverPushMessage", {
    origin: baseURL,
    registrationId,
    data: JSON.stringify({
      notificationId: "01999999-9999-7999-8999-000000000992",
      body: "must never appear",
    }),
  });
  await cdp.send("ServiceWorker.deliverPushMessage", {
    origin: baseURL,
    registrationId,
    data: JSON.stringify({ notificationId: "01999999-9999-7999-8999-000000000993" }),
  });
  await expect
    .poll(async () => (await displayed()).map((item) => item.tag).sort())
    .toEqual([`workos-${notificationId}`, "workos-01999999-9999-7999-8999-000000000993"]);
  await page.goto("/?notifications=1");
  await expect(page.getByTestId("notification-center")).toBeVisible();
  expect(new URL(page.url()).search).toBe("");
});
