import { describe, expect, it } from "vitest";

import { encodeProofTranscript, isCanonicalUUIDv7, type ProofFacts } from "./index.js";

// The cross-language fixture: these bytes must match the Go server's
// encoding exactly (internal/gateway/auth/domain/proof_test.go pins the same
// SHA-256). Any drift on either side breaks pairing proofs.
const pairingFacts: ProofFacts = {
  publicOrigin: "https://workos.example",
  purpose: "pairing",
  challengeId: "0198d7ea-2110-7c42-b659-c5e4d73bc301",
  nonce: new Uint8Array(32).fill(0x14),
  deviceId: "0198d7ea-2110-7c42-b659-c5e4d73bc302",
  publicKeyHash: `sha256:${"ab".repeat(32)}`,
  ticketId: "0198d7ea-2110-7c42-b659-c5e4d73bc303",
  tlsFingerprint: `sha256:${"cd".repeat(32)}`,
};

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", bytes.slice().buffer);
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

describe("proof transcript", () => {
  it("encodes the shared fixture to the pinned vector", async () => {
    const transcript = encodeProofTranscript(pairingFacts);
    expect(transcript.length).toBe(366);
    const header = new TextEncoder().encode("workos.device-proof/v1");
    expect([...transcript.slice(0, header.length)]).toEqual([...header]);
    expect(transcript[header.length]).toBe(0x01);
    expect(await sha256Hex(transcript)).toBe(
      "c857b751ae958ac27c6a0de976d8beb51808d59b8878ec6f85abc56215347713",
    );
  });

  it("omits ticket fields for session proofs", async () => {
    const transcript = encodeProofTranscript({
      publicOrigin: pairingFacts.publicOrigin,
      purpose: "session",
      challengeId: pairingFacts.challengeId,
      nonce: pairingFacts.nonce,
      deviceId: pairingFacts.deviceId,
      publicKeyHash: pairingFacts.publicKeyHash,
    });
    expect(transcript.length).toBeLessThan(366);
    expect(await sha256Hex(transcript)).not.toBe(
      "c857b751ae958ac27c6a0de976d8beb51808d59b8878ec6f85abc56215347713",
    );
  });

  it("rejects every malformed fact", () => {
    const mutations: Array<[string, (facts: ProofFacts) => ProofFacts]> = [
      ["empty origin", (f) => ({ ...f, publicOrigin: "" })],
      ["bad challenge", (f) => ({ ...f, challengeId: "not-a-uuid" })],
      ["short nonce", (f) => ({ ...f, nonce: new Uint8Array(31) })],
      ["bad device", (f) => ({ ...f, deviceId: "0198D7EA-2110-7C42-B659-C5E4D73BC302" })],
      ["bad digest", (f) => ({ ...f, publicKeyHash: "sha256:xyz" })],
      [
        "missing ticket",
        (f) => {
          const copy = { ...f };
          delete copy.ticketId;
          return copy;
        },
      ],
      ["bad fingerprint", (f) => ({ ...f, tlsFingerprint: "md5:aa" })],
      ["non-v7 challenge", (f) => ({ ...f, challengeId: "0198d7ea-2110-6c42-b659-c5e4d73bc301" })],
    ];
    const expectations: Record<string, string> = {
      "empty origin": "origin",
      "missing ticket": "ticket",
    };
    for (const [name, mutate] of mutations) {
      expect(() => encodeProofTranscript(mutate(pairingFacts))).toThrow(
        expectations[name] ?? "grammar",
      );
    }
  });

  it("recognizes canonical UUIDv7 only", () => {
    expect(isCanonicalUUIDv7("0198d7ea-2110-7c42-b659-c5e4d73bc301")).toBe(true);
    expect(isCanonicalUUIDv7("0198d7ea-2110-6c42-b659-c5e4d73bc301")).toBe(false);
    expect(isCanonicalUUIDv7("0198D7EA-2110-7C42-B659-C5E4D73BC301")).toBe(false);
  });
});
