import { execFileSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { _android, expect, test, type AndroidDevice, type Page } from "@playwright/test";

const host = process.env.WORKOS_ANDROID_ADB_HOST;
const origin = process.env.WORKOS_ANDROID_ORIGIN ?? "";
const emulator = process.env.WORKOS_ANDROID_CONTAINER ?? "";
const gateway = `${process.env.WORKOS_V2_NAMESPACE ?? "missing"}-gateway-1`;
const pkg = "dev.workos.mobile.acceptance";
const slot = `workos.device-identity.v1:${origin}`;
const tlsFailure = (error: unknown) =>
  error instanceof Error &&
  /certificate|SSL|CertPath|Trust anchor|Hostname .*not verified|Chain validation failed/i.test(
    error.message,
  );
test.skip(!host, "requires the isolated KVM Android fixture");
test.setTimeout(300000);

function adb(...args: string[]) {
  return execFileSync("docker", ["exec", emulator, "adb", "-s", "emulator-5554", ...args], {
    encoding: "utf8",
  });
}

async function launch(device: AndroidDevice, application = pkg) {
  adb("shell", "am", "force-stop", application);
  await expect
    .poll(() => device.webViews().some((view) => view.pkg() === application), { timeout: 15000 })
    .toBe(false);
  adb("shell", "am", "start", "-n", `${application}/dev.workos.mobile.MainActivity`);
  const view = await device.webView({ pkg: application });
  const page = await view.page();
  page.setDefaultTimeout(15000);
  await expect(page.locator(".workos-mobile")).toBeVisible({ timeout: 30000 });
  return page;
}

async function plugin(page: Page, name: string, method: string, data: object = {}) {
  return page.evaluate(
    async ({ name, method, data }) => {
      const capacitor = (
        window as unknown as {
          Capacitor: {
            Plugins: Record<string, Record<string, (value: object) => Promise<unknown>>>;
          };
        }
      ).Capacitor;
      const call = capacitor.Plugins[name]?.[method];
      if (!call) throw new Error("native plugin method unavailable");
      return call(data);
    },
    { name, method, data },
  );
}

async function rpc(
  page: Page,
  service: string,
  method: string,
  data: object = {},
  server = origin,
) {
  const result = (await plugin(page, "CapacitorHttp", "request", {
    url: `${server}/${service}/${method}`,
    method: "POST",
    headers: { Origin: server, "Content-Type": "application/json" },
    data,
    responseType: "json",
    disableRedirects: true,
    connectTimeout: 10000,
    readTimeout: 10000,
  })) as { status: number; data: Record<string, unknown> };
  return result;
}

async function current(page: Page) {
  const response = await rpc(page, "workos.auth.v1.DeviceService", "GetCurrentDevice");
  expect(response.status).toBe(200);
  return response.data.device as { deviceId: string; revision: string };
}

async function pair(page: Page, initial: boolean) {
  const text = execFileSync("docker", ["exec", gateway, "workosctl", "device", "pair"], {
    encoding: "utf8",
  });
  const link = text.split("\n").find((line) => line.startsWith("https://"));
  if (!link) throw new Error("fixture pairing link missing");
  const input = page.getByLabel(initial ? "WorkOS address or pairing link" : "Pairing link", {
    exact: true,
  });
  await expect(input).toBeVisible();
  try {
    await input.fill(link);
  } catch {
    throw new Error("could not enter pairing link");
  }
  await page
    .getByRole("button", { name: initial ? "Connect" : "Pair device", exact: true })
    .click();
  await expect(page.getByTestId("mobile-tabs")).toBeVisible({ timeout: 30000 });
  await page.getByRole("button", { name: "Projects", exact: true }).click();
  await expect(page.getByTestId("mobile-projects")).toContainText("Development fixture", {
    timeout: 30000,
  });
  await expect(page.getByTestId("mobile-key-status")).toHaveText(
    "Device key: native secure storage",
  );
  // Only the public origin may enter web storage; no ticket or identity JWK.
  const stored = await page.evaluate(() => Object.entries(localStorage));
  expect(stored).toEqual([["workos.mobile-origin.v1", origin]]);
  expect(page.url()).not.toContain("#");
}

async function capture(page: Page, state: string) {
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  });
  await expect
    .poll(() =>
      page.evaluate(() => Math.abs(innerHeight - (visualViewport?.height ?? innerHeight))),
    )
    .toBeLessThan(2);
  const size = await page.evaluate(() => ({
    width: innerWidth,
    height: innerHeight,
    scale: devicePixelRatio,
  }));
  expect(size.width).toBe(390);
  expect(size.scale).toBe(1);
  const path = "/workos-gate/android-visuals";
  await mkdir(path, { recursive: true });
  // Only the fixture's randomly allocated public port is normalized for visual
  // records. HTTP, TLS and authentication assertions use the real origin.
  const labels = page.locator("code");
  await labels.evaluateAll((nodes, value) => {
    for (const node of nodes) {
      if (node.textContent === value) node.textContent = "https://workos.fixture";
    }
  }, origin);
  try {
    await page.screenshot({
      path: `${path}/android-shell--${state}--${String(size.width)}x${String(size.height)}.png`,
      animations: "disabled",
    });
  } finally {
    await labels.evaluateAll((nodes, value) => {
      for (const node of nodes) {
        if (node.textContent === "https://workos.fixture") node.textContent = value;
      }
    }, origin);
  }
}

