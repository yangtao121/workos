import { describe, expect, it } from "vitest";
import { createSecureDeviceKeyBackend, type SecureVault } from "./backend.js";

describe("platform device key persistence", () => {
  function fixture() {
    const records = new Map<string, string>();
    const vault: SecureVault = {
      get: (key) => Promise.resolve(records.get(key)),
      set: (key, value) => {
        records.set(key, value);
        return Promise.resolve();
      },
      remove: (key) => {
        records.delete(key);
        return Promise.resolve();
      },
    };
    return { records, vault, backend: createSecureDeviceKeyBackend(vault, "identity") };
  }
  it("retains the key across metadata updates and reloads as non-extractable", async () => {
    const { backend, vault } = fixture();
    await backend.save(await backend.generate("phone", "phone"));
    const saved = required(await backend.load());
    expect(saved.privateKey.extractable).toBe(false);
    expect(await backend.validate(saved)).toBe(true);
    await backend.save({ ...saved, deviceId: "0198d7ea-2110-7c42-b659-c5e4d73bc301" });
    const reloaded = required(await createSecureDeviceKeyBackend(vault, "identity").load());
    expect(reloaded.publicKeyHash).toBe(saved.publicKeyHash);
    expect(reloaded.deviceId).toBe("0198d7ea-2110-7c42-b659-c5e4d73bc301");
    expect(await backend.validate(reloaded)).toBe(true);
    await backend.clear();
    expect(await backend.load()).toBeUndefined();
  });
  it("rejects corrupt/mismatched records without overwriting them", async () => {
    const { backend, records } = fixture();
    for (const value of ["invalid", "null", "{}"]) {
      records.set("identity", value);
      await expect(backend.load()).rejects.toThrow();
      expect(records.get("identity")).toBe(value);
    }
    records.clear();
    await backend.save(await backend.generate("first", "phone"));
    const record = JSON.parse(required(records.get("identity"))) as { publicKeyHash: string };
    record.publicKeyHash = `sha256:${"00".repeat(32)}`;
    records.set("identity", JSON.stringify(record));
    await expect(backend.load()).rejects.toThrow("does not match");
  });
  it("propagates vault outages instead of treating them as a missing identity", async () => {
    const backend = createSecureDeviceKeyBackend(
      {
        get: () => Promise.reject(new Error("vault locked")),
        set: () => Promise.reject(new Error("must not write")),
        remove: () => Promise.resolve(),
      },
      "identity",
    );
    await expect(backend.load()).rejects.toThrow("vault locked");
  });
});

function required<T>(value: T | undefined): T {
  if (value === undefined) throw new Error("fixture value missing");
  return value;
}
