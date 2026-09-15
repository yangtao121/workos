# DeepSeek Harness adapter

This adapter keeps `deepseek` as the stable WorkOS provider ID while delegating
the model loop and DeepSeek HTTP/SSE protocol to the official
`deepseek-ai/deepseek-harness` runtime. The container pins
`deepseek-harness-runtime-bin==0.1.1rc1` and verifies the platform wheel by
SHA-256. Each WorkOS Task gets one isolated runtime subprocess; the subprocess
is terminated for cancellation because this prerelease JSON-RPC protocol has no
cancel or session-close method.

The runtime's internal route is `deepseek-official`. That vendor-internal name
does not cross the WorkOS provider, Core, Proto, or database boundary.

## Safety and input policy

- The provider is registered but disabled by default. A configured key never
  implicitly enables it.
- The adapter holds NO long-lived credential. Since ADR-0009 the long-lived
  API key lives encrypted in the Core Credential Vault; every run requires a
  short-lived, task-bound credential lease (`requires_task_credential_lease`)
  derived by Core from the active task lease over the private mTLS execution
  channel. The lease secret is passed only to the allowlisted child
  environment of that one task and the official runtime sends it as the HTTP
  Authorization header. WorkOS never logs it or includes it in an event. A
  legacy `DEEPSEEK_API_KEY` in the harness-host environment produces a
  sanitized configuration issue (migration direction) and is never read.
- Production base URLs require HTTPS. Development and test may use HTTP only
  for a literal loopback host, enabling the keyless fixture.
- An empty role and the exact role `general` have the same fixed, no-tools
  persona. All other roles are rejected; role text is never promoted into a
  system instruction.
- `context_refs` are consumed as bounded untrusted context through the
  versioned task envelope (ADR-0010); requested capabilities, cost budgets,
  tools, MCP, approvals, sessions, workspace access, and subagents are
  rejected or disabled rather than silently ignored.
- Structured review output (ADR-0011): when the task requests
  `document.markdown.v1` / `code.unified-diff.v1`, the run must answer with
  exactly one strict JSON document; the adapter parses it with duplicate-key
  and trailing-content rejection, bounds the summary and every candidate
  against the canonical review grammar, derives the output keys and titles
  itself, and publishes both outputs atomically through the private batch
  protocol. Raw JSON never enters the timeline; malformed, partial,
  oversize, or unsupported output fails the run closed.
- `max_tokens` defaults to 8192 and accepts at most 384000. The effective local
  runtime limit is the smaller of the configured timeout and
  `max_runtime_seconds`, capped at ten minutes.
- The adapter declares `hard_token_budget` and `hard_runtime_deadline` (ADR-0005)
  because both are provably enforced: the provider caps output at `max_tokens`
  and the adapter kills the runtime subprocess at the mapped deadline. WorkOS
  pre-run approval is unrelated to provider tool approvals and is never mapped
  onto `approvals`.
- The adapter emits text deltas, one bounded aggregate assistant message,
  provider token usage, and one terminal event. Cost stays empty because no
  changing price table is embedded in WorkOS.

## Continuous sessions (ADR-0030)

`sessions.go` adds a second execution path for tasks that arrive with the
server-derived `agent_session_id` linkage (set only by the Core session
dispatcher; public `SubmitTask` rejects it). The native protocol has no
wire-level resume, so continuity is process continuity: `SessionManager`
keeps one pinned runtime child per Core session id and serializes each turn
as one `session/prompt` on that child.

- The worker routes such tasks through `ports.SessionExecution`
  (`SessionID`, `WorkspaceRoot`, `StateRoot`). `WorkspaceRoot` stays empty in
  this slice — the harness host has no workspace registry yet — so each
  session runs in its private scratch workspace under the state root.
- `StateRoot` is harness-host private (`WORKOS_HARNESS_SESSION_STATE_ROOT`,
  default `/var/lib/workos/harness-sessions`, created 0700). Per session it
  holds `home` (child HOME/TMPDIR/DSH_HOME), `state`, `ws` (scratch cwd),
  `persistence` (native jsonl session logs), the generated `cordis.yml`, and
  `runtime.stderr` (captured, never logged).
