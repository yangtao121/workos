import { mkdir } from "node:fs/promises";
import { createDesktopProject, openDesktopApp } from "./open-app.js";
import { expect, test } from "@playwright/test";

// The virtual-display native runner gate (ADR-0029): the real desktop Native
// window receives the loopback WebRTC video track of an Xvfb display, renders
// genuine frames, and its keyboard input changes the remote display content.
test.setTimeout(240_000);

test.skip(process.env.WORKOS_NATIVE_E2E !== "true", "requires the native-surface gate stack");

const CAPTURE =
  process.env.WORKOS_CAPTURE_DIR ?? "../../docs/ui/desktop-web/changes/20260914-merge-review/after";

test("Native window streams the real virtual display and forwards input", async ({ page }) => {
  await mkdir(CAPTURE, { recursive: true });
  await page.setViewportSize({ width: 1440, height: 900 });
  const sessions: string[] = [];
  let connects = 0;
  page.on("response", async (response) => {
    if (response.url().endsWith("/ConnectNativeSession") && response.ok()) connects++;
    if (response.url().endsWith("/CreateNativeSession") && response.ok()) {
      const body = (await response.json()) as { session?: { id?: string; state?: string } };
      if (body.session?.id) sessions.push(body.session.id);
    }
  });
  await page.goto("/");
  await createDesktopProject(page, "Native Desktop Fixture");

  await openDesktopApp(page, "home");
  const home = page.getByTestId("home-app");
  const nativeEntry = home.getByTestId("home-entry-native");
  await expect(nativeEntry).toBeEnabled();
  // Visual record: the launchpad now carries the Native entry.
  await page.screenshot({
    path: `${CAPTURE}/home--launchpad--1440x900.png`,
    animations: "disabled",
  });
  await nativeEntry.click();

  const native = page.getByTestId("native-app");
  await expect(native).toBeVisible();
  await expect(page.getByTestId("native-status")).toHaveText("streaming", { timeout: 90_000 });

  // Genuine decoded video: dimensions are known and live frames keep coming.
  await expect
    .poll(
      async () =>
        page.evaluate(() => {
          const video = document.querySelector<HTMLVideoElement>('[data-testid="native-video"]');
          if (!video || video.videoWidth === 0) return 0;
          return video.videoWidth * 100000 + video.videoHeight;
        }),
      { timeout: 60_000 },
    )
    .toBeGreaterThan(0);
  const videoTime = () =>
    page.getByTestId("native-video").evaluate((node) => (node as HTMLVideoElement).currentTime);
  const firstTime = await videoTime();
  await expect.poll(videoTime).toBeGreaterThan(firstTime + 0.5);

  // Pixel readback proves the decoded display content is real (non-uniform)
  // and changes after keyboard input reaches the remote xterm. The variance
  // computation is inlined so it evaluates inside the page.
  const readVariance = () =>
    page.evaluate(() => {
      const video = document.querySelector<HTMLVideoElement>('[data-testid="native-video"]');
      if (!video || video.videoWidth === 0 || video.readyState < 2) return 0;
      const canvas = document.createElement("canvas");
      canvas.width = 64;
      canvas.height = 48;
      const context = canvas.getContext("2d");
      if (!context) return 0;
      context.drawImage(video, 0, 0, canvas.width, canvas.height);
      const { data } = context.getImageData(0, 0, canvas.width, canvas.height);
      let sum = 0;
      let sumSquares = 0;
      const samples = data.length / 4;
      for (let index = 0; index + 3 < data.length; index += 4) {
        const red = data[index] ?? 0;
        const green = data[index + 1] ?? 0;
        const blue = data[index + 2] ?? 0;
        const luminance = (red + green + blue) / 3;
        sum += luminance;
        sumSquares += luminance * luminance;
      }
      const mean = sum / samples;
      return Math.round(sumSquares / samples - mean * mean);
    });
  const stage = page.getByTestId("native-stage");
  await stage.click();
  await expect.poll(readVariance).toBeGreaterThan(0);
  // Only input can turn the xterm background red; cursor/frame timing cannot
  // satisfy this assertion. This catches missing SCTP negotiation in the offer.
  await page.keyboard.type("printf '\\033[41m\\033[2J\\033[H\\033[?25l'");
  await page.keyboard.press("Enter");
  const readRedPixels = () =>
    page.evaluate(() => {
      const video = document.querySelector<HTMLVideoElement>('[data-testid="native-video"]');
      if (!video || video.readyState < 2) return 0;
      const canvas = document.createElement("canvas");
      canvas.width = 64;
      canvas.height = 48;
      const context = canvas.getContext("2d");
      if (!context) return 0;
      context.drawImage(video, 0, 0, 64, 48);
      const { data } = context.getImageData(0, 0, 64, 48);
      let red = 0;
      for (let i = 0; i < data.length; i += 4) {
        if ((data[i] ?? 0) > 120 && (data[i + 1] ?? 255) < 80 && (data[i + 2] ?? 255) < 80) red++;
      }
      return red;
    });
  await expect.poll(readRedPixels, { timeout: 30000 }).toBeGreaterThan(500);
  await expect.poll(() => connects, { timeout: 35000 }).toBeGreaterThan(1);
  await expect(page.getByTestId("native-status")).toHaveText("streaming");
  // Visual record: the streaming Native window with real decoded content at
  // the repo's three standard viewports (the session and video keep flowing
  // through the resizes).
  for (const size of [
    { width: 1440, height: 900 },
    { width: 820, height: 1180 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(size);
    await expect(page.getByTestId("native-stage")).toBeVisible();
    await expect.poll(readRedPixels, { timeout: 30000 }).toBeGreaterThan(500);
    expect(sessions).toHaveLength(1);
    await expect(page.getByTestId("native-status")).toHaveText("streaming");
    const resizedTime = await videoTime();
    await expect.poll(videoTime, { timeout: 15000 }).toBeGreaterThan(resizedTime + 0.5);
    // Re-resolve the video after responsive remounts; a callback registered on
    // the detached desktop element can never fire.
    await page.screenshot({
      path: `${CAPTURE}/native-window--streaming--${String(size.width)}x${String(size.height)}.png`,
      animations: "disabled",
    });
  }
  // Closing the window must release the durable session, even with media active.
  await page.getByRole("button", { name: "Close Native", exact: true }).click();
  await expect(native).toHaveCount(0);
  await expect
    .poll(async () => {
      if (sessions.length === 0) return "missing";
      const response = await page.request.post(
        "/workos.surface.v1.NativeSessionService/GetNativeSession",
        { data: { sessionId: sessions[sessions.length - 1] } },
      );
      const body = (await response.json()) as { session?: { id?: string; state?: string } };
      return body.session?.state;
    })
    .toBe("closed");
});
