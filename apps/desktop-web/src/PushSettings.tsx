import { useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { DeviceAuthClient } from "@workos/device-auth";
import { Button } from "@workos/ui-kit";
import { Code, ConnectError } from "@connectrpc/connect";

export function PushSettings({
  workosClients,
  deviceAuth,
}: {
  workosClients: WorkOSClients;
  deviceAuth?: DeviceAuthClient | undefined;
}) {
  const [key, setKey] = useState("");
  const [unavailable, setUnavailable] = useState("");
  const [quiet, setQuiet] = useState({
    revision: 0n,
    quietEnabled: false,
    quietStartUtc: "22:00",
    quietEndUtc: "07:00",
  });
  const [subscription, setSubscription] = useState<PushSubscription | null>(null);
  const [busy, setBusy] = useState(false);
  const [ready, setReady] = useState(false);
  const [message, setMessage] = useState("");
  const [attempt, setAttempt] = useState(0);
  const running = useRef(false);
  useEffect(() => {
    let live = true;
    setReady(false);
    setMessage("");
    void workosClients.notifications
      .getPushPreferences({})
      .then(async (result) => {
        let existing: PushSubscription | null = null;
        if ("serviceWorker" in navigator)
          existing =
            (await (
              await navigator.serviceWorker.getRegistration("/")
            )?.pushManager.getSubscription()) ?? null;
        if (!live) return;
        if (result.preferences) setQuiet(result.preferences);
        setKey(result.webPushPublicKey);
        setUnavailable(result.webPushUnavailableReason);
        setSubscription(existing);
        setReady(true);
      })
      .catch(() => {
        if (live) setMessage("Notification preferences could not be loaded. Try again.");
      });
    return () => {
      live = false;
    };
  }, [workosClients, attempt]);
  async function run(action: () => Promise<void>) {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    setMessage("");
    try {
      await action();
    } catch (error) {
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        try {
          const latest = await workosClients.notifications.getPushPreferences({});
          if (latest.preferences) setQuiet(latest.preferences);
          setMessage("Quiet hours changed on another device. The latest settings are loaded.");
          return;
        } catch {
          /* Preserve the retryable failure below. */
        }
      }
      setMessage(
        "Could not save notification preferences. Check the connection and browser permission, then retry.",
      );
    } finally {
      running.current = false;
      setBusy(false);
    }
  }
  async function enable() {
    if (!deviceAuth || !key) return;
    const permission = await Notification.requestPermission();
    if (permission !== "granted") {
      setMessage(
        "Browser notifications are blocked. Allow them in this site's browser settings to enable background alerts.",
      );
      return;
    }
    const device = await deviceAuth.getCurrentDevice();
    await navigator.serviceWorker.register("/push-worker.js", {
      scope: "/",
      updateViaCache: "none",
    });
    const worker = await navigator.serviceWorker.ready;
    const publicKey = Uint8Array.from(atob(key.replace(/-/g, "+").replace(/_/g, "/")), (value) =>
      value.charCodeAt(0),
    );
    const current = await worker.pushManager.getSubscription();
    const created =
      current ??
      (await worker.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: publicKey,
      }));
    const json = created.toJSON();
    if (!json.keys?.p256dh || !json.keys.auth) throw new Error("subscription keys missing");
    try {
      await workosClients.notifications.subscribePush({
        deviceId: device.deviceId,
        platform: "web-push",
        endpoint: created.endpoint,
        p256dh: json.keys.p256dh,
        authSecret: json.keys.auth,
      });
    } catch (error) {
      if (!current) await created.unsubscribe();
      throw error;
    }
    setSubscription(created);
    setMessage("Background alerts enabled for this browser.");
  }
  async function disable() {
    if (!deviceAuth) return;
    const device = await deviceAuth.getCurrentDevice();
    await workosClients.notifications.unsubscribePush({
      deviceId: device.deviceId,
      platform: "web-push",
    });
    await subscription?.unsubscribe();
    setSubscription(null);
    setMessage("Background alerts disabled for this browser.");
  }
  const browserAvailable =
    typeof Notification !== "undefined" && "serviceWorker" in navigator && "PushManager" in window;
  return (
    <section className="push-settings" aria-label="Background notification settings">
      <div>
        <strong>Background alerts</strong>
        <p>Alerts show a generic message. Open WorkOS to read the notification.</p>
      </div>
      {ready ? (
        <>
          <Button
            type="button"
            disabled={busy || !browserAvailable || !deviceAuth || (!subscription && !key)}
            onClick={() => {
              void run(subscription ? disable : enable);
            }}
          >
            {subscription ? "Disable browser alerts" : "Enable browser alerts"}
          </Button>
          {!browserAvailable ? (
            <p>This browser does not support background alerts.</p>
          ) : unavailable ? (
            <p>{unavailable}</p>
          ) : null}
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void run(async () => {
                const result = await workosClients.notifications.setPushPreferences({
                  preferences: quiet,
                  expectedRevision: quiet.revision,
                });
                if (result.preferences) setQuiet(result.preferences);
                setMessage("Quiet hours saved.");
              });
            }}
          >
            <label>
              <input
                type="checkbox"
                checked={quiet.quietEnabled}
                disabled={busy}
                onChange={(event) => {
                  setQuiet({ ...quiet, quietEnabled: event.target.checked });
                }}
              />
              Quiet hours
            </label>
            <div className="quiet-hours">
              <label>
                From (UTC)
                <input
                  type="time"
                  value={quiet.quietStartUtc}
                  required
                  disabled={busy}
                  onChange={(event) => {
                    setQuiet({ ...quiet, quietStartUtc: event.target.value });
                  }}
                />
              </label>
              <label>
                Until (UTC)
                <input
                  type="time"
                  value={quiet.quietEndUtc}
                  required
                  disabled={busy}
                  onChange={(event) => {
                    setQuiet({ ...quiet, quietEndUtc: event.target.value });
                  }}
                />
              </label>
            </div>
            <Button type="submit" disabled={busy}>
              Save quiet hours
            </Button>
          </form>
        </>
      ) : message ? (
        <Button
          type="button"
          onClick={() => {
            setAttempt((value) => value + 1);
          }}
        >
          Retry
        </Button>
      ) : (
        <p role="status">Loading preferences…</p>
      )}
      {message ? <p role="status">{message}</p> : null}
    </section>
  );
}
