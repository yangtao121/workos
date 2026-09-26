# LAN password entry in Desktop

- Status: in_progress (component and visual checks passed; integrated Gateway acceptance pending).
- Owner: Desktop Web and device-auth client, branch `feat/lan-password-ui`.
- Scope: Detect Gateway auth mode, offer username/password sign-in for password deployments, require password again after session expiry or logout, and hide pairing actions in password mode. Preserve pairing deployments and development bypass.
- Dependencies: additive `workos.auth.v1.PasswordAuthService` contract from `20260926-lan-password-auth`; Gateway implementation on `feat/lan-password-auth`.
- Acceptance: password mode never invokes silent P-256 reauthentication; a successful login mounts Desktop with the server cookie; errors and unavailable states are distinct; Device Center still lists/revokes devices and signs out; pairing mode remains functional; tests and deterministic visual records pass.
- Visual evidence: [`before/`](../ui/desktop-web/changes/20260926-lan-password-ui/before/), [`after/`](../ui/desktop-web/changes/20260926-lan-password-ui/after/), [`notes.md`](../ui/desktop-web/changes/20260926-lan-password-ui/notes.md), and the corresponding `current/` screenshots.
- Verification: `@workos/device-auth check` (10 tests), `@workos/desktop-web check` (219 tests), and `lan-password-visual.spec.ts` (1 Chromium visual test) passed in Node 24 / Playwright 1.62.1 containers. Full integrated Gateway login acceptance remains a dependency of the auth and HTTPS tasks.
