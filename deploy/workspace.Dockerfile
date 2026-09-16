# Runtime-owned development toolchain. No daemon socket or model credentials.
FROM node:24.19.0-bookworm-slim AS node
FROM golang:1.26.7-bookworm
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ARG DEBIAN_MIRROR=https://mirrors.aliyun.com
RUN sed -i "s|http://deb.debian.org|${DEBIAN_MIRROR}|g; s|http://security.debian.org|${DEBIAN_MIRROR}/debian-security|g" /etc/apt/sources.list.d/debian.sources \
 && apt-get update && apt-get install -y --no-install-recommends xterm x11-apps && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
