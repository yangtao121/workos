// The mobile auth controller: pairing through the canonical device-auth
// client (deviceClass "phone"), session restore over the cookie-authenticated
// transport, and honest phase reporting for the shell. No parallel identity
// protocol or DTO exists here — this is the same DevicePairingService proof
// flow the desktop uses.
import {
  DeviceAuthClient,
  createSecureDeviceKeyBackend,
  parsePairingFragment,
  type DeviceInfo,
  type SecureVault,
} from "@workos/device-auth";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { Transport } from "@connectrpc/connect";

export const DEVICE_IDENTITY_SLOT = "workos.device-identity.v1";

export type MobileAuthPhase =
  | { phase: "connecting" }
  | { phase: "unpaired" }
  | { phase: "pairing" }
  | { phase: "paired"; device: DeviceInfo }
  | { phase: "unavailable" };

export interface MobileAuth {
  readonly transport: Transport;
  readonly backendId: string;
  // begin runs restore first; a pairing fragment in the URL pairs this
  // device and scrubs the fragment on success.
  begin(fragment: string | undefined): Promise<MobileAuthPhase>;
  logout(): Promise<MobileAuthPhase>;
}

// createMobileAuth wires the canonical device-auth client. The secure vault
// backend is only used when the caller proved the vault is platform-backed
// (native webview); the web runtime keeps the non-extractable profile
// backend instead of storing key material in localStorage.
export function createMobileAuth(origin: string, secureVault: SecureVault | undefined): MobileAuth {
  // credentials: include keeps the __Host- session cookie flowing on the
  // cross-origin native webview (the desktop is same-origin by construction).
  const transport = createConnectTransport({
    baseUrl: origin,
    fetch: (input, init) => globalThis.fetch(input, { ...init, credentials: "include" }),
  });
  const client = new DeviceAuthClient(origin, transport);
  const secureClient =
    secureVault !== undefined
      ? new DeviceAuthClient(
          origin,
          transport,
          createSecureDeviceKeyBackend(secureVault, DEVICE_IDENTITY_SLOT),
        )
      : client;

  return {
    transport,
    backendId: secureVault !== undefined ? "platform-secure-storage" : "profile-indexeddb",
    async begin(fragment) {
      try {
        if (fragment !== undefined && fragment.length > 0) {
          const parsed = parsePairingFragment(fragment);
          const device = await secureClient.pairWithTicket({
            secret: parsed.secret,
            tlsFingerprint: parsed.tlsFingerprint,
            deviceName: "WorkOS Mobile",
            deviceClass: "phone",
          });
          return { phase: "paired", device };
        }
        const device = await client.restoreSession();
        return device ? { phase: "paired", device } : { phase: "unpaired" };
      } catch (error) {
        if (error instanceof Error && error.message.includes("pairing")) {
          // A malformed fragment is a user-visible pairing failure, not an
          // outage: surface the unpaired screen with the fragment scrubbed.
          return { phase: "unpaired" };
        }
        return { phase: "unavailable" };
      }
    },
    async logout() {
      try {
        await secureClient.forget();
      } catch {
        // Logout is best effort locally; the next begin() reconciles state.
      }
      return { phase: "unpaired" };
    },
  };
}
