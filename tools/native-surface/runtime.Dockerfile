# The gate's runtime-host carries the real X11 toolchain (ADR-0029): Xvfb
# for virtual displays, ffmpeg x11grab/VP8 for capture, xdotool for XTEST
# input injection, and xterm as the native client. Gate-scoped image; the
# default stack keeps the unified image and reports the runner unavailable
# without the toolchain.
FROM debian:bookworm-slim
RUN sed -i "s|deb.debian.org|mirrors.aliyun.com|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    apt-get update && apt-get install -y --no-install-recommends \
      xvfb ffmpeg xdotool xterm fonts-dejavu-core xfonts-base x11-utils && \
    rm -rf /var/lib/apt/lists/* && \
    install -d -m 0755 -o 10001 -g 10001 /run/workos && install -d -m 1777 /var/workos-native
COPY --from=workos:dev /usr/local/bin/ /usr/local/bin/
COPY --from=workos:dev /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 10001:10001
WORKDIR /tmp
