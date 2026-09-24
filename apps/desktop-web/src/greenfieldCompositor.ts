// Published Greenfield 1.0.0-rc1 compositor. The canvas is the WorkOS window.
const pressed = new Set<string>();

export function noteKey(type: "down" | "up", key: string) {
  if (type === "down") pressed.add(key);
  else pressed.delete(key);
}

export function releasePressedKeys() {
  const keys = [...pressed];
  pressed.clear();
  return keys;
}

// Uses the published 1.0.0-rc1 prebuild. initScene binds pointer, wheel,
// keydown/keyup, and blur-to-focus-out on the canvas. One canvas is the
// WorkOS window; the library composites child surfaces into it.
export async function attachGreenfieldCompositor(input: {
  canvas: HTMLCanvasElement;
  launchPath: string;
  compositorSessionId: string;
}): Promise<void> {
  const compositor = (await import("@gfld/compositor")) as {
    initWasm: () => Promise<void>;
    createCompositorSession: (sessionId: string) => Promise<{
      userShell: { actions: { initScene: (sceneId: string, canvas: HTMLCanvasElement) => void } };
      globals: { register: () => void };
    }>;
    createAppLauncher: (
      session: unknown,
      type: "remote",
    ) => { launch: (url: URL, onChild: (child: unknown) => void) => unknown };
  };
  await compositor.initWasm();
  const session = await compositor.createCompositorSession(input.compositorSessionId);
  if (!input.canvas.id) input.canvas.id = "greenfield-output";
  session.userShell.actions.initScene(input.canvas.id, input.canvas);
  session.globals.register();
  const launcher = compositor.createAppLauncher(session, "remote");
  launcher.launch(new URL(input.launchPath, window.location.origin), () => undefined);
}
