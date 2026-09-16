# Trusted Runtime X11/media toolchain; user X clients execute in the separate
# workspace image under Runtime Docker supervision. Build workos:dev first.
ARG WORKOS_IMAGE=workos:dev
FROM ${WORKOS_IMAGE} AS workos
FROM debian:bookworm-slim
RUN sed -i "s|deb.debian.org|mirrors.aliyun.com|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    apt-get update && apt-get install -y --no-install-recommends \
      xvfb ffmpeg xdotool xterm fonts-dejavu-core xfonts-base x11-utils && \
    rm -rf /var/lib/apt/lists/* && \
    install -d -m 0755 -o 10001 -g 10001 /run/workos && install -d -m 1777 /var/workos-native
COPY --from=workos /usr/local/bin/ /usr/local/bin/
COPY --from=workos /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 10001:10001
WORKDIR /tmp
