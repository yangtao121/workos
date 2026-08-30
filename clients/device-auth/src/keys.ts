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
const HASH_PATTERN = /^sha256:[0-9a-f]{64}$/;
const KEY_CHECK_MESSAGE = new TextEncoder().encode("workos.device-key-check/v1");

function assertWebCrypto(): SubtleCrypto {
  const subtle = (globalThis as { crypto?: Crypto }).crypto?.subtle;
  if (subtle === undefined) {
    throw new Error("WebCrypto is unavailable in this browser context");
  }
  return subtle;
}

// generateDeviceKeyPair creates one P-256 pair: its private member is the
// non-exportable sign-only profile credential, while its public member is
// exportable verify-only material whose SPKI can be submitted.
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

// validateDeviceKeyMaterial proves that an IndexedDB round-tripped private
// key is still the exact non-exportable P-256 signing key paired with the
// stored canonical SPKI and digest. Shape checks alone are insufficient: a
// partially-written record could otherwise mix two valid but unrelated
// keys and fail only after a ticket had already been claimed.
export async function validateDeviceKeyMaterial(
  privateKey: CryptoKey,
  publicKeySpki: Uint8Array,
  expectedPublicKeyHash: string,
): Promise<boolean> {
  const cryptoKeyConstructor = (globalThis as { CryptoKey?: typeof CryptoKey }).CryptoKey;
  if (
    cryptoKeyConstructor === undefined ||
    !(privateKey instanceof cryptoKeyConstructor) ||
    !(publicKeySpki instanceof Uint8Array) ||
    publicKeySpki.length === 0 ||
    publicKeySpki.length > 256 ||
    !HASH_PATTERN.test(expectedPublicKeyHash)
  ) {
    return false;
  }
  const algorithm = privateKey.algorithm as EcKeyAlgorithm;
  if (
    privateKey.extractable ||
    algorithm.name !== "ECDSA" ||
    algorithm.namedCurve !== "P-256" ||
    privateKey.usages.length !== 1 ||
    privateKey.usages[0] !== "sign"
  ) {
    return false;
  }

  try {
    const subtle = assertWebCrypto();
    const spkiBuffer = publicKeySpki.slice().buffer;
    const publicKey = await subtle.importKey("spki", spkiBuffer, ALGORITHM, true, ["verify"]);
    const canonical = new Uint8Array(await subtle.exportKey("spki", publicKey));
    if (!equalBytes(canonical, publicKeySpki)) return false;
    const digest = new Uint8Array(await subtle.digest("SHA-256", spkiBuffer));
    if (`sha256:${hex(digest)}` !== expectedPublicKeyHash) return false;
    const signature = await signTranscript(privateKey, KEY_CHECK_MESSAGE);
    return await subtle.verify(
      { name: "ECDSA", hash: "SHA-256" },
      publicKey,
      signature.slice().buffer,
      KEY_CHECK_MESSAGE.slice().buffer,
    );
  } catch {
    return false;
  }
}

function equalBytes(left: Uint8Array, right: Uint8Array): boolean {
  if (left.length !== right.length) return false;
  let difference = 0;
  for (const [index, leftByte] of left.entries()) {
    const rightByte = right[index];
    if (rightByte === undefined) return false;
    difference |= leftByte ^ rightByte;
  }
  return difference === 0;
}

export function hex(bytes: Uint8Array): string {
  let out = "";
  for (const byte of bytes) {
    out += byte.toString(16).padStart(2, "0");
  }
  return out;
}
