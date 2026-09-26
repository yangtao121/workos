# LAN P0 resident Code failure and authorization gate

- Status: in_progress. The browser scenarios below are authored, not yet run with the owner's password.
- Owner/branch/worktree: Codex, `feat/lan-p0-failure-gate`, `/home/aquatao/workos-p0-failure-gate`.
- Baseline: `8c0b102`; the separate worktree was clean at creation.
- Scope: a credential-free-authored, owner-run Chromium acceptance gate against the already prepared isolated Code project. It tests active window and frame streams after attachment loss and session revocation, and actual Chromium clipboard denial and size handling. No product UI, Proto, migration, workload lifecycle, or owner credential change.
- Dependencies: LAN HTTPS CA and password mode, Runtime-resident Code child, the successful Code gate's prepared project record and running workload, and the owner's private password fixture. Run after `test-code-p0.sh`; this gate never creates or stops Code.

## Acceptance

- [ ] At fixed 1440×900, DPR 1, an authenticated Chromium profile displays nonuniform pixels from the actual Code top-level. No route mock or TLS bypass is used.
- [ ] While the actual Code viewer and independent `WatchGreenfieldWindows` and `WatchGreenfieldWindowFrames` subscriptions are live, `DetachSurface` for that profile closes both subscriptions within 15 seconds. The old attachment loses clipboard read and input access, the Code window disappears or becomes read-only, and a visible native-service error remains. The same workload stays running.
- [ ] A separate admin Chromium profile revokes a second profile's test device after that victim has two active media subscriptions and a real Code window. Both subscriptions close within 40 seconds of revocation, the victim's protected RPCs return 401, the Code window is no longer interactive, and the Desktop or auth gate visibly reports unavailable access. The admin's session remains valid. Only the gate-created victim device is revoked.
- [ ] Chromium's real clipboard with more than 1 MiB text shows the size error; the marker never reaches the actual Code buffer. With Chromium clipboard-read permission explicitly denied, paste shows the permission error and its marker likewise does not reach Code. Permissions are restored before native-copy verification. No clipboard contents are written to evidence.
- [ ] Password/cookie/source content never enters console output, results, screenshots, traces, or video. Owner records actual command/result; before that, A09 remains unpassed.

## Contract and data

Uses existing `DeviceService`, `SurfaceContinuityService`, and `GreenfieldWindowService` only. Connect streaming requests use the public framed JSON protocol against the real Gateway. The test reads one chunk of each live stream, consumes subsequent chunks without retaining them, and records only completion times. It does not infer stream closure from an unauthorized new request or an HTTP 404. The isolated project may already contain edits made by the earlier Code gate; this gate performs no save.

## Verification and handoff

Implemented [browser spec](../../apps/desktop-web/e2e/lan-code-p0-failures.spec.ts), [runner](../../tools/lan/test-code-p0-failures.sh), and [runbook](../runbooks/lan-code-p0-failure-e2e.md). Static checks in pinned Node 24 with the main checkout's installed dependencies: Desktop `tsc --noEmit` passed; ESLint on the new spec passed; Playwright `--list` discovered one case; Prettier formatted the spec and checked both Markdown files. `bash -n` and `git diff --check` passed. The runner's no-fixture preflight exited 2 before contacting Docker or Gateway. The framed JSON request shape was checked against the pinned Connect Web transport source, including `Connect-Protocol-Version: 1` and the five-byte envelope.

No owner password was supplied, so no authenticated browser scenario or A09 result is claimed. After cherry-picking this task, integrate `test-code-p0-failures.sh` as the final stage of `run-browser-p0.sh`, reusing its temporary credential file before the trap removes it. The main checkout must run `make generate` and `make check`; the owner then runs the one-command gate and records its actual result. The P0 matrix and `docs/status.json` remain non-passing until that evidence exists.
