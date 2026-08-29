SHELL := /bin/sh

GO_IMAGE := golang:1.26.7-bookworm
NODE_IMAGE := node:24.19.0-bookworm-slim
BUF_IMAGE := bufbuild/buf:1.55.1
SQLC_IMAGE := sqlc/sqlc:1.30.0
PLAYWRIGHT_VERSION := 1.62.1
E2E_IMAGE := workos-playwright:$(PLAYWRIGHT_VERSION)
WORKDIR := /workspace
DEBIAN_MIRROR ?= https://mirrors.aliyun.com
GOPROXY ?= https://goproxy.cn,direct
NPM_REGISTRY ?= https://registry.npmmirror.com
PLAYWRIGHT_DOWNLOAD_HOST ?= https://npmmirror.com/mirrors/playwright
PYPI_MIRROR ?= https://mirrors.aliyun.com/pypi
USER_FLAGS := --user $(shell id -u):$(shell id -g) -e HOME=/tmp
MOUNT := -v $(CURDIR):$(WORKDIR) -w $(WORKDIR)
GO_RUN := docker run --rm $(USER_FLAGS) -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOPROXY=$(GOPROXY) $(MOUNT) -v workos-go-cache:/go/pkg/mod $(GO_IMAGE)
GO_HOST_RUN := docker run --rm --network host $(USER_FLAGS) -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOPROXY=$(GOPROXY) $(MOUNT) -v workos-go-cache:/go/pkg/mod $(GO_IMAGE)
NODE_RUN := docker run --rm $(USER_FLAGS) -e COREPACK_NPM_REGISTRY=$(NPM_REGISTRY) -e npm_config_registry=$(NPM_REGISTRY) $(MOUNT) $(NODE_IMAGE)
BUF_RUN := docker run --rm $(USER_FLAGS) $(MOUNT) $(BUF_IMAGE)
SQLC_RUN := docker run --rm $(USER_FLAGS) -v $(CURDIR):/src -w /src $(SQLC_IMAGE)

.PHONY: bootstrap generate docs check check-native proto-check go-check web-check test test-integration test-deepseek-fixture e2e-image test-e2e build web-build scaffold-module dev down logs clean

bootstrap:
	@docker version >/dev/null
	@docker compose version >/dev/null
	@docker compose config --quiet
	@echo "WorkOS toolchain is ready."

generate:
	$(BUF_RUN) generate
	$(SQLC_RUN) generate
	$(NODE_RUN) node tools/status/render.mjs

docs:
	$(NODE_RUN) node tools/status/render.mjs

proto-check:
	$(BUF_RUN) format --diff --exit-code
	$(BUF_RUN) lint
	$(SQLC_RUN) vet

go-check:
	$(GO_RUN) sh -c 'test -z "$$(gofmt -l cmd internal tests 2>/dev/null)" && go vet ./... && go test ./...'

web-check:
	$(NODE_RUN) sh -c 'corepack pnpm architecture && corepack pnpm exec eslint . && corepack pnpm exec prettier --check . && corepack pnpm -r --if-present check && corepack pnpm --filter @workos/desktop-web build'

check: proto-check go-check web-check
	$(NODE_RUN) node tools/status/render.mjs --check

check-native:
	buf format --diff --exit-code
	buf lint
	$(SQLC_RUN) vet
	go vet ./...
	go test ./...
	pnpm architecture
	pnpm exec eslint .
	pnpm exec prettier --check .
	pnpm -r --if-present check
	pnpm --filter @workos/desktop-web build
	node tools/status/render.mjs --check

test: go-check web-check

