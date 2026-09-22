# Shared desktop and launcher

Task: [shared desktop UI](../../../../tasks/20260922-shared-desktop-ui.md).

- Before: existing `current/home--launchpad` baseline, copied before editing.
- After/current: shared owner desktop, default Home, Agent Sessions primary entry,
  Tasks and approvals retained as a secondary application, Running apps before
  launch tiles, one focused responsive pane on phone/tablet.
- Route `/`; deterministic Studio/Field notes projects, empty installed/running
  app lists, fixed UTC time `2026-09-06T09:00:00Z`. No live account, credentials,
  user content or paid provider requests.
- Chromium, device scale factor 1, viewports 1440×900, 820×1180, 390×844.
- Fixture: `apps/desktop-web/e2e/shared-desktop.spec.ts` uses the existing desktop
  fixture plus canonical desktop RPCs served by an actual local Connect stream.
  The same file separately checks three independent contexts in Chromium and
  WebKit. Linux WebKit does not substitute for physical Safari acceptance.

Capture with a Vite instance serving this worktree and the pinned test image:

```sh
WORKOS_E2E_URL=http://127.0.0.1:5185 \
WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260922-shared-desktop-ui/after \
pnpm exec playwright test shared-desktop.spec.ts --workers=1
```

Images were inspected after capture. Application-specific conversation/pending
states are covered by the parent continuity task after integration.
