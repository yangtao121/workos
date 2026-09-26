import { create } from "@bufbuild/protobuf";
import { createRoot } from "react-dom/client";
import { useEffect, useMemo, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  GreenfieldInputVerdict,
  GreenfieldKeyAction,
  GreenfieldWindowFrameTileSchema,
  GreenfieldWindowSchema,
} from "@workos/protocol";
import { GreenfieldWindowApp } from "../../src/GreenfieldWindowApp.js";
import {
  GreenfieldWindowInputClient,
  type GreenfieldAttachment,
} from "../../src/greenfieldWindowClient.js";
import "../../src/styles.css";

const initialAttachment: GreenfieldAttachment = {
  sessionId: "01999999-9999-7999-8999-000000000010",
  attachmentId: "01999999-9999-7999-8999-000000000011",
  workloadGeneration: 3n,
  controlGeneration: 8n,
  controls: new URLSearchParams(location.search).has("lease-loss"),
};

const windows = [
  create(GreenfieldWindowSchema, {
    id: "01999999-9999-7999-8999-000000000012",
    title: "Code fixture window",
    appId: "code",
    zOrder: 1,
    active: true,
    revision: 2n,
    contentRect: { x: 10, y: 10, width: 512, height: 320 },
    visualRect: { x: 10, y: 10, width: 512, height: 320 },
  }),
  create(GreenfieldWindowSchema, {
    id: "01999999-9999-7999-8999-000000000013",
    parentWindowId: "01999999-9999-7999-8999-000000000012",
    title: "Dialog fixture window",
    appId: "code",
    zOrder: 2,
    revision: 1n,
    contentRect: { x: 180, y: 120, width: 320, height: 180 },
    visualRect: { x: 180, y: 120, width: 320, height: 180 },
  }),
];

async function pngFor(id: string): Promise<Uint8Array> {
  const dialog = id === windows[1]?.id;
  const canvas = document.createElement("canvas");
  canvas.width = dialog ? 320 : 512;
  canvas.height = dialog ? 180 : 320;
  const painter = canvas.getContext("2d");
  if (!painter) throw new Error("missing 2D canvas");
  painter.fillStyle = dialog ? "#253544" : "#151d26";
  painter.fillRect(0, 0, canvas.width, canvas.height);
  painter.fillStyle = dialog ? "#d5e3ee" : "#84d1a2";
  painter.font = dialog ? "bold 18px sans-serif" : "bold 24px sans-serif";
  painter.fillText(dialog ? "FIXTURE · dialog" : "FIXTURE · native tile", 28, dialog ? 48 : 58);
  painter.fillStyle = "#8292a5";
  painter.font = "15px sans-serif";
  painter.fillText(
    dialog ? "Synthetic frame only" : "Synthetic PNG frame, not a real Code capture",
    28,
    dialog ? 82 : 94,
  );
  if (!dialog) {
    painter.fillStyle = "#283644";
    painter.fillRect(28, 130, 456, 136);
    painter.fillStyle = "#b1ddc3";
    painter.fillText("const viewer = 'resident';", 46, 168);
    painter.fillText("// complete first frame", 46, 203);
  }
  const blob = await new Promise<Blob>((resolve, reject) => {
    canvas.toBlob((value) => {
      if (value) resolve(value);
      else reject(new Error("PNG encoding failed"));
    }, "image/png");
  });
  return new Uint8Array(await blob.arrayBuffer());
}

const client = {
  sendGreenfieldWindowInput: async () => ({
    verdict: GreenfieldInputVerdict.UNAVAILABLE,
    lastAppliedSequence: 0n,
  }),
  watchGreenfieldWindowFrames: async function* (
    request: { windowId: string },
    options: { signal?: AbortSignal },
  ) {
    const nativeWindow = windows.find((item) => item.id === request.windowId);
    if (!nativeWindow?.visualRect) throw new Error("unknown fixture window");
    const png = await pngFor(request.windowId);
    yield {
      tile: create(GreenfieldWindowFrameTileSchema, {
        windowId: request.windowId,
        workloadGeneration: 3n,
        windowRevision: nativeWindow.revision,
        frameSequence: 1n,
        tileIndex: 0,
        tileCount: 1,
        x: 0,
        y: 0,
        width: nativeWindow.visualRect.width,
        height: nativeWindow.visualRect.height,
        frameWidth: nativeWindow.visualRect.width,
        frameHeight: nativeWindow.visualRect.height,
        fullRefresh: true,
        png,
      }),
    };
    await new Promise<void>((resolve) => {
      if (options.signal?.aborted) resolve();
      else
        options.signal?.addEventListener(
          "abort",
          () => {
            resolve();
          },
          { once: true },
        );
    });
  },
} as unknown as WorkOSClients["greenfieldWindows"];
function ResidentFixture() {
  const [attachment, setAttachment] = useState(initialAttachment);
  const input = useMemo(
    () =>
      new GreenfieldWindowInputClient(client, attachment, () => {
        setAttachment((current) => ({ ...current, controls: false }));
      }),
    [],
  );
  useEffect(() => {
    if (!initialAttachment.controls) return;
    const timer = setTimeout(() => {
      void input
        .send(windows[0]!.id, [
          {
            case: "key",
            value: { action: GreenfieldKeyAction.DOWN, code: "KeyA", key: "a" },
          },
        ])
        .catch(() => undefined);
    }, 500);
    return () => clearTimeout(timer);
  }, [input]);
  return (
    <main className="desktop-shell" style={{ width: "100vw", height: "100vh" }}>
      {windows.map((nativeWindow, index) => (
        <section
          className="workos-window"
          key={nativeWindow.id}
          data-window-id={nativeWindow.id}
          style={{
            left: index ? 740 : 190,
            top: index ? 332 : 80,
            width: index ? 500 : 870,
            height: index ? 340 : 680,
            zIndex: index + 1,
          }}
        >
          <header>
            <span className="window-identity">
              <strong>{nativeWindow.title}</strong>
            </span>
          </header>
          <GreenfieldWindowApp
            client={client}
            attachment={attachment}
            input={input}
            nativeWindow={nativeWindow}
            connection="connected"
            connectionEpoch={1}
            onTakeControl={() => Promise.resolve()}
          />
        </section>
      ))}
    </main>
  );
}
const root = document.getElementById("fixture-root");
if (!root) throw new Error("missing fixture root");
createRoot(root).render(<ResidentFixture />);
