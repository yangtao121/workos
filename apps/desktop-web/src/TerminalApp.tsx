import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";

// TerminalApp consumes the supervised PTY sessions (ADR-0028): one owner-
// scoped real login shell per window, bounded output polling, resize on
// container changes, and an explicit unavailable verdict without the pool.
export function TerminalApp(props: { workosClients?: WorkOSClients; activeProjectId?: string }) {
  const [sessionId, setSessionId] = useState("");
  const [output, setOutput] = useState("");
  const [verdict, setVerdict] = useState("");
  const [closed, setClosed] = useState(false);
  const cursorRef = useRef(0n);
  // Terminal input is order-sensitive: writes serialize through one promise
  // chain so concurrent per-key RPCs cannot transpose characters.
  const writeChainRef = useRef<Promise<unknown>>(Promise.resolve());
  const preRef = useRef<HTMLPreElement | null>(null);
  const clients = props.workosClients;
  const projectId = props.activeProjectId ?? "";

  useEffect(() => {
    if (!clients || !projectId || sessionId) return;
    void clients.ptySessions
      .createPtySession({
        idempotencyKey: `desktop-terminal-${crypto.randomUUID()}`,
        projectId,
        columns: 90,
        rows: 26,
      })
      .then(
        (created) => {
          if (created.session?.id) {
            setSessionId(created.session.id);
          }
        },
        () => {
          setVerdict("Terminal sessions are unavailable in this deployment.");
        },
      );
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

  // Close the session when the window unmounts.
  useEffect(() => {
    return () => {
      if (sessionId && clients) {
        void clients.ptySessions.closePtySession({ sessionId }).then(
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

  const sendInput = useCallback(
    (data: string) => {
      if (!clients || !sessionId || closed) return;
      const encoded = new TextEncoder().encode(data);
      if (encoded.length === 0 || encoded.length > 16384) return;
      writeChainRef.current = writeChainRef.current.then(() =>
        clients.ptySessions.writePtySession({ sessionId, input: encoded }),
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
      <pre
        aria-label="Terminal output"
        className="terminal-output"
        data-testid="terminal-output"
        ref={preRef}
        tabIndex={0}
        onKeyDown={(event) => {
          // Bounded interactive input: printable keys, Enter, Backspace and
          // arrows reach the shell; everything else stays local.
          if (event.metaKey || event.altKey) return;
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
          The shell exited. Close this window and open a new terminal.
        </p>
      ) : null}
    </div>
  );
}
