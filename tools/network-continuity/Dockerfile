FROM workos-playwright:1.62.1
USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    libnss3-tools iproute2 iptables coturn openssl \
    && rm -rf /var/lib/apt/lists/*
LABEL org.opencontainers.image.title="WorkOS isolated network acceptance tools"
