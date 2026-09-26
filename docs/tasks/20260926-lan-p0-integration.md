# V3 P0 and LAN HTTPS integration

- Status: in_progress.
- Owner: integration, `main` worktree (the existing V3 P0 task began on `main`; component tasks use independent branches/worktrees).
- Scope: merge the password contract, Gateway, Desktop, persistent HTTPS entry, Greenfield runtime/browser work, then run the full reproducible checks and real browser acceptance.
- Dependencies: `20260926-lan-password-auth`, `20260926-lan-https-entry`, `20260926-lan-password-ui`, `20260926-lan-browser-e2e`, `20260924-v3-p0-native-experience`, `20260926-v3-p0-browser-input`.
- Acceptance: CA-verified `https://192.168.5.5:8443` serves Desktop; only Gateway is LAN-exposed; local password setup, secure session, logout/revoke, two-device behavior and proxy isolation work; A01–A12 P0 matrix has genuine evidence or explicit non-passing status; `make generate` leaves no generated diff and `make check` passes; module docs and `docs/status.json` match evidence.

## Current verification

- 2026-09-26: `./tools/lan/start.sh up` built and started six-process stack and issued persistent local CA/leaf. `curl --cacert .workos/lan-tls/ca.crt` verified Gateway TLS; `GetMode` returned `AUTH_MODE_PASSWORD`, unauthenticated `GetCurrentDevice` returned 401. `ss -lnt` showed `192.168.5.5:8443`, loopback PostgreSQL and OTLP, no `8080`.
- Chromium in test-only `ignoreHTTPSErrors` mode displayed the password form; `isSecureContext` and `navigator.clipboard.readText` were available. This is not physical Mac trust-store evidence.
- `sh tools/lan/test.sh` passed; `make generate` left no generated diff. First `make check` passed Proto/SQLC/Go but found Desktop lint errors in password UI fixtures. Fixed by `efcdc91`; `make web-check` is running.
- Owner password choice is pending; no credential has been submitted to the live Gateway. The remaining login and two-device tests cannot pass before setup.
- Greenfield P0 runtime/browser work remains in separate branches; existing P0 matrix is partial, not accepted.
