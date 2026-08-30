// The versioned proof transcript encoding, shared byte-for-byte with the Go
// server (internal/gateway/auth/domain/proof.go, ADR-0007):
//
//	ASCII domain separator: "workos.device-proof/v1"
//	purpose byte:           0x01 pairing | 0x02 session
//	then every field:       uint32 big-endian length || raw bytes
//
// The client never signs server-provided opaque bytes: both sides construct
// the same transcript from independently validated facts.

export const PROOF_DOMAIN_SEPARATOR = "workos.device-proof/v1";

export type ProofPurpose = "pairing" | "session";

const PURPOSE_BYTES: Record<ProofPurpose, number> = {
  pairing: 0x01,
  session: 0x02,
};

const PURPOSE_LABELS: Record<ProofPurpose, string> = {
  pairing: "pairing",
  session: "session",
};

export interface ProofFacts {
  publicOrigin: string;
  purpose: ProofPurpose;
  challengeId: string;
  nonce: Uint8Array;
  deviceId: string;
  publicKeyHash: string;
  ticketId?: string;
  tlsFingerprint?: string;
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/;

export function isCanonicalUUIDv7(value: string): boolean {
  return UUID_PATTERN.test(value) && value[14] === "7";
}

export function isCanonicalDigest(value: string): boolean {
  return DIGEST_PATTERN.test(value);
}

function field(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

// encodeProofTranscript validates every field grammar first, then serializes
// the canonical bytes. Any deviation throws: nothing noncanonical is ever
// signed.
export function encodeProofTranscript(facts: ProofFacts): Uint8Array {
  // Runtime mirror of the Go-side unknown-purpose rejection: values cross a
  // process boundary as strings, so the union type is not trusted here.
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  if (facts.purpose !== "pairing" && facts.purpose !== "session") {
    throw new Error("invalid proof purpose");
  }
  if (!facts.publicOrigin) throw new Error("proof origin is empty");
  if (!isCanonicalUUIDv7(facts.challengeId)) throw new Error("proof challenge id grammar");
  if (facts.nonce.length !== 32) throw new Error("proof nonce grammar");
  if (!isCanonicalUUIDv7(facts.deviceId)) throw new Error("proof device id grammar");
  if (!isCanonicalDigest(facts.publicKeyHash)) throw new Error("proof key digest grammar");

  const fields: Uint8Array[] = [
    field(facts.publicOrigin),
    field(PURPOSE_LABELS[facts.purpose]),
    field(facts.challengeId),
    facts.nonce,
    field(facts.deviceId),
    field(facts.publicKeyHash),
  ];
  if (facts.purpose === "pairing") {
    if (!facts.ticketId || !isCanonicalUUIDv7(facts.ticketId)) {
      throw new Error("proof ticket id grammar");
    }
    if (!facts.tlsFingerprint || !isCanonicalDigest(facts.tlsFingerprint)) {
      throw new Error("proof fingerprint grammar");
    }
    fields.push(field(facts.ticketId), field(facts.tlsFingerprint));
  }

  const header = field(PROOF_DOMAIN_SEPARATOR);
  const total = header.length + 1 + fields.reduce((sum, f) => sum + 4 + f.length, 0);
  const buffer = new ArrayBuffer(total);
  const transcript = new Uint8Array(buffer);
  const view = new DataView(buffer);
  transcript.set(header, 0);
  transcript[header.length] = PURPOSE_BYTES[facts.purpose];
  let offset = header.length + 1;
  for (const item of fields) {
    view.setUint32(offset, item.length, false);
    offset += 4;
    transcript.set(item, offset);
    offset += item.length;
  }
  return transcript;
}
