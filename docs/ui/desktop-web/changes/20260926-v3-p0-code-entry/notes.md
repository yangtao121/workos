# Code main entry visual record

- Task: [V3 P0 Code main entry](../../../../tasks/20260926-v3-p0-code-entry.md).
- Client/route: desktop-web `/`, expanded desktop with synthetic `Studio` project; Home Code card and opened Code action.
- Fixture: `apps/desktop-web/e2e/code-entry-visual.spec.ts`, using the deterministic shared desktop and project fixtures. The native service response deliberately reports a fixture display unavailable; neither image claims real Code pixels or contains credentials.
- Browser: pinned Chromium Playwright 1.62.1; viewport 1440×900; device scale factor 1; clock fixed at 2026-09-06 09:00 UTC.
- Capture: run Vite for each revision on `http://127.0.0.1:5174`, then run `node node_modules/@playwright/test/cli.js test code-entry-visual.spec.ts --workers=1` in `workos-playwright:1.62.1` with `WORKOS_CODE_ENTRY_CAPTURE_DIR` set to this task's `before/` or `after/` and `WORKOS_E2E_URL` set to that local address. For `after/`, set `WORKOS_CODE_ENTRY_EXPECT_NATIVE=1`; for the baseline, leave it unset.
- Before: Home Code hint says “Review proposed changes”; opening it shows the `CodeApp` patch review empty state.
- After: Home Code hint says “Open WorkOS Code in this project”; opening it requests a native workload, displays the native viewer's honest unavailable fixture state, and marks Code running in the Dock. The actual LAN Code browser gate separately verifies live pixels.
- Current: `home--launchpad--1440x900.png` and `code-entry--opened--1440x900.png` from `after/` are copied to `current/`. The old current Home image and the fresh task `before/home--launchpad--1440x900.png` have identical SHA-256 bytes.
