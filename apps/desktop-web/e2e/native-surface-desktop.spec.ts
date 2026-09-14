import { mkdir } from "node:fs/promises";
import { createDesktopProject, openDesktopApp } from "./open-app.js";
import { expect, test } from "@playwright/test";

// The virtual-display native runner gate (ADR-0029): the real desktop Native
// window receives the loopback WebRTC video track of an Xvfb display, renders
// genuine frames, and its keyboard input changes the remote display content.
test.setTimeout(240_000);

test.skip(process.env.WORKOS_NATIVE_E2E !== "true", "requires the native-surface gate stack");

const CAPTURE = "../../docs/ui/desktop-web/changes/20260914-native-runner/after";
await mkdir(CAPTURE, { recursive: true });

test("Native window streams the real virtual display and forwards input", async ({ page }) => {
  await page.goto("/");
  await createDesktopProject(page, `Native Desktop ${String(Date.now())}`);

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
  await expect
    .poll(
      async () => {
        const text = await page.getByTestId("native-frames").textContent();
        return Number((text ?? "").split(": ")[1] ?? "0");
      },
      { timeout: 60_000 },
    )
    .toBeGreaterThan(5);

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
  const varianceBefore = await readVariance();
  await page.keyboard.type("workos-native-proof");
  await page.waitForTimeout(2500);
  const varianceAfter = await readVariance();
  if (varianceBefore === 0 && varianceAfter === 0) {
    throw new Error("native video pixels stayed uniform; not a real display");
  }
  // Typing a long line leaves visibly more ink on the display.
  await expect.poll(() => readVariance(), { timeout: 30_000 }).not.toBe(varianceBefore);
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
    await page.screenshot({
      path: `${CAPTURE}/native-window--streaming--${String(size.width)}x${String(size.height)}.png`,
      animations: "disabled",
    });
  }
});
