// Mobile native wrapper (ADR-0019, W5): the Capacitor-side bridge around
// the shared adaptive shell. Device keys live in native secure storage when
// the runtime provides it; otherwise the wrapper degrades to a documented
// fallback and REPORTS the degraded status honestly instead of pretending.
import { Capacitor, registerPlugin } from "@capacitor/core";

export interface SecureKeyStatus {
  secure: boolean;
  reason: string;
}

export interface SecureKeyVaultPlugin {
  set(options: { key: string; value: string }): Promise<void>;
  get(options: { key: string }): Promise<{ value?: string | undefined }>;
  remove(options: { key: string }): Promise<void>;
}

// The plugin id matches @capacitor-community/secure-storage; when the
// community plugin is absent from the native build the registry resolves to
// the web fallback, which the wrapper then classifies as insecure.
const SecureStorage = registerPlugin<SecureKeyVaultPlugin>("SecureStorage");

export interface DeviceKeyStore {
  status(): Promise<SecureKeyStatus>;
  load(): Promise<string | undefined>;
  store(value: string): Promise<void>;
  clear(): Promise<void>;
}

const deviceKeySlot = "workos.device-key.v1";

export function createDeviceKeyStore(): DeviceKeyStore {
  const nativeSecure = Capacitor.isNativePlatform() && Capacitor.isPluginAvailable("SecureStorage");
  // The ephemeral fallback keeps a session working without ever claiming
  // protection it does not have.
  let fallback: string | undefined;
  return {
    status(): Promise<SecureKeyStatus> {
      if (nativeSecure) {
        return Promise.resolve({ secure: true, reason: "native secure storage" });
      }
      if (Capacitor.isNativePlatform()) {
        return Promise.resolve({
          secure: false,
          reason: "native secure storage plugin is not installed; using ephemeral memory",
        });
      }
      return Promise.resolve({
        secure: false,
        reason: "web runtime; using ephemeral memory",
      });
    },
    async load() {
      if (nativeSecure) {
        const result = await SecureStorage.get({ key: deviceKeySlot });
        return result.value;
      }
      return fallback;
    },
    async store(value: string) {
      if (nativeSecure) {
        await SecureStorage.set({ key: deviceKeySlot, value });
        return;
      }
      fallback = value;
    },
    async clear() {
      if (nativeSecure) {
        await SecureStorage.remove({ key: deviceKeySlot });
        return;
      }
      fallback = undefined;
    },
  };
}
