import { useCallback, useEffect, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { DeviceAuthClient } from "@workos/device-auth";
import { type DeviceInfo } from "@workos/device-auth";
import { DeviceClass as DeviceClassEnum } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

// Device Center is a normal (non-permanent) window listing the owner's
// paired devices: the current device, session expiry, the short-lived
// "Pair another device" QR, and revoke/logout actions.
//
// Secret boundary: the pairing ticket exists only in this window's memory
// while the QR is displayed — never in window-manager state, sessionStorage,
// DOM debug attributes, or logs. Rotating a ticket or closing the window
// clears it.

export interface DeviceCenterProps {
  deviceAuth: DeviceAuthClient;
}

interface TicketView {
  pairingUrl: string;
  qrDataUrl: string;
  tlsFingerprint: string;
  expiresAt: Date;
}

export function DeviceCenter({ deviceAuth }: DeviceCenterProps) {
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [sessionExpiresAt, setSessionExpiresAt] = useState<Date>();
  const [loadError, setLoadError] = useState<string>();
  const [ticket, setTicket] = useState<TicketView>();
  const [ticketError, setTicketError] = useState<string>();
  const [actionError, setActionError] = useState<string>();
  const [confirmingRevoke, setConfirmingRevoke] = useState<string>();
  const [busy, setBusy] = useState(false);
  const revokeAttempts = useRef(
    new Map<string, { expectedRevision: bigint; idempotencyKey: string }>(),
  );

  const refresh = useCallback(async () => {
    setLoadError(undefined);
    try {
      const page = await deviceAuth.listDevices(100);
      setDevices(page.devices);
      const current = await deviceAuth.getCurrentSession();
      setSessionExpiresAt(current.sessionExpiresAt);
    } catch {
      setLoadError("The device list is temporarily unavailable.");
    }
  }, [deviceAuth]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // A ticket is memory-only and disappears exactly at its server-provided
  // expiry. Replacing it cancels the old timer; unmounting destroys both the
  // timer and component state.
  useEffect(() => {
    if (!ticket) return undefined;
    const remaining = Math.max(0, ticket.expiresAt.getTime() - Date.now());
    const timeout = window.setTimeout(() => {
      setTicket((current) => (current === ticket ? undefined : current));
    }, remaining);
    return () => {
      window.clearTimeout(timeout);
    };
  }, [ticket]);

  const rotateTicket = useCallback(async () => {
    setTicketError(undefined);
    setTicket(undefined);
    try {
      const rotated = await deviceAuth.rotatePairingTicket();
      const now = Date.now();
      const expiresAt = rotated.expiresAt?.getTime() ?? Number.NaN;
      if (!Number.isFinite(expiresAt) || expiresAt <= now || expiresAt > now + 15 * 60 * 1000) {
        throw new Error("gateway returned an invalid pairing ticket expiry");
      }
      const { default: QRCode } = await import("qrcode");
      const qrDataUrl = await QRCode.toDataURL(rotated.pairingUrl, { margin: 1, width: 220 });
      if (expiresAt <= Date.now()) {
        throw new Error("pairing ticket expired before its QR was ready");
      }
      setTicket({
        pairingUrl: rotated.pairingUrl,
        qrDataUrl,
        tlsFingerprint: rotated.tlsFingerprint,
        expiresAt: new Date(expiresAt),
      });
      void refresh();
    } catch {
      setTicketError("A new pairing code could not be created right now.");
    }
  }, [deviceAuth, refresh]);

  const revoke = useCallback(
    async (device: DeviceInfo) => {
      if (confirmingRevoke !== device.deviceId || busy) {
        setConfirmingRevoke(device.deviceId);
        return;
      }
      setBusy(true);
      setActionError(undefined);
      let attempt = revokeAttempts.current.get(device.deviceId);
      if (attempt?.expectedRevision !== device.revision) {
        attempt = {
          expectedRevision: device.revision,
          idempotencyKey: crypto.randomUUID(),
        };
        revokeAttempts.current.set(device.deviceId, attempt);
      }
      try {
        await deviceAuth.revokeDevice({
          deviceId: device.deviceId,
          idempotencyKey: attempt.idempotencyKey,
          expectedRevision: attempt.expectedRevision,
        });
        revokeAttempts.current.delete(device.deviceId);
        setConfirmingRevoke(undefined);
        await refresh();
        if (device.isCurrent) {
          // Revoking the current device ends this session; the server
          // cleared the cookie, and the local profile key is dropped so the
          // shell returns to the unpaired screen.
          await deviceAuth.forget();
          window.location.reload();
        }
      } catch (error) {
        if (
          error instanceof ConnectError &&
          (error.code === Code.Aborted || error.code === Code.NotFound)
        ) {
          revokeAttempts.current.delete(device.deviceId);
          setConfirmingRevoke(undefined);
          await refresh();
        }
        setActionError(
          device.isCurrent
            ? "This device could not be removed. Retry once the gateway is reachable."
            : "The device could not be removed. It may have changed — refresh and retry.",
        );
      } finally {
        setBusy(false);
      }
    },
    [busy, confirmingRevoke, deviceAuth, refresh],
  );

  const logout = useCallback(async () => {
    setBusy(true);
    setActionError(undefined);
    try {
      await deviceAuth.logout();
      window.location.reload();
    } catch {
      setActionError("Signing out failed. Retry once the gateway is reachable.");
    } finally {
      setBusy(false);
    }
  }, [deviceAuth]);

  return (
    <div className="device-center" data-testid="device-center">
      <p className="device-center-summary">
        {sessionExpiresAt
          ? `Session expires ${sessionExpiresAt.toLocaleString()}`
          : "Session expiry is not available right now."}
      </p>
      {loadError ? (
        <p className="auth-gate-error" role="alert">
          {loadError}
        </p>
      ) : (
        <ul className="device-list" aria-label="Paired devices">
          {devices.map((device) => (
            <li className="device-row" key={device.deviceId}>
              <div className="device-facts">
                <strong>{device.name}</strong>
                <small>
                  {deviceLabel(device.deviceClass)}
                  {device.isCurrent ? " · this device" : ""}
                  {device.revokedAt ? " · revoked" : ""}
                  {` · revision ${device.revision.toString()}`}
                </small>
              </div>
              {device.revokedAt ? null : (
                <Button
                  disabled={busy}
                  type="button"
                  onClick={() => {
                    void revoke(device);
                  }}
                >
                  {confirmingRevoke === device.deviceId
                    ? device.isCurrent
                      ? "Confirm: remove this device?"
                      : "Confirm revoke"
                    : device.isCurrent
                      ? "Remove this device"
                      : "Revoke"}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {actionError ? (
        <p className="auth-gate-error" role="alert">
          {actionError}
        </p>
      ) : null}
      <div className="device-actions">
        <Button disabled={busy} type="button" onClick={() => void rotateTicket()}>
          {ticket ? "Replace pairing code" : "Pair another device"}
        </Button>
        <Button disabled={busy} type="button" onClick={() => void logout()}>
          Sign out
        </Button>
      </div>
      {ticketError ? (
        <p className="auth-gate-error" role="alert">
          {ticketError}
        </p>
      ) : null}
      {ticket ? (
        <div className="pairing-ticket" data-testid="pairing-ticket">
          <img alt="Pairing QR code for another device" src={ticket.qrDataUrl} />
          <p className="auth-gate-hint">
            Expires {ticket.expiresAt.toLocaleTimeString()}. The new code replaces any earlier one.
            Confirm the fingerprint on the pairing device: <code>{ticket.tlsFingerprint}</code>
          </p>
          <Button
            type="button"
            onClick={() => {
              void navigator.clipboard.writeText(ticket.pairingUrl);
            }}
          >
            Copy pairing URL
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function deviceLabel(deviceClass: DeviceClassEnum): string {
  switch (deviceClass) {
    case DeviceClassEnum.DESKTOP:
      return "Desktop";
    case DeviceClassEnum.TABLET:
      return "Tablet";
    case DeviceClassEnum.FOLDABLE:
      return "Foldable";
    case DeviceClassEnum.PHONE:
      return "Phone";
    default:
      return "Device";
  }
}
