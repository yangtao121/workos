import { bindGreenfieldInput } from "../../src/greenfieldCompositor.js";

const canvas = document.getElementById("fixture-canvas") as HTMLCanvasElement;
const input = document.getElementById("fixture-input") as HTMLTextAreaElement;
const selection = document.getElementById("fixture-selection") as HTMLOutputElement;
const keys = document.getElementById("fixture-keys") as HTMLOutputElement;

const seat = {
  selectionDataSource: undefined as unknown,
  selectionListeners: [] as Array<() => void>,
  setSelectionInternal(source: unknown) {
    seat.selectionDataSource = source;
    const textSource = source as {
      send(
        mimeType: string,
        fd: { write(blob: Blob): Promise<void>; close(): Promise<void> },
      ): void;
    };
    textSource.send("text/plain;charset=utf-8", {
      write: async (blob) => {
        selection.textContent = await blob.text();
      },
      close: () => Promise.resolve(),
    });
    for (const listener of seat.selectionListeners) listener();
  },
  notifyKeyboardFocusIn() {},
  notifyKeyboardFocusOut() {},
  notifyKey(event: { pressed: boolean; keyCode: { evdevKeyCode: number } }) {
    keys.textContent += `${String(event.keyCode.evdevKeyCode)}:${event.pressed ? "down" : "up"} `;
  },
  notifyMotion() {},
  notifyButton() {},
  notifyAxis() {},
  notifyFrame() {},
  pointer: { buttonCount: 0 },
};
const session = {
  renderer: { initScene() {}, resetCursor() {} },
  globals: { register() {}, seat },
  display: { nextEventSerial: () => 1 },
  flush() {},
};
const keyCodes: Record<string, number> = {
  ControlLeft: 29,
  KeyV: 47,
  KeyA: 30,
};
const bridge: ReturnType<typeof bindGreenfieldInput> = bindGreenfieldInput(
  session as unknown as Parameters<typeof bindGreenfieldInput>[0],
  canvas,
  input,
  {
    canControl: () => true,
    onCopyShortcut: () => undefined,
    onPasteShortcut: () => {
      void navigator.clipboard.readText().then((text) => {
        bridge.pasteIntoApp(text);
      });
    },
    onCompositionCommitted: () => undefined,
    mapKeyEvent: (event, pressed) => {
      const code = keyCodes[event.code];
      if (!code) return undefined;
      return {
        keyCode: { evdevKeyCode: code, x11KeyCode: code + 8 },
        timeStamp: event.timeStamp,
        pressed,
        capsLock: false,
        numLock: false,
      };
    },
  },
);
