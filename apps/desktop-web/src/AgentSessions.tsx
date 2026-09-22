import { Code, ConnectError } from "@connectrpc/connect";
import {
  MAX_DRAFT_LENGTH,
  readSessionContinuity,
  patchSessionContinuity,
  sessionContinuityGeneration,
  coalesceSessionRefresh,
  type ContinuityPatch,
  sessionContinuityKey,
  sessionRetryDelay,
  type PendingSessionInput,
} from "./sessionContinuity.js";
import { SessionAutomation } from "./SessionAutomation.js";
import { ExecutionQuestions } from "./ExecutionQuestions.js";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AgentTimeline } from "@workos/agent-center";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AgentSessionInputState,
  AgentSessionState,
  WorkspaceBindingState,
  type AgentEvent,
  type AgentSession,
  type AgentSessionInput,
  type HarnessProviderInfo,
  type SessionDirective,
  SessionDirectiveKind,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";

// The Agent session window (ADR-0030, B05): a normal work window — not a
// permanent sidebar — listing the project's continuous harness sessions and
// running one selected session. Inputs are idempotent per client_input_id:
// a lost SubmitSessionInput response is recovered through GetSessionInput
// with the exact same key, never by submitting a second input. Switching
// projects or closing the window only unmounts this component; the session
// and any running task keep their server-side lifecycle untouched.
//
// The selected session is a controlled prop held by the desktop: crossing
// the responsive breakpoint remounts window bodies, and the session the
// user was reading must survive that remount instead of snapping back to
// the list.
export function AgentSessionsApp(props: {
  projectId: string;
  providers?: HarnessProviderInfo[] | undefined;
  workosClients: WorkOSClients;
  selectedSessionId?: string | undefined;
  onSelectSession: (sessionId: string | undefined) => void;
}) {
  const { projectId, workosClients, selectedSessionId, onSelectSession } = props;
  // Every async flow captures the current generation; a project switch
  // remounts this component (keyed by project), and the unmount bump makes
  // late responses inert before they can touch the new project's state.
  const generationRef = useRef(0);
  useEffect(
    () => () => {
      generationRef.current += 1;
    },
    [projectId],
  );
  // The read-only workspace fact for the session header (B08): one active
  // binding's display name, honestly absent when none exists.
  const [workspaceName, setWorkspaceName] = useState<string>();
  useEffect(() => {
    setWorkspaceName(undefined);
    let cancelled = false;
    workosClients.projectWorkspaces
      .listProjectWorkspaces({ projectId, includeArchived: false })
      .then((response) => {
        if (cancelled) return;
        const active = response.bindings.find(
          (binding) => binding.state === WorkspaceBindingState.ACTIVE,
        );
        if (active) setWorkspaceName(active.displayName);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [projectId, workosClients]);

  if (!selectedSessionId) {
    return (
      <SessionList
        onOpenSession={onSelectSession}
        projectId={projectId}
        workosClients={workosClients}
      />
    );
  }
  return (
    <SessionView
      key={selectedSessionId}
      onBack={() => {
        onSelectSession(undefined);
      }}
      providers={props.providers}
      sessionId={selectedSessionId}
      workosClients={workosClients}
      workspaceName={workspaceName}
    />
  );
}

function SessionList(props: {
  projectId: string;
  workosClients: WorkOSClients;
  onOpenSession: (sessionId: string) => void;
}) {
  const { projectId, workosClients, onOpenSession } = props;
  const [sessions, setSessions] = useState<AgentSession[]>([]);
  const [excerpts, setExcerpts] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string>();
  const [creating, setCreating] = useState(false);
  // One idempotency key per creation attempt: a retry after a failed
  // CreateSession reuses the key so the server replays instead of creating
  // a second session.
  const createKeyRef = useRef("");
  const generationRef = useRef(0);
  useEffect(
    () => () => {
      generationRef.current += 1;
    },
    [projectId],
  );

  const listLoading = useRef(false);
  const load = useCallback(
    async (quiet = false) => {
      if (listLoading.current) return;
      listLoading.current = true;
      const generation = generationRef.current;
      if (!quiet) setLoading(true);
      setError(undefined);
      try {
        const listed: AgentSession[] = [];
        let token = "";
        for (;;) {
          const response = await workosClients.agentSessions.listSessions({
            projectId,
            includeClosed: false,
            pageToken: token,
            pageSize: 20,
          });
          listed.push(...response.sessions);
          token = response.nextPageToken;
          if (token === "") break;
        }
        if (generation !== generationRef.current) return;
        setSessions(listed);
        // Session names are the first input's excerpt; the excerpt never
        // leaves this component and a failed read degrades to "Untitled".
        const firstInputs = await Promise.all(
          listed.map((session) =>
            workosClients.agentSessions
              .listSessionInputs({ sessionId: session.id, afterSequence: 0n, limit: 1 })
              .then((page) => ({
                id: session.id,
                text: page.inputs[0]?.directive
                  ? directiveLabel(page.inputs[0].directive)
                  : (page.inputs[0]?.text ?? ""),
              }))
              .catch(() => ({ id: session.id, text: "" })),
          ),
        );
        if (generation !== generationRef.current) return;
        setExcerpts(Object.fromEntries(firstInputs.map((item) => [item.id, item.text])));
      } catch (reason) {
        if (generation !== generationRef.current) return;
        setError(asMessage(reason));
      } finally {
        listLoading.current = false;
        if (generation === generationRef.current) setLoading(false);
      }
    },
    [projectId, workosClients],
  );

  useEffect(() => {
    setSessions([]);
    setExcerpts({});
    void load();
    const update = () => {
      if (document.visibilityState !== "hidden") void load(true);
    };
    const timer = window.setInterval(update, 5000);
    window.addEventListener("online", update);
    document.addEventListener("visibilitychange", update);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("online", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, [load]);

  const createSession = async () => {
    if (creating) return;
    if (createKeyRef.current === "") createKeyRef.current = crypto.randomUUID();
    const key = createKeyRef.current;
    const generation = generationRef.current;
    setCreating(true);
    setError(undefined);
    try {
      const response = await workosClients.agentSessions.createSession({
        projectId,
        idempotencyKey: key,
      });
      if (generation !== generationRef.current) return;
      const session = response.session;
      if (!session) throw new Error("Session creation returned no session.");
      createKeyRef.current = "";
      onOpenSession(session.id);
    } catch (reason) {
      if (generation !== generationRef.current) return;
      setError(asMessage(reason));
    } finally {
      if (generation === generationRef.current) setCreating(false);
    }
  };

  return (
    <div className="agent-sessions-app system-app" data-testid="agent-sessions-app">
      <header className="app-heading">
        <p>PROJECT AGENT</p>
        <h1>Agent Sessions</h1>
        <span>Persistent sessions that keep their context across windows.</span>
      </header>
      <div className="session-list-actions">
        <Button
          data-testid="new-agent-session"
          disabled={creating}
          onClick={() => void createSession()}
          type="button"
        >
          {creating ? "Creating…" : "New session"}
        </Button>
      </div>
      {loading ? <p role="status">Loading sessions…</p> : null}
      {error ? (
        <p role="alert" className="sessions-verdict">
          {error}
        </p>
      ) : null}
      <ul className="session-list" data-testid="agent-session-list">
        {sessions.map((session) => (
          <li key={session.id}>
            <button
              className="session-entry"
              data-testid={`agent-session-entry-${session.id}`}
              onClick={() => {
                onOpenSession(session.id);
              }}
              type="button"
            >
              <span className="session-entry-title">
                {excerpts[session.id] || "Untitled session"}
              </span>
              <span className="session-entry-facts">
                <span className="session-state-chip" data-state={sessionStateName(session.state)}>
                  {sessionStateName(session.state)}
                </span>
                <span>{session.providerId || "unknown provider"}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>
      {!loading && !error && sessions.length === 0 ? (
        <p className="empty-state">No open sessions in this project yet.</p>
      ) : null}
    </div>
  );
}

type PendingInput = PendingSessionInput;

const SUBMIT_TIMEOUT_MS = 12_000;
const RECOVERY_POLL_MS = 4_000;

function SessionView(props: {
  providers?: HarnessProviderInfo[] | undefined;
  sessionId: string;
  workspaceName?: string | undefined;
  workosClients: WorkOSClients;
  onBack: () => void;
}) {
  const { sessionId, workspaceName, workosClients, onBack } = props;
  const [session, setSession] = useState<AgentSession>();
  const [inputs, setInputs] = useState<AgentSessionInput[]>([]);
  const [pending, setPendingState] = useState<PendingInput[]>([]);
  const pendingRef = useRef<PendingInput[]>([]);
  const [draft, setDraftState] = useState("");
  const draftRef = useRef("");
  const storageKey = useRef("");
  const sessionCursor = useRef(0n);
  const storageEpoch = useRef("");
  const journalGeneration = useRef(sessionContinuityGeneration());
  const retries = useRef(new Set<AbortController>());
  const journaling = useRef(false);
  const refreshQueue = useRef<{ generation: number; run: () => Promise<boolean> } | undefined>(
    undefined,
  );
  const persist = useCallback(
    (patch: ContinuityPatch) =>
      patchSessionContinuity(storageKey.current, patch, {
        epoch: storageEpoch.current,
        generation: journalGeneration.current,
      }),
    [],
  );
  const setPending = useCallback((update: (current: PendingInput[]) => PendingInput[]) => {
    pendingRef.current = update(pendingRef.current);
    setPendingState(pendingRef.current);
  }, []);
  const setDraft = (value: string) => {
    draftRef.current = value;
    setDraftState(value);
    const generation = generationRef.current;
    if (storageKey.current)
      void persist({ draft: value }).then((saved) => {
        if (!saved && isLive(generation))
          setNotice(
            "Draft storage is unavailable; keep this window open until your message is confirmed.",
          );
      });
  };
  // Assistant/tool/usage timelines keyed by the input whose task produced
  // them. The active run streams live; the most recent task-bearing input
  // keeps its replayed timeline after the run reached a terminal state, so
  // a finished conversation still shows its messages.
  const [timelines, setTimelines] = useState<Record<string, AgentEvent[]>>({});
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [cancelling, setCancelling] = useState(false);
  const [closing, setClosing] = useState(false);
  const [goalControlling, setGoalControlling] = useState(false);
  const pauseKey = useRef({ ref: "", key: "" });
  const [loading, setLoading] = useState(true);
  const generationRef = useRef(0);
  // One task-event stream per watched input, abortable on session change.
  const taskStreamsRef = useRef(new Map<string, AbortController>());
  const isLive = useCallback((generation: number) => generation === generationRef.current, []);

  const activeInput = useMemo(
    () => inputs.find((input) => input.state === AgentSessionInputState.DISPATCHED),
    [inputs],
  );
  const queuedInputs = inputs.filter((input) => input.state === AgentSessionInputState.ACCEPTED);
  const busy = activeInput !== undefined || queuedInputs.length > 0;
  // The active run plus the most recent task-bearing input stay watchable;
  // older finished inputs show their bounded server summary only.
  const watchedInputs = useMemo(() => {
    const lastWithTask = [...inputs].reverse().find((input) => input.taskId !== "");
    const watched: AgentSessionInput[] = [];
    if (activeInput) watched.push(activeInput);
    if (lastWithTask && lastWithTask.id !== activeInput?.id) watched.push(lastWithTask);
    return watched;
  }, [inputs, activeInput]);
  const watchedSignature = watchedInputs.map((input) => `${input.id}:${input.taskId}`).join("|");

  const loadSnapshot = useCallback(
    async (generation: number) => {
      const abort = new AbortController();
      retries.current.add(abort);
      try {
        const [sessionResponse, inputPages] = await Promise.all([
          workosClients.agentSessions.getSession({ sessionId }, { signal: abort.signal }),
          (async () => {
            const pages: AgentSessionInput[][] = [];
            let after = 0n;
            for (;;) {
              if (!isLive(generation) || abort.signal.aborted) break;
              const page = await workosClients.agentSessions.listSessionInputs(
                {
                  sessionId,
                  afterSequence: after,
                  limit: 50,
                },
                { signal: abort.signal },
              );
              pages.push(page.inputs);
              if (page.inputs.length < 50) break;
              after = page.inputs[page.inputs.length - 1]?.sequence ?? after;
            }
            return pages.flat();
          })(),
        ]);
        if (!isLive(generation)) return false;
        if (sessionResponse.session) {
          const fact = sessionResponse.session;
          const key = sessionContinuityKey(fact.ownerUserId, fact.projectId, fact.id);
          if (key && key !== storageKey.current) {
            const saved = await readSessionContinuity(key);
            if (!isLive(generation)) return false;
            storageKey.current = key;
            storageEpoch.current = saved.epoch ?? "";
            sessionCursor.current = saved.cursor > fact.lastEventSequence ? 0n : saved.cursor;
            if (saved.cursor !== sessionCursor.current)
              void persist({ resetCursor: sessionCursor.current });
            draftRef.current = saved.draft;
            setDraftState(saved.draft);
            pendingRef.current = saved.pending;
            setPendingState(saved.pending);
          }
          setSession(fact);
          // A server-confirmed input retires its local receipt, including a
          // submission accepted just before this tab was killed.
          const accepted = new Set(inputPages.map((input) => input.clientInputId));
          setPending((current) => current.filter((item) => !accepted.has(item.clientInputId)));
          void persist({ removePendingIds: [...accepted] });
        }
        setError(undefined);
        setInputs(inputPages.sort((left, right) => Number(left.sequence - right.sequence)));
        return true;
      } catch (reason) {
        if (isLive(generation)) setError(asMessage(reason));
        return false;
      } finally {
        retries.current.delete(abort);
      }
    },
    [isLive, sessionId, workosClients, setPending, persist],
  );

  const refresh = useCallback(
    (generation: number) => {
      if (!isLive(generation)) return Promise.resolve(false);
      if (refreshQueue.current?.generation !== generation)
        refreshQueue.current = {
          generation,
          run: coalesceSessionRefresh(() =>
            isLive(generation) ? loadSnapshot(generation) : Promise.resolve(false),
          ),
        };
      return refreshQueue.current.run();
    },
    [isLive, loadSnapshot],
  );

  useEffect(() => {
    const generation = ++generationRef.current;
    for (const abort of taskStreamsRef.current.values()) abort.abort();
    taskStreamsRef.current.clear();
    setSession(undefined);
    setInputs([]);
    pendingRef.current = [];
    setPendingState([]);
    storageKey.current = "";
    sessionCursor.current = 0n;
    setTimelines({});
    setError(undefined);
    setNotice(undefined);
    setLoading(true);
    void refresh(generation).finally(() => {
      if (isLive(generation)) setLoading(false);
    });
    return () => {
      generationRef.current += 1;
      for (const abort of retries.current) abort.abort();
      retries.current.clear();
      for (const abort of taskStreamsRef.current.values()) abort.abort();
      taskStreamsRef.current.clear();
    };
  }, [refresh, isLive]);

  // Idle sessions also follow events from other devices. Lifecycle events
  // invalidate the authoritative input projection; they never execute work.
  useEffect(() => {
    const generation = generationRef.current;
    const stop = new AbortController();
    const stopped = () => stop.signal.aborted;
    let attempt: AbortController | undefined;
    const wake = () => {
      if (document.visibilityState === "hidden") return;
      void refresh(generation);
      attempt?.abort();
    };
    window.addEventListener("online", wake);
    document.addEventListener("visibilitychange", wake);
    const run = async () => {
      let delay = 500;
      while (!stopped() && isLive(generation)) {
        attempt = new AbortController();
        const abort = () => attempt?.abort();
        stop.signal.addEventListener("abort", abort, { once: true });
        try {
          for await (const page of workosClients.agentSessions.watchSessionEvents(
            { sessionId, after: sessionCursor.current, follow: true },
            { signal: attempt.signal },
          )) {
            if (stopped() || !isLive(generation)) break;
            const last = page.events.at(-1)?.sequence;
            if (last !== undefined && last > sessionCursor.current) {
              const refreshed = await refresh(generation);
              if (stopped() || !isLive(generation)) break;
              if (!refreshed) break;
              sessionCursor.current = last;
              void persist({ cursor: last });
            }
            delay = 500;
          }
        } catch (reason) {
          if (
            reason instanceof ConnectError &&
            [Code.Unauthenticated, Code.PermissionDenied, Code.NotFound].includes(reason.code)
          ) {
            if (isLive(generation))
              setError(
                "This session is no longer accessible. Reconnect your device or select another session.",
              );
            return;
          }
        } finally {
          stop.signal.removeEventListener("abort", abort);
        }
        await sessionRetryDelay(delay, stop.signal);
        delay = Math.min(delay * 2, 10_000);
      }
    };
    void run();
    // Reconcile snapshots even if a proxy truncates a stream without an error.
    const timer = window.setInterval(() => {
      if (document.visibilityState !== "hidden") void refresh(generation);
    }, 5000);
    return () => {
      stop.abort();
      attempt?.abort();
      window.clearInterval(timer);
      window.removeEventListener("online", wake);
      document.removeEventListener("visibilitychange", wake);
    };
  }, [isLive, persist, refresh, sessionId, workosClients]);

  // Each stream reconnects from its accepted sequence. A new component has
  // no transcript projection, so it deliberately rebuilds from the server.
  useEffect(() => {
    const generation = generationRef.current;
    const controllers = taskStreamsRef.current;
    for (const input of watchedInputs) {
      if (controllers.has(input.id)) continue;
      const lifetime = new AbortController();
      const disposed = () => lifetime.signal.aborted;
      controllers.set(input.id, lifetime);
      const run = async () => {
        let cursor = 0n;
        let terminal = false;
        let delay = 500;
        let attempt: AbortController | undefined;
        const wake = () => {
          if (document.visibilityState !== "hidden") attempt?.abort();
        };
        const stop = () => attempt?.abort();
        lifetime.signal.addEventListener("abort", stop, { once: true });
        window.addEventListener("online", wake);
        document.addEventListener("visibilitychange", wake);
        while (!disposed() && isLive(generation) && !terminal) {
          attempt = new AbortController();
          try {
            for await (const page of workosClients.agentTasks.watchTaskEvents(
              { taskId: input.taskId, afterSequence: cursor },
              { signal: attempt.signal },
            )) {
              if (disposed() || !isLive(generation)) break;
              const received = page.event;
              if (!received || received.sequence <= cursor) continue;
              cursor = received.sequence;
              setTimelines((current) => {
                const previous = current[input.id] ?? [];
                if (previous.some((event) => event.sequence === received.sequence)) return current;
                return { ...current, [input.id]: [...previous, received] };
              });
              if (isTerminalEvent(received)) terminal = true;
              delay = 500;
            }
          } catch (reason) {
            if (
              reason instanceof ConnectError &&
              [Code.Unauthenticated, Code.PermissionDenied, Code.NotFound].includes(reason.code)
            )
              break;
          }
          if (!terminal) {
            await sessionRetryDelay(delay, lifetime.signal);
            delay = Math.min(delay * 2, 10_000);
          }
        }
        lifetime.signal.removeEventListener("abort", stop);
        window.removeEventListener("online", wake);
        document.removeEventListener("visibilitychange", wake);
        if (isLive(generation) && terminal) void refresh(generation);
      };
      void run();
    }
    // Only currently visible task projections keep a live transport.
    for (const [id, controller] of controllers) {
      if (!watchedInputs.some((input) => input.id === id)) {
        controller.abort();
        controllers.delete(id);
      }
    }
  }, [watchedSignature, refresh, workosClients, isLive]);

  // A pending input whose SubmitSessionInput response timed out is only
  // ever recovered by re-reading GetSessionInput with the same key — a
  // second submission could duplicate the run.
  useEffect(() => {
    const recovering = pending.filter((item) => item.phase === "recovering");
    if (recovering.length === 0) return;
    const generation = generationRef.current;
    const reconcile = async () => {
      for (const item of recovering) {
        if (!isLive(generation)) return;
        try {
          const response = await workosClients.agentSessions.getSessionInput({
            sessionId,
            clientInputId: item.clientInputId,
          });
          if (generation !== generationRef.current) return;
          if (response.input) {
            void persist({ removePendingIds: [item.clientInputId] });
            setPending((current) =>
              current.filter((entry) => entry.clientInputId !== item.clientInputId),
            );
            await refresh(generation);
          }
        } catch {
          if (generation !== generationRef.current) return;
          setPending((current) =>
            current.map((entry) =>
              entry.clientInputId === item.clientInputId
                ? {
                    ...entry,
                    phase: "failed",
                    error: "The server could not confirm this input yet.",
                  }
                : entry,
            ),
          );
        }
      }
    };
    const timer = window.setTimeout(() => {
      void reconcile();
    }, RECOVERY_POLL_MS);
    return () => {
      window.clearTimeout(timer);
    };
  }, [pending, refresh, sessionId, workosClients, persist, isLive]);

  const submit = async (text: string, directive?: SessionDirective) => {
    const trimmed = text.trim();
    if (!trimmed && !directive) return;
    if (!navigator.onLine) {
      setNotice("You are offline. Your draft is kept on this device.");
      return;
    }
    if (pendingRef.current.length >= 16) {
      setNotice("Confirm the pending messages before sending more.");
      return;
    }
    if (journaling.current) return;
    journaling.current = true;
    const clientInputId = crypto.randomUUID();
    const generation = generationRef.current;
    const item: PendingInput = { clientInputId, text: trimmed, directive, phase: "submitting" };
    setPending((current) => [...current, item]);
    const saved = await persist({
      addPending: [item],
      ...(!directive ? { clearDraftIf: text } : {}),
    });
    journaling.current = false;
    if (!isLive(generation)) return;
    if (!saved) {
      setPending((current) => current.filter((entry) => entry.clientInputId !== clientInputId));
      setNotice(
        "Message was not sent because durable draft storage is unavailable. Your draft is still here.",
      );
      return;
    }
    setError(undefined);
    setNotice(undefined);
    if (!directive && draftRef.current === text) {
      draftRef.current = "";
      setDraftState("");
    }
    try {
      await withTimeout(
        workosClients.agentSessions.submitSessionInput({
          sessionId,
          clientInputId,
          text: directive ? "" : trimmed,
          ...(directive ? { directive } : {}),
        }),
        SUBMIT_TIMEOUT_MS,
      );
      if (generation !== generationRef.current) return;
      void persist({ removePendingIds: [clientInputId] });
      setPending((current) => current.filter((entry) => entry.clientInputId !== clientInputId));
      await refresh(generation);
    } catch (reason) {
      if (generation !== generationRef.current) return;
      if (
        reason instanceof TimeoutError ||
        !(reason instanceof ConnectError) ||
        [Code.Unavailable, Code.DeadlineExceeded, Code.Canceled, Code.Unknown].includes(reason.code)
      ) {
        setPending((current) =>
          current.map((entry) =>
            entry.clientInputId === clientInputId ? { ...entry, phase: "recovering" } : entry,
          ),
        );
        setNotice("Confirming your input with the server…");
      } else {
        setPending((current) =>
          current.map((entry) =>
            entry.clientInputId === clientInputId
              ? { ...entry, phase: "failed", error: asMessage(reason) }
              : entry,
          ),
        );
      }
    }
  };

  // Retry re-uses the exact same client_input_id and text: the server
  // replays the recorded input instead of dispatching a second run.
  const retryPending = async (item: PendingInput) => {
    const generation = generationRef.current;
    const abort = new AbortController();
    retries.current.add(abort);
    const live = () => isLive(generation) && !abort.signal.aborted;
    setPending((current) =>
      current.map((entry) =>
        entry.clientInputId === item.clientInputId ? { ...entry, phase: "submitting" } : entry,
      ),
    );
    try {
      try {
        const found = await workosClients.agentSessions.getSessionInput(
          { sessionId, clientInputId: item.clientInputId },
          { signal: abort.signal },
        );
        if (!live()) return;
        if (found.input) {
          await persist({ removePendingIds: [item.clientInputId] });
          if (!live()) return;
          setPending((current) =>
            current.filter((entry) => entry.clientInputId !== item.clientInputId),
          );
          await refresh(generation);
          return;
        }
      } catch (reason) {
        if (!live()) return;
        if (!(reason instanceof ConnectError) || reason.code !== Code.NotFound) throw reason;
      }
      if (!live()) return;
      if (!navigator.onLine)
        throw new Error("You are offline. The pending message remains on this device.");
      // Re-confirm the receipt is durable before every explicit retry.
      if (!(await persist({ addPending: [item] })))
        throw new Error("Message was not sent because durable draft storage is unavailable.");
      if (!live()) return;
      await workosClients.agentSessions.submitSessionInput(
        {
          sessionId,
          clientInputId: item.clientInputId,
          text: item.directive ? "" : item.text,
          ...(item.directive ? { directive: item.directive } : {}),
        },
        { signal: abort.signal },
      );
      if (!live()) return;
      void persist({ removePendingIds: [item.clientInputId] });
      setPending((current) =>
        current.filter((entry) => entry.clientInputId !== item.clientInputId),
      );
      await refresh(generation);
    } catch (reason) {
      if (live())
        setPending((current) =>
          current.map((entry) =>
            entry.clientInputId === item.clientInputId
              ? { ...entry, phase: "failed", error: asMessage(reason) }
              : entry,
          ),
        );
    } finally {
      retries.current.delete(abort);
    }
  };

  const pauseGoal = async () => {
    const goal = session?.goal;
    if (!goal) return;
    const generation = generationRef.current;
    const activation = `${goal.ref}:${session.activeTaskId}`;
    if (pauseKey.current.ref !== activation)
      pauseKey.current = { ref: activation, key: crypto.randomUUID() };
    setGoalControlling(true);
    setError(undefined);
    try {
      await workosClients.agentSessions.requestSessionGoalPause({
        sessionId,
        goalRef: goal.ref,
        idempotencyKey: pauseKey.current.key,
      });
      if (isLive(generation)) await refresh(generation);
    } catch (reason) {
      if (isLive(generation)) setError(asMessage(reason));
    } finally {
      if (isLive(generation)) setGoalControlling(false);
    }
  };

  const cancelExecution = async () => {
    const generation = generationRef.current;
    setCancelling(true);
    setError(undefined);
    try {
      await workosClients.agentSessions.cancelSessionExecution({
        sessionId,
        reason: "Stopped from the session window",
      });
      if (generation !== generationRef.current) return;
      await refresh(generation);
    } catch (reason) {
      if (generation !== generationRef.current) return;
      setError(asMessage(reason));
    } finally {
      if (generation === generationRef.current) setCancelling(false);
    }
  };

  const closeSession = async () => {
    const generation = generationRef.current;
    setClosing(true);
    setError(undefined);
    try {
      await workosClients.agentSessions.closeSession({ sessionId });
      if (generation !== generationRef.current) return;
      await refresh(generation);
    } catch (reason) {
      if (generation !== generationRef.current) return;
      setError(asMessage(reason));
    } finally {
      if (generation === generationRef.current) setClosing(false);
    }
  };

  const sessionClosed =
    session?.state === AgentSessionState.CLOSED ||
    session?.state === AgentSessionState.CLOSING ||
    session?.state === AgentSessionState.NEEDS_REVIEW;

  return (
    <div className="agent-sessions-app session-view" data-testid="agent-session-view">
      <header className="session-header" data-testid="agent-session-header">
        <Button
          className="session-back"
          data-testid="agent-session-back"
          onClick={onBack}
          type="button"
        >
          Sessions
        </Button>
        <div className="session-facts">
          <span className="session-state-chip" data-state={sessionStateName(session?.state)}>
            {sessionStateName(session?.state)}
          </span>
          <span data-testid="agent-session-provider">
            Provider · {session?.providerId || "resolving…"}
          </span>
          {workspaceName ? <span>Workspace · {workspaceName}</span> : null}
        </div>
        <div className="session-header-actions">
          {activeInput ? (
            <Button
              data-testid="agent-session-cancel"
              disabled={cancelling}
              onClick={() => void cancelExecution()}
              type="button"
            >
              {cancelling ? "Stopping…" : "停止当前执行"}
            </Button>
          ) : null}
          {!sessionClosed ? (
            <Button
              className="session-close"
              data-testid="agent-session-close"
              disabled={closing}
              onClick={() => void closeSession()}
              type="button"
            >
              {closing ? "Closing…" : "关闭会话"}
            </Button>
          ) : null}
        </div>
      </header>
      {watchedInputs.map((input) => (
        <ExecutionQuestions
          key={input.taskId}
          taskId={input.taskId}
          workosClients={workosClients}
        />
      ))}
      {session?.state === AgentSessionState.NEEDS_REVIEW ? (
        <p role="alert" className="session-error">
          Execution stopped or its outcome is uncertain. Inspect the project files and tool results
          before starting a new session. Queued inputs will not run automatically.
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="sessions-verdict">
          {error}
        </p>
      ) : null}
      {notice ? (
        <p role="status" className="session-notice" data-testid="agent-session-notice">
          {notice}
        </p>
      ) : null}
      <div className="session-transcript" data-testid="agent-session-transcript">
        {loading ? <p role="status">Loading session…</p> : null}
        {session ? (
          <SessionAutomation
            session={session}
            capabilities={
              props.providers?.find((provider) => provider.id === session.providerId)?.capabilities
            }
            busy={busy || pending.length > 0}
            controlling={goalControlling || pending.length > 0}
            submit={(directive) => void submit(directiveLabel(directive), directive)}
            pause={() => void pauseGoal()}
            workosClients={workosClients}
          />
        ) : null}
        <ol className="session-inputs">
          {inputs.map((input) => (
            <li
              className="session-input"
              data-input-state={inputStateName(input.state)}
              key={input.id}
            >
              <div className="session-input-row">
                <p className="session-input-text">
                  {input.directive ? directiveLabel(input.directive) : input.text}
                </p>
                <span className="session-input-state" data-testid="session-input-state">
                  {inputStateName(input.state)}
                </span>
              </div>
              {input.state === AgentSessionInputState.COMPLETED && input.resultSummary ? (
                <p className="session-input-summary">{input.resultSummary}</p>
              ) : null}
              {input.state === AgentSessionInputState.FAILED ||
              input.state === AgentSessionInputState.CANCELLED ? (
                <p className="session-input-summary failure">
                  {input.resultSummary || inputStateName(input.state)}
                </p>
              ) : null}
              {timelines[input.id]?.length ? (
                <div className="session-task-timeline">
                  <AgentTimeline events={timelines[input.id] ?? []} />
                </div>
              ) : null}
            </li>
          ))}
          {pending.map((item) => (
            <li className="session-input" data-input-state={item.phase} key={item.clientInputId}>
              <div className="session-input-row">
                <p className="session-input-text">{item.text}</p>
                <span className="session-input-state">
                  {item.phase === "submitting"
                    ? "sending"
                    : item.phase === "recovering"
                      ? "confirming"
                      : "not sent"}
                </span>
              </div>
              {item.phase === "failed" ? (
                <div className="session-input-retry">
                  <span>{item.error}</span>
                  <Button onClick={() => void retryPending(item)} type="button">
                    Retry
                  </Button>
                </div>
              ) : null}
            </li>
          ))}
        </ol>
      </div>
      <form
        className="session-composer"
        onSubmit={(event) => {
          event.preventDefault();
          void submit(draftRef.current);
        }}
      >
        {queuedInputs.length > 0 ? (
          <p className="session-queue-hint" data-testid="agent-session-queue-hint">
            {`${String(queuedInputs.length)} queued input${queuedInputs.length > 1 ? "s" : ""} will run in order.`}
          </p>
        ) : null}
        <textarea
          aria-label="Session message"
          disabled={loading || !session || sessionClosed}
          value={draft}
          maxLength={MAX_DRAFT_LENGTH}
          onChange={(event) => {
            setDraft(event.currentTarget.value);
          }}
          name="text"
          placeholder={sessionClosed ? "This session is closed." : "Message this session…"}
        />
        <Button disabled={loading || !session || sessionClosed} type="submit">
          Send
        </Button>
      </form>
    </div>
  );
}

function sessionStateName(state: AgentSessionState | undefined): string {
  switch (state) {
    case AgentSessionState.ACTIVE:
      return "active";
    case AgentSessionState.CLOSING:
      return "closing";
    case AgentSessionState.CLOSED:
      return "closed";
    case AgentSessionState.NEEDS_REVIEW:
      return "needs review";
    default:
      return "unknown";
  }
}

function inputStateName(state: AgentSessionInputState): string {
  switch (state) {
    case AgentSessionInputState.ACCEPTED:
      return "queued";
    case AgentSessionInputState.DISPATCHED:
      return "running";
    case AgentSessionInputState.COMPLETED:
      return "done";
    case AgentSessionInputState.FAILED:
      return "failed";
    case AgentSessionInputState.CANCELLED:
      return "cancelled";
    default:
      return "accepted";
  }
}

function isTerminalEvent(event: AgentEvent): boolean {
  return (
    event.event.case === "runCompleted" ||
    event.event.case === "runFailed" ||
    event.event.case === "runCancelled"
  );
}

class TimeoutError extends Error {
  constructor() {
    super("submission timed out");
  }
}

function withTimeout<T>(promise: Promise<T>, milliseconds: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = window.setTimeout(() => {
      reject(new TimeoutError());
    }, milliseconds);
    promise.then(
      (value) => {
        window.clearTimeout(timer);
        resolve(value);
      },
      (reason: unknown) => {
        window.clearTimeout(timer);
        reject(reason instanceof Error ? reason : new Error(asMessage(reason)));
      },
    );
  });
}

function asMessage(reason: unknown): string {
  if (reason instanceof Error && reason.message) return reason.message;
  return "The agent session request failed.";
}

function directiveLabel(directive: SessionDirective): string {
  switch (directive.kind) {
    case SessionDirectiveKind.CREATE_GOAL:
      return `Goal · ${directive.objective}`;
    case SessionDirectiveKind.RESUME_GOAL:
      return "Resume goal";
    case SessionDirectiveKind.PAUSE_GOAL:
      return "Pause goal";
    default:
      return "Goal control";
  }
}
