# A08 Native application lifecycle visual record

- Task: [P0 Native application Exit and failure](../../../../tasks/20260926-p0-native-application-exit.md).
- Client/route: Desktop Web Home at `/`; Chromium, UTC, deterministic `sharedDesktopFixture`, device scale factor 1.
- Before: branch base `aa86ef4`, captured with the same fixture and viewports. After: A08 implementation branch. No real owner session, workspace data, credentials, external services, or live child frames appear.
- Running list fixture: `e2e/v2-completion-visual.spec.ts`, `WORKOS_V2_CAPTURE_DIR`; 1440×900, 820×1180, 390×844. The only intentional change is the section's wording from “Running apps” to “App sessions” so recent terminal rows are described accurately.
- Terminal fixture: `e2e/a08-lifecycle-visual.spec.ts`, `WORKOS_A08_CAPTURE_DIR`; 1440×900. It supplies one stopped and one failed Native row, verifies Open is disabled and both Restart actions remain visible. The before capture shows how the old heading mislabeled those rows; the after capture shows the corrected heading.
- Capture command: run Vite for each checkout, then `WORKOS_E2E_URL=http://127.0.0.1:<port> WORKOS_V2_CAPTURE_DIR=<before-or-after> playwright test v2-completion-visual.spec.ts --grep 'development states'` and `WORKOS_A08_CAPTURE_DIR=<before-or-after> playwright test a08-lifecycle-visual.spec.ts` in the pinned `workos-playwright:1.62.1` image. The visual tests passed in both checkouts.
- `after/` was copied to the matching `current/` filenames. The real owner-run A08 Chromium gate proves terminal state and Restart against Code; these screenshots only show deterministic UI presentation.
