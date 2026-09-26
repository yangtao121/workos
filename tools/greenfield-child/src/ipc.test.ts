import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  GreenfieldChildEnvelopeSchema,
  GreenfieldWindowRectSchema,
  GreenfieldWindowSchema,
  GreenfieldWindowSnapshotSchema,
  GreenfieldWindowInputEventSchema,
} from "@workos/protocol";
import { describe, expect, it } from "vitest";
import { CHILD_PROTOCOL_VERSION, encodeRecord, InputSequenceLedger, RecordReader } from "./ipc.js";

const snapshot = create(GreenfieldWindowSnapshotSchema, {
  sessionId: "01997e40-0000-7000-8000-000000000001",
  workloadGeneration: 3n,
  revision: 1n,
});
const envelope = create(GreenfieldChildEnvelopeSchema, {
  protocolVersion: CHILD_PROTOCOL_VERSION,
  sessionId: snapshot.sessionId,
  workloadGeneration: snapshot.workloadGeneration,
  payload: { case: "windows", value: snapshot },
});

describe("child IPC record framing", () => {
  it("reassembles arbitrarily split records and reads adjacent records", () => {
    const decoded: string[] = [];
    const reader = new RecordReader((message) => decoded.push(message.payload.case ?? ""));
    const record = encodeRecord(envelope);
    reader.push(record.subarray(0, 1));
    reader.push(record.subarray(1, 4));
    reader.push(record.subarray(4, 9));
    expect(decoded).toEqual([]);
    reader.push(Buffer.concat([record.subarray(9), record]));
    expect(decoded).toEqual(["windows", "windows"]);
  });

  it("rejects malformed, oversized and wrong-version records", () => {
    const oversized = Buffer.alloc(4);
    oversized.writeUInt32BE(2_200_001);
    expect(() => new RecordReader(() => undefined).push(oversized)).toThrow("IPC_RECORD_TOO_LARGE");
    expect(() => encodeRecord({ ...envelope, protocolVersion: 2 })).toThrow("IPC_VERSION_INVALID");
    const record = encodeRecord(envelope);
    const invalid = Buffer.from(record);
    invalid[4] = 0;
    expect(() => new RecordReader(() => undefined).push(invalid)).toThrow();
  });

  it("encodes nested window facts supplied by the compositor", () => {
    const withWindow = create(GreenfieldWindowSnapshotSchema, {
      ...snapshot,
      windows: [
        create(GreenfieldWindowSchema, {
          id: "01997e40-0000-7000-8000-000000000002",
          title: "Code",
          appId: "code",
          contentRect: create(GreenfieldWindowRectSchema, { x: 20, y: 30, width: 100, height: 80 }),
          visualRect: create(GreenfieldWindowRectSchema, { x: 20, y: 30, width: 100, height: 80 }),
          devicePixelRatioMillis: 1000,
          revision: 1n,
        }),
      ],
    });
    const decoded = fromBinary(
      GreenfieldWindowSnapshotSchema,
      toBinary(GreenfieldWindowSnapshotSchema, withWindow),
    );
    expect(decoded.windows[0]?.contentRect?.width).toBe(100);
  });
});

describe("input replay ledger", () => {
  it("accepts a single sequence once across broker reconnects", () => {
    const ledger = new InputSequenceLedger();
    const first = create(GreenfieldWindowInputEventSchema, { sequence: 1n, windowId: "window-a" });
    expect(ledger.inspect("attachment-a", 3n, first, "event-1")).toBe("new");
    ledger.accept("attachment-a", 3n, 1n, "event-1");
    expect(ledger.inspect("attachment-a", 3n, first, "event-1")).toBe("duplicate");
    expect(ledger.inspect("attachment-a", 3n, first, "changed-event")).toBe("invalid");
    expect(ledger.inspect("attachment-a", 3n, { ...first, sequence: 3n }, "event-3")).toBe(
      "invalid",
    );
    expect(ledger.inspect("attachment-a", 3n, { ...first, sequence: 2n }, "event-2")).toBe("new");
    expect(ledger.lastApplied("attachment-a", 3n)).toBe(1n);
    expect(ledger.inspect("attachment-b", 3n, first, "event-1")).toBe("new");
    expect(ledger.inspect("attachment-a", 4n, first, "event-1")).toBe("new");
  });

  it("does not reapply a potentially partial native action", () => {
    const ledger = new InputSequenceLedger();
    const event = create(GreenfieldWindowInputEventSchema, { sequence: 1n, windowId: "window-a" });
    ledger.markAttempt("attachment-a", 3n, 1n, "event-1");
    expect(ledger.inspect("attachment-a", 3n, event, "event-1")).toBe("uncertain");
    expect(ledger.inspect("attachment-a", 3n, event, "changed-event")).toBe("invalid");
    expect(ledger.lastApplied("attachment-a", 3n)).toBe(0n);
    ledger.accept("attachment-a", 3n, 1n, "event-1");
    expect(ledger.inspect("attachment-a", 3n, event, "event-1")).toBe("duplicate");
  });
});
