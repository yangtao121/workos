# V2 development and continuity completion

Task: [B00–B10](../../../../tasks/20260915-v2-agent-workspace-web-continuity.md).

- Before: 20 relevant existing `current/` baselines copied before implementation.
- After/current: 24 Chromium PNGs, eight states at 1440×900, 820×1180 and 390×844,
  device scale factor 1. Route `/`; fixture project Studio; fixed UTC clock
  2026-09-06 09:00. No real model, credentials or user data are used for screenshots.
- Source: `apps/desktop-web/e2e/v2-completion-visual.spec.ts`, built desktop served
  by the isolated Gateway. Browser image `workos-playwright:1.62.1`.
- Capture: `make capture-v2-development-journey` (also runs the real software
  chain). During implementation the same spec ran against the prepared isolated
  stack with `WORKOS_V2_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260916-v2-completion/after`.
- Assertions check the question choice is selectable and Send answer becomes
  enabled, Reject question is inside the viewport, conflicting file writes keep
  the edited draft, and the actual preview iframe contains its fixture page.
- An adaptive-pane focus bug discovered by this capture was fixed: focus now
  happens on pointer-down/focus instead of click capture, which previously
  reset radio activation before React's change handler on tablet/mobile.

Intentional differences: new direct file editing, workspace binding controls,
preview process controls, execution questions, needs-review recovery copy,
Native observer takeover and Terminal stopped state. The pre-change UI did not
have these states; before files show their closest existing entry points. Existing
Home running-app states use the same Studio fixture and viewports. Old screenshots
remain historical examples of their named states; these eight new state names
are the current baselines for the changed flows. The new question and preview
screens have no meaningful identical pre-change state. No pixel-equality claim
is made across different states.

`after/` was reviewed at desktop and mobile sizes. Real Native video, keyboard,
workspace writes and preserved shell memory are verified separately by
`v2-completion.spec.ts`; fixture images do not prove the media path. Physical
second-device LAN acceptance remains unexecuted, as confirmed by the user.
