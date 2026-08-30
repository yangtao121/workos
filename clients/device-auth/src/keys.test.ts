import { describe, expect, it } from "vitest";

import { generateDeviceKeyPair, validateDeviceKeyMaterial } from "./keys.js";

describe("browser device key integrity", () => {
  it("proves the non-exportable private key, canonical SPKI, and digest belong together", async () => {
    const first = await generateDeviceKeyPair();
    const second = await generateDeviceKeyPair();

    expect(first.privateKey.extractable).toBe(false);
    expect(first.privateKey.usages).toEqual(["sign"]);
    await expect(
      validateDeviceKeyMaterial(first.privateKey, first.publicKeySpki, first.publicKeyHash),
    ).resolves.toBe(true);
    await expect(
      validateDeviceKeyMaterial(first.privateKey, second.publicKeySpki, second.publicKeyHash),
    ).resolves.toBe(false);
    await expect(
      validateDeviceKeyMaterial(first.privateKey, first.publicKeySpki, `sha256:${"00".repeat(32)}`),
    ).resolves.toBe(false);
    await expect(
      validateDeviceKeyMaterial(first.privateKey, new Uint8Array([1, 2, 3]), first.publicKeyHash),
    ).resolves.toBe(false);
  });
});
