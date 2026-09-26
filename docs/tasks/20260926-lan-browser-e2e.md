# Task: LAN password and HTTPS Chromium acceptance

- Status: active (test implementation complete only after integrated stack evidence)
- Owner/Agent: Codex, branch `feat/lan-browser-e2e`, worktree `/home/aquatao/workos-lan-e2e`
- Baseline: `0cd59b8`; worktree clean at start
- Processes/modules: Gateway public edge, Desktop Web browser E2E, test runner
- Dependencies: [LAN HTTPS entry](20260926-lan-https-entry.md), [Gateway password mode](20260926-lan-password-auth.md), [Desktop password UI](20260926-lan-password-ui.md)

## Scope

Run real Chromium against the host's CA-verified `https://<LAN IP>:8443` origin and the real six-process stack. Prove the password UI and Gateway session gate with two independent browser profiles. Test-only credentials are read from a mode-0600 file and never printed. The test uses read-only business RPCs and revokes only its own freshly created browser devices.

An optional expiry phase waits for a real short Gateway session TTL, restores the expired cookie, and proves that the server rejects it. It requires `WORKOS_AUTH_SESSION_TTL=5m` and takes about five minutes.

No product UI or protocol change. This task does not treat local Chromium as physical Mac or second-device evidence.

## Contract and data

- Uses existing `workos.auth.v1.PasswordAuthService`, `DeviceService`, and protected Core and Runtime routes. No Proto or migration changes.
- Creates password device/session rows in the real Gateway database. Revokes its own second device and logs out the first.
- Makes no project or app writes.

## Acceptance

- [ ] CA trust works in Chromium without `ignoreHTTPSErrors` or a certificate bypass flag; page reports a secure context and Clipboard/WebCodecs APIs.
- [ ] Anonymous and expired/logged-out/revoked profiles cannot reach protected Core or Runtime routes; private admin and pairing routes remain unavailable over public TCP in password mode.
- [ ] Username/password login from the actual Desktop mounts the shell and issues a `Secure`, `HttpOnly`, `SameSite=Strict`, path `/` host-only cookie.
- [ ] Two isolated profiles receive different device IDs; one can revoke the other while keeping its own session.
- [ ] Logout and cookie removal return to password entry, with no silent P-256 profile-key reauthentication.
- [ ] Optional real-expiry phase passes with a five-minute Gateway TTL.
- [ ] `make check` and integrated stack gate pass after branch integration.

## Visual evidence

No product pixels or UI behavior are changed by this test-only task. The password UI task owns its deterministic before/after/current screenshots. This gate deliberately avoids screenshots and traces containing credentials or owner data.

## Verification and handoff

- `node node_modules/typescript/bin/tsc --noEmit` in `apps/desktop-web` using the pinned Node 24 image and existing workspace dependencies: pass.
- ESLint on the new spec, Prettier on the new spec and Markdown, `sh -n tools/lan/test-browser.sh`, and `git diff --check`: pass.
- `node node_modules/@playwright/test/cli.js test lan-password-browser.spec.ts --list`: discovers the two expected cases.
- `docker build -t workos-playwright:1.62.1 -f deploy/e2e/Dockerfile deploy/e2e`: pass.
- CA trust smoke against the running HTTPS Gateway using a fresh NSS profile and `ignoreHTTPSErrors: false`: HTTP 200, `isSecureContext=true`, Clipboard API present, WebCodecs API present.

The credential-bearing integrated run is pending the configured owner password; the live stack was left untouched by this branch. Run with `tools/lan/test-browser.sh`; the runner fails if CA or private password fixture is missing. Do not mark done until the real login/session results are recorded here. The integration task updates `docs/status.json` once that end-to-end fact is established.
