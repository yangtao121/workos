// The mobile auth controller: pairing through the canonical device-auth
// client (deviceClass "phone"), session restore over the cookie-authenticated
// transport, and honest phase reporting for the shell. No parallel identity
// protocol or DTO exists here — this is the same DevicePairingService proof
// flow the desktop uses.
import {
  DeviceAuthClient,
  profileDeviceKeyBackend,
  createSecureDeviceKeyBackend,
  parsePairingFragment,
  type DeviceInfo,
  type SecureVault,
} from "@workos/device-auth";
import { Capacitor } from "@capacitor/core";
import { createMobileTransport } from "./transport.js";
import { Code, ConnectError } from "@connectrpc/connect";
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
  const transport = createMobileTransport(origin);
  const backend =
    secureVault !== undefined
      ? createSecureDeviceKeyBackend(
          secureVault,
          `${DEVICE_IDENTITY_SLOT}:${new URL(origin).origin}`,
        )
      : profileDeviceKeyBackend;
  const client = new DeviceAuthClient(origin, transport, backend);
  let pending: Promise<MobileAuthPhase> | undefined;
  let forgetting: Promise<MobileAuthPhase> | undefined;
  const begin = async (fragment: string | undefined): Promise<MobileAuthPhase> => {
    if (Capacitor.isNativePlatform() && secureVault === undefined) return { phase: "unavailable" };
    try {
      if (fragment) {
        let parsed;
        try {
          parsed = parsePairingFragment(fragment);
        } catch {
          return { phase: "unpaired" };
        }
        const device = await client.pairWithTicket({
          secret: parsed.secret,
          tlsFingerprint: parsed.tlsFingerprint,
          deviceName: "WorkOS Mobile",
          deviceClass: "phone",
        });
        return { phase: "paired", device };
      }
      const session = await client.restoreSession();
      if (session) return { phase: "paired", device: session };
      const identity = await backend.load();
      if (!identity?.deviceId) return { phase: "unpaired" };
      return { phase: "paired", device: await client.reauthenticate() };
    } catch (error) {
      if (
        error instanceof ConnectError &&
        (error.code === Code.Unauthenticated ||
          error.code === Code.NotFound ||
          error.code === Code.PermissionDenied)
      )
        return { phase: "unpaired" };
      return { phase: "unavailable" };
    }
  };
  return {
    transport,
    backendId: backend.id,
    begin(fragment) {
      if (forgetting) return forgetting;
      // React StrictMode and repeated retry taps must not claim a ticket twice.
      pending ??= begin(fragment).finally(() => {
        pending = undefined;
      });
      return pending;
    },
    logout() {
      // Finish any in-flight proof before Logout so its late Set-Cookie cannot
      // recreate a server session after the device has been forgotten.
      forgetting ??= (async (): Promise<MobileAuthPhase> => {
        try {
          await pending;
          await client.forget();
          return { phase: "unpaired" };
        } catch {
          return { phase: "unavailable" };
        }
      })().finally(() => {
        forgetting = undefined;
      });
      return forgetting;
    },
  };
}
