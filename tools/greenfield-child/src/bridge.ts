import { createHash } from "node:crypto";
import { createReadStream, existsSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import {
  createServer as createHttpServer,
  type IncomingMessage,
  type ServerResponse,
} from "node:http";
import { createConnection, createServer as createNetServer, type Socket } from "node:net";
import { spawn, type ChildProcess } from "node:child_process";
import { dirname, extname, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { once } from "node:events";
import { chromium, type Browser, type Page } from "playwright-core";
import { create, toBinary } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  GreenfieldChildEnvelopeSchema,
  GreenfieldChildFailureSchema,
  GreenfieldDisplayState,
  GreenfieldInputVerdict,
  GreenfieldWindowFrameTileSchema,
  GreenfieldWindowRectSchema,
  GreenfieldWindowSchema,
  GreenfieldWindowInputEventSchema,
  GreenfieldWindowSnapshotSchema,
  ReadGreenfieldClipboardResponseSchema,
  SendGreenfieldWindowInputResponseSchema,
  type GreenfieldChildEnvelope,
  type GreenfieldWindowInputEvent,
} from "@workos/protocol";
import { CHILD_PROTOCOL_VERSION, encodeRecord, InputSequenceLedger, RecordReader } from "./ipc.js";
import {
  normalApplicationExit,
  processAlive,
  readProcessIdentity,
  type ApplicationExit,
} from "./applicationExit.js";

type Rect = { x: number; y: number; width: number; height: number };
type WindowFact = {
  id: string;
  parentWindowId: string;
  title: string;
  appId: string;
  contentRect: Rect;
  visualRect: Rect;
  devicePixelRatioMillis: number;
  zOrder: number;
  active: boolean;
  revision: string;
};
type BrowserMessage =
  | { kind: "windows"; windows: WindowFact[] }
  | {
      kind: "frame";
      windowId: string;
      windowRevision: string;
      frameSequence: string;
      frameWidth: number;
      frameHeight: number;
      fullRefresh: boolean;
      renderedAt: string;
      tiles: Array<{ x: number; y: number; width: number; height: number; pngBase64: string }>;
    }
  | { kind: "failure"; reasonCode: string }
  | { kind: "applicationExit"; exit?: ApplicationExit };

type Options = {
  socket: string;
  sessionId: string;
  generation: bigint;
  width: number;
  height: number;
  workspace?: string;
  application: "code" | "text_editor";
  renderDevice: string;
};

const PAGE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "page");
const PRIVATE_DIR = "/tmp/workos/greenfield";
const PROXY_MAIN = "/opt/greenfield/packages/compositor-proxy-cli/dist/main.js";
const CODE_BINARY = "/usr/share/code/code";
const TEXT_EDITOR_BINARY = "/usr/bin/mousepad";
const MAX_SNAPSHOT_BYTES = 1024 * 1024;
const MAX_FRAME_BYTES = 24 * 1024 * 1024;
const MAX_TILE_BYTES = 2 * 1024 * 1024;
const MAX_TEXT_BYTES = 1024 * 1024;
const MAX_WINDOWS = 16;
const textDecoder = new TextDecoder("utf-8", { fatal: true });
const safeCode = (reason: string) =>
  /^[A-Z][A-Z0-9_]{1,63}$/.test(reason) ? reason : "CHILD_COMPOSITOR_FAILED";

function socketIsDestroyed(socket: Socket): boolean {
  return socket.destroyed;
}

