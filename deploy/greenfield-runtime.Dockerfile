# Experimental Greenfield compositor-proxy plus official Linux VS Code.
# GitHub archives come from the ghfast.top mirror. npm packages come from npmmirror.
# Pinned commit: 6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57
FROM node:24.19.0-bookworm-slim

ARG GREENFIELD_COMMIT=6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57
ARG GITHUB_MIRROR=https://ghfast.top/https://github.com
ARG NPM_REGISTRY=https://registry.npmmirror.com
ARG DEBIAN_MIRROR=http://mirrors.huaweicloud.com/debian
ARG DEBIAN_SECURITY_MIRROR=http://mirrors.huaweicloud.com/debian-security

ENV YARN_NPM_REGISTRY_SERVER=${NPM_REGISTRY} \
    LIBGL_ALWAYS_SOFTWARE=1 \
    GALLIUM_DRIVER=llvmpipe \
    RENDERER_ALLOW_SOFTWARE=1 \
    ELECTRON_OZONE_PLATFORM_HINT=x11

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
      xwayland xauth xxd inotify-tools \
    && rm -rf /var/lib/apt/lists/*

COPY deploy/patches/greenfield/0001-keep-clients-without-browser.patch /tmp/0001-keep-clients-without-browser.patch

RUN curl -fsSL "${GITHUB_MIRROR}/udevbe/greenfield/archive/${GREENFIELD_COMMIT}.tar.gz" \
      -o /tmp/greenfield.tar.gz \
    && mkdir -p /opt/greenfield \
    && tar -xzf /tmp/greenfield.tar.gz -C /opt/greenfield --strip-components=1 \
    && patch -d /opt/greenfield -p1 < /tmp/0001-keep-clients-without-browser.patch \
    && rm /tmp/greenfield.tar.gz \
    && cd /opt/greenfield \
    && node .yarn/releases/yarn-4.5.0.cjs install \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/common build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/compositor-protocol build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/xtsb build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/compositor-proxy build \
    && node .yarn/releases/yarn-4.5.0.cjs workspace @gfld/compositor-proxy-cli build

# The browser compositor is the published 1.0.0-rc1 prebuild (@gfld/compositor
# and its wasm packages). Compiling Emscripten from source is not part of this
# image; that path stalls on cross-compiled xkbcommon.

WORKDIR /opt/greenfield
