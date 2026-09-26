# Resident Greenfield viewer visual record

- Task: [20260926-greenfield-window-viewer](../../../../tasks/20260926-greenfield-window-viewer.md).
- Client and states: Desktop Web native display unavailable state, plus two separate WorkOS Shell native window bodies with complete synthetic PNG frames, observer input disabled, and a visible takeover action in each window.
- Route/fixture: `/e2e/fixtures/greenfield-unavailable.html` and `/e2e/fixtures/greenfield-viewer-ready.html`.
- Browser: Playwright Chromium 1.62.1, viewport 1440×900, device scale factor 1, `zh-CN`, UTC, reduced motion.
- Capture: Vite at `http://127.0.0.1:5175`; `WORKOS_CAPTURE_DIR=/src/docs/ui/desktop-web/changes/20260926-greenfield-window-viewer/after WORKOS_E2E_URL=http://127.0.0.1:5175 playwright test e2e/greenfield-input-visual.spec.ts e2e/greenfield-viewer-visual.spec.ts --config=playwright.config.ts` in the repository's `workos-playwright-shared:1.62.1` image.
- Before: [unavailable](before/greenfield-window--unavailable--1440x900.png) is the prior `current/` baseline. The resident multi-window surface was new, so it has no matching prior screenshot.
- After: [unavailable](after/greenfield-window--unavailable--1440x900.png) updates the hint to match disabled input; [resident windows](after/greenfield-resident-windows--synthetic-ready--1440x900.png) records the new window bodies and visible takeover buttons. Both are copied to `current/`.
- Fixture scope: the resident image labels itself synthetic. It exercises complete tile decode/draw and the separate Shell window layout; it is **not** a capture of real Code, native focus, live LAN media, or clipboard. Those require the Runtime/Gateway integration gate.