function parseOptions(argv: string[]): Options {
  const values = new Map<string, string>();
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv.at(index);
    const value = argv.at(index + 1);
    if (!key?.startsWith("--") || !value || values.has(key))
      throw new Error("CHILD_ARGUMENT_INVALID");
    values.set(key, value);
  }
  const permitted = new Set([
    "--socket",
    "--session-id",
    "--generation",
    "--width",
    "--height",
    "--workspace",
    "--application",
    "--render-device",
  ]);
  if ([...values.keys()].some((key) => !permitted.has(key)))
    throw new Error("CHILD_ARGUMENT_INVALID");
  const socket = values.get("--socket") ?? "";
  const sessionId = values.get("--session-id") ?? "";
  const generationText = values.get("--generation") ?? "";
  const width = Number(values.get("--width"));
  const height = Number(values.get("--height"));
  const renderDevice = values.get("--render-device") ?? "";
  const workspace = values.get("--workspace");
  const application = values.get("--application") ?? "code";
  if (
    socket !== "/run/workos/greenfield/bridge.sock" ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(sessionId) ||
    !/^[1-9][0-9]*$/.test(generationText) ||
    !Number.isInteger(width) ||
    !Number.isInteger(height) ||
    width < 1 ||
    height < 1 ||
    width > 4096 ||
    height > 4096 ||
    !/^\/dev\/dri\/renderD[0-9]+$/.test(renderDevice) ||
    (workspace !== undefined && workspace !== "/workspace") ||
    (application !== "code" && application !== "text_editor")
  ) {
    throw new Error("CHILD_ARGUMENT_INVALID");
  }
  return {
    socket,
    sessionId,
    generation: BigInt(generationText),
    width,
    height,
    renderDevice,
    application,
    ...(workspace ? { workspace } : {}),
  };
}

function checkIsolation(options: Options): void {
  for (const key of [
    "WORKOS_DATABASE_URL",
    "WORKOS_CORE_URL",
    "WORKOS_RUNTIME_URL",
    "WORKOS_GATEWAY_SESSION_SECRET",
    "DOCKER_HOST",
  ]) {
    if (process.env[key]) throw new Error("CHILD_ISOLATION_UNAVAILABLE");
  }
  if (
    existsSync("/var/run/docker.sock") ||
    !existsSync(options.renderDevice) ||
    !existsSync(PROXY_MAIN) ||
    !existsSync(CODE_BINARY)
  ) {
    throw new Error("CHILD_ISOLATION_UNAVAILABLE");
  }
  if (process.getuid?.() !== 10001) throw new Error("CHILD_ISOLATION_UNAVAILABLE");
}

async function freePort(): Promise<number> {
  const server = createNetServer();
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("CHILD_PORT_UNAVAILABLE");
  const port = address.port;
  server.close();
  await once(server, "close");
  return port;
}

function staticResponse(request: IncomingMessage, response: ServerResponse): void {
  if (request.method !== "GET") {
    response.writeHead(405).end();
    return;
  }
  const pathname = new URL(request.url ?? "/", "http://127.0.0.1").pathname;
  const file = resolve(PAGE_ROOT, `.${pathname === "/" ? "/index.html" : pathname}`);
  if (!file.startsWith(PAGE_ROOT + sep) || !existsSync(file)) {
    response.writeHead(404).end();
    return;
  }
  const contentType =
    {
      ".html": "text/html; charset=utf-8",
      ".js": "text/javascript; charset=utf-8",
      ".css": "text/css; charset=utf-8",
      ".wasm": "application/wasm",
    }[extname(file)] ?? "application/octet-stream";
  response.writeHead(200, {
    "content-type": contentType,
    "cache-control": "no-store",
    "cross-origin-resource-policy": "same-origin",
  });
  createReadStream(file).pipe(response);
}

async function servePage(): Promise<{ port: number; close: () => Promise<void> }> {
  const server = createHttpServer(staticResponse);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("CHILD_PAGE_UNAVAILABLE");
  return {
    port: address.port,
    close: async () => {
      server.close();
      await once(server, "close");
    },
  };
}

function childEnvironment(): NodeJS.ProcessEnv {
  const allowed = [
    "PATH",
    "LANG",
    "LC_ALL",
    "HOME",
    "XDG_RUNTIME_DIR",
    "TMPDIR",
    "LD_LIBRARY_PATH",
    "NVIDIA_VISIBLE_DEVICES",
    "NVIDIA_DRIVER_CAPABILITIES",
  ];
  const env = Object.fromEntries(
    allowed.flatMap((key) => (process.env[key] ? [[key, process.env[key]]] : [])),
  );
  return { ...env, HOME: PRIVATE_DIR, XDG_RUNTIME_DIR: "/tmp/xdg", RENDERER_ALLOW_SOFTWARE: "1" };
}

