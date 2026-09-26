# Task: P0 Terminal text clipboard

- Status: in_progress (isolated Chromium gate passed; owner-authenticated LAN and real PTY clipboard acceptance pending)
- Branch/worktree: `feat/p0-terminal-clipboard` / `/home/aquatao/workos-p0-terminal-clipboard`
- Base: `main@8c0b102`; isolated from the concurrent Native window-close task.
- Scope: the existing Desktop `TerminalApp` and its supervised PTY session. Browser text copy/paste, 16 KiB ordered writes, explicit feedback, and single-controller lease safety.
- Dependencies: V3 §3.3/§6.1, ADR-0028 supervised PTY, ADR-0031 surface control, existing `workos.surface.v1.PtySessionService` and `SurfaceContinuityService` contracts. No Proto or migration change.

## Acceptance

1. In standard Chromium, selecting terminal output and pressing Ctrl+C copies UTF-8 text; Ctrl+C without a selection sends ETX to the PTY. Ctrl+V and an explicit Paste action send the browser's text clipboard to the same PTY session. The Copy selection action reports permission failure explicitly.
2. More than 256 characters, including Chinese, emoji, tabs and newlines, survive ordered PTY writes with each RPC at or below the existing 16 KiB bound. A failed chunk stops later chunks and never reports success.
3. Observer sessions cannot write. Loss or expiry of control switches the view to observer and cancels queued input; only explicit Take control can request a new lease.
4. Unit and deterministic Chromium fixture tests cover the behavior. Fixed 1440×900 `before/`, `after/`, and `current/` screenshots record the visible controls and feedback. Integrated credential-bearing LAN acceptance remains owner-run.

## Evidence and verification

- [Architecture note](../architecture/terminal-clipboard.md) describes the existing Runtime boundary, UTF-8 chunking, control lease and uncertain-write handling.
- Fixed Chromium 1440×900 screenshots: [before](../ui/desktop-web/changes/20260926-p0-terminal-clipboard/before/terminal-window--clipboard-controls--1440x900.png), [after](../ui/desktop-web/changes/20260926-p0-terminal-clipboard/after/terminal-window--clipboard-controls--1440x900.png), [current](../ui/desktop-web/current/terminal-window--clipboard-controls--1440x900.png), [capture notes](../ui/desktop-web/changes/20260926-p0-terminal-clipboard/notes.md). The `before/` image was taken from the base commit with the same deterministic fixture before implementation.
- Verified in the isolated worktree using `node:24.19.0-bookworm-slim`: `tsc --noEmit` for desktop-web, Vitest `TerminalApp.test.tsx` plus `terminalClipboard.test.ts` (11 passing), ESLint on all changed TypeScript files, and Prettier on changed source/docs.
- Verified with `workos-playwright:1.62.1` against local Vite and a deterministic Connect fixture: `terminal-clipboard.spec.ts` (3 passing). It uses the actual Chromium Clipboard API for Ctrl+C/Ctrl+V and asserts ordered UTF-8 RPC bytes, ETX, and observer refusal. No real owner credentials or project data were used.
- Remaining whole-system acceptance: the owner-run LAN browser gate must exercise the Terminal against its real Runtime PTY with actual text clipboard in the authenticated browser. This fixture E2E does not prove shell line discipline or HTTPS login. Keep P0 clipboard status no higher than scaffolded until that gate passes.
