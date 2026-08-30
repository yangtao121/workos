ARG NODE_IMAGE=node:24.19.0-bookworm-slim
ARG GO_IMAGE=golang:1.26.7-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim
ARG DEBIAN_MIRROR=https://mirrors.aliyun.com
ARG PYPI_MIRROR=https://mirrors.aliyun.com/pypi

FROM ${NODE_IMAGE} AS web
ARG NPM_REGISTRY=https://registry.npmmirror.com
ENV npm_config_registry=${NPM_REGISTRY}
WORKDIR /src
COPY .npmrc package.json pnpm-workspace.yaml pnpm-lock.yaml tsconfig.base.json ./
COPY apps ./apps
COPY clients ./clients
COPY sdk ./sdk
RUN npm install --global pnpm@11.4.0 --registry="${NPM_REGISTRY}" \
    && pnpm install --frozen-lockfile \
    && pnpm --filter @workos/desktop-web build

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY gen ./gen
COPY internal ./internal
COPY schemas ./schemas
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workos-gateway ./cmd/workos-gateway \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workos-core ./cmd/workos-core \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/harness-host ./cmd/harness-host \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runtime-host ./cmd/runtime-host \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/reliability-host ./cmd/reliability-host \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/indexer ./cmd/indexer \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workosctl ./cmd/workosctl \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/generic-harness-fixture ./cmd/generic-harness-fixture

FROM ${RUNTIME_IMAGE} AS deepseek-runtime
ARG DEBIAN_MIRROR
ARG PYPI_MIRROR
ARG TARGETARCH
RUN mirror="$(printf '%s' "${DEBIAN_MIRROR}" | sed 's|^https://|http://|')" \
    && sed -i "s|https://deb.debian.org|${mirror}|g; s|http://deb.debian.org|${mirror}|g; s|http://security.debian.org|${mirror}/debian-security|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) \
        wheel_url="${PYPI_MIRROR}/packages/38/26/0f1b453134a45841624e3791c4b2ed98484b75fc7e35ccd137cff16e2eda/deepseek_harness_runtime_bin-0.1.1rc1-py3-none-manylinux_2_28_x86_64.whl"; \
        wheel_sha='8eb31e3ab2bc3ff45474fe419eb389e32553391f1a40789ea2cc3dc8d6de137b'; \
        runtime_name='dsh-jsonrpc-agent-pkg-linux-x64' ;; \
      arm64) \
        wheel_url="${PYPI_MIRROR}/packages/bb/b5/7c840b4859fbec5de4b84a3794a06695f9da1100391a251cafbc37bf6ab2/deepseek_harness_runtime_bin-0.1.1rc1-py3-none-manylinux_2_28_aarch64.whl"; \
        wheel_sha='e73987c6c08d8322bce2b8b2ce75db6a139ecf546417b6015ce7a8de5e5f19b5'; \
        runtime_name='dsh-jsonrpc-agent-pkg-linux-arm64' ;; \
      *) exit 2 ;; \
    esac; \
    curl --fail --location --retry 3 --output /tmp/runtime.whl "${wheel_url}"; \
    printf '%s  %s\n' "${wheel_sha}" /tmp/runtime.whl | sha256sum --check --strict; \
    unzip -q /tmp/runtime.whl -d /tmp/runtime-wheel; \
    mkdir -p /out; \
    runtime_path="$(find /tmp/runtime-wheel -type f -name "${runtime_name}" -print -quit)"; \
    test -n "${runtime_path}"; \
    test -f "${runtime_path}-rg"; \
    install -m 0755 "${runtime_path}" /out/dsh-jsonrpc-agent; \
    install -m 0755 "${runtime_path}-rg" /out/dsh-jsonrpc-agent-rg

FROM ${RUNTIME_IMAGE}
COPY --from=deepseek-runtime /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=deepseek-runtime /out/dsh-jsonrpc-agent /usr/local/libexec/workos/dsh-jsonrpc-agent
COPY --from=deepseek-runtime /out/dsh-jsonrpc-agent-rg /usr/local/libexec/workos/dsh-jsonrpc-agent-rg
COPY --from=web /src/apps/desktop-web/dist/ /srv/workos/desktop/
COPY deploy/harness/deepseek.cordis.yml /etc/workos/deepseek.cordis.yml
COPY --from=build /out/ /usr/local/bin/
# The gateway-owned admin Unix socket lives here in production pairing mode
# (systemd provides RuntimeDirectory on hosts; the image ships the mount
# point for containerized runs).
RUN install -d -m 0755 -o 10001 -g 10001 /run/workos /run/workos/tls
USER 10001:10001
WORKDIR /tmp
EXPOSE 8080 8081 8082 8083 8084 8085
