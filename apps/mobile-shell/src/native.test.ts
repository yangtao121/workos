import { describe, expect, it } from "vitest";

import { createMobileVault } from "./native.js";

describe("mobile vault", () => {
  it("reports an honest insecure status on the web runtime and never claims protection", async () => {
    const mobileVault = createMobileVault();
    const status = await mobileVault.status();
    expect(status.secure).toBe(false);
    expect(status.reason).toContain("web runtime");
  });

  // The web fallback stores through localStorage; the plain node test
  // environment has no storage at all, so the round trip runs only where
  // the browser storage exists (real coverage: the Chromium mount gate).
  it.skipIf(typeof globalThis.localStorage === "undefined")(
    "round-trips values through the vault contract",
    async () => {
      const mobileVault = createMobileVault();
      await mobileVault.vault.set("workos.test.slot", "material");
      expect(await mobileVault.vault.get("workos.test.slot")).toBe("material");
      await mobileVault.vault.remove("workos.test.slot");
      expect(await mobileVault.vault.get("workos.test.slot")).toBeUndefined();
    },
  );
});
