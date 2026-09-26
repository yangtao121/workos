# Native session names visual record

- Task: [P0 Native session display names](../../../../tasks/20260926-p0-native-session-display-names.md).
- Client/route: Desktop Web Home `/`; pinned Chromium, UTC, deterministic `sharedDesktopFixture`, device scale factor 1. No owner data, credentials, external services, or live Native frames.
- Viewports: 1440×900, 820×1180, and 390×844. Every before/after pair uses the same code, route, fixture rows, and viewport; only the simulated Runtime API display names change from generic `Native display` to `WorkOS Code` and `Text Editor`.
- `home--running-apps` uses the existing `v2-completion-visual.spec.ts` fixture with one Code row. `home--two-native-apps` uses `native-session-names-visual.spec.ts` with simultaneous Code and Text Editor rows and both Stop/Restart controls. The Go tests prove the actual Runtime mapping that these API fixtures represent.
- Capture: start Vite at `http://127.0.0.1:5178`, then run pinned Playwright with `WORKOS_E2E_URL` and `WORKOS_V2_CAPTURE_DIR=<before-or-after> WORKOS_V2_HOME_ONLY=true` for the existing fixture; add `WORKOS_V2_LEGACY_NATIVE_NAME=true` for before. For the dual Native fixture use `WORKOS_NATIVE_NAMES_CAPTURE_DIR=<before-or-after>` and `WORKOS_NATIVE_NAMES_LEGACY=true` for before. Both specs passed at all three viewports. Copy `after/` images to matching `current/` filenames.
