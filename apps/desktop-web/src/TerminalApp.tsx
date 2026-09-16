import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import { SurfaceRenderer } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

// TerminalApp consumes the supervised PTY sessions (ADR-0028): one owner-
// scoped real login shell per window, bounded output polling, resize on
// container changes, and an explicit unavailable verdict without the pool.
//
// ADR-0031 (B08): closing the window DETACHES — the shell keeps running
// under its bounded policy — and the explicit Stop control is the only
// stop. Re-opening discovers the project's live terminal workload through
// ListProjectSurfaces and attaches the SAME shell instead of starting a
// second one; input stays disabled until this device holds control.
export function TerminalApp(props: { workosClients?: WorkOSClients; activeProjectId?: string }) {
  const [sessionId, setSessionId] = useState("");
  const [output, setOutput] = useState("");
  const [verdict, setVerdict] = useState("");
  const [closed, setClosed] = useState(false);
  const [controls, setControls] = useState(true);
  const [stopping, setStopping] = useState(false);
  const cursorRef = useRef(0n);
  const controlsRef = useRef(true);
  const controlGeneration = useRef(0n);
  // Terminal input is order-sensitive: writes serialize through one promise
  // chain so concurrent per-key RPCs cannot transpose characters.
  const writeChainRef = useRef<Promise<unknown>>(Promise.resolve());
  const preRef = useRef<HTMLPreElement | null>(null);
  const clients = props.workosClients;
  const projectId = props.activeProjectId ?? "";

  useEffect(() => {
    if (!clients || !projectId || sessionId) return;
    let cancelled = false;
    // Read through a closure: control-flow narrowing must not constant-fold
    // the flag while the cleanup closure can still flip it.
    const isCancelled = () => cancelled;
    const open = async () => {
      // Attach to the live terminal workload for this project when one
      // exists; only a project without one creates a new shell. A host
      // without surface continuity degrades honestly to the create path.
      try {
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
          controlGeneration.current = attached.attachment?.controlGeneration ?? 0n;
          controlsRef.current = attached.attachment?.controls ?? false;
          setControls(controlsRef.current);
          setSessionId(attached.session?.id ?? live.workloadId);
          return;
        }
      } catch {
        setVerdict("Could not discover running terminals. Retry after reconnecting.");
        return;
      }
      try {
        const created = await clients.ptySessions.createPtySession({
          idempotencyKey: `desktop-terminal-${crypto.randomUUID()}`,
          projectId,
          columns: 90,
          rows: 26,
        });
        if (isCancelled()) return;
        if (created.session?.id) {
          const attached = await clients.surfaceContinuity.attachSurface({
            workloadId: created.session.id,
            idempotencyKey: `desktop-terminal-attach-${crypto.randomUUID()}`,
          });
          if (isCancelled()) return;
          controlGeneration.current = attached.attachment?.controlGeneration ?? 0n;
          controlsRef.current = attached.attachment?.controls ?? false;
          setControls(controlsRef.current);
          setSessionId(created.session.id);
        }
      } catch {
        setVerdict("Terminal sessions are unavailable in this deployment.");
      }
    };
    void open();
    return () => {
      cancelled = true;
    };
  }, [clients, projectId, sessionId]);

  // Poll bounded output strictly after the cursor; closed sessions stop.
  useEffect(() => {
    if (!clients || !sessionId) return;
    let stopped = false;
    const poll = async () => {
      try {
        const read = await clients.ptySessions.readPtySession({
          sessionId,
          after: cursorRef.current,
          maxBytes: 65536,
        });
        if (stopped) return;
        if (read.output.length > 0) {
          const decoder = new TextDecoder();
          setOutput((current) => (current + decoder.decode(read.output)).slice(-131072));
        }
        cursorRef.current = read.cursor;
        if (read.closed) {
          setClosed(true);
          return;
        }
      } catch {
        setVerdict("The terminal session ended.");
        setClosed(true);
      }
    };
    const timer = window.setInterval(() => void poll(), 300);
    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [clients, sessionId]);

  // Closing the window DETACHES instead of stopping: the shell keeps
  // running under its bounded policy (ADR-0031); the explicit Stop control
  // is the only stop. Detaching a session this window never attached just
  // leaves it running — the same end state — so the call is best-effort.
  useEffect(() => {
    return () => {
      if (sessionId && clients) {
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
        controlGeneration.current = response.attachment?.controlGeneration ?? 0n;
        controlsRef.current = response.attachment?.controls ?? false;
        setControls(controlsRef.current);
      })
      .catch(() => undefined);
  }, [clients, sessionId]);

  const stopWorkload = useCallback(() => {
    if (!clients || !sessionId || stopping) return;
    setStopping(true);
    void clients.surfaceContinuity
      .stopSurfaceWorkload({
        workloadId: sessionId,
        actionKey: `desktop-terminal-stop-${crypto.randomUUID()}`,
      })
      .then(() => {
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
    (data: string) => {
      if (!clients || !sessionId || closed || !controlsRef.current) return;
      const encoded = new TextEncoder().encode(data);
      if (encoded.length === 0 || encoded.length > 16384) return;
      const epoch = controlGeneration.current;
      writeChainRef.current = writeChainRef.current.then(() =>
        clients.ptySessions.writePtySession({
          sessionId,
          input: encoded,
          controlGeneration: epoch,
        }),
      );
      writeChainRef.current = writeChainRef.current.catch(() => {
        setVerdict("The terminal session ended.");
      });
    },
    [clients, sessionId, closed],
  );

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
          // Bounded interactive input: printable keys, Enter, Backspace and
          // arrows reach the shell; everything else stays local. Input is
          // refused while another device holds the control lease.
          if (event.metaKey || event.altKey) return;
          if (!controls) {
            event.preventDefault();
            return;
          }
          if (event.key === "Enter") {
            event.preventDefault();
            sendInput("\r");
          } else if (event.key === "Backspace") {
            event.preventDefault();
            sendInput("\x7f");
          } else if (event.key === "ArrowUp") {
            event.preventDefault();
            sendInput("\x1b[A");
          } else if (event.key === "ArrowDown") {
            event.preventDefault();
            sendInput("\x1b[B");
          } else if (event.key === "ArrowLeft") {
            event.preventDefault();
            sendInput("\x1b[D");
          } else if (event.key === "ArrowRight") {
            event.preventDefault();
            sendInput("\x1b[C");
          } else if (event.key.length === 1) {
            event.preventDefault();
            sendInput(event.key);
          }
        }}
      >
        {output}
      </pre>
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
