import { describe, expect, it } from "vitest";

import { createDeviceKeyStore } from "./native.js";

describe("device key store", () => {
  it("reports an honest insecure status on the web runtime and never claims protection", async () => {
    const store = createDeviceKeyStore();
    const status = await store.status();
    expect(status.secure).toBe(false);
    expect(status.reason).toContain("ephemeral");
    await store.store("device-secret");
    expect(await store.load()).toBe("device-secret");
    await store.clear();
    expect(await store.load()).toBeUndefined();
  });
});
