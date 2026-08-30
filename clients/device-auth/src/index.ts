// @workos/device-auth — the trusted Desktop Shell's device authentication
// client. It owns the browser profile key (non-extractable WebCrypto, only
// in IndexedDB), the pairing/session proof flows, and the device management
// calls. None of these APIs may be re-exported through @workos/app-sdk,
// @workos/surface-sdk, or any App Bridge surface: sandboxed app surfaces
// have no access to the device credential.

import { Code, ConnectError } from "@connectrpc/connect";
import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  DevicePairingService,
  DeviceProofPurpose,
  DeviceService,
  type DeviceInfo,
} from "@workos/protocol";

import { assertProofChallenge } from "./challenge.js";
import { encodeProofTranscript, isCanonicalUUIDv7, type ProofFacts } from "./transcript.js";
import { generateDeviceKeyPair, signTranscript, validateDeviceKeyMaterial } from "./keys.js";
import {
  clearDeviceIdentity,
  loadDeviceIdentity,
  saveDeviceIdentity,
  type StoredDeviceIdentity,
} from "./store.js";

export {
  encodeProofTranscript,
  isCanonicalDigest,
  isCanonicalUUIDv7,
  type ProofFacts,
} from "./transcript.js";
export { generateDeviceKeyPair, signTranscript, validateDeviceKeyMaterial } from "./keys.js";
export { clearDeviceIdentity, loadDeviceIdentity, saveDeviceIdentity } from "./store.js";
export type { DeviceInfo } from "@workos/protocol";

export type DeviceClass = "desktop" | "tablet" | "foldable" | "phone";

export interface PairingFragment {
  version: number;
  secret: string;
  tlsFingerprint: string;
}

// parsePairingFragment applies the strict URL-fragment grammar the Gateway
// prints into the QR code: #v=1&t=<43-char base64url secret>&fp=sha256:<hex>.
// isAuthRequiredDeployment distinguishes a production gateway (which serves
// the device auth endpoints and answers with real Connect codes) from a
// development-bypass gateway (which does not serve them at all). The probe
// sends a grammar-invalid request: the production service rejects it with
// InvalidArgument before any state is touched; an absent route surfaces as
// an unknown-code error.
export async function isAuthRequiredDeployment(
  client: Client<typeof DevicePairingService>,
): Promise<boolean> {
  try {
    await client.beginDeviceSession({ deviceId: "not-a-device-id" });
    return true;
  } catch (error) {
    if (error instanceof ConnectError && error.code === Code.InvalidArgument) {
      return true;
    }
    return false;
  }
}

export function parsePairingFragment(fragment: string): PairingFragment {
  const params = new URLSearchParams(fragment.startsWith("#") ? fragment.slice(1) : fragment);
  if (params.get("v") !== "1") throw new Error("unsupported pairing URL version");
  const secret = params.get("t") ?? "";
  const fingerprint = params.get("fp") ?? "";
  if (secret.length !== 43 || /[^A-Za-z0-9_-]/.test(secret)) {
    throw new Error("invalid pairing secret grammar");
  }
  if (!/^sha256:[0-9a-f]{64}$/.test(fingerprint)) {
    throw new Error("invalid pairing fingerprint grammar");
  }
  return { version: 1, secret, tlsFingerprint: fingerprint };
}

export interface PairWithTicketInput {
  secret: string;
  tlsFingerprint: string;
  deviceName: string;
  deviceClass: DeviceClass;
}

export interface RevokeDeviceInput {
  deviceId: string;
  idempotencyKey: string;
  expectedRevision: bigint;
}

// isUnavailable classifies transient infrastructure failures so the Auth
// Gate can offer a retry instead of the unpaired screen.
export function isUnavailable(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.Unavailable;
}

export class DeviceAuthClient {
  private readonly pairing: Client<typeof DevicePairingService>;
  private readonly devices: Client<typeof DeviceService>;

  // pairingClient exposes the generated client for capability probes only.
  get pairingClient(): Client<typeof DevicePairingService> {
    return this.pairing;
  }

  constructor(baseUrl: string, transport?: Transport) {
    const active = transport ?? createConnectTransport({ baseUrl });
    this.pairing = createClient(DevicePairingService, active);
    this.devices = createClient(DeviceService, active);
  }

  // canonicalOrigin is the origin the Gateway validates Host/Origin against.
  private get canonicalOrigin(): string {
    return window.location.origin;
  }

