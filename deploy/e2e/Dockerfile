ARG NODE_IMAGE=node:24.19.0-bookworm-slim
FROM ${NODE_IMAGE}

ARG DEBIAN_MIRROR=https://mirrors.aliyun.com
ARG NPM_REGISTRY=https://registry.npmmirror.com
ARG PLAYWRIGHT_DOWNLOAD_HOST=https://npmmirror.com/mirrors/playwright
ARG PLAYWRIGHT_VERSION=1.62.1

ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    PLAYWRIGHT_DOWNLOAD_CONNECTION_TIMEOUT=120000

RUN set -eux; \
    bootstrap_mirror="${DEBIAN_MIRROR#https://}"; \
    if [ "${bootstrap_mirror}" != "${DEBIAN_MIRROR}" ]; then \
      bootstrap_mirror="http://${bootstrap_mirror}"; \
    fi; \
    sed -i "s|http://deb.debian.org|${bootstrap_mirror}|g" \
      /etc/apt/sources.list.d/debian.sources; \
    apt-get update; \
    apt-get install --yes --no-install-recommends ca-certificates; \
    sed -i "s|${bootstrap_mirror}|${DEBIAN_MIRROR}|g" \
      /etc/apt/sources.list.d/debian.sources; \
    apt-get update; \
    rm -rf /var/lib/apt/lists/*

RUN npm install --global \
      "pnpm@11.4.0" \
      "playwright@${PLAYWRIGHT_VERSION}" \
      --registry="${NPM_REGISTRY}" \
    && rm -rf /root/.npm

RUN playwright install-deps chromium \
    && rm -rf /var/lib/apt/lists/*

RUN PLAYWRIGHT_DOWNLOAD_HOST="${PLAYWRIGHT_DOWNLOAD_HOST}" \
      playwright install chromium \
    && chmod -R a+rX "${PLAYWRIGHT_BROWSERS_PATH}"

WORKDIR /workspace/apps/desktop-web

LABEL org.opencontainers.image.title="WorkOS Playwright test runner" \
      org.opencontainers.image.description="Reproducible Chromium runner for the WorkOS desktop E2E suite"
