// Manual private-UDS proof: Code Ctrl+O creates a transient and an exact
// sequenced close removes only that native child. No owner credentials enter.
import { create } from "@bufbuild/protobuf";
import { createServer, type Socket } from "node:net";
import { writeFile } from "node:fs/promises";
import {
  GreenfieldChildEnvelopeSchema,
  GreenfieldInputVerdict,
  GreenfieldKeyAction,
  GreenfieldKeyInputSchema,
  GreenfieldPointerAction,
  GreenfieldPointerInputSchema,
  GreenfieldWindowCloseSchema,
  GreenfieldWindowFocusSchema,
  GreenfieldWindowInputEventSchema,
  SendGreenfieldWindowInputRequestSchema,
  type GreenfieldChildEnvelope,
  type GreenfieldWindowInputEvent,
} from "@workos/protocol";
import { CHILD_PROTOCOL_VERSION, encodeRecord, RecordReader } from "../src/ipc.js";

const [socketPath, resultPath, sessionId, generationText, attachmentId] = process.argv.slice(2);
if (!socketPath || !resultPath || !sessionId || !generationText || !attachmentId)
  throw new Error("usage: close-smoke <socket> <result> <session-id> <generation> <attachment-id>");
const generation = BigInt(generationText);
const server = createServer();
let socket: Socket | undefined;
let parentId = "";
let childId = "";
let windows: Array<{ id: string; parentWindowId: string; title: string }> = [];
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
  if (!connected) throw new Error("close broker disconnected");
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

function key(action: GreenfieldKeyAction, code: string, value: string, ctrl = false) {
  return {
    case: "key" as const,
    value: create(GreenfieldKeyInputSchema, { action, code, key: value, ctrl }),
  };
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
      windows = envelope.payload.value.windows.map((window) => ({
        id: window.id,
        parentWindowId: window.parentWindowId,
        title: window.title,
      }));
      if (!parentId && windows.length) parentId = windows[0]?.id ?? "";
      const child = windows.find((window) => window.parentWindowId === parentId);
      if (child) childId = child.id;
    }
    if (envelope.payload.case === "frameTile") {
      const tile = envelope.payload.value;
      if (tile.fullRefresh && tile.tileIndex + 1 === tile.tileCount) readyFrames.add(tile.windowId);
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
  process.stdout.write("close-broker-listening\n");
  await until(
    () => Boolean(parentId) && readyFrames.has(parentId),
    "Code full native frame missing",
    90_000,
  );
  await input(parentId, { case: "focus", value: create(GreenfieldWindowFocusSchema) });
  await pause(1000);
  // Fresh official Code profiles display two onboarding pages.
  await click(1100, 730);
  await pause(1200);
  await click(1150, 730);
  await pause(1200);
  await input(parentId, key(GreenfieldKeyAction.DOWN, "ControlLeft", "Control"));
  await input(parentId, key(GreenfieldKeyAction.DOWN, "KeyO", "o", true));
  await input(parentId, key(GreenfieldKeyAction.UP, "KeyO", "o", true));
  await input(parentId, key(GreenfieldKeyAction.UP, "ControlLeft", "Control"));
  await pause(5000);
  const shortcutOpened = Boolean(childId) && readyFrames.has(childId);
  if (!shortcutOpened) {
    await click(90, 50); // File menu on the native Code parent
    await pause(250);
    await click(150, 182); // Open File... menu item
  }
  await until(
    () => Boolean(childId) && readyFrames.has(childId),
    `Open File produced no native child: ${JSON.stringify(windows)}`,
  );
  const before = [...windows];
  let closeAcknowledged = false;
  let closeError = "";
  try {
    await input(childId, { case: "close", value: create(GreenfieldWindowCloseSchema) });
    closeAcknowledged = true;
    await until(
      () =>
        windows.some((window) => window.id === parentId) &&
        !windows.some((window) => window.id === childId),
      `native close did not remove only the child: ${JSON.stringify(windows)}`,
    );
  } catch (error) {
    closeError = error instanceof Error ? error.message : String(error);
  }
  const childRemoved = !windows.some((window) => window.id === childId);
  const result = {
    parentId,
    childId,
    before,
    after: windows,
    childHadFullFrame: readyFrames.has(childId),
    shortcutOpened,
    closeAcknowledged,
    childRemoved,
    closeError,
  };
  await writeFile(resultPath, `${JSON.stringify(result, null, 2)}\n`);
  process.stdout.write(`${JSON.stringify(result)}\n`);
  if (!childRemoved) throw new Error(closeError || "native child did not close");
} finally {
  socket?.destroy();
  server.close();
}
