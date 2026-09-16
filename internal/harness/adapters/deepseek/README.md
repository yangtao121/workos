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

The pinned official runtime remains `0.1.1rc1`. `workos-session.mjs` adapts
its `agents.create/resume`, native `followup` and JSONL persistence to a
bounded JSON-RPC transport. No WorkOS agent loop or transcript replay is
introduced. Each task creates a credential-bearing process, flushes native
persistence before reporting idle, then releases the process. The next task
loads the same native session with its own credential lease.

`workos-workspace.mjs` implements the official `ctx.fs` and `ctx.shell`
backend seams. The native read/write/edit and Bash tools retain their native
policy and rendering. Every operation passes through the parent worker's
private mTLS `TaskToolService`; Core derives the owner, project and pinned
workspace revision from the live task lease and rechecks authorization while
commands run. Runtime resolves the actual registered directory. Neither tool
arguments nor the Harness environment choose a host directory or device.

Runtime commands use a dedicated Docker toolchain with only the project tree
mounted at `/workspace`, no network, a read-only image, a bounded temporary
filesystem, dropped capabilities, no-new-privileges and PID/CPU/memory limits.
The Docker socket is available only to Runtime. Foreground timeout/cancellation
removes the container and descendants. Missing backend configuration fails
unavailable; there is no local Bash fallback. The official search plugin is
not loaded because it bypasses the fs/shell seam through a host subprocess;
search may use Bash in the same isolated workspace. Background Bash is disabled;
application preview owns persistent processes.

The per-turn output budget is applied to every native model request and
reduced by reported usage. Missing usage blocks further requests. Provider
retries are disabled for this composition. Core serializes durable input and
event transitions, repairs lost dispatch/finalization acknowledgements, and
never reclaims an expired continuous execution for another model run. Failed
or interrupted sessions enter `needs_review`; queued inputs wait for the user
to inspect effects and start a new session.

System tools use the same lease-bound bridge: project/workspace facts,
project artifact listing/reading, and review artifact publication through the
existing task-provenance materializer. There are no synthetic owner/device
headers. The official user-questions provider routes ask_user_question to a canonical
Core interaction and the execution UI. Answers are task/lease scoped, expire
after two minutes and are idempotent. Sandbox escalation remains unavailable;
unsupported approval events fail closed. The system tools also list installed
applications and start/list/stop isolated project development previews.

Evidence distinguishes layers: `TestSessionTransactionsAndInterruptedLease`
uses isolated PostgreSQL and includes concurrent input keys, rollback,
expired leases, and recovery after more than 200 inputs.
`TestRealWorkspaceContainerIsolation` runs real Docker commands and checks
shared files, read-only access, escape denial and timeout.
`TestSessionManagerAgainstRealRuntime` uses the pinned official binary, a
local model fixture and the Runtime execution fixture, then reaps and replaces
the native process before checking historical context. This is deterministic
fixture evidence, not the still-pending real-model or second-device acceptance.

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
record. The owner's
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

The complete deterministic chain is `make test-v2-completion`: isolated
PostgreSQL/Vault and six product processes, the official native runtime, real
Docker tools, two code/test turns across Core/Harness/Gateway restart, review
artifact, question, preview, control epochs and Chromium native input.

Real API and physical-device acceptance remain separate from fixture evidence.
The ordinary suite never spends quota. Monetary cost is not inferred from a
hardcoded product price table; an explicitly budgeted acceptance must check
current official rates and use bounded requests through Vault. See the task
record for actual A15/A16 results.
