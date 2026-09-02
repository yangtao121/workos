# Deployment boundary

The foundation is intentionally safe only on a single Linux host. With `WORKOS_DEV_AUTH_BYPASS=true` (loopback only, fixed configured identity, no cookies) it must never leave the machine; bypass on a non-loopback bind is rejected at startup.

Production device authentication (ADR-0007) lets a trusted LAN client pair over an operator-shown QR and sign proofs with a browser profile key. It has real boundaries: the operator provides a trusted TLS certificate (no ACME, no automatic issuance), the browser still relies on its platform trust store (the pairing fingerprint is a human check, not native pinning), and mDNS discovery, mobile-native key storage, and public-internet exposure remain out of scope.

## Containers

`compose.yaml` runs PostgreSQL and all six stable processes on loopback. It is the reproducible development and acceptance environment, not a production topology.

Core reaches the ordinary harness-host address through `WORKOS_HARNESS_URL` only for provider description. The Gateway exposes Core's separate read-only Catalog facade and Project binding command; it does not forward the private Harness execution service. Long-lived Provider credentials belong only to the Core Credential Vault; harness-host receives one short-lived task-bound lease in memory, while Gateway, Desktop, and the other resident processes receive neither the credential nor vault key material.

The non-secret Project binding preset is configured on Core with:

- `WORKOS_PROJECT_HARNESS_INSTANCE_POLICY`
- `WORKOS_PROJECT_HARNESS_PROFILE_ID`
- `WORKOS_PROJECT_HARNESS_RESOURCE_POLICY_ID`

The resource policy value is currently a persisted policy reference, not evidence that resource enforcement exists. Catalog timeout is controlled by `WORKOS_AGENT_CATALOG_TIMEOUT`; Catalog failure does not participate in Core readiness.

The repository defaults are chosen for development in mainland China:

- Go modules: goproxy.cn
- npm/pnpm packages: npmmirror (also recorded in the root `.npmrc`)
- Debian packages: Alibaba Cloud (`https://mirrors.aliyun.com`)
- Python release artifacts: Alibaba Cloud PyPI (`https://mirrors.aliyun.com/pypi`), with pinned SHA-256 verification
- Playwright browsers: npmmirror

The browser E2E runner is built locally with `make e2e-image`; the resulting Docker layers are reused by later test runs. Every package route is an overridable Make or Compose variable. For example, an environment outside mainland China can use upstream services without changing tracked files:

```bash
GOPROXY=https://proxy.golang.org,direct \
  NPM_REGISTRY=https://registry.npmjs.org \
  PYPI_MIRROR=https://files.pythonhosted.org \
  make build

make e2e-image \
  DEBIAN_MIRROR=https://deb.debian.org \
  NPM_REGISTRY=https://registry.npmjs.org \
  PLAYWRIGHT_DOWNLOAD_HOST=https://cdn.playwright.dev
```

Base container images still use the operator's Docker registry configuration. Alibaba Cloud accelerators are account/region specific, so configure the Docker daemon with the accelerator assigned to that machine or override `GO_IMAGE`, `NODE_IMAGE`, and `RUNTIME_IMAGE`; do not commit a personal accelerator URL.

Keep `PLAYWRIGHT_VERSION` in the Makefile aligned with `@playwright/test` in `apps/desktop-web/package.json` whenever Playwright is upgraded.

`make test-deepseek-fixture` starts a loopback API fixture with a fake credential, exercises the public Catalog/binding browser flow, and stops only that fixture container when it exits. It neither needs nor performs a live DeepSeek smoke and never removes the PostgreSQL volume.

Enable local OTLP traces with:

```bash
docker compose -f compose.yaml -f deploy/compose.observability.yaml up -d
```

The included collector logs spans through its debug exporter. Replace that exporter in a deployment-specific file; services only need `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Production LAN pairing (ADR-0007)

With the development bypass off, the Gateway requires its own TLS 1.3 termination, a canonical `https://` public origin, a canonical UUIDv7 owner, a reachable PostgreSQL, and a Gateway-owned admin Unix socket. `WORKOS_DEVICE_ID` is not used in this mode; device identities are minted by the Gateway.