test-integration:
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
	$(GO_HOST_RUN) go test -tags=integration -count=1 -v ./tests/integration
	@set -eu; task_id="$$( $(GO_HOST_RUN) go run ./tests/restart seed )"; \
		app_ref="$$( $(GO_HOST_RUN) go run ./tests/restart app-seed )"; \
		install_ref="$$( $(GO_HOST_RUN) go run ./tests/restart install-seed )"; \
		surface_ref="$$( $(GO_HOST_RUN) go run ./tests/restart surface-seed )"; \
		bridge_ref="$$( $(GO_HOST_RUN) go run ./tests/restart bridge-seed )"; \
		grants_ref="$$( $(GO_HOST_RUN) go run ./tests/restart grants-seed )"; \
		policy_ref="$$( $(GO_HOST_RUN) go run ./tests/restart policy-seed )"; \
		set -- $$app_ref; \
		docker compose restart workos-core harness-host runtime-host >/dev/null; \
		$(GO_HOST_RUN) go run ./tests/restart verify "$$task_id"; \
		$(GO_HOST_RUN) go run ./tests/restart app-verify "$$1" "$$2"; \
		set -- $$install_ref; \
		$(GO_HOST_RUN) go run ./tests/restart install-verify "$$1" "$$2" "$$3" "$$4" "$$5"; \
		set -- $$surface_ref; \
		$(GO_HOST_RUN) go run ./tests/restart surface-verify "$$1" "$$2" "$$3" "$$4" "$$5"; \
		set -- $$bridge_ref; \
		$(GO_HOST_RUN) go run ./tests/restart bridge-verify "$$1" "$$2" "$$3"; \
		set -- $$grants_ref; \
		$(GO_HOST_RUN) go run ./tests/restart grants-verify "$$1" "$$2" "$$3" "$$4" "$$5" "$$6"; \
		set -- $$policy_ref; \
		$(GO_HOST_RUN) go run ./tests/restart policy-verify "$$1" "$$2" "$$3" "$$4" "$$5" "$$6" "$$7" "$$8"

test-deepseek-fixture: e2e-image
	@set -eu; \
		cleanup() { docker compose --profile deepseek-fixture stop deepseek-api-fixture >/dev/null 2>&1 || true; }; \
		trap cleanup EXIT INT TERM; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		WORKOS_DEEPSEEK_ENABLED=true \
		DEEPSEEK_API_KEY=workos-fixture-only-not-a-real-key \
		WORKOS_DEEPSEEK_BASE_URL=http://127.0.0.1:18086 \
		docker compose --profile deepseek-fixture up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway deepseek-api-fixture; \
		$(GO_HOST_RUN) go test -tags='integration deepseekfixture' -count=1 -run '^TestDeepSeekProjectBindingFixtureVerticalSlice$$' -v ./tests/integration; \
		task_id="$$( $(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=deepseek go run ./tests/restart seed' )"; \
		docker compose restart workos-core harness-host >/dev/null; \
		$(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=deepseek go run ./tests/restart verify "$$1"' _ "$$task_id"; \
		docker run --rm --network host $(USER_FLAGS) \
			-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
			-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
			-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
			-e WORKOS_DEEPSEEK_FIXTURE_E2E=true \
			-v $(CURDIR):$(WORKDIR) \
			-w $(WORKDIR)/apps/desktop-web \
			$(E2E_IMAGE) pnpm exec playwright test deepseek-fixture.spec.ts

e2e-image:
	docker build \
		--build-arg DEBIAN_MIRROR=$(DEBIAN_MIRROR) \
		--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
		--build-arg PLAYWRIGHT_DOWNLOAD_HOST=$(PLAYWRIGHT_DOWNLOAD_HOST) \
		--build-arg PLAYWRIGHT_VERSION=$(PLAYWRIGHT_VERSION) \
		-t $(E2E_IMAGE) \
		-f deploy/e2e/Dockerfile deploy/e2e

test-e2e: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm test:e2e

build:
	docker build \
		--build-arg GOPROXY=$(GOPROXY) \
		--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
		--build-arg DEBIAN_MIRROR=$(DEBIAN_MIRROR) \
		--build-arg PYPI_MIRROR=$(PYPI_MIRROR) \
		-t workos:dev .

web-build:
	$(NODE_RUN) sh -c 'corepack pnpm --filter @workos/desktop-web build'

scaffold-module:
	@test -n "$(PROCESS)" -a -n "$(NAME)" || (echo "usage: make scaffold-module PROCESS=workos-core NAME=calendar" && exit 2)
	$(GO_RUN) go run ./cmd/workos-scaffold module --process "$(PROCESS)" --name "$(NAME)"

dev:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

clean:
	docker compose down --remove-orphans
