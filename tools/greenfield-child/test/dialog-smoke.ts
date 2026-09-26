// Manual native Open File dialog probe after menu-smoke leaves the File menu open.
import { create } from "@bufbuild/protobuf";
import { createServer, type Socket } from "node:net";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  GreenfieldChildEnvelopeSchema,
  GreenfieldPointerAction,
  GreenfieldPointerInputSchema,
  GreenfieldWindowInputEventSchema,
  SendGreenfieldWindowInputRequestSchema,
  type GreenfieldChildEnvelope,
  type GreenfieldWindowInputEvent,
} from "@workos/protocol";
import { CHILD_PROTOCOL_VERSION, encodeRecord, RecordReader } from "../src/ipc.js";

const [socketPath, evidenceDir, sessionId, generationText, attachmentId] = process.argv.slice(2);
if (!socketPath || !evidenceDir || !sessionId || !generationText || !attachmentId)
  throw new Error(
    "usage: menu-smoke <socket> <evidence-dir> <session-id> <generation> <attachment-id>",
  );
const generation = BigInt(generationText);
const server = createServer();
const pause = (duration: number) => new Promise((resolve) => setTimeout(resolve, duration));
let socket: Socket | undefined;
let windowId = "";
let requestId = 1n;
let sequence = 1n;
let afterMenu = false;
let savedFrame = false;
let framesAfterMenu = 0;
let firstFrameReady = false;
const savedFrameWindowIds = new Set<string>();
const capturingSequences = new Map<string, bigint>();
const snapshots: Array<
  Array<{ id: string; parentWindowId: string; title: string; appId: string }>
> = [];
const waiting = new Map<bigint, (envelope: GreenfieldChildEnvelope) => void>();
let finish!: (error?: Error) => void;
const done = new Promise<void>((resolve, reject) => {
  finish = (error) => (error ? reject(error) : resolve());
});

async function input(event: GreenfieldWindowInputEvent["event"]): Promise<void> {
  const connected = socket;
  if (!connected) throw new Error("menu broker disconnected");
  const currentRequest = requestId++;
  const request = create(SendGreenfieldWindowInputRequestSchema, {
    sessionId,
    attachmentId,
    expectedWorkloadGeneration: generation,
    controlGeneration: 1n,
    events: [
      create(GreenfieldWindowInputEventSchema, {
        sequence: sequence++,
        windowId,
        event,
      }),
    ],
  });
  const envelope = create(GreenfieldChildEnvelopeSchema, {
    protocolVersion: CHILD_PROTOCOL_VERSION,
    sessionId,
    workloadGeneration: generation,
    requestId: currentRequest,
    payload: { case: "input", value: request },
  });
  const response = new Promise<GreenfieldChildEnvelope>((resolve) =>
    waiting.set(currentRequest, resolve),
  );
  connected.write(encodeRecord(envelope));
  const result = await response;
  if (result.payload.case !== "inputResult" || result.payload.value.verdict !== 1) {
    throw new Error(`input failed at sequence ${sequence - 1n}`);
  }
}

server.on("connection", (connected) => {
  socket = connected;
  const reader = new RecordReader((envelope) => {
    if (envelope.sessionId !== sessionId || envelope.workloadGeneration !== generation) {
      finish(new Error("child identity mismatch"));
      return;
    }
    if (envelope.payload.case === "failure") {
      finish(new Error(`child failure: ${envelope.payload.value.reasonCode}`));
      return;
    }
    const waiter = waiting.get(envelope.requestId);
    if (waiter) {
      waiting.delete(envelope.requestId);
      waiter(envelope);
      return;
    }
    if (envelope.payload.case === "windows") {
      const windows = envelope.payload.value.windows;
      snapshots.push(
        windows.map((window) => ({
          id: window.id,
          parentWindowId: window.parentWindowId,
          title: window.title,
          appId: window.appId,
        })),
      );
      if (!windowId && windows.length) windowId = windows[0]!.id;
    }
    if (!afterMenu && envelope.payload.case === "frameTile") {
      const tile = envelope.payload.value;
      if (tile.windowId === windowId && tile.fullRefresh && tile.tileIndex + 1 === tile.tileCount) {
        firstFrameReady = true;
      }
    }
    if (afterMenu && envelope.payload.case === "frameTile") {
      const tile = envelope.payload.value;
      if (tile.tileIndex === 0) framesAfterMenu++;
      if (tile.fullRefresh && !savedFrameWindowIds.has(tile.windowId)) {
        if (!capturingSequences.has(tile.windowId)) {
          capturingSequences.set(tile.windowId, tile.frameSequence);
        }
        if (capturingSequences.get(tile.windowId) !== tile.frameSequence) return;
        void mkdir(evidenceDir, { recursive: true }).then(() =>
          writeFile(
            join(evidenceDir, `dialog-${tile.windowId}-${tile.tileIndex}-${tile.x}-${tile.y}.png`),
            tile.png,
          ),
        );
        if (tile.tileIndex + 1 === tile.tileCount) {
          savedFrameWindowIds.add(tile.windowId);
          savedFrame = true;
        }
      }
    }
  });
  connected.on("data", (chunk: Buffer) => {
    try {
      reader.push(chunk);
    } catch (error) {
      finish(error as Error);
    }
  });
});
server.listen(socketPath, () => process.stdout.write("menu-broker-listening\n"));

try {
  const timeout = setTimeout(() => finish(new Error("menu smoke timeout")), 90_000);
  while (!windowId || !firstFrameReady) await pause(100);
  await pause(1000);
  const click = async (x: number, y: number) => {
    const pointer = (action: GreenfieldPointerAction) => ({
      case: "pointer" as const,
      value: create(GreenfieldPointerInputSchema, { action, x, y, button: 0 }),
    });
    await input(pointer(GreenfieldPointerAction.MOVE));
    await pause(50);
    await input(pointer(GreenfieldPointerAction.DOWN));
    await input(pointer(GreenfieldPointerAction.UP));
  };
  await click(150, 182); // Open File... in the already visible File menu
  afterMenu = true;
  await pause(5000);
  clearTimeout(timeout);
  await mkdir(evidenceDir, { recursive: true });
  const result = {
    windowId,
    snapshots,
    framesAfterMenu,
    savedFrame,
    savedFrameWindowIds: [...savedFrameWindowIds],
  };
  await writeFile(join(evidenceDir, "dialog.json"), JSON.stringify(result, null, 2));
  process.stdout.write(`${JSON.stringify(result)}\n`);
  finish();
  await done;
} finally {
  socket?.destroy();
  server.close();
}
