// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { Blob as NodeBlob } from "node:buffer";
import { bindGreenfieldInput } from "./greenfieldCompositor.js";

afterEach(() => {
  vi.restoreAllMocks();
});

function fixture(controlling = true) {
  const canvas = document.createElement("canvas");
  const input = document.createElement("textarea");
  document.body.append(canvas, input);
  const keys: Array<{ pressed: boolean; evdev: number }> = [];
  let copySource: unknown;
  const seat = {
    selectionDataSource: undefined as unknown,
    selectionListeners: [] as Array<() => void>,
    setSelectionInternal(source: unknown) {
      seat.selectionDataSource = source;
      for (const listener of [...seat.selectionListeners]) listener();
    },
    notifyKeyboardFocusIn: vi.fn(),
    notifyKeyboardFocusOut: vi.fn(),
    notifyKey: vi.fn((event: { pressed: boolean; keyCode: { evdevKeyCode: number } }) => {
      keys.push({ pressed: event.pressed, evdev: event.keyCode.evdevKeyCode });
      if (copySource && event.pressed && keys.filter((key) => key.pressed).length === 2) {
        seat.setSelectionInternal(copySource);
        copySource = undefined;
      }
    }),
    notifyMotion: vi.fn(),
    notifyButton: vi.fn(),
    notifyAxis: vi.fn(),
    notifyFrame: vi.fn(),
    pointer: { buttonCount: 0 },
  };
  const session = {
    renderer: { initScene: vi.fn(), resetCursor: vi.fn() },
    globals: { register: vi.fn(), seat },
    display: { nextEventSerial: () => 42 },
    flush: vi.fn(),
  };
  const onCopyShortcut = vi.fn();
  const onPasteShortcut = vi.fn();
  const onCompositionCommitted = vi.fn();
  const bridge = bindGreenfieldInput(
    session as unknown as Parameters<typeof bindGreenfieldInput>[0],
    canvas,
    input,
    {
      canControl: () => controlling,
      onCopyShortcut,
      onPasteShortcut,
      onCompositionCommitted,
      mapKeyEvent: (event, pressed) => {
        const codes: Record<string, number> = {
          ControlLeft: 29,
          KeyA: 30,
          ShiftLeft: 42,
          KeyZ: 44,
          KeyC: 46,
          KeyV: 47,
        };
        const code = codes[event.code];
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
  return {
    canvas,
    input,
    seat,
    session,
    keys,
    bridge,
    onCopyShortcut,
    onPasteShortcut,
    onCompositionCommitted,
    setCopySource(source: unknown) {
      copySource = source;
    },
    dispose() {
      bridge.dispose();
      canvas.remove();
      input.remove();
    },
  };
}

describe("Greenfield Chromium input", () => {
  it("releases a pressed modifier when focus is lost", () => {
    const f = fixture();
    f.input.dispatchEvent(
      new KeyboardEvent("keydown", { code: "ControlLeft", key: "Control", bubbles: true }),
    );
    expect(f.keys).toHaveLength(1);
    window.dispatchEvent(new Event("blur"));
    expect(f.keys.map((key) => key.pressed)).toEqual([true, false]);
    expect(f.seat.notifyKeyboardFocusOut).toHaveBeenCalled();
    f.dispose();
  });

  it("blocks observer keyboard and clipboard input", () => {
    const f = fixture(false);
    f.input.dispatchEvent(new KeyboardEvent("keydown", { code: "KeyA", key: "a", bubbles: true }));
    f.input.dispatchEvent(
      new KeyboardEvent("keydown", { code: "KeyV", key: "v", metaKey: true, bubbles: true }),
    );
    expect(f.keys).toHaveLength(0);
    expect(f.onPasteShortcut).not.toHaveBeenCalled();
    expect(() => {
      f.bridge.pasteIntoApp("blocked");
    }).toThrow("controller unavailable");
    f.dispose();
  });

  it("maps a letterboxed CSS pointer position to canvas pixels", () => {
    const f = fixture();
    f.canvas.width = 1440;
    f.canvas.height = 900;
    vi.spyOn(f.canvas, "getBoundingClientRect").mockReturnValue({
      left: 0,
      top: 0,
      width: 900,
      height: 450,
      right: 900,
      bottom: 450,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    Object.defineProperty(f.canvas, "setPointerCapture", { value: vi.fn() });
    f.canvas.dispatchEvent(
      new MouseEvent("pointerdown", {
        bubbles: true,
        clientX: 450,
        clientY: 225,
        button: 2,
      }),
    );
    expect(f.seat.notifyButton).toHaveBeenCalledWith(
      expect.objectContaining({ x: 720, y: 450, buttonCode: 2, released: false }),
    );
    expect(f.seat.notifyMotion).toHaveBeenCalledWith(expect.objectContaining({ x: 720, y: 450 }));
    f.dispose();
  });

  it("uses Cmd+V as a clipboard gesture and sends text through Wayland selection", () => {
    const f = fixture();
    f.input.dispatchEvent(
      new KeyboardEvent("keydown", { code: "KeyV", key: "v", metaKey: true, bubbles: true }),
    );
    expect(f.onPasteShortcut).toHaveBeenCalledOnce();
    expect(f.keys).toHaveLength(0);
    f.bridge.pasteIntoApp("你好\t🙂\nline 2");
    expect(f.seat.selectionDataSource).toBeTruthy();
    expect(f.seat.notifyKeyboardFocusIn).toHaveBeenCalled();
    expect(f.keys.map((key) => key.pressed)).toEqual([true, true, false, false]);
    expect(() => {
      f.bridge.pasteIntoApp("x".repeat(1024 * 1024 + 1));
    }).toThrow(RangeError);
    f.dispose();
  });

  it("maps Cmd+Shift+Z to Ctrl+Shift+Z and releases Shift before Meta", () => {
    const f = fixture();
    f.input.dispatchEvent(
      new KeyboardEvent("keydown", { code: "ShiftLeft", key: "Shift", metaKey: true }),
    );
    f.input.dispatchEvent(
      new KeyboardEvent("keydown", {
        code: "KeyZ",
        key: "Z",
        shiftKey: true,
        metaKey: true,
      }),
    );
    f.input.dispatchEvent(
      new KeyboardEvent("keyup", { code: "ShiftLeft", key: "Shift", metaKey: true }),
    );
    expect(f.keys.map((key) => [key.evdev, key.pressed])).toEqual([
      [42, true],
      [29, true],
      [44, true],
      [44, false],
      [29, false],
      [42, false],
    ]);
    f.dispose();
  });

  it("commits one IME string and suppresses Process key events", () => {
    const f = fixture();
    f.input.dispatchEvent(new CompositionEvent("compositionstart", { data: "" }));
    f.input.dispatchEvent(
      new KeyboardEvent("keydown", { code: "KeyA", key: "Process", isComposing: true }),
    );
    expect(f.keys).toHaveLength(0);
    f.input.dispatchEvent(new CompositionEvent("compositionend", { data: "中文🙂" }));
    expect(f.onCompositionCommitted).toHaveBeenCalledExactlyOnceWith("sent");
    expect(f.keys.map((key) => key.pressed)).toEqual([true, true, false, false]);
    f.dispose();
  });

  it("copies only a fresh plain-text selection provided by the application", async () => {
    const f = fixture();
    const source = {
      mimeTypes: ["text/plain;charset=utf-8"],
      inputOutput: {
        mkfifo: () =>
          Promise.resolve([
            {
              readBlob: () => Promise.resolve(new NodeBlob(["中文\t🙂\nline 2"])),
              close: () => Promise.resolve(),
            },
            { close: () => Promise.resolve() },
          ]),
      },
      send: vi.fn(),
    };
    f.setCopySource(source);
    await expect(f.bridge.copyFromApp()).resolves.toBe("中文\t🙂\nline 2");
    expect(f.seat.notifyKeyboardFocusIn).toHaveBeenCalled();
    expect(source.send).toHaveBeenCalledWith("text/plain;charset=utf-8", expect.anything());
    f.dispose();
  });
});
