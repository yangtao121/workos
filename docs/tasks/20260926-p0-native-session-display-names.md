# P0 Native session display names

- Status: done (scoped display-name projection; overall V3 P0 remains scaffolded until owner browser acceptance).
- Branch/worktree: `feat/p0-native-session-names` in `/home/aquatao/workos-p0-names`, based on `main@192e44b`.
- Scope: carry a trusted, application-specific Native display name through the existing Runtime Surface continuity port. Show `WorkOS Code` and `Text Editor` in Home workload rows so Stop and Restart identify the intended application. Preserve PTY and installed-app labels.
- Dependencies: persisted Native `Application` kind, Surface continuity projection, existing `SurfaceWorkloadView.display_name` Proto field; no Proto or migration change.
- Acceptance: Code, Text Editor, terminal and installed-app display names are tested at composition and transport boundaries; deterministic Home screenshots show two Native rows at fixed viewports, with before/after/current records. `make generate` leaves no generated diff and the relevant Go/UI gates pass.

## Findings

The Native store already persists `ApplicationCode` or `ApplicationTextEditor`, and the Desktop already renders `SurfaceWorkloadView.displayName`. The composition root drops the application kind when it builds `InteractiveWorkload`, then Surface transport assigns every Native row the generic `Native display` label.

## Implementation and evidence

- The composition root maps the persisted Native `ApplicationCode` to `WorkOS Code` and `ApplicationTextEditor` to `Text Editor` in `InteractiveWorkload.DisplayName`. The Surface transport copies that trusted label into existing `SurfaceWorkloadView.display_name`; legacy Native rows retain `Native display`. PTY stays `Terminal`; installed apps stay on their app ID. [Runtime lifecycle](../architecture/runtime-lifecycle.md) records this projection.
- `TestNativeWorkloadCarriesPersistedApplicationName` and `TestContinuityWorkloadViewDisplaysApplicationName` cover the composition and transport seams, including both existing workload families and the Native fallback. `go test ./cmd/runtime-host ./internal/runtime/surface/transport` passed in `golang:1.26.7-bookworm` with the host proxy forwarded.
- Deterministic Chromium visual tests passed at 1440×900, 820×1180, and 390×844 for both [before](../ui/desktop-web/changes/20260926-native-session-names/before/) and [after](../ui/desktop-web/changes/20260926-native-session-names/after/). The six after images were copied to `current/`. [Capture notes](../ui/desktop-web/changes/20260926-native-session-names/notes.md) describe the fixtures; no owner session or credentials were used.

## Integration verification

The integration owner merged this branch as `18cf9c7`. On that merged tree, `make generate` left no generated diff and `make check` passed Proto/SQLC, Go, TypeScript, 261 Desktop tests and the production Vite build. The scoped display-name change is complete. The owner-authenticated LAN browser gate remains a separate P0 acceptance task, so `docs/status.json` keeps the V3 resident surface scaffolded.
