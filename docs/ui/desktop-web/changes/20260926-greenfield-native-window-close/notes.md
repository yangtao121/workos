# Resident native child close visual record

- Task: [native window close routing](../../../../tasks/20260926-greenfield-native-window-close.md).
- Client: Desktop Web WorkOS window title bars. The existing ready and control-lost surfaces now show the Close control; a new pending state shows the child retained while Runtime decides whether it disappeared.
- Route and fixture: `/e2e/fixtures/greenfield-viewer-ready.html` with no query, `?lease-loss=1`, and `?close-pending=1`. All frames use the labeled synthetic PNG fixture. No live Code pixels or credentials are present.
- Browser: Playwright Chromium 1.62.1, 1440×900 viewport, device scale factor 1, `zh-CN`, UTC, reduced motion.
- Capture: Vite at `http://127.0.0.1:5177`; `WORKOS_CAPTURE_DIR=docs/ui/desktop-web/changes/20260926-greenfield-native-window-close/after WORKOS_E2E_URL=http://127.0.0.1:5177 playwright test greenfield-viewer-visual.spec.ts --workers=1` in `workos-playwright:1.62.1`.
- Before: [ready](before/greenfield-resident-windows--synthetic-ready--1440x900.png) and [control lost](before/greenfield-resident-windows--control-lost--1440x900.png) are copies of the preceding current baselines. There was no prior pending-close state.
- After: [ready](after/greenfield-resident-windows--synthetic-ready--1440x900.png), [control lost](after/greenfield-resident-windows--control-lost--1440x900.png), and [close pending](after/greenfield-resident-windows--close-pending--1440x900.png) were captured with the same fixture and copied to `current/`.
- These screenshots show deterministic UI states only. The owner-run Code gate checks the native close request, Runtime snapshot removal and unchanged Code process identity.
