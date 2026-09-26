# Surface control renewal: visible loss of input

- Task: [P0 Surface control renewal](../../../../tasks/20260926-p0-surface-control-renewal.md).
- Surface: two resident native windows and their takeover buttons.
- Route: `/e2e/fixtures/greenfield-viewer-ready.html?lease-loss=1`.
- Fixture: deterministic synthetic PNG tiles; it begins as a controller and sends one synthetic input after 500 ms. The input receives `UNAVAILABLE`, without any real app, user data, or external service. The same fixture source was copied into the `62963d2` baseline worktree before capture.
- Browser and viewport: Playwright Chromium in `workos-playwright:1.62.1`, 1440×900, scale factor 1, `zh-CN`, UTC; capture after the first frame and 1400 ms settle time.
- Capture: `pnpm exec playwright screenshot --viewport-size='1440,900' --lang=zh-CN --timezone=UTC --wait-for-selector='[data-frame-state="ready"]' --wait-for-timeout=1400 <route> <file>` against baseline and changed Vite servers. The repository test is `pnpm exec playwright test greenfield-viewer-visual.spec.ts` with `WORKOS_CAPTURE_DIR` and `WORKOS_E2E_URL`.
- Intentional difference: before, rejected input disabled interaction but left both native windows without a takeover button. After, both windows present `接管输入`. The canvas content and window geometry are unchanged.
- Current baseline: [control lost](../../current/greenfield-resident-windows--control-lost--1440x900.png).
