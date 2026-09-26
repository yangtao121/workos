# Native parent focus visual record

- Task: [P0 A02 native windows](../../../../tasks/20260926-p0-native-window-a02.md).
- Surface/state: WorkOS Code parent and `Open File` transient after local focus raises the parent. `before/` shows the parent incorrectly covering the child; `after/` shows the child retained above the parent. The images contain labeled synthetic frames, no real Code/user content.
- Route: `/e2e/fixtures/greenfield-viewer-ready.html?parent-focused=1`.
- Fixture: the existing resident-viewer fixture projects a fixed Code window and child with Runtime IDs, then applies the WorkOS window reducer's `focus` action to Code and calls the production `mergeGreenfieldWindows` function for z-order. The same fixture code, route and state produced both images; the only difference is the production stacking algorithm.
- Browser: Playwright Chromium 1.62.1, 1440×900 viewport, device scale factor 1, `zh-CN`, UTC, reduced motion; animation disabled.
- Capture command: `WORKOS_E2E_URL=http://127.0.0.1:5199 WORKOS_CAPTURE_DIR=<task>/before|after node node_modules/@playwright/test/cli.js test greenfield-viewer-visual.spec.ts --grep 'native child after the Code parent' --workers=1` in `workos-playwright:1.62.1` against local Vite.
- The test samples `document.elementFromPoint(800, 400)` in the known overlap and requires the dialog window ID. `current/greenfield-resident-windows--parent-focus--1440x900.png` matches `after/`.
