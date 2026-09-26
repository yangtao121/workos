# LAN password authentication

- Owner: Gateway, branch `feat/lan-password-auth`.
- Scope: Add a mutually exclusive password authentication mode for the single owner over the existing TLS Gateway, with a private Unix-socket password setup command, rate-limited public login, device-scoped cookie sessions, logout and revocation. Keep pairing mode behavior and tests.
- Dependencies: Gateway-owned auth schema; additive `workos.auth.v1` contract; LAN HTTPS entry from the separate transport task.
- Acceptance: `workosctl auth set-password` reads the secret from the terminal; password is stored only as an Argon2id hash; login issues a secure cookie; distinct browsers have distinct device IDs; password changes revoke all old sessions; expired, logged-out, or revoked sessions cannot reach proxied services; pairing regressions and `make check` pass.
- Evidence: pending implementation and tests.
