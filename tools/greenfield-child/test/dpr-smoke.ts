// Manual resident Code proof for a DPR-only resize over the private child IPC.
import { create } from "@bufbuild/protobuf";
import { createServer, type Socket } from "node:net";
import { writeFile } from "node:fs/promises";
import {
  GreenfieldChildEnvelopeSchema,
  GreenfieldInputVerdict,
  GreenfieldWindowInputEventSchema,
  GreenfieldWindowResizeSchema,
  SendGreenfieldWindowInputRequestSchema,
  type GreenfieldChildEnvelope,
} from "@workos/protocol";
import { CHILD_PROTOCOL_VERSION, encodeRecord, RecordReader } from "../src/ipc.js";

const [socketPath, resultPath, sessionId, generationText, attachmentId] = process.argv.slice(2);
if (!socketPath || !resultPath || !sessionId || !generationText || !attachmentId) {
  throw new Error("usage: dpr-smoke <socket> <result> <session-id> <generation> <attachment-id>");
}
const generation = BigInt(generationText);
const server = createServer();
let socket: Socket | undefined;
let windowId = "";
let initialRevision = 0n;
let initialWidth = 0;
let initialHeight = 0;
let initialContentWidth = 0;
let initialContentHeight = 0;
let requested = false;
let acknowledged = false;
let updatedRevision = 0n;
let updatedDpr = 0;
let updatedFrameWidth = 0;
let updatedFrameHeight = 0;
let updatedFrameTiles = 0;
let completed = false;
let finish!: (error?: Error) => void;
const done = new Promise<void>((resolve, reject) => {
  finish = (error) => {
    if (completed) return;
    completed = true;
    if (error) reject(error);
    else resolve();
  };
});

function sendResize(connected: Socket): void {
  const resize = create(GreenfieldWindowResizeSchema, {
    contentWidth: initialContentWidth,
    contentHeight: initialContentHeight,
    devicePixelRatioMillis: 2000,
  });
  const event = create(GreenfieldWindowInputEventSchema, {
    sequence: 1n,
    windowId,
    event: { case: "resize", value: resize },
  });
  const request = create(SendGreenfieldWindowInputRequestSchema, {
    sessionId,
    attachmentId,
    expectedWorkloadGeneration: generation,
    controlGeneration: 1n,
    events: [event],
  });
  const envelope = create(GreenfieldChildEnvelopeSchema, {
    protocolVersion: CHILD_PROTOCOL_VERSION,
    sessionId,
    workloadGeneration: generation,
    requestId: 1n,
    payload: { case: "input", value: request },
  });
  connected.write(encodeRecord(envelope));
}

function handle(connected: Socket, envelope: GreenfieldChildEnvelope): void {
  if (envelope.sessionId !== sessionId || envelope.workloadGeneration !== generation) {
    finish(new Error("child identity mismatch"));
    return;
  }
  if (envelope.payload.case === "failure") {
    finish(new Error(`child failure: ${envelope.payload.value.reasonCode}`));
    return;
  }
  if (envelope.payload.case === "windows") {
    const window = envelope.payload.value.windows.at(0);
    if (!window) return;
    if (!windowId) {
      windowId = window.id;
      initialRevision = window.revision;
      initialContentWidth = window.contentRect?.width ?? 0;
      initialContentHeight = window.contentRect?.height ?? 0;
    }
    if (
      window.id === windowId &&
      window.devicePixelRatioMillis === 2000 &&
      window.revision > initialRevision
    ) {
      updatedDpr = window.devicePixelRatioMillis;
      updatedRevision = window.revision;
    }
    return;
  }
  if (envelope.payload.case === "inputResult") {
    if (
      envelope.requestId !== 1n ||
      envelope.payload.value.verdict !== GreenfieldInputVerdict.APPLIED
    ) {
      finish(new Error("DPR resize was not applied"));
      return;
    }
    acknowledged = true;
  }
  if (envelope.payload.case === "frameTile") {
    const frame = envelope.payload.value;
    if (frame.windowId !== windowId || !frame.fullRefresh) return;
    if (!requested) {
      if (frame.tileIndex + 1 !== frame.tileCount) return;
      initialWidth = frame.frameWidth;
      initialHeight = frame.frameHeight;
      if (!initialContentWidth || !initialContentHeight) {
        finish(new Error("initial Code geometry unavailable"));
        return;
      }
      requested = true;
      sendResize(connected);
      return;
    }
    if (
      updatedRevision > initialRevision &&
      frame.windowRevision >= updatedRevision &&
      frame.frameWidth > initialWidth * 1.5 &&
      frame.frameHeight > initialHeight * 1.5
    ) {
      updatedFrameTiles++;
      if (frame.tileIndex + 1 === frame.tileCount) {
        updatedFrameWidth = frame.frameWidth;
        updatedFrameHeight = frame.frameHeight;
      }
    }
  }
  if (acknowledged && updatedDpr === 2000 && updatedFrameWidth && updatedFrameHeight) finish();
}

server.on("connection", (connected) => {
  socket = connected;
  const reader = new RecordReader((envelope) => {
    handle(connected, envelope);
  });
  connected.on("data", (chunk: Buffer) => {
    try {
      reader.push(chunk);
    } catch (error) {
      finish(error as Error);
    }
  });
});
server.listen(socketPath, () => {
  process.stdout.write("dpr-broker-listening\n");
});
const timer = setTimeout(() => {
  finish(new Error("DPR probe timed out"));
}, 120_000);
await done.finally(() => {
  clearTimeout(timer);
  socket?.destroy();
  server.close();
});
const result = {
  windowId,
  initialRevision: initialRevision.toString(),
  initialWidth,
  initialHeight,
  updatedRevision: updatedRevision.toString(),
  updatedDpr,
  updatedFrameWidth,
  updatedFrameHeight,
  updatedFrameTiles,
  acknowledged,
};
await writeFile(resultPath, JSON.stringify(result, null, 2));
process.stdout.write(`${JSON.stringify(result)}\n`);
