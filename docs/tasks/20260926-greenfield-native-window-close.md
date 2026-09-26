# V3 P0 native window close routing

- Status: in_progress.
- Owner: Desktop Web, branch `codex/native-window-close` in `/home/aquatao/workos-native-window-close`.
- Scope: verify and harden WorkOS title-bar close handling for Runtime-projected Greenfield windows. Closing a native top level must remove its Core desktop anchor and detach this viewer without stopping the workload; a transient child close must not detach the whole Code viewer or disappear only from local projection.
- Dependencies: resident window projection (`20260926-greenfield-window-viewer.md`), Core shared desktop, `GreenfieldWindowService` input contract, Runtime's native `parent_window_id` facts.
- Acceptance: tests exercise the real Desktop close path and subsequent projection reconciliation, prove no native Stop/Close is called, and distinguish a child with `parent_window_id`. Document any missing native child close capability honestly. Complete module documentation, visual evidence if user-visible behavior changes, and run focused Desktop checks.
- Existing behavior at start: `Desktop.dispatch` already closes the Core anchor for every projected native window; this covers top-level detach, but a transient child title-bar close also removes the whole Code viewer. The v1 Greenfield input contract has no native close event.
