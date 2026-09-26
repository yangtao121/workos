# LAN password Desktop visuals

- Task: [`docs/tasks/20260926-lan-password-ui.md`](../../../../tasks/20260926-lan-password-ui.md).
- Browser: Playwright Chromium 1.62.1; viewport 1440×900; device scale factor 1; `en-US`; UTC.
- `before/` copies the previous pairing entry and Device Center screenshots from `current/` before implementation. There was no password entry in the previous build.
- `after/auth-gate--password-login--1440x900.png` uses the real Desktop entry route (`/`) with deterministic `PasswordAuthService.GetMode=PASSWORD` and expired `DeviceService.GetCurrentDevice` responses. The new login form is intentionally empty; no credential appears.
- `after/device-center--password-devices--1440x900.png` uses `/e2e/fixtures/lan-password-device-center.html`, rendering the production `DeviceCenter` component with a fixed device and 2030 session expiry. The pairing action is absent and Sign out remains.
- Capture: start Vite on localhost port 5174, then run `WORKOS_E2E_URL=http://127.0.0.1:5174 WORKOS_CAPTURE_DIR=/src/docs/ui/desktop-web/changes/20260926-lan-password-ui/after playwright test lan-password-visual.spec.ts` inside the Playwright 1.62.1 image.
- The two `after/` files were copied to `current/`. Existing pairing baselines remain because pairing mode still exists.
