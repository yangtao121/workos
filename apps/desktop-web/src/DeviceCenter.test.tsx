// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { DeviceAuthClient } from "@workos/device-auth";
import { DeviceClass, type DeviceInfo } from "@workos/protocol";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DeviceCenter } from "./DeviceCenter.js";

vi.mock("qrcode", () => ({
  default: { toDataURL: vi.fn(() => Promise.resolve("data:image/png;base64,fixture")) },
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Device Center security state", () => {
  it("removes an in-memory pairing QR when its server expiry arrives", async () => {
    const client = clientFixture({
      rotatePairingTicket: vi.fn(() =>
        Promise.resolve({
          pairingUrl: `https://workos.example/pair#v=1&t=${"A".repeat(43)}&fp=sha256:${"ab".repeat(32)}`,
          tlsFingerprint: `sha256:${"ab".repeat(32)}`,
          expiresAt: new Date(Date.now() + 80),
        }),
      ),
    });
    render(<DeviceCenter deviceAuth={client} />);

    fireEvent.click(await screen.findByRole("button", { name: "Pair another device" }));
    expect(await screen.findByTestId("pairing-ticket")).toBeTruthy();
    await waitFor(
      () => {
        expect(screen.queryByTestId("pairing-ticket")).toBeNull();
      },
      { timeout: 1_000 },
    );
  });

  it("rejects a malformed server expiry instead of displaying a QR", async () => {
    const client = clientFixture({
      rotatePairingTicket: vi.fn(() =>
        Promise.resolve({
          pairingUrl: `https://workos.example/pair#v=1&t=${"A".repeat(43)}&fp=sha256:${"ab".repeat(32)}`,
          tlsFingerprint: `sha256:${"ab".repeat(32)}`,
          expiresAt: new Date(Number.NaN),
        }),
      ),
    });
    render(<DeviceCenter deviceAuth={client} />);

    fireEvent.click(await screen.findByRole("button", { name: "Pair another device" }));
    expect(await screen.findByText(/could not be created/)).toBeTruthy();
    expect(screen.queryByTestId("pairing-ticket")).toBeNull();
  });

  it("reuses one idempotency key when a lost revocation response is retried", async () => {
    const revokeDevice = vi
      .fn<DeviceAuthClient["revokeDevice"]>()
      .mockRejectedValueOnce(new Error("lost response"))
      .mockResolvedValueOnce(undefined);
    vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue(
      "0198d7ea-2110-7c42-b659-c5e4d73bc399",
    );
    render(<DeviceCenter deviceAuth={clientFixture({ revokeDevice })} />);

    const center = await screen.findByTestId("device-center");
    fireEvent.click(await within(center).findByRole("button", { name: "Revoke" }));
    fireEvent.click(within(center).getByRole("button", { name: "Confirm revoke" }));
    await screen.findByText(/may have changed/);
    fireEvent.click(within(center).getByRole("button", { name: "Confirm revoke" }));
    await waitFor(() => {
      expect(revokeDevice).toHaveBeenCalledTimes(2);
    });

    expect(revokeDevice.mock.calls[0]?.[0].idempotencyKey).toBe(
      revokeDevice.mock.calls[1]?.[0].idempotencyKey,
    );
  });
});

function clientFixture(
  overrides: Partial<{
    rotatePairingTicket: DeviceAuthClient["rotatePairingTicket"];
    revokeDevice: DeviceAuthClient["revokeDevice"];
  }> = {},
): DeviceAuthClient {
  const device = {
    deviceId: "0198d7ea-2110-7c42-b659-c5e4d73bc301",
    name: "Fixture phone",
    deviceClass: DeviceClass.PHONE,
    revision: 7n,
    isCurrent: false,
  } as unknown as DeviceInfo;
  return {
    listDevices: vi.fn(() => Promise.resolve({ devices: [device], nextPageToken: "" })),
    getCurrentSession: vi.fn(() => Promise.resolve({ device })),
    rotatePairingTicket:
      overrides.rotatePairingTicket ?? vi.fn(() => Promise.reject(new Error("unused"))),
    revokeDevice: overrides.revokeDevice ?? vi.fn(() => Promise.resolve()),
    logout: vi.fn(() => Promise.resolve()),
    forget: vi.fn(() => Promise.resolve()),
  } as unknown as DeviceAuthClient;
}
