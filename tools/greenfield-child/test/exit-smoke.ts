// Manual private-UDS proof: Code File -> Exit reports STOPPED; an exact
// launched Code process SIGKILL reports FAILED. No owner credentials enter.
import { create } from "@bufbuild/protobuf";
import { createServer, type Socket } from "node:net";
import { writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import {
  GreenfieldChildEnvelopeSchema,
  GreenfieldDisplayState,
  GreenfieldInputVerdict,
  GreenfieldPointerAction,
  GreenfieldPointerInputSchema,
  GreenfieldWindowFocusSchema,
  GreenfieldWindowInputEventSchema,
  SendGreenfieldWindowInputRequestSchema,
  type GreenfieldChildEnvelope,
  type GreenfieldWindowInputEvent,
} from "@workos/protocol";
import { CHILD_PROTOCOL_VERSION, encodeRecord, RecordReader } from "../src/ipc.js";

const [socketPath, resultPath, sessionId, generationText, attachmentId, mode = "normal"] =
  process.argv.slice(2);
if (!socketPath || !resultPath || !sessionId || !generationText || !attachmentId)
  throw new Error("usage: exit-smoke <socket> <result> <session-id> <generation> <attachment-id>");
if (mode !== "normal" && mode !== "signal") throw new Error("invalid exit-smoke mode");
const generation = BigInt(generationText);
const server = createServer();
let socket: Socket | undefined;
let parentId = "";
let windows: Array<{ id: string; parentWindowId: string; title: string }> = [];
let terminalState = "";
let afterMenu = false;
const savedMenuTiles = new Set<string>();
let requestId = 1n;
let sequence = 1n;
const readyFrames = new Set<string>();
const waiting = new Map<bigint, (answer: GreenfieldChildEnvelope) => void>();
let failure: Error | undefined;
const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function until(predicate: () => boolean, reason: string, timeoutMs = 15_000): Promise<void> {
  const untilAt = Date.now() + timeoutMs;
  while (!predicate()) {
    if (failure) throw failure;
    if (Date.now() > untilAt) throw new Error(reason);
    await pause(100);
  }
}

async function input(target: string, event: GreenfieldWindowInputEvent["event"]): Promise<void> {
  const connected = socket;
  if (!connected) throw new Error("exit broker disconnected");
  const id = requestId++;
  const inputSequence = sequence++;
  const answer = new Promise<GreenfieldChildEnvelope>((resolve) => waiting.set(id, resolve));
  connected.write(
    encodeRecord(
      create(GreenfieldChildEnvelopeSchema, {
        protocolVersion: CHILD_PROTOCOL_VERSION,
        sessionId,
        workloadGeneration: generation,
        requestId: id,
        payload: {
          case: "input",
          value: create(SendGreenfieldWindowInputRequestSchema, {
            sessionId,
            attachmentId,
            expectedWorkloadGeneration: generation,
            controlGeneration: 1n,
            events: [
              create(GreenfieldWindowInputEventSchema, {
                sequence: inputSequence,
                windowId: target,
                event,
              }),
            ],
          }),
        },
      }),
    ),
  );
  const result = await Promise.race([
    answer,
    pause(10_000).then(() => {
      throw new Error(`native input response timed out at ${String(inputSequence)}`);
    }),
  ]);
  if (
    result.payload.case !== "inputResult" ||
    result.payload.value.verdict !== GreenfieldInputVerdict.APPLIED
  )
    throw new Error(`native input was rejected at ${String(inputSequence)}`);
}

async function click(x: number, y: number): Promise<void> {
  const pointer = (action: GreenfieldPointerAction) => ({
    case: "pointer" as const,
    value: create(GreenfieldPointerInputSchema, { action, x, y, button: 0 }),
  });
  await input(parentId, pointer(GreenfieldPointerAction.MOVE));
  await input(parentId, pointer(GreenfieldPointerAction.DOWN));
  await input(parentId, pointer(GreenfieldPointerAction.UP));
}

server.on("connection", (connected) => {
  socket = connected;
  const reader = new RecordReader((envelope) => {
    if (envelope.sessionId !== sessionId || envelope.workloadGeneration !== generation) {
      failure = new Error("child identity mismatch");
      return;
    }
    if (envelope.payload.case === "failure") {
      failure = new Error(`child failure: ${envelope.payload.value.reasonCode}`);
      return;
    }
    const waiter = waiting.get(envelope.requestId);
    if (waiter) {
      waiting.delete(envelope.requestId);
      waiter(envelope);
      return;
    }
    if (envelope.payload.case === "windows") {
      if (envelope.payload.value.state !== GreenfieldDisplayState.RUNNING) {
        terminalState = String(envelope.payload.value.state);
      }
      windows = envelope.payload.value.windows.map((window) => ({
        id: window.id,
        parentWindowId: window.parentWindowId,
        title: window.title,
      }));
      if (!parentId && windows.length) parentId = windows[0]?.id ?? "";
    }
    if (envelope.payload.case === "frameTile") {
      const tile = envelope.payload.value;
      if (tile.fullRefresh && tile.tileIndex + 1 === tile.tileCount) readyFrames.add(tile.windowId);
      const key = `${String(tile.x)}-${String(tile.y)}`;
      if (afterMenu && tile.fullRefresh && tile.windowId === parentId && !savedMenuTiles.has(key)) {
        savedMenuTiles.add(key);
        void writeFile(join(dirname(resultPath), `menu-${key}.png`), tile.png);
      }
    }
  });
  connected.on("data", (chunk: Buffer) => {
    try {
      reader.push(chunk);
    } catch (error) {
      failure = error as Error;
    }
  });
  connected.on("error", (error) => {
    failure = error;
  });
});

try {
  server.listen(socketPath);
  await new Promise<void>((resolve) => server.once("listening", resolve));
  process.stdout.write("exit-broker-listening\n");
  await until(
    () => Boolean(parentId) && readyFrames.has(parentId),
    "Code full native frame missing",
    90_000,
  );
  await input(parentId, { case: "focus", value: create(GreenfieldWindowFocusSchema) });
  const before = [...windows];
  if (mode === "normal") {
    await pause(1000);
    // Fresh official Code profiles display two onboarding pages.
    await click(980, 681);
    await pause(1200);
    await click(980, 681);
    await pause(1200);
    await click(90, 50); // File menu on the native Code parent
    afterMenu = true;
    await pause(4000);
    await click(115, 633); // File -> Exit in the pinned no-workspace Code menu
  } else {
    process.stdout.write("application-ready-for-signal\n");
  }
  await until(() => Boolean(terminalState), "Code produced no terminal snapshot", 30_000);
  const result = {
    parentId,
    mode,
    before,
    after: windows,
    terminalState,
  };
  await writeFile(resultPath, `${JSON.stringify(result, null, 2)}\n`);
  process.stdout.write(`${JSON.stringify(result)}\n`);
  const expected =
    mode === "normal" ? GreenfieldDisplayState.STOPPED : GreenfieldDisplayState.FAILED;
  if (terminalState !== String(expected) || windows.length !== 0)
    throw new Error("application exit did not report its exact terminal outcome");
} finally {
  socket?.destroy();
  server.close();
}
