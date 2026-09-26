import { Code, ConnectError } from "@connectrpc/connect";
import { useEffect, useRef, useState, type RefObject } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { GreenfieldWindow } from "@workos/protocol";
import type { GreenfieldAttachment } from "./greenfieldWindowClient.js";
import { GreenfieldFrameAssembler, type GreenfieldFrame } from "./greenfieldWindowFrames.js";

export type GreenfieldFrameState = "waiting" | "ready" | "reconnecting" | "unavailable";

async function paintFrame(
  canvas: HTMLCanvasElement,
  scratch: HTMLCanvasElement,
  frame: GreenfieldFrame,
  signal: AbortSignal,
): Promise<boolean> {
  if (scratch.width !== frame.width) scratch.width = frame.width;
  if (scratch.height !== frame.height) scratch.height = frame.height;
  const back = scratch.getContext("2d");
  const front = canvas.getContext("2d");
  if (!back || !front) throw new Error("canvas 2D unavailable");
  back.clearRect(0, 0, frame.width, frame.height);
  if (!frame.fullRefresh) back.drawImage(canvas, 0, 0);
  for (const tile of frame.tiles) {
    const bitmap = await createImageBitmap(
      new Blob([new Uint8Array(tile.png)], { type: "image/png" }),
    );
    try {
      if (bitmap.width !== tile.width || bitmap.height !== tile.height)
        throw new Error("native PNG dimensions do not match tile");
      back.drawImage(bitmap, tile.x, tile.y);
    } finally {
      bitmap.close();
    }
  }
  if (signal.aborted) return false;
  if (canvas.width !== frame.width) canvas.width = frame.width;
  if (canvas.height !== frame.height) canvas.height = frame.height;
  front.drawImage(scratch, 0, 0);
  return true;
}

export function GreenfieldWindowCanvas(props: {
  client: WorkOSClients["greenfieldWindows"];
  attachment: GreenfieldAttachment;
  nativeWindow: GreenfieldWindow;
  connectionEpoch: number;
  canvasRef: RefObject<HTMLCanvasElement | null>;
  onStateChange?: (state: GreenfieldFrameState) => void;
}) {
  const [state, setState] = useState<GreenfieldFrameState>("waiting");
  const [attempt, setAttempt] = useState(0);
  const stateCallback = useRef(props.onStateChange);
  stateCallback.current = props.onStateChange;
  const { client, attachment, nativeWindow, connectionEpoch, canvasRef } = props;

  useEffect(() => {
    const controller = new AbortController();
    const scratch = document.createElement("canvas");
    let retry: ReturnType<typeof setTimeout> | undefined;
    let ready = false;
    const report = (next: GreenfieldFrameState) => {
      if (controller.signal.aborted) return;
      setState(next);
      stateCallback.current?.(next);
    };
    report("waiting");
    const assembler = new GreenfieldFrameAssembler(
      nativeWindow.id,
      attachment.workloadGeneration,
      nativeWindow.revision,
    );
    const firstFrameTimer = setTimeout(() => {
      if (ready || controller.signal.aborted) return;
      report("unavailable");
      controller.abort();
    }, 10_000);
    void (async () => {
      try {
        for await (const response of client.watchGreenfieldWindowFrames(
          {
            sessionId: attachment.sessionId,
            attachmentId: attachment.attachmentId,
            expectedWorkloadGeneration: attachment.workloadGeneration,
            windowId: nativeWindow.id,
          },
          { signal: controller.signal },
        )) {
          if (controller.signal.aborted) return;
          if (!response.tile) throw new Error("native frame stream omitted a tile");
          const frame = assembler.push(response.tile);
          if (!frame) continue;
          const canvas = canvasRef.current;
          if (!canvas) return;
          if (!(await paintFrame(canvas, scratch, frame, controller.signal))) return;
          assembler.committed(frame);
          if (!ready) {
            ready = true;
            clearTimeout(firstFrameTimer);
          }
          report("ready");
        }
        if (controller.signal.aborted) return;
        report("reconnecting");
      } catch (error) {
        if (controller.signal.aborted) return;
        if (
          error instanceof ConnectError &&
          (error.code === Code.PermissionDenied || error.code === Code.Unauthenticated)
        ) {
          report("unavailable");
          return;
        }
        report("reconnecting");
      }
      retry = setTimeout(() => {
        setAttempt((value) => value + 1);
      }, 1500);
    })();
    return () => {
      controller.abort();
      clearTimeout(retry);
      clearTimeout(firstFrameTimer);
    };
  }, [
    client,
    attachment.sessionId,
    attachment.attachmentId,
    attachment.workloadGeneration,
    nativeWindow.id,
    nativeWindow.revision,
    connectionEpoch,
    attempt,
    canvasRef,
  ]);

  return (
    <div className="greenfield-window-frame" data-frame-state={state}>
      <canvas ref={canvasRef} data-testid="greenfield-window-canvas" tabIndex={0} />
      {state !== "ready" ? (
        <div className="greenfield-window-overlay" role="status">
          {state === "waiting"
            ? "正在等待原生窗口首帧"
            : state === "reconnecting"
              ? "画面连接中断，正在重连"
              : "原生窗口画面不可用"}
          {state === "unavailable" ? (
            <button
              type="button"
              onClick={() => {
                setAttempt((value) => value + 1);
              }}
            >
              重试画面
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
