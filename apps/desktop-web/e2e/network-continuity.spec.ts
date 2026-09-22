import { execFileSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { chromium, expect, test, type Page } from "@playwright/test";
import { openDesktopApp } from "./open-app.js";
import { mobileContinuation, touchHome } from "./mobile-continuity.js";

const mode = process.env.WORKOS_NETWORK_MODE ?? "";
const mobile = process.env.WORKOS_MOBILE_BROWSER === "1";
test.skip(!mode, "requires isolated HTTPS and Docker network topology");
test.setTimeout(300_000);

async function rpc(page: Page, service: string, method: string, data: object) {
  return page.evaluate(
    async ({ service, method, data }) => {
      const response = await fetch(`/${service}/${method}`, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(data),
      });
      const text = await response.text();
      let body: Record<string, unknown> = {};
      if (response.headers.get("Content-Type")?.includes("application/json")) {
        body = JSON.parse(text) as Record<string, unknown>;
      }
      return { status: response.status, body };
    },
    { service, method, data },
  );
}

async function openNative(page: Page) {
  if (mobile) {
    await touchHome(page);
    await page.getByRole("button", { name: "Native", exact: true }).tap();
    await expect(page.getByTestId("native-app")).toBeVisible();
    return;
  }
  await openDesktopApp(page, "home");
  await page.getByTestId("home-entry-native").click();
  await expect(page.getByTestId("native-app")).toBeVisible();
}

async function command(page: Page, value: string) {
  if (mobile) {
    await page.getByLabel("Native text", { exact: true }).fill(value);
    await page.getByRole("button", { name: "Send text", exact: true }).tap();
    await page.getByRole("button", { name: "Enter", exact: true }).tap();
    return;
  }
  await page.getByTestId("native-stage").click();
  await page.keyboard.type(value);
  await page.keyboard.press("Enter");
}

async function pixels(page: Page, color: "red" | "green") {
  return page.getByTestId("native-video").evaluate((element, color) => {
    const video = element as HTMLVideoElement;
    if (video.readyState < 2) return 0;
    const canvas = document.createElement("canvas");
    canvas.width = 64;
    canvas.height = 48;
    const context = canvas.getContext("2d");
    if (!context) return 0;
    context.drawImage(video, 0, 0, 64, 48);
    const data = context.getImageData(0, 0, 64, 48).data;
    let count = 0;
    for (let i = 0; i < data.length; i += 4) {
      const red = data[i] ?? 0,
        green = data[i + 1] ?? 0,
        blue = data[i + 2] ?? 0;
      if (
        color === "red"
          ? red > 120 && green < 80 && blue < 80
          : green > 100 && red < 80 && blue < 80
      )
        count++;
    }
    return count;
  }, color);
}

