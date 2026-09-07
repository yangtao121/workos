import { useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { DeviceAuthClient } from "@workos/device-auth";
import { Button } from "@workos/ui-kit";
import { Code, ConnectError } from "@connectrpc/connect";

function publicKeyBytes(key: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(atob(key.replace(/-/g, "+").replace(/_/g, "/")), (character) =>
    character.charCodeAt(0),
  );
}
function matchesKey(subscription: PushSubscription, key: string): boolean {
  const current = subscription.options.applicationServerKey;
  if (!current || !key) return false;
  const expected = publicKeyBytes(key),
    actual = new Uint8Array(current);
  return (
    expected.length === actual.length && expected.every((byte, index) => byte === actual[index])
  );
}
async function endpointDigest(endpoint: string): Promise<string> {
  const bytes = new Uint8Array(
    await crypto.subtle.digest("SHA-256", new TextEncoder().encode(endpoint)),
  );
  return `sha256:${Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}
async function readyWorker(): Promise<ServiceWorkerRegistration> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      navigator.serviceWorker.ready,
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => {
          reject(new Error("worker activation timed out"));
        }, 20_000);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

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
  const [browserChecked, setBrowserChecked] = useState(false);
  const [serverDigest, setServerDigest] = useState("");
  const [localDigest, setLocalDigest] = useState("");
  const [subscription, setSubscription] = useState<PushSubscription | null>(null);
  const [busy, setBusy] = useState(false);
  const [ready, setReady] = useState(false);
  const [message, setMessage] = useState("");
  const [attempt, setAttempt] = useState(0);
  const running = useRef(false);
  useEffect(() => {
    let live = true;
    const isLive = () => live;
    setReady(false);
    setBrowserChecked(false);
    setMessage("");
    void workosClients.notifications
      .getPushPreferences({})
      .then(async (result) => {
        if (!isLive()) return;
        if (result.preferences) setQuiet(result.preferences);
        setKey(result.webPushPublicKey);
        setUnavailable(result.webPushUnavailableReason);
        setServerDigest(result.webPushSubscriptionDigest);
        setReady(true);
        try {
          const existing =
            "serviceWorker" in navigator
              ? ((await (
                  await navigator.serviceWorker.getRegistration("/")
                )?.pushManager.getSubscription()) ?? null)
              : null;
          const digest = existing ? await endpointDigest(existing.endpoint) : "";
          if (!isLive()) return;
          setSubscription(existing);
          setLocalDigest(digest);
        } catch {
          if (isLive())
            setMessage(
              "Browser subscription could not be checked. Quiet hours are still available.",
            );
        } finally {
          if (isLive()) setBrowserChecked(true);
        }
      })
      .catch(() => {
        if (isLive()) setMessage("Notification preferences could not be loaded. Try again.");
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
    const worker = await readyWorker();
    const publicKey = publicKeyBytes(key);
    let current = await worker.pushManager.getSubscription();
    if (current && !matchesKey(current, key)) {
      await workosClients.notifications.unsubscribePush({
        deviceId: device.deviceId,
        platform: "web-push",
      });
      setServerDigest("");
      if (!(await current.unsubscribe()) && (await worker.pushManager.getSubscription()))
        throw new Error("old subscription remains");
      current = null;
      setSubscription(null);
      setLocalDigest("");
    }
    const created =
      current ??
      (await worker.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: publicKey,
      }));
    const digest = await endpointDigest(created.endpoint);
    setSubscription(created);
    setLocalDigest(digest);
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
      if (!current) {
        try {
          if (await created.unsubscribe()) {
            setSubscription(null);
            setLocalDigest("");
          }
        } catch {
          /* Keep the local reference so cleanup remains available. */
        }
      }
      throw error;
    }
    setServerDigest(digest);
    setMessage("Background alerts enabled for this browser.");
  }
  async function disable() {
    if (!deviceAuth) return;
    const device = await deviceAuth.getCurrentDevice();
    await workosClients.notifications.unsubscribePush({
      deviceId: device.deviceId,
      platform: "web-push",
    });
    setServerDigest("");
    try {
      if (subscription && !(await subscription.unsubscribe())) {
        const remaining = await (
          await navigator.serviceWorker.getRegistration("/")
        )?.pushManager.getSubscription();
        if (remaining) {
          setSubscription(remaining);
          throw new Error("subscription remains");
        }
      }
    } catch {
      setMessage(
        "WorkOS alerts are disabled. Browser cleanup failed; retry disabling to remove the local subscription.",
      );
      return;
    }
    setSubscription(null);
    setLocalDigest("");
    setMessage("Background alerts disabled for this browser.");
  }
  const enabled =
    !!subscription &&
    !!serverDigest &&
    localDigest === serverDigest &&
    matchesKey(subscription, key);
  const hasRegistration = !!subscription || !!serverDigest;
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
          <div className="push-actions">
            {!enabled ? (
              <Button
                type="button"
                disabled={busy || !browserChecked || !browserAvailable || !deviceAuth || !key}
                onClick={() => {
                  void run(enable);
                }}
              >
                {hasRegistration ? "Reconnect browser alerts" : "Enable browser alerts"}
              </Button>
            ) : null}
            {hasRegistration ? (
              <Button
                type="button"
                disabled={busy || !browserChecked || !browserAvailable || !deviceAuth}
                onClick={() => {
                  void run(disable);
                }}
              >
                Disable browser alerts
              </Button>
            ) : null}
          </div>
          {hasRegistration && !enabled ? (
            <p>This browser needs to reconnect before WorkOS can send background alerts.</p>
          ) : null}
          {!browserAvailable ? (
            <p>This browser does not support background alerts.</p>
          ) : !deviceAuth ? (
            <p>Pair this browser with WorkOS to enable background alerts.</p>
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
