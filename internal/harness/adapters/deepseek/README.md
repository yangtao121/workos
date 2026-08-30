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
- `context_refs`, requested capabilities, structured artifacts, cost budgets,
  tools, MCP, approvals, sessions, workspace access, and subagents are rejected
  or disabled rather than silently ignored.
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
