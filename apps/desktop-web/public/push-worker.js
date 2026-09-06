self.addEventListener("activate", (event) => {
  event.waitUntil(self.clients.claim());
});
// Wake payloads carry one id. Display text is fixed; notification contents are
// fetched by the authenticated shell after it resumes, never by this worker.
self.addEventListener("push", (event) => {
  let payload;
  try {
    if (!event.data || event.data.text().length > 128) return;
    payload = event.data.json();
  } catch {
    return;
  }
  if (
    !payload ||
    typeof payload !== "object" ||
    Object.keys(payload).length !== 1 ||
    typeof payload.notificationId !== "string" ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(
      payload.notificationId,
    )
  )
    return;
  event.waitUntil(
    (async () => {
      const tag = `workos-${payload.notificationId}`;
      const shown = await self.registration.getNotifications({ tag });
      if (shown.length === 0) {
        await self.registration.showNotification("WorkOS", {
          body: "You have a new workspace notification.",
          tag,
          renotify: false,
        });
      }
      for (const client of await self.clients.matchAll({ type: "window" })) {
        client.postMessage({ type: "workos.notification.wake.v1" });
      }
    })(),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window" });
      const client = windows.find((item) => new URL(item.url).origin === self.location.origin);
      if (client) {
        await client.focus();
        client.postMessage({ type: "workos.notification.open.v1" });
      } else {
        await self.clients.openWindow("/?notifications=1");
      }
    })(),
  );
});
