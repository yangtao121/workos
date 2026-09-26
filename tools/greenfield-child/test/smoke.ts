// Manual GPU integration proof. Run against one networkless child container;
// this broker fixture never handles user credentials or public sessions.
import { create } from "@bufbuild/protobuf";
import { createServer, type Socket } from "node:net";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  GreenfieldChildEnvelopeSchema,
  GreenfieldWindowFocusSchema,
  GreenfieldWindowInputEventSchema,
  SendGreenfieldWindowInputRequestSchema,
  type GreenfieldChildEnvelope,
} from "@workos/protocol";
import { CHILD_PROTOCOL_VERSION, encodeRecord, RecordReader } from "../src/ipc.js";

const [socketPath, evidenceDir, sessionId, generationText] = process.argv.slice(2);
if (!socketPath || !evidenceDir || !sessionId || !generationText) {
  throw new Error("usage: smoke.mjs <socket> <evidence-dir> <session-id> <generation>");
}
const generation = BigInt(generationText);
const attachmentId = "01997e40-0000-7000-8000-000000000001";
const server = createServer();
let connections = 0;
let firstWindow = "";
let firstFrameSequence = 0n;
let firstFrameTiles = 0;
let secondFrameTiles = 0;
let sawInputResult = false;
let firstFrameFull = false;
let secondFrameFull = false;
let firstFrameLatencyMs = 0;
let listeningAt = 0;
const firstTiles: Array<{ index: number; x: number; y: number; width: number; height: number }> =
  [];
let timer: NodeJS.Timeout;
let finish!: (error?: Error) => void;
const done = new Promise<void>((resolve, reject) => {
  finish = (error) => {
    if (error) reject(error);
    else resolve();
  };
});
const summarize = () => ({
  connections,
  firstWindow,
  firstFrameSequence: firstFrameSequence.toString(),
  firstFrameTiles,
  secondFrameTiles,
  firstFrameFull,
  secondFrameFull,
  firstFrameLatencyMs,
  sawInputResult,
  firstTiles,
});

function sendFocus(socket: Socket): void {
  const event = create(GreenfieldWindowInputEventSchema, {
    sequence: 1n,
    windowId: firstWindow,
    event: { case: "focus", value: create(GreenfieldWindowFocusSchema) },
  });
  const input = create(SendGreenfieldWindowInputRequestSchema, {
    sessionId,
    attachmentId,
    expectedWorkloadGeneration: generation,
    controlGeneration: 1n,
    events: [event],
  });
  socket.write(
    encodeRecord(
      create(GreenfieldChildEnvelopeSchema, {
        protocolVersion: CHILD_PROTOCOL_VERSION,
        sessionId,
        workloadGeneration: generation,
        requestId: 1n,
        payload: { case: "input", value: input },
      }),
    ),
  );
}

async function handle(socket: Socket, envelope: GreenfieldChildEnvelope): Promise<void> {
  if (envelope.sessionId !== sessionId || envelope.workloadGeneration !== generation) {
    throw new Error("child identity mismatch");
  }
  if (envelope.payload.case === "failure") {
    throw new Error(`child failure: ${envelope.payload.value.reasonCode}`);
  }
  if (envelope.payload.case === "windows") {
    const windows = envelope.payload.value.windows;
    if (windows.length && !firstWindow) firstWindow = windows[0]!.id;
    if (connections === 2 && windows.length && windows[0]!.id !== firstWindow) {
      throw new Error("window identity changed across broker reconnect");
    }
    return;
  }
  if (envelope.payload.case === "inputResult") {
    if (envelope.requestId !== 1n || envelope.payload.value.verdict !== 1) {
      throw new Error("native focus input not applied");
    }
    sawInputResult = true;
    if (secondFrameFull && secondFrameTiles > 0) finish();
    return;
  }
  if (envelope.payload.case !== "frameTile") return;
  const tile = envelope.payload.value;
  if (tile.windowId !== firstWindow || !tile.fullRefresh) return;
  if (connections === 1) {
    firstFrameFull = true;
    firstFrameTiles++;
    firstTiles.push({
      index: tile.tileIndex,
      x: tile.x,
      y: tile.y,
      width: tile.width,
      height: tile.height,
    });
    await mkdir(evidenceDir, { recursive: true });
    await writeFile(join(evidenceDir, `tile-${tile.tileIndex}-${tile.x}-${tile.y}.png`), tile.png);
    if (firstFrameTiles === 1) {
      await writeFile(join(evidenceDir, "first-native-tile.png"), tile.png);
      firstFrameSequence = tile.frameSequence;
      firstFrameLatencyMs = Date.now() - listeningAt;
    }
    if (tile.tileIndex + 1 === tile.tileCount) {
      socket.destroy();
    }
    return;
  }
  if (tile.frameSequence <= firstFrameSequence) throw new Error("frame sequence did not advance");
  secondFrameFull = true;
  secondFrameTiles++;
  if (tile.tileIndex + 1 === tile.tileCount) {
    sendFocus(socket);
  }
}

server.on("connection", (socket) => {
  connections++;
  let chain = Promise.resolve();
  const reader = new RecordReader((envelope) => {
    chain = chain
      .then(() => handle(socket, envelope))
      .catch((error: unknown) => finish(error as Error));
  });
  socket.on("data", (chunk: Buffer) => {
    try {
      reader.push(chunk);
    } catch (error) {
      finish(error as Error);
    }
  });
});
server.listen(socketPath, () => {
  listeningAt = Date.now();
  process.stdout.write("broker-listening\n");
  timer = setTimeout(() => finish(new Error("child smoke timed out")), 120_000);
});
await done.finally(() => {
  clearTimeout(timer);
  server.close();
});
await writeFile(join(evidenceDir, "smoke.json"), JSON.stringify(summarize(), null, 2));
process.stdout.write(`${JSON.stringify(summarize())}\n`);