1. Obtain an operator-managed certificate for the public origin (the repository ships no CA and no ACME client). Install the leaf key pair, e.g. `/etc/workos/tls/leaf.crt` + `leaf.key`, readable only by the service user.
2. Configure `deploy/systemd/workos-gateway.env.example` in production mode: `WORKOS_DEV_AUTH_BYPASS=false`, `WORKOS_HTTP_ADDRESS=:443`, `WORKOS_HTTP_TLS_CERT_FILE`/`WORKOS_HTTP_TLS_KEY_FILE`, `WORKOS_AUTH_PUBLIC_ORIGIN=https://workos.example`, `WORKOS_AUTH_ADMIN_SOCKET=/run/workos/gateway-admin.sock`.
3. Give the gateway process the runtime directory via the reviewed drop-in in `deploy/systemd/workos-gateway.socket.example` (`RuntimeDirectory=workos`), then restart the unit. The gateway requires that directory to be owned by its process user and not group/other-writable; it removes an existing socket only after `ECONNREFUSED` plus an unchanged-inode recheck, and chmods the new socket to `0600`. Timeout, permission denial, or a replacement race fails startup without deleting the endpoint. A runtime admin-listener failure shuts down the public listener and exits nonzero so `Restart=on-failure` can restore the whole gateway.
4. Pair the first device as the operator: `sudo -u workos workosctl device pair` prints the one-time pairing URL (ticket secret only in the URL fragment), the public origin, the SHA-256 of the served leaf certificate, and a terminal QR. Every rotation invalidates all previously displayed QR codes; losing the response just means running the command again.
5. The user opens the URL on the LAN device, confirms the certificate fingerprint, names the device, and the browser proves possession of a non-extractable WebCrypto P-256 key. IndexedDB is read back and the private/SPKI binding is self-verified before the ticket claim. The Gateway sets the `__Host-workos_session` cookie (HttpOnly/Secure/SameSite=Strict) and every public request resolves it before any upstream call.

Device management lives in the Desktop Device Center (list/revoke/logout, "Pair another device" QR). The QR is cleared on replacement, window close, or its server expiry; retries of one revocation revision reuse the same idempotency key. Revocation takes effect on the revoked device's next request. Losing the browser profile (IndexedDB key) requires pairing again; there is no account recovery.

## systemd skeleton

The units in `deploy/systemd` assume binaries under `/usr/local/libexec/workos`, the Desktop bundle under `/usr/share/workos/desktop`, PostgreSQL managed separately, a default `workos` user for ordinary processes, and distinct `workos-core` / `workos-harness` / `workos-indexer` service accounts for credential or local-admin boundaries.

1. Install `workos.env.example` as `/etc/workos/workos.env` with mode `0600` and replace the database credential.
2. Install each process environment example without the `.example` suffix.
3. Create locked `workos-core`, `workos-harness`, and `workos-indexer` system users/groups. Provision `/etc/workos/credentials/{execution-ca.crt,execution-core.crt,execution-core.key,execution-harness.crt,execution-harness.key,vault-master.key}` as root-owned, non-symlink files. Private keys and the 32-raw-byte vault key must be mode `0600`. Install the Core/Harness credential drop-ins and `workos@indexer.service.d/runtime.conf.example` at the matching unit paths. systemd then isolates Core's server identity and vault key, Harness's client identity, and Indexer's mode-0600 admin socket from every sibling service. Replace `WORKOS_INDEX_PAGE_TOKEN_KEY` in `indexer.env` with an independently generated stable secret of at least 32 bytes; rotating it invalidates open search pagination chains. Never expose a shared credential or runtime directory to the common service account.
4. Install the units, run `systemctl daemon-reload`, and enable `workos.target`.

The Runtime unit is hardened for its current read-only capability probe. Enabling the supervised rootless Podman workload runner (ADR-0006) requires the reviewed, runtime-host-only drop-in in `deploy/systemd/workos-runtime-host.service.d/rootless-podman.conf.example`: it redirects podman's per-user state onto StateDirectory/RuntimeDirectory owned by the service user and grants nothing to any other WorkOS process. The host itself must provide cgroup v2 with the memory/cpu/pids controllers, permitted unprivileged user namespaces, and a rootless podman installation; the runtime verifies all of this at startup with a bounded `podman info` probe and honestly reports `container-runner` unavailable — it never falls back to Docker, a rootful daemon, or a bare process.
