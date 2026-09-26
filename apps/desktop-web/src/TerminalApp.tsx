import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import { LifecycleMode, SurfaceRenderer, type SurfaceAttachment } from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import {
  MAX_TERMINAL_CLIPBOARD_BYTES,
  terminalClipboardChunks,
  terminalSelection,
} from "./terminalClipboard.js";

function timestampMillis(value?: { seconds: bigint; nanos: number }): number {
  return value ? Number(value.seconds) * 1000 + Math.floor(value.nanos / 1_000_000) : 0;
}

class ClipboardReadDenied extends Error {}
type InputContent = { text: string; denied?: false } | { text: ""; denied: true };

// TerminalApp consumes the supervised PTY sessions (ADR-0028): one owner-
// scoped real login shell per window, bounded output polling, resize on
// container changes, and an explicit unavailable verdict without the pool.
//
// ADR-0031 (B08): closing the window DETACHES — the shell keeps running
// under its bounded policy — and the explicit Stop control is the only
// stop. Re-opening discovers the project's live terminal workload through
// ListProjectSurfaces and attaches the SAME shell instead of starting a
// second one; input stays disabled until this device holds control.
export function TerminalApp(props: {
  workosClients?: WorkOSClients;
  activeProjectId?: string;
  workloadId?: string | undefined;
  expectedWorkloadGeneration?: bigint | undefined;
}) {
  const [attachAttempt, setAttachAttempt] = useState(0);
  const [sessionId, setSessionId] = useState("");
  const [output, setOutput] = useState("");
  const [verdict, setVerdict] = useState("");
  const [closed, setClosed] = useState(false);
  const [controls, setControls] = useState(true);
  const [stopping, setStopping] = useState(false);
  const [clipboardResult, setClipboardResult] = useState("");
  const cursorRef = useRef(0n);
  const decoderRef = useRef(new TextDecoder());
  const controlsRef = useRef(true);
  const controlGeneration = useRef(0n);
  const attachmentRef = useRef<{
    id: string;
    workloadGeneration: bigint;
    expiresAt: number;
  } | null>(null);
  const writeRevisionRef = useRef(0);
  // Terminal input is order-sensitive: writes serialize through one promise
  // chain so concurrent per-key RPCs cannot transpose characters.
  const writeChainRef = useRef<Promise<unknown>>(Promise.resolve());
  const preRef = useRef<HTMLPreElement | null>(null);
  const clients = props.workosClients;
  const projectId = props.activeProjectId ?? "";

  const loseControl = useCallback(() => {
    controlsRef.current = false;
    writeRevisionRef.current++;
    setControls(false);
    setClipboardResult("Input disabled: terminal control was lost. Take control to continue.");
  }, []);

  const recordAttachment = useCallback(
    (attachment: SurfaceAttachment | undefined, workloadGeneration: bigint) => {
      controlGeneration.current = attachment?.controlGeneration ?? 0n;
      controlsRef.current = attachment?.controls ?? false;
      attachmentRef.current = attachment?.id
        ? {
            id: attachment.id,
            workloadGeneration,
            expiresAt: timestampMillis(attachment.controlExpiresAt),
          }
        : null;
      setControls(controlsRef.current);
    },
    [],
  );

  const canWrite = useCallback(() => {
    if (!controlsRef.current || closed) return false;
    const expiresAt = attachmentRef.current?.expiresAt ?? 0;
    if (expiresAt > 0 && Date.now() >= expiresAt) {
      loseControl();
      return false;
    }
    return true;
  }, [closed, loseControl]);

  useEffect(() => {
    if (!clients || !projectId || sessionId) return;
    let cancelled = false;
    let retry: ReturnType<typeof setTimeout> | undefined;
    // Read through a closure: control-flow narrowing must not constant-fold
    // the flag while the cleanup closure can still flip it.
    const isCancelled = () => cancelled;
    const open = async () => {
      // Attach to the live terminal workload for this project when one
      // exists; only a project without one creates a new shell. A host
      // without surface continuity degrades honestly to the create path.
      try {
        if (props.workloadId) {
          const attached = await clients.surfaceContinuity.attachSurface({
            workloadId: props.workloadId,
            expectedWorkloadGeneration: props.expectedWorkloadGeneration ?? 0n,
            idempotencyKey: `desktop-terminal-attach-${crypto.randomUUID()}`,
          });
          if (isCancelled()) {
            if (attached.session?.id)
              void clients.ptySessions
                .detachPtySession({ sessionId: attached.session.id })
                .catch(() => undefined);
            return;
          }
          recordAttachment(
            attached.attachment,
            attached.session?.workloadGeneration ?? props.expectedWorkloadGeneration ?? 0n,
          );
          setSessionId(attached.session?.id ?? props.workloadId);
          return;
        }
        const listed = await clients.surfaceContinuity.listProjectSurfaces({ projectId });
        if (isCancelled()) return;
        const live = listed.workloads.find(
          (workload) =>
            workload.state === "running" &&
            workload.appInstanceId === "" &&
            workload.renderer === SurfaceRenderer.UNSPECIFIED,
        );
        if (live) {
          const attached = await clients.surfaceContinuity.attachSurface({
            workloadId: live.workloadId,
            idempotencyKey: `desktop-terminal-attach-${crypto.randomUUID()}`,
          });
          if (isCancelled()) return;
          recordAttachment(attached.attachment, attached.session?.workloadGeneration ?? 0n);
          setSessionId(attached.session?.id ?? live.workloadId);
          return;
        }
      } catch (error) {
        if (isCancelled()) return;
        if (
          error instanceof ConnectError &&
          [
            Code.NotFound,
            Code.PermissionDenied,
            Code.Unauthenticated,
            Code.FailedPrecondition,
          ].includes(error.code)
        ) {
          setClosed(true);
          setVerdict(
            "This terminal is stopped or no longer accessible. Open Running apps to inspect it.",
          );
        } else {
          setVerdict("Connection interrupted. Reconnecting to this terminal…");
          retry = setTimeout(() => {
            setAttachAttempt((attempt) => attempt + 1);
          }, 1500);
        }
        return;
      }
      try {
        const created = await clients.ptySessions.createPtySession({
          idempotencyKey: `desktop-terminal-${crypto.randomUUID()}`,
          projectId,
          columns: 90,
          rows: 26,
          lifecycleMode: LifecycleMode.MANUAL_STOP,
        });
        if (isCancelled()) return;
        if (created.session?.id) {
          const attached = await clients.surfaceContinuity.attachSurface({
            workloadId: created.session.id,
            idempotencyKey: `desktop-terminal-attach-${crypto.randomUUID()}`,
          });
          if (isCancelled()) return;
          recordAttachment(attached.attachment, attached.session?.workloadGeneration ?? 0n);
          setSessionId(created.session.id);
        }
      } catch {
        setVerdict("Terminal sessions are unavailable in this deployment.");
      }
    };
    void open();
    return () => {
      cancelled = true;
      clearTimeout(retry);
    };
  }, [
    clients,
    projectId,
    sessionId,
    props.workloadId,
    props.expectedWorkloadGeneration,
    attachAttempt,
    recordAttachment,
  ]);

  // Compare-and-extend the current lease only. A stale tab or another device
  // taking over moves this view to observer; it never requests control here.
  useEffect(() => {
    if (!clients || !sessionId || !controls || closed) return;
    let stopped = false;
    let renewing = false;
    const timer = window.setInterval(() => {
      const attachment = attachmentRef.current;
      if (!attachment?.id || !attachment.expiresAt || !controlsRef.current || renewing) return;
      if (Date.now() >= attachment.expiresAt) {
        loseControl();
        return;
      }
      renewing = true;
      const epoch = controlGeneration.current;
      void clients.surfaceContinuity
        .renewSurfaceControl({
          surfaceSessionId: sessionId,
          attachmentId: attachment.id,
          expectedControlGeneration: epoch,
          expectedWorkloadGeneration: attachment.workloadGeneration,
        })
        .then((response) => {
          if (stopped || !controlsRef.current || controlGeneration.current !== epoch) return;
          const renewed = response.attachment;
          if (!renewed?.controls || renewed.id !== attachment.id || !renewed.controlExpiresAt) {
            loseControl();
            return;
          }
          attachment.expiresAt = timestampMillis(renewed.controlExpiresAt);
        })
        .catch(() => {
          if (!stopped && controlsRef.current && controlGeneration.current === epoch) loseControl();
        })
        .finally(() => {
          renewing = false;
        });
    }, 15_000);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [clients, sessionId, controls, closed, loseControl]);

  // Poll bounded output strictly after the cursor; closed sessions stop.
  useEffect(() => {
    if (!clients || !sessionId || closed) return;
    let stopped = false;
    const isStopped = () => stopped;
    let polling = false;
    const poll = async () => {
      if (polling || isStopped()) return;
      polling = true;
      try {
        const read = await clients.ptySessions.readPtySession({
          sessionId,
          after: cursorRef.current,
          maxBytes: 65536,
        });
        if (isStopped()) return;
        if (read.output.length > 0) {
          const text = decoderRef.current.decode(read.output, { stream: true });
          if (text) setOutput((current) => (current + text).slice(-131072));
        }
        cursorRef.current = read.cursor;
        if (read.closed) {
          const tail = decoderRef.current.decode();
          if (tail) setOutput((current) => (current + tail).slice(-131072));
          writeRevisionRef.current++;
          controlsRef.current = false;
          setControls(false);
          setClosed(true);
          return;
        }
        setVerdict("");
      } catch (error) {
        if (isStopped()) return;
        if (
          error instanceof ConnectError &&
          [
            Code.NotFound,
            Code.PermissionDenied,
            Code.Unauthenticated,
            Code.FailedPrecondition,
          ].includes(error.code)
        ) {
          setClosed(true);
          setControls(false);
          controlsRef.current = false;
          writeRevisionRef.current++;
          setVerdict("This terminal is stopped or no longer accessible.");
        } else setVerdict("Connection interrupted. Reconnecting to this terminal…");
      } finally {
        polling = false;
      }
    };
    const timer = window.setInterval(() => void poll(), 300);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [clients, sessionId, closed]);

  // Closing the window DETACHES instead of stopping: the shell keeps
  // running under its bounded policy (ADR-0031); the explicit Stop control
  // is the only stop. Detaching a session this window never attached just
  // leaves it running — the same end state — so the call is best-effort.
  useEffect(() => {
    return () => {
      if (sessionId && clients) {
        writeRevisionRef.current++;
        controlsRef.current = false;
        void clients.ptySessions.detachPtySession({ sessionId }).then(
          () => undefined,
          () => undefined,
        );
      }
    };
  }, [sessionId, clients]);

  useEffect(() => {
    if (preRef.current) {
      preRef.current.scrollTop = preRef.current.scrollHeight;
    }
  }, [output]);

  const takeControl = useCallback(() => {
    if (!clients || !sessionId) return;
    void clients.surfaceContinuity
      .requestSurfaceControl({ surfaceSessionId: sessionId })
      .then((response) => {
        recordAttachment(
          response.attachment,
          attachmentRef.current?.workloadGeneration ?? props.expectedWorkloadGeneration ?? 0n,
        );
        if (controlsRef.current) setClipboardResult("Terminal control acquired.");
        else setClipboardResult("Terminal control is unavailable.");
      })
      .catch(() => {
        setClipboardResult("Could not take terminal control. Try again.");
      });
  }, [clients, sessionId, props.expectedWorkloadGeneration, recordAttachment]);

  const stopWorkload = useCallback(() => {
    if (!clients || !sessionId || stopping) return;
    setStopping(true);
    void clients.surfaceContinuity
      .stopSurfaceWorkload({
        workloadId: sessionId,
        actionKey: `desktop-terminal-stop-${crypto.randomUUID()}`,
      })
      .then(() => {
        writeRevisionRef.current++;
        controlsRef.current = false;
        setControls(false);
        setClosed(true);
        setVerdict("The shell was stopped.");
      })
      .catch(() => {
        setVerdict("The shell could not be stopped. Try again.");
      })
      .finally(() => {
        setStopping(false);
      });
  }, [clients, sessionId, stopping]);

  const sendInput = useCallback(
    async (content: InputContent | Promise<InputContent>, source: "key" | "paste") => {
      if (!clients || !sessionId || !canWrite()) {
        if (source === "paste")
          setClipboardResult("Paste failed: terminal control is unavailable.");
        return false;
      }
      const epoch = controlGeneration.current;
      const revision = writeRevisionRef.current;
      if (source === "paste") setClipboardResult("Pasting text into the terminal…");
      const write = writeChainRef.current.then(async () => {
        const value = await content;
        if (value.denied) throw new ClipboardReadDenied();
        const chunks = terminalClipboardChunks(value.text);
        for (const chunk of chunks) {
          if (
            writeRevisionRef.current !== revision ||
            controlGeneration.current !== epoch ||
            !canWrite()
          )
            throw new Error("terminal control changed");
          await clients.ptySessions.writePtySession({
            sessionId,
            input: chunk,
            controlGeneration: epoch,
          });
        }
      });
      // Keep later gestures ordered even after a rejected RPC, but invalidate
      // every earlier queued gesture when any chunk fails.
      writeChainRef.current = write.catch(() => undefined);
      try {
        await write;
        if (source === "paste") setClipboardResult("Pasted text into the terminal.");
        return true;
      } catch (error) {
        if (error instanceof ClipboardReadDenied) {
          setClipboardResult("Paste failed: browser denied clipboard access.");
          return false;
        }
        if (error instanceof RangeError) {
          if (source === "paste")
            setClipboardResult(
              `Paste failed: text must be nonempty, contain no NUL, and be at most ${String(MAX_TERMINAL_CLIPBOARD_BYTES / 1024)} KiB.`,
            );
          return false;
        }
        writeRevisionRef.current++;
        if (
          error instanceof ConnectError &&
          [Code.PermissionDenied, Code.FailedPrecondition, Code.NotFound].includes(error.code)
        )
          loseControl();
        setClipboardResult(
          source === "paste"
            ? "Paste was not confirmed. Check the output and control owner before retrying."
            : "Input was not confirmed. Check the connection and control owner before typing again.",
        );
        return false;
      }
    },
    [clients, sessionId, canWrite, loseControl],
  );

  const copySelection = useCallback(async () => {
    const selected = terminalSelection(preRef.current);
    if (!selected) {
      setClipboardResult("Copy failed: select terminal output first.");
      return;
    }
    if (new TextEncoder().encode(selected).length > MAX_TERMINAL_CLIPBOARD_BYTES) {
      setClipboardResult("Copy failed: terminal selection exceeds 256 KiB.");
      return;
    }
    const clipboard = (navigator as { clipboard?: Clipboard }).clipboard;
    if (!clipboard?.writeText) {
      setClipboardResult("Copy failed: browser clipboard is unavailable.");
      return;
    }
    try {
      await clipboard.writeText(selected);
      setClipboardResult("Copied terminal selection to the browser clipboard.");
    } catch {
      setClipboardResult("Copy failed: browser denied clipboard access.");
    }
  }, []);

  const pasteClipboard = useCallback(() => {
    if (!canWrite()) {
      setClipboardResult("Paste failed: terminal control is unavailable.");
      return;
    }
    const clipboard = (navigator as { clipboard?: Clipboard }).clipboard;
    if (!clipboard?.readText) {
      setClipboardResult("Paste failed: browser clipboard is unavailable.");
      return;
    }
    // Reserve the write queue position before the asynchronous browser read
    // completes, so keys typed after Ctrl+V cannot overtake the pasted text.
    try {
      const content: Promise<InputContent> = clipboard.readText().then(
        (text) => ({ text }),
        () => ({ text: "", denied: true }),
      );
      void sendInput(content, "paste");
    } catch {
      setClipboardResult("Paste failed: browser denied clipboard access.");
    }
  }, [canWrite, sendInput]);

  if (verdict && !sessionId) {
    return (
      <div className="terminal-app" data-testid="terminal-app">
        <p className="empty-state" role="status" data-testid="terminal-unavailable">
          {verdict}
        </p>
      </div>
    );
  }

  return (
    <div className="terminal-app" data-testid="terminal-app">
      <div className="terminal-toolbar">
        <span className="terminal-toolbar-state" data-testid="terminal-controls-state">
          {closed ? "stopped" : controls ? "controlling" : "observer"}
        </span>
        <Button data-testid="terminal-copy" onClick={() => void copySelection()} type="button">
          Copy selection
        </Button>
        <Button
          data-testid="terminal-paste"
          disabled={!controls || closed}
          onClick={pasteClipboard}
          type="button"
        >
          Paste
        </Button>
        {!controls && !closed ? (
          <Button data-testid="terminal-take-control" onClick={takeControl} type="button">
            Take control
          </Button>
        ) : null}
        {!closed ? (
          <Button
            className="terminal-stop"
            data-testid="terminal-stop"
            disabled={stopping}
            onClick={stopWorkload}
            type="button"
          >
            {stopping ? "Stopping…" : "Stop"}
          </Button>
        ) : null}
      </div>
      <pre
        aria-label="Terminal output"
        className="terminal-output"
        data-testid="terminal-output"
        ref={preRef}
        tabIndex={0}
        onKeyDown={(event) => {
          const key = event.key.toLowerCase();
          if ((event.ctrlKey || event.metaKey) && !event.altKey && key === "c") {
            event.preventDefault();
            if (terminalSelection(preRef.current)) void copySelection();
            else if (event.ctrlKey && !event.metaKey && canWrite())
              void sendInput({ text: "\x03" }, "key");
            else setClipboardResult("Copy failed: select terminal output first.");
            return;
          }
          if ((event.ctrlKey || event.metaKey) && !event.altKey && key === "v") {
            event.preventDefault();
            pasteClipboard();
            return;
          }
          if (event.ctrlKey || event.metaKey || event.altKey) return;
          if (!canWrite()) {
            event.preventDefault();
            return;
          }
          if (event.key === "Enter") {
            event.preventDefault();
            void sendInput({ text: "\r" }, "key");
          } else if (event.key === "Backspace") {
            event.preventDefault();
            void sendInput({ text: "\x7f" }, "key");
          } else if (event.key === "ArrowUp") {
            event.preventDefault();
            void sendInput({ text: "\x1b[A" }, "key");
          } else if (event.key === "ArrowDown") {
            event.preventDefault();
            void sendInput({ text: "\x1b[B" }, "key");
          } else if (event.key === "ArrowLeft") {
            event.preventDefault();
            void sendInput({ text: "\x1b[D" }, "key");
          } else if (event.key === "ArrowRight") {
            event.preventDefault();
            void sendInput({ text: "\x1b[C" }, "key");
          } else if (event.key.length === 1) {
            event.preventDefault();
            void sendInput({ text: event.key }, "key");
          }
        }}
        onCopy={(event) => {
          const selected = terminalSelection(preRef.current);
          if (!selected) return;
          if (new TextEncoder().encode(selected).length > MAX_TERMINAL_CLIPBOARD_BYTES) {
            event.preventDefault();
            setClipboardResult("Copy failed: terminal selection exceeds 256 KiB.");
            return;
          }
          event.clipboardData.setData("text/plain", selected);
          event.preventDefault();
          setClipboardResult("Copied terminal selection to the browser clipboard.");
        }}
        onPaste={(event) => {
          event.preventDefault();
          if (!canWrite()) {
            setClipboardResult("Paste failed: terminal control is unavailable.");
            return;
          }
          void sendInput({ text: event.clipboardData.getData("text/plain") }, "paste");
        }}
      >
        {output}
      </pre>
      {clipboardResult ? (
        <p className="terminal-verdict" data-testid="terminal-clipboard-result" role="status">
          {clipboardResult}
        </p>
      ) : null}
      {verdict && !closed ? (
        <p className="terminal-verdict" role="status">
          {verdict}
        </p>
      ) : null}
      {closed ? (
        <p className="terminal-verdict" role="status">
          {verdict || "The shell exited."} Close this window or stop it from Running apps.
        </p>
      ) : null}
      {!controls && !closed ? (
        <p className="terminal-control-hint" role="status">
          Input is disabled: another device holds control.
        </p>
      ) : null}
    </div>
  );
}
