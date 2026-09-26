import { fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  GreenfieldChildEnvelopeSchema,
  type GreenfieldChildEnvelope,
  type GreenfieldWindowInputEvent,
} from "@workos/protocol";

export const CHILD_PROTOCOL_VERSION = 1;
export const MAX_CHILD_RECORD_BYTES = 2_200_000;

export function encodeRecord(envelope: GreenfieldChildEnvelope): Buffer {
  if (envelope.protocolVersion !== CHILD_PROTOCOL_VERSION) throw new Error("IPC_VERSION_INVALID");
  const raw = toBinary(GreenfieldChildEnvelopeSchema, envelope);
  if (raw.byteLength === 0 || raw.byteLength > MAX_CHILD_RECORD_BYTES) {
    throw new Error("IPC_RECORD_TOO_LARGE");
  }
  const record = Buffer.allocUnsafe(raw.byteLength + 4);
  record.writeUInt32BE(raw.byteLength, 0);
  record.set(raw, 4);
  return record;
}

export class RecordReader {
  private buffered: Buffer<ArrayBufferLike> = Buffer.alloc(0);
  constructor(private readonly onEnvelope: (envelope: GreenfieldChildEnvelope) => void) {}

  push(chunk: Buffer): void {
    if (chunk.byteLength === 0) return;
    this.buffered = this.buffered.byteLength === 0 ? chunk : Buffer.concat([this.buffered, chunk]);
    while (this.buffered.byteLength >= 4) {
      const length = this.buffered.readUInt32BE(0);
      if (length === 0 || length > MAX_CHILD_RECORD_BYTES) throw new Error("IPC_RECORD_TOO_LARGE");
      if (this.buffered.byteLength < length + 4) return;
      const payload = this.buffered.subarray(4, 4 + length);
      this.buffered = this.buffered.subarray(4 + length);
      const envelope = fromBinary(GreenfieldChildEnvelopeSchema, payload);
      if (
        envelope.protocolVersion !== CHILD_PROTOCOL_VERSION ||
        envelope.payload.case === undefined
      ) {
        throw new Error("IPC_RECORD_INVALID");
      }
      this.onEnvelope(envelope);
    }
  }
}

// This ledger lives in the resident child, so a Go broker reconnect or a lost
// input ACK cannot cause the same accepted key/text/clipboard action twice.
export class InputSequenceLedger {
  private readonly accepted = new Map<string, { sequence: bigint; fingerprint: string }>();
  private readonly attempted = new Map<string, { sequence: bigint; fingerprint: string }>();

  inspect(
    attachmentId: string,
    generation: bigint,
    event: GreenfieldWindowInputEvent,
    fingerprint: string,
  ): "new" | "duplicate" | "uncertain" | "invalid" {
    const key = `${generation}:${attachmentId}`;
    const last = this.accepted.get(key);
    if (last && event.sequence === last.sequence) {
      return fingerprint === last.fingerprint ? "duplicate" : "invalid";
    }
    const attempted = this.attempted.get(key);
    if (attempted) {
      return event.sequence === attempted.sequence && fingerprint === attempted.fingerprint
        ? "uncertain"
        : "invalid";
    }
    if (!last) return event.sequence === 1n ? "new" : "invalid";
    return event.sequence === last.sequence + 1n ? "new" : "invalid";
  }

  markAttempt(
    attachmentId: string,
    generation: bigint,
    sequence: bigint,
    fingerprint: string,
  ): void {
    this.attempted.set(`${generation}:${attachmentId}`, { sequence, fingerprint });
  }

  accept(attachmentId: string, generation: bigint, sequence: bigint, fingerprint: string): void {
    const key = `${generation}:${attachmentId}`;
    this.accepted.set(key, { sequence, fingerprint });
    this.attempted.delete(key);
  }

  lastApplied(attachmentId: string, generation: bigint): bigint {
    return this.accepted.get(`${generation}:${attachmentId}`)?.sequence ?? 0n;
  }
}
