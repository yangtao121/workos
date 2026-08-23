ARG NODE_IMAGE=node:24.19.0-bookworm-slim
ARG GO_IMAGE=golang:1.26.7-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

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
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workos-gateway ./cmd/workos-gateway \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workos-core ./cmd/workos-core \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/harness-host ./cmd/harness-host \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runtime-host ./cmd/runtime-host \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/reliability-host ./cmd/reliability-host \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/indexer ./cmd/indexer \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/workosctl ./cmd/workosctl \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/generic-harness-fixture ./cmd/generic-harness-fixture

FROM ${RUNTIME_IMAGE}
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ /usr/local/bin/
COPY --from=web /src/apps/desktop-web/dist/ /srv/workos/desktop/
USER 10001:10001
WORKDIR /tmp
EXPOSE 8080 8081 8082 8083 8084 8085