test("trusted HTTPS with independent clients preserves workload and control across the network", async () => {
  const a = await chromium.connect(process.env.WORKOS_NETWORK_A_WS ?? "");
  const b = await chromium.connect(process.env.WORKOS_NETWORK_B_WS ?? "");
  const contexts = await Promise.all([
    a.newContext({
      ignoreHTTPSErrors: false,
      isMobile: mobile,
      hasTouch: mobile,
      deviceScaleFactor: 1,
      viewport: mobile ? { width: 390, height: 844 } : { width: 1440, height: 900 },
    }),
    b.newContext({
      ignoreHTTPSErrors: false,
      isMobile: mobile,
      hasTouch: mobile,
      deviceScaleFactor: 1,
      viewport: mobile ? { width: 820, height: 1180 } : { width: 1440, height: 900 },
    }),
  ]);
  const pages: Page[] = [];
  try {
    for (const [index, context] of contexts.entries()) {
      await context.addInitScript(() => {
        const target = window as typeof window & {
          networkPeers: RTCPeerConnection[];
          networkDiagnostics: object[];
        };
        target.networkPeers = [];
        target.networkDiagnostics = [];
        const Original = window.RTCPeerConnection;
        window.RTCPeerConnection = class extends Original {
          constructor(config?: RTCConfiguration) {
            super(config);
            target.networkPeers.push(this);
            this.addEventListener("icecandidateerror", (event) => {
              target.networkDiagnostics.push({
                event: "icecandidateerror",
                code: event.errorCode,
                text: event.errorText,
              });
            });
            this.addEventListener("iceconnectionstatechange", () => {
              const state = this.iceConnectionState;
              void this.getStats().then((stats) => {
                const records: object[] = [];
                stats.forEach((record: RTCStats) => {
                  if (
                    ["local-candidate", "remote-candidate", "candidate-pair"].includes(record.type)
                  )
                    records.push(record);
                });
                target.networkDiagnostics.push({
                  event: "iceconnectionstatechange",
                  state,
                  records,
                });
              });
            });
          }
        };
      });
      const page = await context.newPage();
      page.setDefaultTimeout(15000);
      pages.push(page);
      const ticket = execFileSync(
        "docker",
        [
          "exec",
          `${process.env.WORKOS_V2_NAMESPACE ?? "missing"}-gateway-1`,
          "workosctl",
          "device",
          "pair",
        ],
        { encoding: "utf8" },
      );
      const pairing = ticket.split("\n").find((line) => line.startsWith("https://"));
      if (!pairing) throw new Error("pairing ticket unavailable");
      await page.goto(pairing);
      await page.getByLabel("Device name").fill(`Network ${mode} ${String(index + 1)}`);
      await page.getByTestId("pairing-panel").getByRole("button", { name: "Pair device" }).click();
      await expect(page.locator(mobile ? ".adaptive-shell" : ".desktop-shell")).toBeVisible({
        timeout: 30000,
      });
      expect(page.url()).not.toContain("#v=1");
      expect(await page.evaluate(() => window.isSecureContext)).toBe(true);
      const response = await page.goto(page.url());
      expect(response?.ok()).toBe(true);
      const security = await response?.securityDetails();
      expect(security?.issuer).toContain("workos lan-pairing test CA");
      const cookies = await context.cookies();
      expect(
        cookies.some(
          (cookie) => cookie.name.startsWith("__Host-") && cookie.secure && cookie.httpOnly,
        ),
      ).toBe(true);
      if (mobile) console.log(`MOBILE_PAIRED_${String(index + 1)}`);
    }
    const first = pages[0],
      second = pages[1];
    if (!first || !second) throw new Error("two browser clients required");
    if (mobile) await mobileContinuation(first, second);
    let identity = { sessionId: "", controlGeneration: "" },
      connects = 0;
    first.on("response", (response) => {
      if (response.url().endsWith("/ConnectNativeSession") && response.ok()) {
        identity = response.request().postDataJSON() as typeof identity;
        connects++;
      }
    });
    await openNative(first);
    await expect(first.getByTestId("native-status")).toHaveText("streaming", { timeout: 60000 });
    await command(
      first,
      "NET_CANARY=NETWORK_MEMORY_PRESERVED; printf '\\033[41m\\033[2J\\033[H\\033[?25l'",
    );
    await expect.poll(() => pixels(first, "red"), { timeout: 30000 }).toBeGreaterThan(500);
    const readPair = () =>
      first.evaluate(async () => {
        const peers = (window as typeof window & { networkPeers: RTCPeerConnection[] })
          .networkPeers;
        for (const peer of [...peers].reverse()) {
          if (peer.connectionState !== "connected") continue;
          const stats = await peer.getStats();
          const records = [...stats.values()] as {
            type: string;
            state?: string;
            nominated?: boolean;
            localCandidateId: string;
            remoteCandidateId: string;
          }[];
          const pair = records.find(
            (value) =>
              value.type === "candidate-pair" && value.state === "succeeded" && value.nominated,
          ) as { localCandidateId: string; remoteCandidateId: string } | undefined;
          if (!pair) continue;
          const local = stats.get(pair.localCandidateId) as {
            candidateType: string;
            address: string;
          };
          const remote = stats.get(pair.remoteCandidateId) as {
            candidateType: string;
            address: string;
          };
          return {
            local: local.candidateType,
            remote: remote.candidateType,
            localAddress: local.address,
            remoteAddress: remote.address,
          };
        }
        return null;
      });
    await expect.poll(readPair).not.toBeNull();
    const pair = await readPair();
    expect(pair?.local).toBe(mode === "relay" ? "relay" : "host");
    expect(pair?.remote).toBe(mode === "relay" ? "relay" : "host");
    expect(pair?.remoteAddress).not.toBe("127.0.0.1");
    await writeFile(`/workos-gate/selected-pair-${mode}.json`, JSON.stringify(pair, null, 2));
    await expect.poll(() => connects, { timeout: 35000 }).toBeGreaterThan(1);
    const original = identity.sessionId;
    expect(original).not.toBe("");
    await openNative(second);
    await expect(second.getByTestId("native-take-control")).toBeVisible();
    await second.getByTestId("native-take-control").click();
    await expect(second.getByTestId("native-status")).toHaveText("streaming", { timeout: 60000 });
    await expect.poll(() => pixels(second, "red"), { timeout: 30000 }).toBeGreaterThan(500);
    const denied = await rpc(
      first,
      "workos.surface.v1.NativeSessionService",
      "GetNativeConnectivity",
      { sessionId: identity.sessionId, controlGeneration: identity.controlGeneration },
    );
    expect(denied.status).toBe(403);
    await command(
      second,
      "printf '%s' \"$NET_CANARY\" > /workspace/network-memory.txt; printf '\\033[42m\\033[2J\\033[H\\033[?25l'",
    );
    await expect
      .poll(() => readFile("/workos-gate/project/network-memory.txt", "utf8").catch(() => ""))
      .toBe("NETWORK_MEMORY_PRESERVED");
    await expect.poll(() => pixels(second, "green"), { timeout: 30000 }).toBeGreaterThan(500);
    const session = await rpc(
      second,
      "workos.surface.v1.NativeSessionService",
      "GetNativeSession",
      { sessionId: original },
    );
    expect((session.body.session as { id: string; state: string }).id).toBe(original);
    expect((session.body.session as { state: string }).state).toBe("running");
    await second.getByRole("button", { name: "Close Native", exact: true }).click();
    await openNative(second);
    await expect(second.getByTestId("native-take-control")).toBeVisible();
    await second.getByTestId("native-take-control").click();
    await expect(second.getByTestId("native-status")).toHaveText("streaming", { timeout: 60000 });
    await expect.poll(() => pixels(second, "green")).toBeGreaterThan(500);
    if (mode === "relay") {
      const name = `${process.env.WORKOS_V2_NAMESPACE ?? "missing"}-turn`;
      execFileSync("docker", ["stop", "--time", "1", name], { stdio: "ignore" });
      try {
        await expect(second.getByTestId("native-status")).toHaveText("ended", { timeout: 60000 });
      } finally {
        execFileSync("docker", ["start", name], { stdio: "ignore" });
      }
      await second.getByRole("button", { name: "Close Native", exact: true }).click();
      await openNative(second);
      await expect(second.getByTestId("native-take-control")).toBeVisible();
      await second.getByTestId("native-take-control").click();
      await expect(second.getByTestId("native-status")).toHaveText("streaming", { timeout: 60000 });
      await expect.poll(() => pixels(second, "green")).toBeGreaterThan(500);
    }
    const capture = `/workos-gate/visuals-${mode}`;
    await mkdir(capture, { recursive: true });
    for (const size of [
      { width: 1440, height: 900 },
      { width: 820, height: 1180 },
      { width: 390, height: 844 },
    ]) {
      await second.setViewportSize(size);
      // Responsive reparenting is asynchronous. Require several decoded frames
      // from the streaming view, rather than accepting the pre-resize video.
      let stableFrames = 0;
      await expect
        .poll(
          async () => {
            const ready =
              (await second.getByTestId("native-status").textContent()) === "streaming" &&
              (await pixels(second, "green")) > 500;
            stableFrames = ready ? stableFrames + 1 : 0;
            return stableFrames;
          },
          { timeout: 30000, intervals: [200] },
        )
        .toBeGreaterThanOrEqual(3);
      await second.screenshot({
        path: `${capture}/native-window--streaming--${String(size.width)}x${String(size.height)}.png`,
        animations: "disabled",
      });
    }
    const current = await rpc(second, "workos.auth.v1.DeviceService", "GetCurrentDevice", {});
    const device = current.body.device as { deviceId: string; revision: string };
    const revoked = await rpc(first, "workos.auth.v1.DeviceService", "RevokeDevice", {
      deviceId: device.deviceId,
      expectedRevision: device.revision,
      idempotencyKey: randomUUID(),
    });
    expect(revoked.status).toBe(200);
    const afterRevocation = await rpc(
      second,
      "workos.surface.v1.NativeSessionService",
      "GetNativeConnectivity",
      { sessionId: original, controlGeneration: "0" },
    );
    expect(afterRevocation.status).toBe(401);
    await expect
      .poll(
        () =>
          second.evaluate(() =>
            (window as typeof window & { networkPeers: RTCPeerConnection[] }).networkPeers.every(
              (peer) => peer.connectionState !== "connected",
            ),
          ),
        { timeout: 40000 },
      )
      .toBe(true);
    const stopped = await rpc(
      first,
      "workos.surface.v1.SurfaceContinuityService",
      "StopSurfaceWorkload",
      { workloadId: original, actionKey: `network-stop-${mode}` },
    );
    expect(stopped.status).toBe(200);
    console.log(`TRUSTED_HTTPS_${mode.toUpperCase()}_MEDIA_INPUT_RENEWAL_TAKEOVER_CONTINUITY_PASS`);
  } finally {
    for (const [index, page] of pages.entries()) {
      if (page.isClosed()) continue;
      const diagnostics = await page.evaluate(
        () => (window as typeof window & { networkDiagnostics: object[] }).networkDiagnostics,
      );
      await writeFile(
        `/workos-gate/ice-${mode}-${String(index)}.json`,
        JSON.stringify(diagnostics, null, 2),
      );
    }
    await Promise.all(contexts.map((context) => context.close()));
    await Promise.all([a.close(), b.close()]);
  }
});
