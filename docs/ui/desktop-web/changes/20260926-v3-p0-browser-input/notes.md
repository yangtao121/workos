# Greenfield browser input visual record

- Task: [V3 P0 browser input and clipboard](../../../../tasks/20260926-v3-p0-browser-input.md).
- Surface: WorkOS Code native window, Greenfield display unavailable state.
- Fixture: `apps/desktop-web/e2e/fixtures/greenfield-unavailable.html`. Its display RPC rejects with a fixed error. It uses no real account, Code process, secret, or external service.
- Browser: Playwright Chromium 1.62.1; viewport 1440×900; device scale factor 1; locale `zh-CN`; timezone UTC; reduced motion.
- Before: base commit `ccf4914`, same fixture and capture spec in a temporary detached worktree.
- After/current: browser input branch, same fixture and capture spec.
- Capture: run Vite on port 5175, then `WORKOS_CAPTURE_DIR=<before-or-after-dir> WORKOS_E2E_URL=http://127.0.0.1:5175 playwright test e2e/greenfield-input-visual.spec.ts --config=playwright.config.ts`.
- Intended difference: an explicit connection state, disabled text clipboard actions while unavailable, a bounded canvas stage, and a keyboard hint.
- Scope limit: these images show only the deterministic unavailable UI. They do not prove real Greenfield pixels, Mac IME, Wayland clipboard transfer, or multi-device observer behavior.
