# The gate's runtime-host carries a real Chromium so the Remote Browser Pool
# launches genuine workers (ADR-0027). Gate-scoped image; the default stack
# keeps the unified image and reports the pool unavailable without a binary.
FROM workos-playwright:1.62.1
COPY --from=workos:dev /usr/local/bin/ /usr/local/bin/
COPY --from=workos:dev /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN install -d -m 0755 -o 10001 -g 10001 /run/workos && install -d -m 1777 /var/workos-browserpool /tmp/workos-buildtest
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
USER 10001:10001
WORKDIR /tmp