  // ensureProfileKey loads the stored profile key or creates and persists a
  // new one BEFORE any ticket is claimed: if IndexedDB fails, pairing stops.
  private async ensureProfileKey(
    deviceName: string,
    deviceClass: DeviceClass,
  ): Promise<StoredDeviceIdentity> {
    const existing = await loadDeviceIdentity();
    if (existing !== undefined && (await isWellFormedIdentity(existing))) {
      return existing;
    }
    const generated = await generateDeviceKeyPair();
    const identity: StoredDeviceIdentity = {
      privateKey: generated.privateKey,
      publicKeyHash: generated.publicKeyHash,
      publicKeySpki: generated.publicKeySpki,
      deviceName,
      deviceClass,
    };
    await saveDeviceIdentity(identity);
    // Read back the committed structured clone and prove the private/public
    // binding before the first network call can claim a ticket.
    const persisted = await loadDeviceIdentity();
    if (persisted === undefined || !(await isWellFormedIdentity(persisted))) {
      throw new Error("IndexedDB did not preserve the device credential");
    }
    return persisted;
  }

  private pairingFacts(
    challengeId: string,
    nonce: Uint8Array,
    deviceId: string,
    publicKeyHash: string,
    ticketId: string,
    tlsFingerprint: string,
  ): ProofFacts {
    return {
      publicOrigin: this.canonicalOrigin,
      purpose: "pairing",
      challengeId,
      nonce,
      deviceId,
      publicKeyHash,
      ticketId,
      tlsFingerprint,
    };
  }

  // pairWithTicket runs the full pairing flow: profile key first, then the
  // claim, then the canonical proof. The server sets the __Host- session
  // cookie on the completion response.
  async pairWithTicket(input: PairWithTicketInput): Promise<DeviceInfo> {
    const identity = await this.ensureProfileKey(input.deviceName, input.deviceClass);
    const begin = await this.pairing.beginPairing({
      pairingSecret: input.secret,
      publicKeySpki: this.publicKeySpkiOf(identity),
      deviceName: input.deviceName,
      deviceClass: deviceClassToProto(input.deviceClass),
    });
    const deviceId = begin.deviceId;
    const challenge = begin.challenge;
    if (!deviceId || !challenge)
      throw new Error("gateway returned an incomplete pairing challenge");
    assertProofChallenge(challenge, DeviceProofPurpose.PAIRING);
    if (!isCanonicalUUIDv7(deviceId) || !isCanonicalUUIDv7(begin.ticketId)) {
      throw new Error("gateway returned a malformed pairing binding");
    }
    // Persist the pending binding before proving, so a lost completion
    // response can be recovered through the session proof.
    await saveDeviceIdentity({
      ...identity,
      deviceId,
      deviceName: input.deviceName,
      deviceClass: input.deviceClass,
    });

    // The transcript mirrors the server exactly: the ticket id comes from
    // the begin response, binding the signature to the ticket snapshot.
    const facts = this.pairingFacts(
      challenge.challengeId,
      new Uint8Array(challenge.nonce),
      deviceId,
      identity.publicKeyHash,
      begin.ticketId,
      input.tlsFingerprint,
    );
    const transcript = encodeProofTranscript(facts);
    const signature = await signTranscript(identity.privateKey, transcript);
    const completion = await this.pairing.completePairing({
      deviceId,
      challengeId: challenge.challengeId,
      signature: new Uint8Array(signature),
      publicKeySpki: this.publicKeySpkiOf(identity),
    });
    if (!completion.device) throw new Error("gateway returned an incomplete pairing result");
    return completion.device;
  }

  // restoreSession asks the Gateway whether the current cookie already
  // authenticates this browser.
  async restoreSession(): Promise<DeviceInfo | undefined> {
    try {
      const response = await this.devices.getCurrentDevice({});
      return response.device ?? undefined;
    } catch (error) {
      if (
        error instanceof ConnectError &&
        (error.code === Code.Unauthenticated || error.code === Code.NotFound)
      ) {
        return undefined;
      }
      throw error;
    }
  }

  // reauthenticate proves the stored profile key to mint a fresh session
  // cookie after expiry or logout. This is authentication only — business
  // operations are never replayed automatically.
  async reauthenticate(): Promise<DeviceInfo> {
    const identity = await loadDeviceIdentity();
    if (
      identity === undefined ||
      !identity.deviceId ||
      !isCanonicalUUIDv7(identity.deviceId) ||
      !(await isWellFormedIdentity(identity))
    ) {
      throw new Error("this browser has no device credential to prove");
    }
    const begin = await this.pairing.beginDeviceSession({ deviceId: identity.deviceId });
    const challenge = begin.challenge;
    if (!challenge) throw new Error("gateway returned an incomplete session challenge");
    assertProofChallenge(challenge, DeviceProofPurpose.SESSION);
    const facts: ProofFacts = {
      publicOrigin: this.canonicalOrigin,
      purpose: "session",
      challengeId: challenge.challengeId,
      nonce: new Uint8Array(challenge.nonce),
      deviceId: identity.deviceId,
      publicKeyHash: identity.publicKeyHash,
    };
    const signature = await signTranscript(identity.privateKey, encodeProofTranscript(facts));
    const completion = await this.pairing.completeDeviceSession({
      deviceId: identity.deviceId,
      challengeId: challenge.challengeId,
      signature: new Uint8Array(signature),
    });
    if (!completion.device) throw new Error("gateway returned an incomplete session result");
    return completion.device;
  }