async function launchProxy(
  options: Options,
  pagePort: number,
): Promise<{ process: ChildProcess; launch: unknown; launchUrl: string }> {
  await mkdir(PRIVATE_DIR, { recursive: true, mode: 0o700 });
  await mkdir("/tmp/xdg", { recursive: true, mode: 0o700 });
  await mkdir("/tmp/.X11-unix", { recursive: true, mode: 0o1777 });
  const codeData = join(PRIVATE_DIR, "code-data");
  if (options.application === "code") await mkdir(codeData, { recursive: true, mode: 0o700 });
  const appsPath = join(PRIVATE_DIR, "apps.json");
  const appArgs =
    options.application === "code"
      ? ["--ozone-platform=x11", "--disable-gpu", "--no-sandbox", "--user-data-dir", codeData]
      : [];
  if (options.workspace && options.application === "code") appArgs.push(options.workspace);
  const path = options.application === "code" ? "/code" : "/text-editor";
  const name = options.application === "code" ? "Code" : "Text Editor";
  const executable = options.application === "code" ? CODE_BINARY : TEXT_EDITOR_BINARY;
  const appEnv = {
    HOME: PRIVATE_DIR,
    ...(options.application === "text_editor" ? { GDK_BACKEND: "x11" } : {}),
  };
  await writeFile(
    appsPath,
    JSON.stringify({
      [path]: { name, executable, args: appArgs, env: appEnv },
    }),
    { mode: 0o600 },
  );
  const port = await freePort();
  const origin = `http://127.0.0.1:${String(pagePort)}`;
  const proxy = spawn(
    process.execPath,
    [
      PROXY_MAIN,
      "--bind-ip",
      "127.0.0.1",
      "--bind-port",
      String(port),
      "--allow-origin",
      origin,
      "--base-url",
      `ws://127.0.0.1:${String(port)}`,
      "--encoder",
      "x264",
      "--render-device",
      options.renderDevice,
      "--applications",
      appsPath,
    ],
    { cwd: "/opt/greenfield", env: childEnvironment(), stdio: "ignore" },
  );
  const proxyState = { exited: false };
  proxy.once("exit", () => {
    proxyState.exited = true;
  });
  const url = `http://127.0.0.1:${String(port)}${path}`;
  try {
    for (let attempt = 0; attempt < 150; attempt++) {
      if (proxyState.exited) throw new Error("GREENFIELD_PROXY_EXITED");
      try {
        const response = await fetch(url, {
          headers: { Origin: origin, "x-compositor-session-id": "workos" },
          signal: AbortSignal.timeout(2000),
        });
        if (response.status !== 201) throw new Error("APP_LAUNCH_FAILED");
        const launch: unknown = await response.json();
        if (
          !launch ||
          typeof launch !== "object" ||
          !/^[1-9][0-9]*$/.test(String((launch as { pid?: unknown }).pid)) ||
          typeof (launch as { key?: unknown }).key !== "string" ||
          !(launch as { key: string }).key ||
          typeof (launch as { signalURL?: unknown }).signalURL !== "string"
        ) {
          throw new Error("APP_LAUNCH_FAILED");
        }
        return { process: proxy, launch, launchUrl: url };
      } catch (error) {
        if (error instanceof Error && error.message === "APP_LAUNCH_FAILED") throw error;
        await new Promise((resolve) => setTimeout(resolve, 100));
      }
    }
    throw new Error("GREENFIELD_PROXY_TIMEOUT");
  } catch (error) {
    proxy.kill("SIGTERM");
    throw error;
  }
}

class ChildBridge {
  private socket?: Socket;
  private reconnecting = false;
  private ended = false;
  private requestChain = Promise.resolve();
  private page?: Page;
  private browser?: Browser;
  private proxy?: ChildProcess;
  private latestWindows: WindowFact[] = [];
  private snapshotRevision = 1n;
  private latestWindowsJson = "[]";
  private terminalState?: GreenfieldDisplayState;
  private applicationMonitor?: NodeJS.Timeout;
  private applicationPollInFlight = false;
  private applicationMissingSince?: number;
  private readonly ledger = new InputSequenceLedger();
  private stopRun!: (reason: string) => void;
  private readonly stopped = new Promise<string>((resolve) => {
    this.stopRun = resolve;
  });

