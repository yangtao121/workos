import { describe, expect, it, vi } from "vitest";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { DevicePairingService, DeviceService, DeviceProofPurpose } from "@workos/protocol";
import {
  createSecureDeviceKeyBackend,
  encodeProofTranscript,
  type SecureVault,
} from "@workos/device-auth";
import { createMobileAuth, DEVICE_IDENTITY_SLOT } from "./auth.js";

const state = vi.hoisted(() => ({ transport: undefined as Transport | undefined }));
vi.mock("./transport.js", () => ({ createMobileTransport: () => state.transport }));
const origin = "https://gateway.example";
const deviceId = "0198d7ea-2110-7c42-b659-c5e4d73bc301";
const challengeId = "0198d7ea-2110-7c42-b659-c5e4d73bc302";

async function fixture() {
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
  const backend = createSecureDeviceKeyBackend(vault, `${DEVICE_IDENTITY_SLOT}:${origin}`);
  await backend.save({ ...(await backend.generate("phone", "phone")), deviceId });
  return { vault, backend, records };
}

describe("mobile authentication", () => {
  it("restores an expired cookie using the native key and deployment origin transcript", async () => {
    const { vault, backend } = await fixture();
    const identity = required(await backend.load());
    let proofs = 0;
    state.transport = createRouterTransport((router) => {
      router.service(DeviceService, {
        getCurrentDevice() {
          throw new ConnectError("expired", Code.Unauthenticated);
        },
      });
      router.service(DevicePairingService, {
        beginDeviceSession(request) {
          expect(request.deviceId).toBe(deviceId);
          return {
            challenge: {
              challengeId,
              nonce: new Uint8Array(32),
              purpose: DeviceProofPurpose.SESSION,
              proofVersion: 1,
              expiresAt: { seconds: 2_000_000_000n, nanos: 0 },
            },
          };
        },
        async completeDeviceSession(request) {
          const publicKey = await crypto.subtle.importKey(
            "spki",
            identity.publicKeySpki.slice().buffer,
            { name: "ECDSA", namedCurve: "P-256" },
            false,
            ["verify"],
          );
          const transcript = encodeProofTranscript({
            publicOrigin: origin,
            purpose: "session",
            challengeId,
            nonce: new Uint8Array(32),
            deviceId,
            publicKeyHash: identity.publicKeyHash,
          });
          expect(
            await crypto.subtle.verify(
              { name: "ECDSA", hash: "SHA-256" },
              publicKey,
              request.signature.slice().buffer,
              transcript.slice().buffer,
            ),
          ).toBe(true);
          proofs++;
          return { device: { deviceId } };
        },
      });
    });
    const auth = createMobileAuth(origin, vault);
    const [first, second] = await Promise.all([auth.begin(undefined), auth.begin(undefined)]);
    expect(first.phase).toBe("paired");
    expect(second.phase).toBe("paired");
    expect(proofs).toBe(1);
  });
  it("serializes and deduplicates Forget after an in-flight session restore", async () => {
    const { vault, records } = await fixture();
    let release: (() => void) | undefined;
    const restoring = new Promise<void>((resolve) => {
      release = resolve;
    });
    let restores = 0;
    let logouts = 0;
    state.transport = createRouterTransport((router) => {
      router.service(DeviceService, {
        async getCurrentDevice() {
          restores++;
          await restoring;
          return { device: { deviceId } };
        },
        logout() {
          logouts++;
          return {};
        },
      });
    });
    const auth = createMobileAuth(origin, vault);
    const begin = auth.begin(undefined);
    await vi.waitFor(() => {
      expect(restores).toBe(1);
    });
    const first = auth.logout();
    const second = auth.logout();
    expect(first).toBe(second);
    expect(logouts).toBe(0);
    const duringForget = auth.begin(undefined);
    required(release)();
    await begin;
    expect((await first).phase).toBe("unpaired");
    expect((await duringForget).phase).toBe("unpaired");
    expect(logouts).toBe(1);
    expect(restores).toBe(1);
    expect(records.size).toBe(0);
  });
  it("does not report forgotten or delete the key when logout fails", async () => {
    const { vault, records } = await fixture();
    state.transport = createRouterTransport((router) => {
      router.service(DeviceService, {
        logout() {
          throw new ConnectError("outage", Code.Unavailable);
        },
      });
    });
    expect((await createMobileAuth(origin, vault).logout()).phase).toBe("unavailable");
    expect(records.size).toBe(1);
  });
});

function required<T>(value: T | undefined): T {
  if (value === undefined) throw new Error("fixture value missing");
  return value;
}