- The generated `cordis.yml` is the exact official base composition verified
  loadable by the pinned runtime (tools, sandbox policy, session
  persistence) plus the one configuration-relative `workos-tools` row (see
  below), with only three substitutions inside the base rows: the persistence
  root, the sandbox workspace root, and the llm-deepseek endpoint/model
  facts. The operator's single-shot `cordis_config_path` is ignored on this
  path.
- A living process is reused only while the credential fingerprint
  (SHA-256 of the lease secret) and workspace binding are unchanged; a
  rotation, rebinding, dead child, or turn error kills the process group and
  the next turn respawns fresh. Turn timeouts and cancellations kill the
  whole group — a partially consumed stream is never trusted again.
- Session turns map tool traffic onto `ToolCallStarted`/`ToolCallCompleted`
  (structured inputs via protojson, `text` outputs, `Error:` prefixes mark
  failures) in addition to the canonical delta/message/usage/terminal
  events. Unknown methods and events still fail closed with a protocol
  error.
- `Describe()` is unchanged: sessions and tools are not advertised
  capabilities yet (B05/B09 own that once proven end to end).

### Session restart and credential-rotation respawn semantics (B09)

The pinned runtime has no wire-level session resume (the service-layer
`agents.resume` exists upstream but the sdk-jsonrpc-server of 0.1.1rc1
dispatches only `initialize` / `session/prompt` / `shutdown`), so session
continuity is process continuity and restarts are honest:

- **harness-host restart**: every session child dies with the host
  (process-group kill). WorkOS keeps the durable Core facts (session row,
  accepted inputs with their task ids and terminal states, lifecycle event
  log); the native context of unfinished work is NOT resurrected. The next
  turn on a live session respawns a fresh child over the same per-session
  state directory (native jsonl persistence and scratch workspace survive
  on disk), and a brand-new session proves the stack recovered. The
  `tools/harness-sessions` gate's restart phase asserts exactly this:
  closed-session history is complete and readable after a
  workos-core+harness-host restart, inputs keep their recorded task ids and
  terminal states (no duplicate executions), and a new session executes a
  fresh native turn (`has_turn1=false total=1`).
- **Credential rotation**: sessions never cache a key. Each turn's
  `Ensure` compares the SHA-256 fingerprint of the current lease secret
  with the child's; a mismatch (rotation, revocation, new lease) plus any
  owner/project change kills the process group and respawns under the new
  secret — the previous native context is deliberately lost rather than
  served under a different credential. Old keys never survive a rotation.
- **Turn errors**: any mapped protocol/transport error fails the turn and
  drops the child (the stream position is untrusted); the next `Ensure`
  respawns. A crash mid-turn leaves results unknown by design — the worker
  marks the input failed rather than blindly replaying side effects.

### Unsupported approvals fail closed (B09, A09)

The pinned runtime's event vocabulary contains `approval/asked` /
`approval/decided` (and the ACP-style `session/request_permission`), but its
sdk server wire exposes **no approval-response method** WorkOS could call,
and B04's WorkOS toolset is read-only (nothing it registers can trigger an
escalation). The adapter therefore treats any approval-style session event
as unsupported: `sessionEventMapper` fails the turn with a non-retryable
protocol error and emits nothing — never a silent continue that would read
as an implicit approval. The generated composition keeps the official
`approval` row at `policy: ask`; WorkOS maps no approval semantics onto the
canonical protocol in this version. `TestSessionApprovalEventsFailClosed`
pins all three facts (rejection, no events, `policy: ask`).

## Read-only WorkOS tools (B04)

The generated session composition appends one configuration-relative row
(`workos-tools` → `./workos-tools.mjs`) after the official 26-row base. The
adapter copies `deploy/harness/workos-tools.mjs` (image location
`/usr/local/libexec/workos/workos-tools.mjs`) into each session's private
state directory so the pinned runtime resolves it beside the generated
`cordis.yml`; a missing plugin file fails the session spawn closed instead of
silently dropping the tools.

