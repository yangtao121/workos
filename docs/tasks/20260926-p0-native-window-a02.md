# Task: P0 A02 native Code window focus, stacking, drag and resize

- Status: in_progress
- Branch/worktree: `feat/p0-native-code-window-a02` / `/home/aquatao/workos-p0-native-a02`, based on `main@70a6c52`.
- Scope: Desktop native-window projection ordering and the existing owner-run real Code `dialog-close` phase. No Runtime, Proto, migration or credential handling change.
- Dependencies: V3 §3.1/§6.1 A02, ADR-0040/0041, `projectGreenfieldWindows`, WorkOS window reducer, resident Code E2E and native child close route.
- Coordination: the parallel auxiliary Text Editor task owns separate Desktop app-entry/type-restoration edits. This task touches only the `.workos-window` parent identity attribute in Desktop, the native projection/reducer test and Code E2E.

## Acceptance

1. After the user focuses a native Code parent, a transient child remains visually above that parent. Focusing another WorkOS app still raises that app above the whole native workload group. The parent-child identity comes from the Runtime window snapshot and survives projection.
2. In the authenticated LAN Code browser gate, the real `Open File` child exposes the exact parent ID; its title bar can be dragged within the desktop and the moved bounds persist through a fresh native snapshot while its complete frame remains visible.
3. Resizing the child changes the shell and native frame size at the active DPR. Parent/child IDs and Code workload generation stay unchanged. Closing only the child still leaves Code running.
4. Focused unit and deterministic Chromium fixture tests plus fixed before/after/current 1440×900 visual evidence cover the local stacking behavior. Real Code gate remains owner-run; no password is read by this task.

## Evidence and verification

- Focused `projectGreenfieldWindows.test.ts` regression proves parent focus cannot cover its child, another Core app remains above the native group when focused, and child move/resize bounds plus parent identity survive reconciliation. Desktop Web TypeScript and targeted Vitest passed in Node 24.19.0.
- Deterministic Chromium 1440×900 [before](../ui/desktop-web/changes/20260926-p0-native-window-a02/before/greenfield-resident-windows--parent-focus--1440x900.png), [after](../ui/desktop-web/changes/20260926-p0-native-window-a02/after/greenfield-resident-windows--parent-focus--1440x900.png), [current](../ui/desktop-web/current/greenfield-resident-windows--parent-focus--1440x900.png), and [capture notes](../ui/desktop-web/changes/20260926-p0-native-window-a02/notes.md). The before image reproduces the parent-obscures-child bug at the same overlap point.
- The existing owner-run real Code `dialog-close` phase now asserts exact parent/child IDs; child hit testing above Code after parent focus and after each drag/resize; a moved title-bar position retained through fresh native frame delivery; active-DPR frame dimensions and nonflat pixels after resize; and child-only close with Code/workload identity intact. The runner independently checks that the resident Docker child ID, start instant and PID stay unchanged across this phase. The phase has passed TypeScript, ESLint and Playwright discovery, but has **not** run with the owner's credential. Static/fixed-fixture results do not complete A02.
