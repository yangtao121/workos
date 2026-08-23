# Deployment boundary

The foundation is intentionally safe only on a single Linux host. Device enrollment and a production session authenticator are not implemented yet, so do not expose the Gateway to a LAN or the public Internet. Development identity bypass is rejected automatically on non-loopback addresses.

## Containers

`compose.yaml` runs PostgreSQL and all six stable processes on loopback. It is the reproducible development and acceptance environment, not a production topology.

The repository defaults are chosen for development in mainland China:

- Go modules: goproxy.cn
- npm/pnpm packages: npmmirror (also recorded in the root `.npmrc`)
- Debian packages: Alibaba Cloud (`https://mirrors.aliyun.com`)
- Playwright browsers: npmmirror

The browser E2E runner is built locally with `make e2e-image`; the resulting Docker layers are reused by later test runs. Every package route is an overridable Make or Compose variable. For example, an environment outside mainland China can use upstream services without changing tracked files:

```bash
GOPROXY=https://proxy.golang.org,direct \
  NPM_REGISTRY=https://registry.npmjs.org \
  make build

make e2e-image \
  DEBIAN_MIRROR=https://deb.debian.org \
  NPM_REGISTRY=https://registry.npmjs.org \
  PLAYWRIGHT_DOWNLOAD_HOST=https://cdn.playwright.dev
```

Base container images still use the operator's Docker registry configuration. Alibaba Cloud accelerators are account/region specific, so configure the Docker daemon with the accelerator assigned to that machine or override `GO_IMAGE`, `NODE_IMAGE`, and `RUNTIME_IMAGE`; do not commit a personal accelerator URL.

Keep `PLAYWRIGHT_VERSION` in the Makefile aligned with `@playwright/test` in `apps/desktop-web/package.json` whenever Playwright is upgraded.

Enable local OTLP traces with:

```bash
docker compose -f compose.yaml -f deploy/compose.observability.yaml up -d
```

The included collector logs spans through its debug exporter. Replace that exporter in a deployment-specific file; services only need `OTEL_EXPORTER_OTLP_ENDPOINT`.

## systemd skeleton

The units in `deploy/systemd` assume binaries under `/usr/local/libexec/workos`, the Desktop bundle under `/usr/share/workos/desktop`, a dedicated `workos` user, and PostgreSQL managed separately.

1. Install `workos.env.example` as `/etc/workos/workos.env` with mode `0600` and replace the database credential.
2. Install each process environment example without the `.example` suffix.
3. Install the units, run `systemctl daemon-reload`, and enable `workos.target`.

The Runtime unit is hardened for its current read-only capability probe. A future rootless Podman runner must add a reviewed systemd drop-in instead of weakening every WorkOS process.
