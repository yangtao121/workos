# Task: V3 P0 Code main entry

- Status: implemented in task branch; repository-wide integration gate pending.
- Owner/branch/worktree: Codex, `feat/v3-p0-code-entry`, `/home/aquatao/workos-code-entry`.
- Baseline: `62963d2`; the task worktree was clean at creation. Main's LAN integration record is owned by another task.
- Scope: route the Home, command palette, and Dock Code action to the existing native session startup/focus path; show the running native workload under Code in the Dock. Keep old `kind: "code"` rendering for restored layouts and leave Artifact Center review available.
- Dependencies: the resident Greenfield Runtime, NativeSessionService, and current project. No Proto or native session lease changes.

## Acceptance

- [x] Code action calls `CreateNativeSession` for the active project and opens a `kind: "native"` window; repeated Code action focuses it without a second creation.
- [x] Dock marks Code running for the native workload and clicking Code refocuses it. Native and legacy Code layouts remain renderable.
- [x] Fixed 1440×900 Chromium before/after/current screenshots use the same synthetic project and API fixture, with no credentials or real user data.
- [x] Desktop unit test, typecheck, lint, format and relevant browser capture pass. Repository-wide `make check` awaits integration on main.

## Visual evidence

- Home launchpad: [before](../ui/desktop-web/changes/20260926-v3-p0-code-entry/before/home--launchpad--1440x900.png), [after](../ui/desktop-web/changes/20260926-v3-p0-code-entry/after/home--launchpad--1440x900.png), [current](../ui/desktop-web/current/home--launchpad--1440x900.png).
- Opened Code action: [before](../ui/desktop-web/changes/20260926-v3-p0-code-entry/before/code-entry--opened--1440x900.png), [after](../ui/desktop-web/changes/20260926-v3-p0-code-entry/after/code-entry--opened--1440x900.png), [current](../ui/desktop-web/current/code-entry--opened--1440x900.png).
- [Fixture, viewport and capture notes](../ui/desktop-web/changes/20260926-v3-p0-code-entry/notes.md). The prior current Home PNG is byte-identical to this task's Home before PNG.

## Verification and handoff

- `vitest run src/Desktop.test.tsx src/sharedDesktopWindows.test.ts` in pinned Node 24: 19/19 passed. The new test proves Home Code creates a native workload for the active project, opens the native renderer, marks Dock Code running and refocuses without another creation.
- `tsc --noEmit`: passed.
- ESLint on the five changed desktop TypeScript files and Prettier check on those plus task/visual notes: passed.
- Chromium Playwright 1.62.1 fixed synthetic capture at 1440×900, DPR 1: baseline and after runs each passed. The after run asserts `CreateNativeSession` once, native viewer, no patch-review window, Code Dock running and no second creation on Dock click.
- `python3 -m json.tool docs/status.json` and `git diff --check`: passed. No Proto contract changed, so generation was not needed for this slice. `make check` is reserved for the parent integration worktree; this task remains short of done until it passes there.

The live LAN Code gate belongs to [the resident browser E2E task](20260926-lan-p0-resident-browser-e2e.md); deterministic UI screenshots here do not claim a real Code pixel result.
