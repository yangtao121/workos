# P0 auxiliary editor visual record

- Surface: Desktop Home launchpad and Code entry background.
- Fixture: `apps/desktop-web/e2e/code-entry-visual.spec.ts` with fixed `Studio` project, canonical fixture IDs, mocked unavailable Native viewer, no owner or provider data.
- Browser and viewport: Chromium, 1440×900, DPR 1, UTC, English locale.
- Capture: run Vite for this checkout on loopback, then set `WORKOS_CODE_ENTRY_CAPTURE_DIR` to this task's `after/`, `WORKOS_CODE_ENTRY_EXPECT_NATIVE=1`, and run the explicit Playwright visual spec.
- Difference: Home now includes the Text Editor entry beside Code; existing Code viewer and desktop chrome follow the same fixture.
- [Before](before/home--launchpad--1440x900.png) and [after](after/home--launchpad--1440x900.png) are from the same deterministic fixture. The opened Code frame is also recorded in both folders.

These UI records document the application launcher only. The real Mousepad pixels and clipboard path require the separate owner-run LAN gate; they are not represented by the fixture's unavailable viewer.
