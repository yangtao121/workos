// Mobile native wrapper (ADR-0019, W5): the Capacitor-side bridge around
// the shared shell. Device key material persists through the platform
// secure storage plugin (Android Keystore / iOS Keychain) when the runtime
// provides it; the web fallback is honestly reported as insecure instead of
// pretending localStorage protects the credential.
import { Capacitor, registerPlugin } from "@capacitor/core";
import type { SecureVault } from "@workos/device-auth";

interface SecureStoragePluginDefinition {
  get(options: { key: string }): Promise<{ value: string }>;
  set(options: { key: string; value: string }): Promise<{ value: boolean }>;
  remove(options: { key: string }): Promise<{ value: boolean }>;
}

const SecureStorage = registerPlugin<SecureStoragePluginDefinition>("SecureStoragePlugin");
const AndroidSecureStorage = registerPlugin<
  SecureStoragePluginDefinition & {
    getStatus(): Promise<{ secure: boolean }>;
  }
>("WorkOSSecureStorage");

export interface VaultStatus {
  secure: boolean;
  reason: string;
}

export interface MobileVault {
  vault: SecureVault;
  status(): Promise<VaultStatus>;
}

// createMobileVault adapts the platform plugin to the device-auth SecureVault
// contract. On native platforms the plugin is Keychain/Keystore-backed; on
// the web its localStorage fallback is explicitly NOT secure storage.
export function createMobileVault(): MobileVault {
  const android = Capacitor.getPlatform() === "android";
  const storage = android ? AndroidSecureStorage : SecureStorage;
  const nativeSecure =
    Capacitor.isNativePlatform() &&
    Capacitor.isPluginAvailable(android ? "WorkOSSecureStorage" : "SecureStoragePlugin");
  return {
    vault: {
      async get(key) {
        try {
          const result = await storage.get({ key });
          return result.value;
        } catch (error) {
          if (error instanceof Error && error.message === "Item with given key does not exist")
            return undefined;
          throw error;
        }
      },
      async set(key, value) {
        const result = await storage.set({ key, value });
        if (!result.value) throw new Error("secure storage write failed");
      },
      async remove(key) {
        try {
          const result = await storage.remove({ key });
          if (!result.value) throw new Error("secure storage removal failed");
        } catch (error) {
          if (error instanceof Error && error.message === "Item with given key does not exist")
            return;
          throw error;
        }
      },
    },
    async status(): Promise<VaultStatus> {
      if (nativeSecure) {
        if (android) {
          const available = await AndroidSecureStorage.getStatus().catch(() => ({ secure: false }));
          if (!available.secure)
            return { secure: false, reason: "Android Keystore unavailable; pairing unavailable" };
        }
        return {
          secure: true,
          reason: "native secure storage (Keystore/Keychain)",
        };
      }
      if (Capacitor.isNativePlatform()) {
        return {
          secure: false,
          reason: "native secure storage plugin is not installed; pairing unavailable",
        };
      }
      return {
        secure: false,
        reason: "web runtime; profile storage fallback",
      };
    },
  };
}
