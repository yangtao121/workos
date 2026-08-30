import type { Challenge, DeviceProofPurpose } from "@workos/protocol";

import { isCanonicalUUIDv7 } from "./transcript.js";

const PROOF_VERSION = 1;

// assertProofChallenge rejects downgrade, cross-purpose, malformed, and
// already-expired challenges before the profile key signs any bytes.
export function assertProofChallenge(
  challenge: Challenge,
  purpose: DeviceProofPurpose,
  now = Date.now(),
): void {
  if (challenge.proofVersion !== PROOF_VERSION || challenge.purpose !== purpose) {
    throw new Error("gateway returned an unsupported proof challenge");
  }
  if (!isCanonicalUUIDv7(challenge.challengeId) || challenge.nonce.length !== 32) {
    throw new Error("gateway returned a malformed proof challenge");
  }
  const timestamp = challenge.expiresAt;
  const expiresAt = timestamp
    ? Number(timestamp.seconds) * 1000 + Math.floor(timestamp.nanos / 1_000_000)
    : Number.NaN;
  if (!Number.isFinite(expiresAt) || expiresAt <= now) {
    throw new Error("gateway returned an expired proof challenge");
  }
}
