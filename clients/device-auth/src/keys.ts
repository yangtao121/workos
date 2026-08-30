// Browser profile device key generation and persistence. The private key is
// a NON-EXPORTABLE WebCrypto CryptoKey (usage: sign only) that lives only in
// IndexedDB via structured clone — never in localStorage, sessionStorage,
// React state, files, or any export. If IndexedDB cannot persist the key,
// callers must stop before claiming a ticket; there is no weaker fallback.

export interface DeviceKeyPair {
  // privateKey is non-extractable; it cannot leave this profile.
  privateKey: CryptoKey;
  // publicKeySpki is the canonical P-256 SubjectPublicKeyInfo DER.
  publicKeySpki: Uint8Array;
  // publicKeyHash is "sha256:<64 lowercase hex>" over the SPKI DER.
  publicKeyHash: string;
}

const ALGORITHM: EcKeyGenParams = { name: "ECDSA", namedCurve: "P-256" };

function assertWebCrypto(): SubtleCrypto {
  const subtle = (globalThis as { crypto?: Crypto }).crypto?.subtle;
  if (subtle === undefined) {
    throw new Error("WebCrypto is unavailable in this browser context");
  }
  return subtle;
}

// generateDeviceKeyPair runs two key generations: an exportable verify pair
// provides the SPKI to submit, while a non-exportable sign key becomes the
// profile credential. The private key never has the verify usage, and the
// exported public key never has the sign usage.
export async function generateDeviceKeyPair(): Promise<DeviceKeyPair> {
  const subtle = assertWebCrypto();
  // One generation with extractable=false and both usages: browsers make the
  // private key non-extractable (sign) while the public key of the same pair
  // stays exportable (verify). Splitting into two generations would give one
  // member of each pair empty usages, which browsers reject outright.
  const pair = await subtle.generateKey(ALGORITHM, false, ["sign", "verify"]);
  if (pair.privateKey.extractable) {
    throw new Error("the browser refused to create a non-extractable private key");
  }
  const spki = new Uint8Array(await subtle.exportKey("spki", pair.publicKey));
  const hash = await subtle.digest("SHA-256", spki.buffer);
  const publicKeyHash = `sha256:${hex(new Uint8Array(hash))}`;
  return { privateKey: pair.privateKey, publicKeySpki: spki, publicKeyHash };
}

// signTranscript produces the fixed-width 64-byte raw r||s ECDSA P-256
// signature over the transcript with SHA-256 — never DER.
export async function signTranscript(
  privateKey: CryptoKey,
  transcript: Uint8Array,
): Promise<Uint8Array> {
  const subtle = assertWebCrypto();
  const signature = await subtle.sign(
    { name: "ECDSA", hash: "SHA-256" },
    privateKey,
    transcript.buffer as ArrayBuffer,
  );
  const raw = new Uint8Array(signature);
  if (raw.length !== 64) {
    throw new Error("WebCrypto returned an unexpected signature width");
  }
  return raw;
}

export function hex(bytes: Uint8Array): string {
  let out = "";
  for (const byte of bytes) {
    out += byte.toString(16).padStart(2, "0");
  }
  return out;
}
