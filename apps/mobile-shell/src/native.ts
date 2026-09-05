// Mobile native wrapper (ADR-0019, W5): the Capacitor-side bridge around
// the shared adaptive shell. Device keys live in native secure storage when
// the runtime provides it; otherwise the wrapper degrades to a documented
// fallback and REPORTS the degraded status honestly instead of pretending.
// Push registration posts the device token to the fixture relay endpoint
// configured by the deployment.
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
  const nativeSecure =
    Capacitor.isNativePlatform() && Capacitor.isPluginAvailable("SecureStorage");
  // The ephemeral fallback keeps a session working without ever claiming
  // protection it does not have.
  let fallback: string | undefined;
  return {
    async status() {
      if (nativeSecure) {
        return { secure: true, reason: "native secure storage" };
      }
      if (Capacitor.isNativePlatform()) {
        return {
          secure: false,
          reason: "native secure storage plugin is not installed; using ephemeral memory",
        };
      }
      return {
        secure: false,
        reason: "web runtime; using ephemeral memory",
      };
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

export interface PushRegistrationResult {
  registered: boolean;
  detail: string;
}

// registerPushToken posts the native push token to the deployment's fixture
// relay registration endpoint. The relay sees only the token and device id —
// never notification content (ADR-0018).
export async function registerPushToken(props: {
  relayEndpoint: string;
  token: string;
  deviceId: string;
  fetchImpl?: typeof fetch;
}): Promise<PushRegistrationResult> {
  const doFetch = props.fetchImpl ?? fetch;
  if (!props.relayEndpoint.startsWith("https://") && !props.relayEndpoint.startsWith("http://127.0.0.1") && !props.relayEndpoint.startsWith("http://localhost")) {
    return { registered: false, detail: "relay endpoint must be https or an explicit loopback" };
  }
  if (props.token.length === 0 || props.token.length > 4096) {
    return { registered: false, detail: "push token grammar is invalid" };
  }
  try {
    const response = await doFetch(props.relayEndpoint, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ token: props.token, deviceId: props.deviceId }),
    });
    if (!response.ok) {
      return { registered: false, detail: `relay rejected registration: ${String(response.status)}` };
    }
    return { registered: true, detail: "registered with relay" };
  } catch (reason) {
    return {
      registered: false,
      detail: reason instanceof Error ? reason.message : "relay unreachable",
    };
  }
}
