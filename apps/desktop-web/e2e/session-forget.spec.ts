import { expect, test } from "@playwright/test";

test("Forget preserves local content on failure and clears it after confirmed logout", async ({
  page,
  browserName,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  let unavailable = true;
  await page.route("**/workos.auth.v1.DeviceService/*", async (route) => {
    const logout = route.request().url().endsWith("/Logout");
    await route.fulfill({
      status: logout ? (unavailable ? 503 : 200) : 401,
      json:
        logout && !unavailable
          ? {}
          : { code: logout ? "unavailable" : "unauthenticated", message: "Fixture only" },
    });
  });
  await page.goto("/");
  await expect(page.getByTestId("auth-gate")).toHaveAttribute("data-state", "unpaired");
  await page.evaluate(async () => {
    const request = indexedDB.open("workos-session-continuity", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("journals");
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      request.onsuccess = () => {
        resolve(request.result);
      };
      request.onerror = () => {
        reject(new Error("fixture database unavailable"));
      };
    });
    const transaction = db.transaction("journals", "readwrite");
    transaction.objectStore("journals").put("Fixture draft", "fixture-record");
    await new Promise<void>((resolve, reject) => {
      transaction.oncomplete = () => {
        resolve();
      };
      transaction.onerror = () => {
        reject(new Error("fixture transaction failed"));
      };
    });
    db.close();
    localStorage.setItem("workos.desktop-projection.v1", "Fixture references");
  });
  await page.getByRole("button", { name: "Forget this browser" }).click();
  const baseline = process.env.WORKOS_FORGET_BASELINE === "true";
  if (baseline)
    await expect(page.getByText("This browser is not yet paired with this WorkOS.")).toBeVisible();
  else
    await expect(
      page.getByText("This browser could not be fully cleared. Try Forget again."),
    ).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("workos.desktop-projection.v1"))).toBe(
    "Fixture references",
  );
  const directory = process.env.WORKOS_CAPTURE_DIR;
  if (directory)
    await page.screenshot({
      path: `${directory}/${browserName}/auth-gate--forget-failed--1440x900.png`,
      animations: "disabled",
    });
  if (baseline) return;
  unavailable = false;
  await page.getByRole("button", { name: "Forget this browser" }).click();
  await expect(page.getByText("This browser is not yet paired with this WorkOS.")).toBeVisible();
  expect(
    await page.evaluate(() => localStorage.getItem("workos.desktop-projection.v1")),
  ).toBeNull();
  expect(
    await page.evaluate(async () => {
      const request = indexedDB.open("workos-session-continuity", 1);
      const db = await new Promise<IDBDatabase>((resolve) => {
        request.onsuccess = () => {
          resolve(request.result);
        };
      });
      const read = db.transaction("journals").objectStore("journals").get("fixture-record");
      const value = await new Promise<unknown>((resolve) => {
        read.onsuccess = () => {
          resolve(read.result);
        };
      });
      db.close();
      return value;
    }),
  ).toBeUndefined();
});