test("real Android APK enforces TLS, Keystore identity and session continuity", async () => {
  const devices = await _android.devices({ host: host ?? "", port: 5037 });
  expect(devices).toHaveLength(1);
  const device = devices[0];
  if (!device) throw new Error("Android device missing");
  try {
    // The separately named disposable acceptance app never contains user data.
    adb("shell", "pm", "clear", pkg);
    let page = await launch(device, "dev.workos.mobile");
    // Normal debug trusts the platform CA set, never the acceptance CA.
    const rejected = await rpc(page, "workos.auth.v1.DeviceService", "GetCurrentDevice").then(
      () => false,
      tlsFailure,
    );
    expect(rejected).toBe(true);
    adb("shell", "am", "force-stop", "dev.workos.mobile");
    page = await launch(device);
    await capture(page, "setup");
    await pair(page, true);
    const identity = await current(page);
    await capture(page, "projects");
    const ciphertext = adb(
      "shell",
      "run-as",
      pkg,
      "cat",
      "shared_prefs/workos_device_vault_v1.xml",
    );
    expect(
      ciphertext.includes("v1.") &&
        !ciphertext.includes("privateJwk") &&
        !ciphertext.includes(identity.deviceId),
    ).toBe(true);
    expect(await plugin(page, "WorkOSSecureStorage", "getStatus")).toEqual({ secure: true });
    page = await launch(device);
    await expect(page.getByTestId("mobile-projects")).toContainText("Development fixture");
    expect((await current(page)).deviceId).toBe(identity.deviceId);
    await plugin(page, "CapacitorCookies", "clearAllCookies");
    expect((await rpc(page, "workos.auth.v1.DeviceService", "GetCurrentDevice")).status).toBe(401);
    await page.reload();
    await expect(page.getByTestId("mobile-projects")).toContainText("Development fixture", {
      timeout: 30000,
    });
    expect((await current(page)).deviceId).toBe(identity.deviceId);
    const wrong = origin.replace("workos.fixture", "wrong.workos.fixture");
    expect(
      await rpc(page, "workos.auth.v1.DeviceService", "GetCurrentDevice", {}, wrong).then(
        () => false,
        tlsFailure,
      ),
    ).toBe(true);
    await page.getByRole("button", { name: /^Alerts/ }).click();
    await expect(page.getByTestId("mobile-notification").first()).toBeVisible();
    const unreadButtons = await page
      .getByRole("button", { name: "Mark read", exact: true })
      .count();
    expect(unreadButtons).toBeGreaterThan(0);
    await page.getByRole("button", { name: "Mark read", exact: true }).first().click();
    await expect(page.getByRole("button", { name: "Mark read", exact: true })).toHaveCount(
      unreadButtons - 1,
    );
    const notifications = await rpc(
      page,
      "workos.notification.v1.NotificationService",
      "ListNotifications",
      { pageSize: 20 },
    );
    expect(notifications.status).toBe(200);
    expect(
      (notifications.data.notifications as { readAt?: string }[]).some((item) => item.readAt),
    ).toBe(true);
    await capture(page, "alerts");
    execFileSync("docker", ["stop", "--time", "1", gateway], { stdio: "ignore" });
    try {
      await page.getByRole("button", { name: "Forget device", exact: true }).click();
      await expect(page.getByRole("alert")).toHaveText("Forget device failed. Try again.", {
        timeout: 30000,
      });
      await capture(page, "forget-failed");
    } finally {
      execFileSync("docker", ["start", gateway], { stdio: "ignore" });
    }
    await expect
      .poll(
        () =>
          current(page).then(
            (value) => value.deviceId,
            () => "",
          ),
        { timeout: 20000 },
      )
      .toBe(identity.deviceId);
    await page.getByRole("button", { name: "Forget device", exact: true }).click();
    await expect(page.getByTestId("mobile-unpaired")).toBeVisible();
    expect(
      await plugin(page, "WorkOSSecureStorage", "get", { key: slot }).then(
        () => false,
        () => true,
      ),
    ).toBe(true);
    await capture(page, "unpaired");
    await pair(page, false);
    const next = await current(page);
    expect(next.deviceId).not.toBe(identity.deviceId);
    expect(
      (
        await rpc(page, "workos.auth.v1.DeviceService", "RevokeDevice", {
          deviceId: next.deviceId,
          expectedRevision: next.revision,
          idempotencyKey: randomUUID(),
        })
      ).status,
    ).toBe(200);
    await page.reload();
    await expect(page.getByTestId("mobile-unpaired")).toBeVisible();
    expect((await rpc(page, "workos.auth.v1.DeviceService", "GetCurrentDevice")).status).toBe(401);
    // Corrupt the real app's ciphertext after cookie removal; no reset/fallback.
    await plugin(page, "CapacitorCookies", "clearAllCookies");
    adb("shell", "am", "force-stop", pkg);
    adb(
      "shell",
      `run-as ${pkg} sh -c 'sed -i "s/>v1\\./>bad./" shared_prefs/workos_device_vault_v1.xml'`,
    );
    page = await launch(device);
    await expect(page.getByTestId("mobile-unavailable")).toBeVisible();
    await expect(page.getByTestId("mobile-unpaired")).toHaveCount(0);
    await capture(page, "storage-unavailable");
    await writeFile(
      "/workos-gate/android-continuity.json",
      JSON.stringify(
        {
          deviceId: identity.deviceId,
          successorDeviceId: next.deviceId,
          tls: "platform CA refusal + fixture CA trust + hostname refusal",
          identity: "encrypted at rest, restart and cookie-free proof",
          forget: "failure preserves identity, success removes it",
          revocation: "401 and unpaired",
          corruption: "unavailable",
        },
        null,
        2,
      ),
    );
    console.log(
      "ANDROID_APK_TLS_KEYSTORE_PAIR_RESTART_REAUTH_NOTIFICATIONS_FORGET_REVOKE_CORRUPTION_PASS",
    );
  } finally {
    await device.close();
  }
});