  constructor(private readonly options: Options) {}

  private isEnded(): boolean {
    return this.ended;
  }

  private pageIfReady(): Page | undefined {
    return this.page;
  }

  private envelope(
    payload: GreenfieldChildEnvelope["payload"],
    requestId = 0n,
  ): GreenfieldChildEnvelope {
    return create(GreenfieldChildEnvelopeSchema, {
      protocolVersion: CHILD_PROTOCOL_VERSION,
      sessionId: this.options.sessionId,
      workloadGeneration: this.options.generation,
      requestId,
      payload,
    });
  }

  private async send(envelope: GreenfieldChildEnvelope, target?: Socket): Promise<void> {
    const socket = target ?? this.socket;
    if (!socket || socket.destroyed || (target && target !== this.socket)) return;
    const record = encodeRecord(envelope);
    if (socket.write(record)) return;
    // A broker restart may close this socket midway through a frame. The
    // compositor and Code remain resident; the next connection receives a
    // fresh snapshot and a complete frame instead of killing the child.
    try {
      await Promise.race([once(socket, "drain"), once(socket, "close")]);
    } catch (error) {
      if (!socketIsDestroyed(socket)) throw error;
    }
  }

  private snapshot() {
    if (this.latestWindows.length > MAX_WINDOWS) throw new Error("WINDOW_LIMIT_EXCEEDED");
    const snapshot = create(GreenfieldWindowSnapshotSchema, {
      sessionId: this.options.sessionId,
      workloadGeneration: this.options.generation,
      revision: this.snapshotRevision,
      state: this.terminalState ?? GreenfieldDisplayState.RUNNING,
      windows: this.latestWindows.map((window) =>
        create(GreenfieldWindowSchema, {
          id: window.id,
          parentWindowId: window.parentWindowId,
          title: window.title,
          appId: window.appId,
          contentRect: create(GreenfieldWindowRectSchema, window.contentRect),
          visualRect: create(GreenfieldWindowRectSchema, window.visualRect),
          devicePixelRatioMillis: window.devicePixelRatioMillis,
          zOrder: window.zOrder,
          active: window.active,
          revision: BigInt(window.revision),
        }),
      ),
    });
    if (toBinary(GreenfieldWindowSnapshotSchema, snapshot).byteLength > MAX_SNAPSHOT_BYTES) {
      throw new Error("WINDOW_SNAPSHOT_TOO_LARGE");
    }
    return snapshot;
  }

  private async sendSnapshot(): Promise<void> {
    await this.send(this.envelope({ case: "windows", value: this.snapshot() }));
  }