  async getCurrentDevice(): Promise<DeviceInfo> {
    const response = await this.devices.getCurrentDevice({});
    if (!response.device) throw new Error("gateway returned no device");
    return response.device;
  }

  // getCurrentSession returns the current device with the absolute session
  // expiry for the Device Center display.
  async getCurrentSession(): Promise<{ device: DeviceInfo; sessionExpiresAt?: Date }> {
    const response = await this.devices.getCurrentDevice({});
    if (!response.device) throw new Error("gateway returned no device");
    const sessionExpiresAt = timestampToDate(response.sessionExpiresAt);
    return sessionExpiresAt
      ? { device: response.device, sessionExpiresAt }
      : { device: response.device };
  }

  async listDevices(
    pageSize?: number,
    pageToken?: string,
  ): Promise<{ devices: DeviceInfo[]; nextPageToken: string }> {
    const response = await this.devices.listDevices({
      pageSize: pageSize ?? 0,
      pageToken: pageToken ?? "",
    });
    return { devices: response.devices, nextPageToken: response.nextPageToken };
  }

  async rotatePairingTicket(): Promise<{
    pairingUrl: string;
    tlsFingerprint: string;
    expiresAt?: Date;
  }> {
    const response = await this.devices.rotatePairingTicket({});
    const ticket = response.ticket;
    if (!ticket) throw new Error("gateway returned no pairing ticket");
    const expiresAt = timestampToDate(ticket.expiresAt);
    return expiresAt
      ? { pairingUrl: ticket.pairingUrl, tlsFingerprint: ticket.tlsFingerprint, expiresAt }
      : { pairingUrl: ticket.pairingUrl, tlsFingerprint: ticket.tlsFingerprint };
  }

  async revokeDevice(input: RevokeDeviceInput): Promise<void> {
    await this.devices.revokeDevice({
      deviceId: input.deviceId,
      idempotencyKey: input.idempotencyKey,
      expectedRevision: input.expectedRevision,
    });
  }

  // logout revokes only the current session; the profile key survives so a
  // later reauthenticate() works.
  async logout(): Promise<void> {
    await this.devices.logout({});
  }

  // forget is the explicit "Forget this browser" action: logout first, then
  // delete the IndexedDB credential. It never runs during a transient
  // outage.
  async forget(): Promise<void> {
    try {
      await this.logout();
    } catch (error) {
      if (!isAuthError(error)) throw error;
    }
    await clearDeviceIdentity();
  }

  private publicKeySpkiOf(identity: StoredDeviceIdentity): Uint8Array {
    // The SPKI is public material persisted alongside the private key; the
    // private key itself never leaves IndexedDB.
    if (identity.publicKeySpki.length === 0) {
      throw new Error("the stored device credential has no public key material");
    }
    return identity.publicKeySpki;
  }
}

// isWellFormedIdentity tolerates records written by older builds or partial
// writes, and re-verifies the loaded private key's contract before anything
// relies on it: non-extractable ECDSA over P-256 with sign-only usage,
// matching exportable verify material. Pairing regenerates malformed state
// before claiming a ticket; re-authentication rejects it before networking.
async function isWellFormedIdentity(identity: StoredDeviceIdentity | undefined): Promise<boolean> {
  if (
    identity === undefined ||
    !(identity.publicKeySpki instanceof Uint8Array) ||
    typeof identity.publicKeyHash !== "string"
  ) {
    return false;
  }
  return validateDeviceKeyMaterial(
    identity.privateKey,
    identity.publicKeySpki,
    identity.publicKeyHash,
  );
}

function isAuthError(error: unknown): boolean {
  return (
    error instanceof ConnectError &&
    (error.code === Code.Unauthenticated || error.code === Code.NotFound)
  );
}

// timestampToDate converts the well-known Timestamp shape without reaching
// into generated internals.
function timestampToDate(
  timestamp: { seconds: bigint; nanos: number } | undefined,
): Date | undefined {
  if (!timestamp) return undefined;
  return new Date(Number(timestamp.seconds) * 1000 + Math.floor(timestamp.nanos / 1_000_000));
}

function deviceClassToProto(deviceClass: DeviceClass): number {
  switch (deviceClass) {
    case "desktop":
      return 1;
    case "tablet":
      return 2;
    case "foldable":
      return 3;
    case "phone":
      return 4;
    default:
      return 0;
  }
}
