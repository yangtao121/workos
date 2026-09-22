# Shared desktop UI consumer

Status: implemented; integrated Core/Runtime gate and physical-device acceptance
are tracked by [the parent task](20260922-shared-desktop.md). Branch:
`feat/shared-desktop-ui`. Do not infer real Safari support from Linux WebKit.

Scope: server-authoritative shared project/window/focus/session state, responsive
shell, exact workload restoration and explicit manual-stop startup. Dependencies:
shared desktop/lifecycle contracts and Core DesktopService producer.

## Delivered

- Typed operations, authenticated snapshot bootstrap, resumable stream with reset,
  reference-only atomic projection/cursor cache, no stale offline command queue.
- Shared project/window/focus/session selection, Home default, Agent Sessions
  primary entry, local geometry, per-device capability reacquisition.
- Explicit PTY/Native creation before publishing, exact generation attachment,
  generation-aware restart/remount, transient network recovery.
- Installed app restore uses attach-only exact generation pins; access capability
  expiry renews a device view without creating/restarting a program.
- Manual-stop requests on explicit creates/restarts; window close only detaches.
- A five-second authoritative read fallback keeps a quiet or delayed event stream
  from blocking shared state convergence. Reads are sequential, with the next
  timer starting after completion; slow reads are not periodically cancelled.
  Revision checks reject older snapshots, and generation, abort, and reset guards
  discard responses from a previous connection or server reset. Authorization
  denial clears the cached projection and stops both stream and fallback reads.

Module documentation: [shared desktop client](../architecture/shared-desktop-client.md).

## Validation

Executed in pinned Node 24.19.0 / Playwright 1.62.1 containers without modifying
shared deployments or calling a paid provider:

- Desktop `tsc --noEmit`; full `vitest run src`: 29 files, 184 tests passed.
- Window manager: 11 tests; adaptive shell: 40 tests. Targeted strict ESLint
  and Prettier checks passed.
- `playwright test shared-desktop.spec.ts --workers=1`: three independent browser
  contexts synchronize project/window/focus and reload authority; Chromium and
  WebKit passed. Explicit Chromium capture run: all four tests passed.
- Root runs generated-code consistency, full `make check`, actual cross-process
  persistence/lifecycle gate, and session composer/pending visual states.
- Follow-up fallback coverage: `vitest run src/sharedDesktop.test.ts` passes all
  13 tests, including silent streams, slow reads without overlap/starvation,
  stale responses after reconnect/stop/reset, transient recovery, and revoked
  authentication. Desktop TypeScript and targeted ESLint/Prettier checks pass.
  This controller-only change has no new visual layout; existing evidence applies.

Visual evidence: [before](../ui/desktop-web/changes/20260922-shared-desktop-ui/before/),
[after](../ui/desktop-web/changes/20260922-shared-desktop-ui/after/),
[notes](../ui/desktop-web/changes/20260922-shared-desktop-ui/notes.md).
The same three after images update `current/`.

## Handoff

Parent integrated `clearLocalDesktopState()` into Desktop identity reset and AuthGate Forget;
active projections stop before transactional journal and layout deletion.
Device-local Surface/bridge credentials remain memory only; shared cache contains
canonical IDs and cursor only. Interrupted/stopped workloads remain references and
never silently fall back to starting new programs. Ordinary app DOM/forms/scroll
state remains app-owned.