  private async handleBrowserMessage(message: BrowserMessage): Promise<void> {
    if (this.terminalState !== undefined) return;
    if (message.kind === "applicationExit") {
      this.terminalState = normalApplicationExit(message.exit)
        ? GreenfieldDisplayState.STOPPED
        : GreenfieldDisplayState.FAILED;
      this.latestWindows = [];
      this.latestWindowsJson = "[]";
      this.snapshotRevision++;
      try {
        await this.sendSnapshot();
      } finally {
        this.stopRun(
          this.terminalState === GreenfieldDisplayState.STOPPED
            ? "APPLICATION_STOPPED"
            : "APPLICATION_FAILED",
        );
      }
      return;
    }
    if (message.kind === "failure") {
      await this.fatal(message.reasonCode);
      return;
    }
    if (message.kind === "windows") {
      const serialized = JSON.stringify(message.windows);
      if (serialized !== this.latestWindowsJson) {
        this.latestWindows = message.windows;
        this.latestWindowsJson = serialized;
        this.snapshotRevision++;
        await this.sendSnapshot();
      }
      return;
    }
    if (!this.socket || this.socket.destroyed) return;
    if (
      message.tiles.length < 1 ||
      message.tiles.length > 64 ||
      message.frameWidth < 1 ||
      message.frameHeight < 1 ||
      message.frameWidth > 4096 ||
      message.frameHeight > 4096
    ) {
      throw new Error("FRAME_BOUNDS_INVALID");
    }
    let totalBytes = 0;
    const renderedAt = timestampFromDate(new Date(message.renderedAt));
    for (const [index, tile] of message.tiles.entries()) {
      const png = Buffer.from(tile.pngBase64, "base64");
      totalBytes += png.byteLength;
      if (
        png.byteLength > MAX_TILE_BYTES ||
        totalBytes > MAX_FRAME_BYTES ||
        tile.width < 1 ||
        tile.height < 1 ||
        tile.width > 512 ||
        tile.height > 512 ||
        tile.x + tile.width > message.frameWidth ||
        tile.y + tile.height > message.frameHeight ||
        !png.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]))
      ) {
        throw new Error("FRAME_TILE_INVALID");
      }
      const value = create(GreenfieldWindowFrameTileSchema, {
        windowId: message.windowId,
        workloadGeneration: this.options.generation,
        windowRevision: BigInt(message.windowRevision),
        frameSequence: BigInt(message.frameSequence),
        tileIndex: index,
        tileCount: message.tiles.length,
        x: tile.x,
        y: tile.y,
        width: tile.width,
        height: tile.height,
        png,
        fullRefresh: message.fullRefresh,
        frameWidth: message.frameWidth,
        frameHeight: message.frameHeight,
        renderedAt,
      });
      await this.send(this.envelope({ case: "frameTile", value }));
    }
  }

  private async handleInput(envelope: GreenfieldChildEnvelope, socket: Socket): Promise<void> {
    if (envelope.payload.case !== "input") return;
    const request = envelope.payload.value;
    const event = request.events.at(0);
    if (
      request.events.length !== 1 ||
      !event ||
      !request.attachmentId ||
      request.sessionId !== this.options.sessionId ||
      request.expectedWorkloadGeneration !== this.options.generation
    ) {
      throw new Error("IPC_INPUT_INVALID");
    }
    const fingerprint = createHash("sha256")
      .update(toBinary(GreenfieldWindowInputEventSchema, event))
      .digest("hex");
    const status = this.ledger.inspect(
      request.attachmentId,
      this.options.generation,
      event,
      fingerprint,
    );
    let verdict = GreenfieldInputVerdict.APPLIED;
    let rejectedSequence = 0n;
    if (status === "invalid") {
      verdict = GreenfieldInputVerdict.INVALID_SEQUENCE;
      rejectedSequence = event.sequence;
    } else if (status === "uncertain") {
      verdict = GreenfieldInputVerdict.UNAVAILABLE;
      rejectedSequence = event.sequence;
    } else if (status === "new") {
      try {
        if (!this.page) throw new Error("CHILD_PAGE_UNAVAILABLE");
        const payload = this.browserInput(event);
        // If the browser throws after a native side effect, a broker retry
        // must fail closed. Only a fully returned call advances lastApplied.
        this.ledger.markAttempt(
          request.attachmentId,
          this.options.generation,
          event.sequence,
          fingerprint,
        );
        await this.page.evaluate((input) => {
          window.workosChildApplyInput(input);
        }, payload);
        this.ledger.accept(
          request.attachmentId,
          this.options.generation,
          event.sequence,
          fingerprint,
        );
      } catch {
        verdict = GreenfieldInputVerdict.UNAVAILABLE;
        rejectedSequence = event.sequence;
      }
    }
    const result = create(SendGreenfieldWindowInputResponseSchema, {
      verdict,
      lastAppliedSequence: this.ledger.lastApplied(request.attachmentId, this.options.generation),
      rejectedSequence,
    });
    await this.send(
      this.envelope({ case: "inputResult", value: result }, envelope.requestId),
      socket,
    );
  }

  private browserInput(event: GreenfieldWindowInputEvent) {
    const variant = event.event;
    if (!variant.case) throw new Error("INPUT_EVENT_INVALID");
    if (variant.case === "clipboardWrite") {
      if (variant.value.textUtf8.byteLength > MAX_TEXT_BYTES)
        throw new Error("SELECTION_TOO_LARGE");
      return {
        windowId: event.windowId,
        event: { case: variant.case, value: { text: textDecoder.decode(variant.value.textUtf8) } },
      };
    }
    return { windowId: event.windowId, event: variant };
  }

  private async handleClipboard(envelope: GreenfieldChildEnvelope, socket: Socket): Promise<void> {
    if (envelope.payload.case !== "clipboardRead") return;
    const request = envelope.payload.value;
    if (
      !request.attachmentId ||
      request.sessionId !== this.options.sessionId ||
      request.expectedWorkloadGeneration !== this.options.generation
    ) {
      throw new Error("IPC_CLIPBOARD_INVALID");
    }
    if (!this.page) throw new Error("CHILD_PAGE_UNAVAILABLE");
    try {
      const text = await this.page.evaluate(() => window.workosChildReadClipboard());
      const raw = Buffer.from(text, "utf8");
      if (raw.byteLength > MAX_TEXT_BYTES) throw new Error("SELECTION_TOO_LARGE");
      const result = create(ReadGreenfieldClipboardResponseSchema, { textUtf8: raw });
      await this.send(
        this.envelope({ case: "clipboardResult", value: result }, envelope.requestId),
        socket,
      );
    } catch {
      await this.failure("CLIPBOARD_UNAVAILABLE", envelope.requestId, socket);
    }
  }

  private async handleRequest(envelope: GreenfieldChildEnvelope, socket: Socket): Promise<void> {
    if (
      envelope.sessionId !== this.options.sessionId ||
      envelope.workloadGeneration !== this.options.generation ||
      envelope.requestId === 0n
    ) {
      throw new Error("IPC_IDENTITY_MISMATCH");
    }
    if (envelope.payload.case === "input") await this.handleInput(envelope, socket);
    else if (envelope.payload.case === "clipboardRead")
      await this.handleClipboard(envelope, socket);
    else throw new Error("IPC_REQUEST_INVALID");
  }

  private async failure(code: string, requestId = 0n, socket?: Socket): Promise<void> {
    const value = create(GreenfieldChildFailureSchema, { reasonCode: safeCode(code) });
    await this.send(this.envelope({ case: "failure", value }, requestId), socket);
  }

  private async fatal(code: string): Promise<void> {
    if (this.terminalState !== undefined || this.ended) return;
    try {
      await this.failure(code);
    } finally {
      this.stopRun(safeCode(code));
    }
  }

  private watchApplication(pid: number, starttime: string): void {
    this.applicationMonitor = setInterval(() => {
      if (this.applicationPollInFlight || this.ended || this.terminalState !== undefined) return;
      this.applicationPollInFlight = true;
      void (async () => {
        try {
          const current = await readProcessIdentity(pid);
          if (processAlive(starttime, current)) {
            this.applicationMissingSince = undefined;
          } else if (this.applicationMissingSince === undefined) {
            this.applicationMissingSince = Date.now();
          } else if (Date.now() - this.applicationMissingSince >= 1500) {
            await this.fatal("APPLICATION_PROCESS_LOST");
          }
        } catch {
          await this.fatal("APPLICATION_PROCESS_UNAVAILABLE");
        } finally {
          this.applicationPollInFlight = false;
        }
      })();
    }, 250);
  }

  private async connectSocket(): Promise<void> {
    if (this.ended || this.reconnecting || this.socket) return;
    this.reconnecting = true;
    try {
      const socket = createConnection(this.options.socket);
      await Promise.race([
        once(socket, "connect"),
        once(socket, "error").then(() => {
          throw new Error("IPC_CONNECT_FAILED");
        }),
      ]);
      this.socket = socket;
      const reader = new RecordReader((envelope) => {
        this.requestChain = this.requestChain
          .then(() => this.handleRequest(envelope, socket))
          .catch(async () => {
            await this.failure("IPC_REQUEST_INVALID", 0n, socket);
            socket.destroy();
          });
      });
      socket.on("data", (chunk: Buffer) => {
        try {
          reader.push(chunk);
        } catch {
          socket.destroy();
        }
      });
      socket.once("close", () => {
        if (this.socket === socket) this.socket = undefined;
        if (!this.ended) setTimeout(() => void this.connectSocket(), 250);
      });
      await this.sendSnapshot();
      const page = this.pageIfReady();
      if (page) await page.evaluate(() => window.workosChildForceFrames());
    } catch {
      if (!this.isEnded()) setTimeout(() => void this.connectSocket(), 250);
    } finally {
      this.reconnecting = false;
    }
  }

  async start(): Promise<void> {
    checkIsolation(this.options);
    if (!existsSync(dirname(this.options.socket))) throw new Error("IPC_SOCKET_UNAVAILABLE");
    void this.connectSocket();
    const pageServer = await servePage();
    try {
      const proxy = await launchProxy(this.options, pageServer.port);
      this.proxy = proxy.process;
      this.proxy.once("exit", () => {
        if (!this.ended) void this.fatal("GREENFIELD_PROXY_EXITED");
      });
      const applicationPid = Number((proxy.launch as { pid: string }).pid);
      const identity = await readProcessIdentity(applicationPid);
      if (!identity || !processAlive(identity.starttime, identity))
        throw new Error("APPLICATION_PROCESS_LOST");
      this.watchApplication(applicationPid, identity.starttime);
      const browser = await chromium.launch({
        headless: true,
        args: ["--no-sandbox", "--enable-webgl", "--ignore-gpu-blocklist"],
      });
      this.browser = browser;
      browser.on("disconnected", () => {
        if (!this.ended) void this.fatal("CHROMIUM_EXITED");
      });
      const context = await browser.newContext({
        viewport: { width: this.options.width, height: this.options.height },
        deviceScaleFactor: 1,
      });
      const page = await context.newPage();
      this.page = page;
      page.on("pageerror", () => {
        if (!this.ended) void this.fatal("CHILD_PAGE_FAILED");
      });
      await page.exposeBinding("workosPush", async (_source, message: BrowserMessage) => {
        try {
          await this.handleBrowserMessage(message);
        } catch {
          await this.fatal("CHILD_MEDIA_INVALID");
        }
      });
      const origin = `http://127.0.0.1:${String(pageServer.port)}`;
      const launchUrl = proxy.launchUrl;
      await page.route(launchUrl, async (route) => {
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          headers: {
            "access-control-allow-origin": origin,
            "access-control-allow-credentials": "true",
          },
          body: JSON.stringify(proxy.launch),
        });
      });
      await page.addInitScript({
        content: `window.workosChildConfig = ${JSON.stringify({
          compositorSessionId: "workos",
          launchUrl,
          width: this.options.width,
          height: this.options.height,
          devicePixelRatioMillis: 1000,
        })}`,
      });
      await page.goto(origin, { waitUntil: "load", timeout: 30_000 });
      await page.waitForFunction(() => window.workosChildReady, null, { timeout: 30_000 });
      await this.sendSnapshot();
      await page.evaluate(() => window.workosChildForceFrames());
      const reason = await Promise.race([
        this.stopped,
        once(process, "SIGTERM").then(() => "SIGTERM"),
      ]);
      // A clean application Exit is a workload terminal event, not a child
      // crash. The terminal snapshot was sent above; Runtime reaps the exact
      // child after it persists the stopped state.
      if (reason !== "SIGTERM" && reason !== "APPLICATION_STOPPED") throw new Error(reason);
    } finally {
      this.ended = true;
      if (this.applicationMonitor) clearInterval(this.applicationMonitor);
      this.socket?.destroy();
      await this.browser?.close().catch(() => undefined);
      this.proxy?.kill("SIGTERM");
      await pageServer.close();
    }
  }
}

async function main(): Promise<void> {
  try {
    const bridge = new ChildBridge(parseOptions(process.argv.slice(2)));
    await bridge.start();
  } catch (error) {
    console.error(error instanceof Error ? safeCode(error.message) : "CHILD_COMPOSITOR_FAILED");
    process.exitCode = 1;
  }
}

void main();
