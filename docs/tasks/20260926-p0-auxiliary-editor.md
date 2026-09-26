# Task: P0 lightweight graphical editor

- Status: in_progress
- Owner: Codex; branch `feat/p0-aux-editor`, worktree `/home/aquatao/workos-p0-aux-editor`
- Scope: add Mousepad as a separately supervised Native session beside WorkOS Code; keep WorkOS Terminal as the existing terminal app. Add a Home entry, persistence, restart/reattach identity, and an owner-run LAN browser gate for Code/editor isolation and text clipboard.
- Dependencies: V3 P0 resident Greenfield child, ADR-0040, current Native session and shared Desktop contracts. Native transient close is developed separately and will be merged first by the integration owner.
- Acceptance: Code and editor have distinct session IDs, child identities, windows, and explicit Stop/Restart effects; Close detaches one viewer without hiding the other. Graphical editor opens a project file and exchanges Unicode plain text with Chromium. No synthetic success is accepted. Browser gate uses the owner-provided credential fixture locally and preserves CA validation.
- Evidence plan: focused Go/TypeScript tests; deterministic Home `before/after/current` screenshots; real LAN browser gate after deployment. The task cannot be marked done until real graphical E2E and visuals pass.

## Implementation and verification so far

- Native contract `NativeApplication` app kind and Runtime-owned migration 080 persist `code` versus `text_editor` per session. Existing omitted requests replay Code. Child command and Docker labels pin the selected app on adoption; Code's existing child profile remains stable.
- Mousepad 0.5.10-2 is installed in the isolated child image. The launcher selects `/usr/bin/mousepad` through `--application text_editor`; the isolated child owns its own Greenfield proxy, Chromium compositor and display.
- Home has an independent Text Editor entry, while Code and editor discovery refocus the correct live workload. The Desktop unit suite passed 18/18 tests, including distinct sessions and refocusing. Native application and resident adapter Go tests passed; the child Docker image built and reported Mousepad 0.5.10.
- [UI before](../ui/desktop-web/changes/20260926-p0-auxiliary-editor/before/home--launchpad--1440x900.png), [UI after](../ui/desktop-web/changes/20260926-p0-auxiliary-editor/after/home--launchpad--1440x900.png), [capture notes](../ui/desktop-web/changes/20260926-p0-auxiliary-editor/notes.md). The deterministic `code-entry-visual.spec.ts` capture passed on the branch's loopback Vite server.
- Real child diagnostic: GTK's default Wayland route did not publish a WorkOS window. With `GDK_BACKEND=x11`, XWayland mapped the real Mousepad window. The Greenfield record had a buffer but was initially not ready for capture; periodic geometry reconciliation now detects the late map and publishes frames. An isolated child using the same UID and Unix socket permissions as Runtime delivered a stable window ID across two broker connections, two complete four-tile PNG frames at 716×577, and an applied native focus input result. The first frame contains the actual Mousepad title, menus, and editor pixels. See [smoke result](evidence/20260926-p0-auxiliary-editor/mousepad-child-smoke.json) and [native frame tile](evidence/20260926-p0-auxiliary-editor/mousepad-first-native-tile.png). This proves the child graphics and input bridge, not the owner HTTPS browser path.

## Handoff

Pending: owner-run HTTPS browser gate must verify editor clipboard and Code/Terminal isolation. Run the auxiliary gate after authentication-failure tests because those tests assume a single Native workload.
