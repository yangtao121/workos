# Resident Greenfield browser and Code share this isolated, networkless child.
# Build the fixed proxy/Code base first with deploy/greenfield-runtime.Dockerfile.
ARG GREENFIELD_BASE_IMAGE=workos-greenfield-runtime:p0

# Isolate the large, version-pinned Chromium download from child source edits
# so a browser-side patch does not fetch the same browser again.
FROM node:24.19.0-bookworm-slim AS chromium-download
ARG NPM_REGISTRY=https://registry.npmmirror.com
ARG PLAYWRIGHT_DOWNLOAD_HOST=https://npmmirror.com/mirrors/playwright
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/workos/ms-playwright \
    PLAYWRIGHT_DOWNLOAD_HOST=${PLAYWRIGHT_DOWNLOAD_HOST}
RUN npm install --prefix /opt/playwright-install --no-audit --no-fund \
      --registry="${NPM_REGISTRY}" playwright-core@1.62.1 \
    && node /opt/playwright-install/node_modules/playwright-core/cli.js install chromium

FROM node:24.19.0-bookworm-slim AS compositor-build
ENV COREPACK_NPM_REGISTRY=https://registry.npmmirror.com \
    npm_config_registry=https://registry.npmmirror.com
WORKDIR /src
COPY .npmrc package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY patches ./patches
COPY sdk/protocol ./sdk/protocol
COPY tools/greenfield-child ./tools/greenfield-child
RUN corepack pnpm install --frozen-lockfile --filter @workos/greenfield-child...
RUN corepack pnpm --filter @workos/greenfield-child build \
    && corepack pnpm --filter @workos/greenfield-child deploy --legacy --prod /out/greenfield-child \
    && cp -a tools/greenfield-child/dist /out/greenfield-child/dist \
    && cp -a tools/greenfield-child/dist/page /out/greenfield-child/page \
    && cp tools/greenfield-child/dist/bridge.mjs /out/greenfield-child/bridge.mjs

FROM ${GREENFIELD_BASE_IMAGE}
USER root
# Bookworm package version is fixed with the base image. The graphical editor
# runs in its own networkless resident child, separate from Code.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends mousepad=0.5.10-2 \
    && test -x /usr/bin/mousepad \
    && rm -rf /var/lib/apt/lists/*
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/workos/ms-playwright \
    XDG_RUNTIME_DIR=/tmp/xdg
COPY --from=chromium-download /opt/workos/ms-playwright /opt/workos/ms-playwright
COPY --from=chromium-download /opt/playwright-install/node_modules/playwright-core /tmp/playwright-core
# playwright-core pins its Chromium revision; a browser download/build mismatch
# fails the image build rather than silently selecting a host browser.
RUN node /tmp/playwright-core/cli.js install-deps chromium \
    && rm -rf /tmp/playwright-core /var/lib/apt/lists/*
COPY --from=compositor-build /out/greenfield-child /opt/workos/greenfield-child
RUN chown -R 10001:10001 /opt/workos/greenfield-child /opt/workos/ms-playwright
WORKDIR /opt/workos/greenfield-child
USER 10001:10001
