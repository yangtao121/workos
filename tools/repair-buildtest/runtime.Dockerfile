# The gate's runtime-host carries the Go toolchain so the process-tier
# Build/Test engine can execute the manifest's fixed build/test commands
# (ADR-0026). It is a gate-scoped image; the default stack keeps the unified
# python-based image.
FROM golang:1.26.7-bookworm
COPY --from=workos:dev /usr/local/bin/ /usr/local/bin/
COPY --from=workos:dev /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN install -d -m 0755 -o 10001 -g 10001 /run/workos && install -d -m 1777 /var/workos-buildtest
USER 10001:10001
WORKDIR /tmp
