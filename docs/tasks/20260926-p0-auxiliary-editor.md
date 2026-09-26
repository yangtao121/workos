# Task: P0 lightweight graphical editor

- Status: in_progress
- Owner: Codex; branch `feat/p0-aux-editor`, worktree `/home/aquatao/workos-p0-aux-editor`
- Scope: add Mousepad as a separately supervised Native session beside WorkOS Code; keep WorkOS Terminal as the existing terminal app. Add a Home entry, persistence, restart/reattach identity, and an owner-run LAN browser gate for Code/editor isolation and text clipboard.
- Dependencies: V3 P0 resident Greenfield child, ADR-0040, current Native session and shared Desktop contracts. Native transient close is developed separately and will be merged first by the integration owner.
- Acceptance: Code and editor have distinct session IDs, child identities, windows, and explicit Stop/Restart effects; Close detaches one viewer without hiding the other. Graphical editor opens a project file and exchanges Unicode plain text with Chromium. No synthetic success is accepted. Browser gate uses the owner-provided credential fixture locally and preserves CA validation.
- Evidence plan: focused Go/TypeScript tests; deterministic Home `before/after/current` screenshots; real LAN browser gate after deployment. The task cannot be marked done until real graphical E2E and visuals pass.

## Handoff

Pending.
