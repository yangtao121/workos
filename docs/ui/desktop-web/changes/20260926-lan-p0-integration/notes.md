# LAN P0 integration visual record

- Task: [V3 P0 and LAN HTTPS integration](../../../../tasks/20260926-lan-p0-integration.md).
- Client and route: `desktop-web` at `/`.
- States: Home launchpad with Code, Text Editor and Terminal entries; Code opened in the Native viewer with a deterministic unavailable display fixture.
- Fixture: `apps/desktop-web/e2e/code-entry-visual.spec.ts` installs `sharedDesktopFixture`, fixed project/session IDs and RPC replies. It calls no live provider or authenticated service and contains no user data or credential.
- Browser: pinned Playwright Chromium 1.62.1; 1440 × 900 CSS viewport, device scale factor 1, locale `en-US`, timezone `UTC`.
- Capture: start Vite on loopback, then set `WORKOS_E2E_URL=http://127.0.0.1:5192`, `WORKOS_CODE_ENTRY_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260926-lan-p0-integration/after`, and `WORKOS_CODE_ENTRY_EXPECT_NATIVE=1`; run `node node_modules/@playwright/test/cli.js test code-entry-visual.spec.ts --workers=1` in the pinned Playwright container. Result: one passing test.
- Difference: Home now calls the lifecycle list “App sessions” and exposes the Text Editor entry alongside Code and Terminal. The opened Code fixture retains its explicit unavailable state while the real owner authenticated LAN gate remains pending.
- [Before launchpad](before/home--launchpad--1440x900.png), [after launchpad](after/home--launchpad--1440x900.png); [before Code entry](before/code-entry--opened--1440x900.png), [after Code entry](after/code-entry--opened--1440x900.png). The after files are copied to `current/` with the same names.

## Integrated V2 visual refresh

- The final `sh tools/v2-completion/gate.sh` used the deterministic `apps/desktop-web/e2e/v2-completion-visual.spec.ts` and a separate real fixture integration. Its first run exposed an out-of-date fake Native session response; the test fixture now answers `GetNativeSession` for the exact Code workload ID. The rerun passed four Chromium cases, including all three visual viewports.
- The V2 visual fixture uses `sharedDesktopFixture`, fixed IDs and test content, Chromium, UTC and device scale factor 1. It captures 1440×900, 820×1180 and 390×844. It does not use the owner account, passwords, live project content or external services.
- Compare the gate output under `tmp/v2-completion.BTWToB/visuals/` with `current/`. The 13 files whose rendered pixels changed were copied from the prior `current/` to this task's `before/`; the gate output was copied to `after/` and the same named `current/` files. Unchanged V2 captures remain at their existing `current/` baseline. The most visible changes are the Code observer title and the Home “App sessions” and Text Editor content behind other windows.
- [Before V2 states](before/), [after V2 states](after/). All copied PNG files are under 2 MiB each.
