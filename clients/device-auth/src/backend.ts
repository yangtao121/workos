// Pluggable device-key persistence. The browser profile backend keeps the
// strict non-extractable IndexedDB model; native wrappers can persist the
// key material through platform secure storage (Keychain/Keystore) by
// exporting the generated key once as JWK. The pairing/session proof flows
// and the transcript contract never change — only where the private key
// lives between runs.

import { generateDeviceKeyPair, signTranscript, validateDeviceKeyMaterial } from "./keys.js";
import {
  clearDeviceIdentity,
  loadDeviceIdentity,
  saveDeviceIdentity,
  type StoredDeviceIdentity,
} from "./store.js";

export interface DeviceKeyBackend {
  /** Stable backend id for diagnostics; never a security claim. */
  readonly id: string;
  load(): Promise<StoredDeviceIdentity | undefined>;
  save(identity: StoredDeviceIdentity): Promise<void>;
  clear(): Promise<void>;
  /** Generates a fresh identity for a device about to pair. */
  generate(deviceName: string, deviceClass: string): Promise<StoredDeviceIdentity>;
  /** Proves a loaded identity still matches its stored public material. */
  validate(identity: StoredDeviceIdentity): Promise<boolean>;
}

/** The browser profile backend: non-extractable keys in IndexedDB. */
export const profileDeviceKeyBackend: DeviceKeyBackend = {
  id: "profile-indexeddb",
  load: loadDeviceIdentity,
  save: saveDeviceIdentity,
  clear: clearDeviceIdentity,
  async generate(deviceName, deviceClass) {
    const generated = await generateDeviceKeyPair();
    return {
      privateKey: generated.privateKey,
      publicKeyHash: generated.publicKeyHash,
      publicKeySpki: generated.publicKeySpki,
      deviceName,
      deviceClass,
    };
  },
  validate(identity) {
    return validateDeviceKeyMaterial(
      identity.privateKey,
      identity.publicKeySpki,
      identity.publicKeyHash,
    );
  },
};

/** Minimal platform secure vault the native wrapper must provide. */
export interface SecureVault {
  get(key: string): Promise<string | undefined>;
  set(key: string, value: string): Promise<void>;
  remove(key: string): Promise<void>;
}

interface SecuredIdentityRecord {
  privateJwk: JsonWebKey;
  publicKeySpki: string;
  publicKeyHash: string;
  deviceId?: string | undefined;
  deviceName?: string | undefined;
  deviceClass?: string | undefined;
}

const ALGORITHM: EcKeyGenParams = { name: "ECDSA", namedCurve: "P-256" };

function subtle(): SubtleCrypto {
  const api = (globalThis as { crypto?: Crypto }).crypto?.subtle;
  if (api === undefined) throw new Error("WebCrypto is unavailable in this context");
  return api;
}

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function fromBase64(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (const [index, char] of Array.from(binary).entries()) bytes[index] = char.charCodeAt(0);
  return bytes;
}

// createSecureDeviceKeyBackend persists the device key through the wrapper's
// platform secure storage: the generated extractable key is exported once as
// a JWK and re-imported sign-only on load. This is the honest native-storage
// tradeoff — the vault, not browser structured clone, owns the material.
export function createSecureDeviceKeyBackend(vault: SecureVault, slot: string): DeviceKeyBackend {
  const readRecord = async (): Promise<SecuredIdentityRecord | undefined> => {
    const raw = await vault.get(slot);
    if (raw === undefined) return undefined;
    try {
      return JSON.parse(raw) as SecuredIdentityRecord;
    } catch {
      return undefined;
    }
  };
  return {
    id: "platform-secure-storage",
    async load() {
      const record = await readRecord();
      if (record === undefined) return undefined;
      const privateKey = await subtle().importKey("jwk", record.privateJwk, ALGORITHM, false, [
        "sign",
      ]);
      const identity: StoredDeviceIdentity = {
        privateKey,
        publicKeySpki: fromBase64(record.publicKeySpki),
        publicKeyHash: record.publicKeyHash,
      };
      if (record.deviceId !== undefined) identity.deviceId = record.deviceId;
      if (record.deviceName !== undefined) identity.deviceName = record.deviceName;
      if (record.deviceClass !== undefined) identity.deviceClass = record.deviceClass;
      return identity;
    },
    async save(identity) {
      const existing = await readRecord();
      let privateJwk: JsonWebKey;
      if (identity.privateKey.extractable) {
        privateJwk = await subtle().exportKey("jwk", identity.privateKey);
      } else if (existing !== undefined) {
        privateJwk = existing.privateJwk;
      } else {
        throw new Error("secure backend cannot persist an unexportable unknown key");
      }
      const record: SecuredIdentityRecord = {
        privateJwk,
        publicKeySpki: toBase64(identity.publicKeySpki),
        publicKeyHash: identity.publicKeyHash,
        deviceId: identity.deviceId,
        deviceName: identity.deviceName,
        deviceClass: identity.deviceClass,
      };
      await vault.set(slot, JSON.stringify(record));
    },
    async clear() {
      await vault.remove(slot);
    },
    async generate(deviceName, deviceClass) {
      // Extractable in memory for this session only: the JWK export happens
      // exactly once at save time; every later load re-imports sign-only.
      const pair = await subtle().generateKey(ALGORITHM, true, ["sign", "verify"]);
      const spki = new Uint8Array(await subtle().exportKey("spki", pair.publicKey));
      const digest = await subtle().digest("SHA-256", spki.slice().buffer);
      let hash = "";
      for (const byte of new Uint8Array(digest)) hash += byte.toString(16).padStart(2, "0");
      return {
        privateKey: pair.privateKey,
        publicKeySpki: spki,
        publicKeyHash: `sha256:${hash}`,
        deviceName,
        deviceClass,
      };
    },
    async validate(identity) {
      try {
        const publicKey = await subtle().importKey(
          "spki",
          identity.publicKeySpki.slice().buffer,
          ALGORITHM,
          true,
          ["verify"],
        );
        const canonical = new Uint8Array(await subtle().exportKey("spki", publicKey));
        if (canonical.length !== identity.publicKeySpki.length) return false;
        for (const [index, byte] of canonical.entries()) {
          if (identity.publicKeySpki[index] !== byte) return false;
        }
        const digest = await subtle().digest("SHA-256", identity.publicKeySpki.slice().buffer);
        let hash = "";
        for (const byte of new Uint8Array(digest)) hash += byte.toString(16).padStart(2, "0");
        if (`sha256:${hash}` !== identity.publicKeyHash) return false;
        const message = new TextEncoder().encode("workos.device-key-check/v1");
        const signature = await signTranscript(identity.privateKey, message);
        return await subtle().verify(
          { name: "ECDSA", hash: "SHA-256" },
          publicKey,
          signature.slice().buffer,
          message.slice().buffer,
        );
      } catch {
        return false;
      }
    },
  };
}
