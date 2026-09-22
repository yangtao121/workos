import { beforeEach, describe, expect, it, vi } from "vitest";
import { Capacitor } from "@capacitor/core";
import { createMobileVault } from "./native.js";

const plugin = vi.hoisted(() => ({
  get: vi.fn(),
  set: vi.fn(),
  remove: vi.fn(),
  getStatus: vi.fn(),
}));
vi.mock("@capacitor/core", () => ({
  registerPlugin: () => plugin,
  Capacitor: { isNativePlatform: vi.fn(), isPluginAvailable: vi.fn(), getPlatform: vi.fn() },
}));
beforeEach(() => vi.resetAllMocks());
describe("mobile vault", () => {
  it("requires an actual Android Keystore check and never admits the legacy fallback", async () => {
    vi.mocked(Capacitor.isNativePlatform).mockReturnValue(true);
    vi.mocked(Capacitor.getPlatform).mockReturnValue("android");
    vi.mocked(Capacitor.isPluginAvailable).mockImplementation(
      (name) => name === "SecureStoragePlugin",
    );
    expect((await createMobileVault().status()).secure).toBe(false);
    vi.mocked(Capacitor.isPluginAvailable).mockImplementation(
      (name) => name === "WorkOSSecureStorage",
    );
    plugin.getStatus.mockResolvedValue({ secure: false });
    expect((await createMobileVault().status()).secure).toBe(false);
    plugin.getStatus.mockRejectedValue(new Error("Keystore locked"));
    expect((await createMobileVault().status()).secure).toBe(false);
    plugin.getStatus.mockResolvedValue({ secure: true });
    expect((await createMobileVault().status()).secure).toBe(true);
  });
  it("reports web storage honestly and requires the native plugin", async () => {
    expect((await createMobileVault().status()).secure).toBe(false);
    vi.mocked(Capacitor.isNativePlatform).mockReturnValue(true);
    expect((await createMobileVault().status()).reason).toContain("pairing unavailable");
    vi.mocked(Capacitor.isPluginAvailable).mockReturnValue(true);
    expect((await createMobileVault().status()).secure).toBe(true);
  });
  it("distinguishes a missing key from locked or broken storage", async () => {
    const { vault } = createMobileVault();
    plugin.get.mockRejectedValue(new Error("Item with given key does not exist"));
    expect(await vault.get("identity")).toBeUndefined();
    plugin.get.mockRejectedValue(new Error("Keychain locked"));
    await expect(vault.get("identity")).rejects.toThrow("locked");
    plugin.set.mockResolvedValue({ value: false });
    await expect(vault.set("identity", "fixture")).rejects.toThrow("write failed");
    plugin.remove.mockResolvedValue({ value: false });
    await expect(vault.remove("identity")).rejects.toThrow("removal failed");
  });
});
