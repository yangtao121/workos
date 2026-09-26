# Experimental Greenfield compositor-proxy plus official Linux VS Code.
# Build Runtime from this source tree so the image and proxy contract cannot
# silently use an older workos:dev binary.
FROM golang:1.26.7-bookworm AS workos-runtime
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY gen ./gen
COPY internal ./internal
COPY schemas ./schemas
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runtime-host ./cmd/runtime-host

FROM node:24.19.0-bookworm-slim

ARG GREENFIELD_COMMIT=6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57
ARG GREENFIELD_SHA256=97e0a72b0e139c8b22088fa4acde199d65d5794f8ee7f85590c435e9231cf433
ARG VSCODE_VERSION=1.139.0
ARG VSCODE_SHA256=5031849ea13d2297ec7c8f1af70c0bc7d585250cae306b53d668cb6a7a900623
ARG NPM_REGISTRY=https://registry.npmmirror.com
ARG DEBIAN_MIRROR=http://mirrors.huaweicloud.com/debian
ARG DEBIAN_SECURITY_MIRROR=http://mirrors.huaweicloud.com/debian-security

ENV YARN_NPM_REGISTRY_SERVER=${NPM_REGISTRY} \
    ELECTRON_OZONE_PLATFORM_HINT=x11 \
    XDG_RUNTIME_DIR=/run/workos/greenfield

RUN sed -i \
      -e "s|http://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
      -e "s|http://deb.debian.org/debian|${DEBIAN_MIRROR}|g" \
      /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
      ca-certificates curl xz-utils patch \
      cmake build-essential ninja-build pkg-config python3 \
      libffi-dev libudev-dev libgbm-dev libdrm-dev libegl-dev libopengl-dev \
      libglib2.0-dev libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev \
      libgstreamer-plugins-bad1.0-dev libgraphene-1.0-dev \
      libffi8 libudev1 libgbm1 libgraphene-1.0-0 \
      gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
      gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-libav \
      gstreamer1.0-gl libosmesa6 libdrm2 libopengl0 libglvnd0 libglx0 \
      libglapi-mesa libegl1-mesa libglx-mesa0 libgl1-mesa-dri \
      xwayland xauth xxd inotify-tools fonts-dejavu-core fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*

COPY deploy/patches/greenfield/0001-keep-clients-without-browser.patch /tmp/0001-keep-clients-without-browser.patch
COPY deploy/patches/greenfield/0002-preserve-client-on-navigation.patch /tmp/0002-preserve-client-on-navigation.patch

COPY --from=greenfield-source /source.tar.gz /tmp/greenfield.tar.gz
RUN printf '%s  %s\n' "${GREENFIELD_SHA256}" /tmp/greenfield.tar.gz | sha256sum --check --strict \
    && mkdir -p /opt/greenfield \
    && tar -xzf /tmp/greenfield.tar.gz -C /opt/greenfield --strip-components=1 \
    && patch -d /opt/greenfield -p1 < /tmp/0001-keep-clients-without-browser.patch \
    && patch -d /opt/greenfield -p1 < /tmp/0002-preserve-client-on-navigation.patch \
    && rm /tmp/greenfield.tar.gz \
    && cd /opt/greenfield \
    && node .yarn/releases/yarn-4.5.0.cjs install \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/common build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/compositor-protocol build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/xtsb build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/compositor-proxy build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/compositor-proxy-cli build

COPY deploy/patches/greenfield/0003-private-xwayland-without-cookie.patch /tmp/0003-private-xwayland-without-cookie.patch
RUN patch -d /opt/greenfield -p1 < /tmp/0003-private-xwayland-without-cookie.patch \
    && cd /opt/greenfield \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/xtsb build

# The upstream package is installed at build time, never fetched when a user
# launches Code. A checksum mismatch fails the build.
RUN curl -fsSL --retry 3 \
      "https://update.code.visualstudio.com/${VSCODE_VERSION}/linux-deb-x64/stable" \
      -o /tmp/code.deb \
    && printf '%s  %s\n' "${VSCODE_SHA256}" /tmp/code.deb | sha256sum --check --strict \
    && apt-get update \
    && printf 'code code/add-microsoft-repo boolean false\n' | debconf-set-selections \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends /tmp/code.deb \
    && test -x /usr/share/code/code \
    && rm -rf /var/lib/apt/lists/* /tmp/code.deb

COPY --from=workos-runtime /out/runtime-host /usr/local/bin/runtime-host
RUN install -d -m 0700 -o 10001 -g 10001 /run/workos/greenfield /var/lib/workos/greenfield \
    && install -d -m 1777 /tmp/.X11-unix

# The browser compositor is the published 1.0.0-rc1 prebuild (@gfld/compositor
# and its wasm packages). Compiling Emscripten from source is not part of this
# image; that path stalls on cross-compiled xkbcommon.

WORKDIR /opt/greenfield
USER 10001:10001