The plugin registers exactly two tools in this slice, both read-only and both
parameterless:

- `workos_project_info` — project id, name, harness binding provider, bounded
  workspace refs, revision of the CURRENT project;
- `workos_list_artifacts` — up to 50 artifact ids/types/titles of the CURRENT
  project.

Authorization honesty:

- The "current" owner/project are never model inputs. The worker derives them
  from the server-owned task facts (`owner_user_id` and the project
  `target_scope` of the session task) and the SessionManager injects them into
  the session child environment (`WORKOS_TOOL_OWNER_ID`,
  `WORKOS_TOOL_PROJECT_ID`) together with the ordinary Core listener
  (`WORKOS_TOOL_CORE_URL`, from `WORKOS_CORE_URL`) and the harness device
  identity (`WORKOS_TOOL_DEVICE_ID`, from the host's `WORKOS_DEVICE_ID`). The
  tools call Core's Connect services (`ProjectService/GetProject`,
  `ArtifactService/ListArtifacts`) with the owner-scoped identity headers, so
  every read is authorized server-side for exactly this owner. The process is
  respawned if the owner/project pair of a session id ever changes.
- Missing environment facts make each tool call fail with an `Error:` result
  (mapped to `ToolCallCompleted Success=false`), never a crash; Core errors and
  non-200 responses surface as bounded `Error:` text. Every tool output is
  bounded to 4 KiB with an explicit truncation marker, and no secret, prompt
  content, or raw response body is ever echoed or logged.
- The B04 set is deliberately read-only. Write-capable tools, artifact
  creation, and approval-path integration remain future work; the runtime's
  `approval` row keeps `policy: ask` and no WorkOS tool escalates around it.
  ADR-0030 covers this direction — no separate decision record is needed.
- The plugin file imports nothing: the packaged runtime resolves bare package
  specifiers only inside its own closure, so an external
  configuration-relative plugin must be plain language built-ins (`fetch`,
  `JSON`, `Promise`).

## Configuration

Non-secret defaults live in `deploy/config/dev.yaml`. To enable the adapter:

```bash
export WORKOS_DEEPSEEK_ENABLED=true
export WORKOS_DEEPSEEK_MODEL=deepseek-v4-flash
export WORKOS_DEEPSEEK_TIMEOUT=2m
# store the credential once, over the Core admin Unix socket:
printf '%s' 'the-key' | workosctl credential put --consumer deepseek
docker compose up -d --build harness-host
```

Optional overrides are `WORKOS_DEEPSEEK_BASE_URL`,
`WORKOS_DEEPSEEK_RUNTIME_PATH`, and `WORKOS_DEEPSEEK_CORDIS_CONFIG`. Never put a
real key in YAML, Compose source, a fixture, a command transcript, or a task
record. A key pasted into chat or logs must be revoked before use. The owner's
vault credential is resolved by Core at binding time and snapshotted per task;
rotating or revoking it stops new acquires and fails the next worker
heartbeat.

Provider health distinguishes disabled/misconfigured (`UNAVAILABLE`), a valid
configuration (`HEALTHY`), and a transient provider/transport failure
(`DEGRADED`). Authentication failure changes health to `UNAVAILABLE` until the
host restarts with corrected credentials.

## Tests

```bash
go test ./internal/harness/adapters/deepseek
make test-deepseek-fixture
```

The second command uses the pinned official runtime against a local DeepSeek
API/SSE fixture. It proves Project binding, Task provider snapshot, streaming
event persistence, idempotency after rebinding, and restart recovery without a
real key or external API request.

A real-API smoke is deliberately not an automated target in this slice. The
current canonical budget can bound tokens and runtime but cannot enforce a
provider-independent monetary ceiling, and this adapter intentionally does not
hardcode a changing price table. Operators may enable the provider manually for
diagnosis, but that is not CI evidence and must not be reported as a passed
smoke test.
