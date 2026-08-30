import { DeviceProofPurpose, type Challenge } from "@workos/protocol";
import { describe, expect, it } from "vitest";

import { assertProofChallenge } from "./challenge.js";

const valid = {
  challengeId: "0198d7ea-2110-7c42-b659-c5e4d73bc301",
  nonce: new Uint8Array(32),
  expiresAt: { seconds: 2_000_000_000n, nanos: 0 },
  proofVersion: 1,
  purpose: DeviceProofPurpose.PAIRING,
} as unknown as Challenge;

describe("proof challenge negotiation", () => {
  it("accepts only version 1 with the expected explicit purpose", () => {
    expect(() => {
      assertProofChallenge(valid, DeviceProofPurpose.PAIRING, 1);
    }).not.toThrow();
    expect(() => {
      assertProofChallenge(
        { ...valid, proofVersion: 0 } as Challenge,
        DeviceProofPurpose.PAIRING,
        1,
      );
    }).toThrow("unsupported proof challenge");
    expect(() => {
      assertProofChallenge(
        { ...valid, proofVersion: 2 } as Challenge,
        DeviceProofPurpose.PAIRING,
        1,
      );
    }).toThrow("unsupported proof challenge");
    expect(() => {
      assertProofChallenge(valid, DeviceProofPurpose.SESSION, 1);
    }).toThrow("unsupported proof challenge");
  });
});
