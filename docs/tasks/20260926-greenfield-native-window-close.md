# V3 P0 native window close routing

- Status: implementation complete; owner-run HTTPS Code browser gate pending. `docs/status.json` remains at most scaffolded until that gate passes.
- Owner: Desktop Web and resident Runtime/child, branch `codex/native-window-close` in `/home/aquatao/workos-native-window-close`.
- Scope: route WorkOS title-bar Close by native window relationship. A top-level closes its Core desktop anchor and detaches only this viewer. A transient child requests native closure for its exact Runtime window ID, without detaching the Code viewer or stopping the workload. Keep it visible until Runtime removes its ID.
- Dependencies: [resident viewer](20260926-greenfield-window-viewer.md), Core shared desktop, [ADR-0041](../decisions/0041-native-transient-window-close.md), generated `GreenfieldWindowClose` contract, resident Runtime window snapshot and attachment/control barrier.
- Acceptance: no parentless/native top-level close reaches the child compositor; stale, orphaned, observer and old-control requests fail; actual Code `Open File` child disappears from the Runtime snapshot while its parent and Code process remain; no local-only removal; visible pending/error state and fixed fixture visuals; owner-run browser gate verifies Gateway/Runtime/child together.

## Implementation

The `close` event is one generated Proto input variant and uses the existing per-attachment sequence ledger, control generation and per-event Runtime barrier. Runtime's resident broker accepts it only for a current child whose parent remains in the running snapshot. The child then checks its current client/surface identity and parent before calling the pinned compositor role's `requestClose()`. For XWayland, it requires `WM_DELETE_WINDOW` and flushes the XCB connection after the request; fixed rc1 otherwise queues an event that never reached the real Code dialog in the isolated test. Desktop waits for snapshot removal, disables a duplicate close while pending, and reports an explicit error for denial or timeout. The top-level path still closes the Core anchor, which detaches the viewer without stopping Code.

The owner-run `tools/lan/test-code-p0.sh` now includes a `dialog-close` phase. It opens the real Code File→Open File native child in trusted Chromium, closes its WorkOS title bar, waits for the child projected from the Runtime snapshot to disappear, checks the Code top-level/workload generation, and compares the resident child container ID, host PID and StartedAt before/after. The E2E also restores focus to the native input before submitting Quick Open, explicitly retakes control after an expired offline lease, and waits for the automatically opened Code window after Restart.

## Evidence and verification

- [Real isolated Code child probe](evidence/20260926-greenfield-native-window-close/probe.md) and [structured close result](evidence/20260926-greenfield-native-window-close/code-dialog-close.json): `Ctrl+O` created `Open File`, exact child Close removed only its ID, parent stayed, Code PID/starttime and container identity stayed. No owner password or user content entered this diagnostic.
- [Before ready](../ui/desktop-web/changes/20260926-greenfield-native-window-close/before/greenfield-resident-windows--synthetic-ready--1440x900.png), [after ready](../ui/desktop-web/changes/20260926-greenfield-native-window-close/after/greenfield-resident-windows--synthetic-ready--1440x900.png), [after pending](../ui/desktop-web/changes/20260926-greenfield-native-window-close/after/greenfield-resident-windows--close-pending--1440x900.png), and [capture notes](../ui/desktop-web/changes/20260926-greenfield-native-window-close/notes.md). The after images are copied into `current/`; they are synthetic and do not claim real Code pixels.
- `go test ./internal/runtime/nativehost/... -count=1`: all nativehost packages passed in `golang:1.26.7-bookworm`.
- Desktop Web TypeScript `tsc --noEmit` and Vitest `run src`: 39 files / 251 tests passed in Node 24.19.0.
- Resident child TypeScript `tsc --noEmit` and Vitest `run src`: 2 files / 7 tests passed in Node 24.19.0.
- Playwright Chromium fixed-viewport visual capture: 3 tests passed in `workos-playwright:1.62.1`; LAN Code spec discovery and `bash -n tools/lan/test-code-p0.sh` passed.

## Remaining gate

The owner-selected browser gate runs with the owner's password fixture outside the repository. Its `dialog-close` phase has not run in this worktree. `make check` and the full `make generate` clean-tree verification belong to the main integration after the contract cherry-pick. A02 native child focus, layering and drag in the full browser, and A08 Code app Exit/fault lifecycle remain separate acceptance work; the unit projection tests and isolated close probe do not prove those cases.
