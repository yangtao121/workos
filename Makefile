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
LAN_PAIRING_CAPTURE_DIR ?= $(CURDIR)/docs/ui/desktop-web/changes/20260830-lan-device-pairing/after
ARTIFACT_CONTEXT_CAPTURE_DIR := $(WORKDIR)/docs/ui/desktop-web/changes/20260830-artifact-agent-context/after
USER_FLAGS := --user $(shell id -u):$(shell id -g) -e HOME=/tmp
MOUNT := -v $(CURDIR):$(WORKDIR) -w $(WORKDIR)
GO_RUN := docker run --rm $(USER_FLAGS) -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOPROXY=$(GOPROXY) $(MOUNT) -v workos-go-cache:/go/pkg/mod $(GO_IMAGE)
GO_HOST_RUN_BASE := docker run --rm --network host $(USER_FLAGS) -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOPROXY=$(GOPROXY) $(MOUNT) -v workos-go-cache:/go/pkg/mod
GO_HOST_RUN := $(GO_HOST_RUN_BASE) $(GO_IMAGE)
NODE_RUN := docker run --rm $(USER_FLAGS) -e COREPACK_NPM_REGISTRY=$(NPM_REGISTRY) -e npm_config_registry=$(NPM_REGISTRY) $(MOUNT) $(NODE_IMAGE)
BUF_RUN := docker run --rm $(USER_FLAGS) $(MOUNT) $(BUF_IMAGE)
SQLC_RUN := docker run --rm $(USER_FLAGS) -v $(CURDIR):/src -w /src $(SQLC_IMAGE)

.PHONY: bootstrap generate docs check check-native proto-check go-check web-check test test-integration test-credential-vault-expansion test-codex-harness test-mcp-harness test-artifact-context test-deepseek-fixture test-deepseek-structured-review test-credential-vault e2e-image test-e2e test-adaptive-shell test-app-version-rollback test-podman-fixture test-lan-pairing test-project-knowledge-search test-app-knowledge-search test-project-knowledge-rebuild test-notification-center test-incident-notifications test-app-notifications capture-notification-visual capture-artifact-context-visual capture-lan-pairing-visual capture-provider-catalog build web-build scaffold-module dev down logs clean

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
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host reliability-host indexer workos-gateway
	$(GO_HOST_RUN) go test -tags=integration -count=1 -v ./tests/integration
	@set -eu; task_id="$$( $(GO_HOST_RUN) go run ./tests/restart seed )"; \
		app_ref="$$( $(GO_HOST_RUN) go run ./tests/restart app-seed )"; \
		install_ref="$$( $(GO_HOST_RUN) go run ./tests/restart install-seed )"; \
		surface_ref="$$( $(GO_HOST_RUN) go run ./tests/restart surface-seed )"; \
		bridge_ref="$$( $(GO_HOST_RUN) go run ./tests/restart bridge-seed )"; \
		grants_ref="$$( $(GO_HOST_RUN) go run ./tests/restart grants-seed )"; \
		version_ref="$$( $(GO_HOST_RUN) go run ./tests/restart version-seed )"; \
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
		$(GO_HOST_RUN) go run ./tests/restart policy-verify "$$1" "$$2" "$$3" "$$4" "$$5" "$$6" "$$7" "$$8"; \
		set -- $$version_ref; \
		$(GO_HOST_RUN) go run ./tests/restart version-verify "$$1" "$$2" "$$3" "$$4" "$$5" "$$6"; \
		index_ref="$$( $(GO_HOST_RUN) go run ./tests/restart index-seed )"; \
		set -- $$index_ref; \
		notification_ref="$$( $(GO_HOST_RUN) go run ./tests/restart notifications-seed )"; \
		docker compose restart workos-core harness-host runtime-host indexer >/dev/null; \
		$(GO_HOST_RUN) go run ./tests/restart index-verify "$$1" "$$2" "$$3"; \
		set -- $$notification_ref; \
		docker compose restart workos-core workos-gateway >/dev/null; \
		$(GO_HOST_RUN) go run ./tests/restart notifications-verify "$$1" "$$2" "$$3" "$$4" "$$5"


# The local-first notification gates (ADR-0014): real PostgreSQL + Core +
# harness-host + reliability-host + Gateway + Chromium. The notification
# center gate proves live delivery, paired-device read convergence, typed
# actions, and durability across a Gateway/Core restart. The incident gate
# proves the software-side cross-process chain (runtime observation ->
# reliability publication -> Core projection); it does not prove rootless
# supervisor acceptance. The app gate proves the granted-app
# notifications.create chain end to end. All three run in unique namespaces
# and never delete shared volumes.
test-notification-center: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host reliability-host workos-gateway
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestNotification' -v ./tests/integration
	docker compose restart workos-core workos-gateway >/dev/null
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestIncidentNotificationCrossProcess' -v ./tests/integration
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test '(^|/)notification-center\.spec\.ts$$'
	docker compose restart workos-core workos-gateway >/dev/null
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-e WORKOS_E2E_NOTIFICATIONS_VERIFY=1 \
		-e WORKOS_E2E_NOTIFICATIONS_SEED_UNREAD=1 \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test '(^|/)notification-center\.spec\.ts$$'
	@echo "test-notification-center: PASS"

NOTIFICATION_CAPTURE_DIR := $(WORKDIR)/docs/ui/desktop-web/changes/20260902-local-first-notifications/after

capture-notification-visual: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
	mkdir -p docs/ui/desktop-web/changes/20260902-local-first-notifications/after
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-e WORKOS_CAPTURE_DIR=$(NOTIFICATION_CAPTURE_DIR) \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test '(^|/)notification-visual\.spec\.ts$$'
	cp docs/ui/desktop-web/changes/20260902-local-first-notifications/after/notification-center--bell-badge--1440x900.png docs/ui/desktop-web/current/notification-center--bell-badge--1440x900.png
	cp docs/ui/desktop-web/changes/20260902-local-first-notifications/after/notification-center--unread-list--1440x900.png docs/ui/desktop-web/current/notification-center--unread-list--1440x900.png
	cp docs/ui/desktop-web/changes/20260902-local-first-notifications/after/notification-center--typed-action--1440x900.png docs/ui/desktop-web/current/notification-center--typed-action--1440x900.png
	cp docs/ui/desktop-web/changes/20260902-local-first-notifications/after/notification-center--bell-badge--820x1180.png docs/ui/desktop-web/current/notification-center--bell-badge--820x1180.png
	cp docs/ui/desktop-web/changes/20260902-local-first-notifications/after/notification-center--unread-list--820x1180.png docs/ui/desktop-web/current/notification-center--unread-list--820x1180.png
	cp docs/ui/desktop-web/changes/20260902-local-first-notifications/after/notification-center--bell-badge--390x844.png docs/ui/desktop-web/current/notification-center--bell-badge--390x844.png
	cp docs/ui/desktop-web/changes/20260902-local-first-notifications/after/notification-center--app-origin--390x844.png docs/ui/desktop-web/current/notification-center--app-origin--390x844.png

test-incident-notifications: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host reliability-host workos-gateway
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestIncidentPublication|TestIncidentNotificationCrossProcess' -v ./tests/integration
	@echo "test-incident-notifications: PASS (software chain; rootless supervisor acceptance remains a separate gate)"

test-app-notifications: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestAppNotificationIngest' -v ./tests/integration
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test '(^|/)app-notifications\.spec\.ts$$'
	@echo "test-app-notifications: PASS"

.PHONY: test-notification-center test-incident-notifications test-app-notifications

# The Credential Vault acceptance gate (ADR-0009): real PostgreSQL, the Core
# private mTLS execution listener, harness-host, the workosctl credential
# admin Unix socket, and the local DeepSeek fixture — never the public
# internet. Missing vault → credential-bearing providers fail closed; storing
# the synthetic credential over the real admin socket → binding carries a
# server-derived credential_ref and a full task runs on a task-bound lease;
# core+harness restart preserves the lease facts; revocation blocks new binds
# and acquires.
test-credential-vault:
	@set -eu; \
		cleanup() { docker compose --profile deepseek-fixture stop deepseek-api-fixture >/dev/null 2>&1 || true; }; \
		trap cleanup EXIT INT TERM; \
		active_deepseek_id() { \
			docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '\
				/^id: /{id=$$2} /^consumer: /{consumer=$$2} /^status: /{status=$$2} \
				consumer=="deepseek" && status=="ACTIVE"{print id; exit}'; \
		}; \
		echo "== phase 1: no active credential → credential-bearing providers fail closed =="; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		WORKOS_DEEPSEEK_ENABLED=true \
		WORKOS_DEEPSEEK_BASE_URL=http://127.0.0.1:18086 \
		docker compose --profile deepseek-fixture up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway deepseek-api-fixture; \
		docker compose exec -T workos-core /bin/sh -c 'test -f /run/workos/execution/ca.crt && test -f /run/workos/execution/core.key && test -f /run/workos/vault/vault-master.key && test ! -e /run/workos/execution/harness.key'; \
		docker compose exec -T harness-host /bin/sh -c 'test -f /run/workos/execution/ca.crt && test -f /run/workos/execution/harness.key && test ! -e /run/workos/execution/core.key && test ! -e /run/workos/vault/vault-master.key'; \
		cred_id="$$(active_deepseek_id)"; \
		if [ -z "$$cred_id" ]; then \
			$(GO_HOST_RUN_BASE) -e WORKOS_TEST_VAULT_PHASE=missing $(GO_IMAGE) go test -tags=integration -count=1 -run '^TestCredentialVaultStackPhase$$' -v ./tests/integration; \
			printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential put --consumer deepseek --purpose provider-api-key.v1 --label 'vault fixture' --idempotency-key 'vault-fixture-$$(date +%s%N)'"; \
			cred_id="$$(active_deepseek_id)"; \
		else \
			echo "an active deepseek credential already exists; phase 1 skipped and fixture material is resealed"; \
			cred_rev="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$$2} /^consumer: /{consumer=$$2} /^revision: /{revision=$$2} /^status: /{status=$$2} consumer=="deepseek" && status=="ACTIVE"{print revision; exit}')"; \
			printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential rotate --credential '$$cred_id' --expected-revision '$$cred_rev' --label 'vault fixture' --idempotency-key 'vault-fixture-reseal-$$(date +%s%N)'" >/dev/null; \
		fi; \
		cred_rev="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$$2} /^consumer: /{consumer=$$2} /^revision: /{revision=$$2} /^status: /{status=$$2} consumer=="deepseek" && status=="ACTIVE"{print revision; exit}')"; \
		test -n "$$cred_id" && test -n "$$cred_rev"; \
		echo "== phase 1b: in-process vault protocol against real PostgreSQL =="; \
		$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestVaultPutRotateRevokeLifecycle|TestVaultSealedMaterialFailsClosed|TestCredentialLeaseStateMachine' -v ./tests/integration; \
		echo "== phase 2: granted → binding + lease-bound task over the local fixture =="; \
		$(GO_HOST_RUN_BASE) -e WORKOS_TEST_VAULT_PHASE=granted $(GO_IMAGE) go test -tags=integration -count=1 -run '^TestCredentialVaultStackPhase$$' -v ./tests/integration; \
		echo "== phase 2b: core + harness restart persistence =="; \
		task_id="$$( $(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=deepseek go run ./tests/restart seed' )"; \
		docker compose restart workos-core harness-host >/dev/null; \
		$(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=deepseek go run ./tests/restart verify "$$1"' _ "$$task_id"; \
		echo "== phase 3: revocation blocks new binds and acquires =="; \
		docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential revoke --credential '$$cred_id' --expected-revision $$cred_rev --idempotency-key 'vault-fixture-revoke-$$(date +%s%N)'" >/dev/null; \
		$(GO_HOST_RUN_BASE) -e WORKOS_TEST_VAULT_PHASE=revoked $(GO_IMAGE) go test -tags=integration -count=1 -run '^TestCredentialVaultStackPhase$$' -v ./tests/integration; \
		echo "test-credential-vault: PASS"

# The Credential Vault expansion gate (ADR-0015): finite credential kinds
# (codex/github/cloud), the audited admin-socket reveal, and the online
# master-key rotation. Real PostgreSQL + Core + workosctl admin socket +
# the in-process protocol suite on scratch databases. The rotation uses a
# persistent successor key inside the vault volume and then swaps the
# configured master key file to it, so the shared dev volume keeps working
# for every later gate.
CLOUD_KIND_FIXTURE_SECRET = {"access_key_id":"AKIAWORKOSFIXTURE","secret_access_key":"fixture-only-material"}

test-credential-vault-expansion:
	@set -eu; \
		echo "== phase 1: new credential kinds over the admin socket =="; \
		docker compose up -d --build postgres bootstrap workos-core; \
		kind_cred_id() { \
			docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | \
			awk -v c="$$1" -v p="$$2" '/^id: /{id=$$2} /^consumer: /{cons=$$2} /^purpose: /{pur=$$2} /^status: /{if (cons==c && pur==p && $$2=="ACTIVE") print id}' | head -1; \
		}; \
		github_cred="$$(kind_cred_id github github-token.v1)"; \
		if [ -z "$$github_cred" ]; then \
			github_cred="$$( docker compose exec -T workos-core /bin/sh -c "printf '%s' 'github_pat_workosfixture000000000000000000' | /usr/local/bin/workosctl credential put --consumer github --purpose github-token.v1 --label 'kind fixture' --idempotency-key 'kind-github-$$(date +%s%N)'" | sed -n 's/^id: //p' | head -1 )"; \
		fi; \
		cloud_cred="$$(kind_cred_id aws-deploy cloud-credential.v1)"; \
		if [ -z "$$cloud_cred" ]; then \
			cloud_cred="$$( docker compose exec -T workos-core /bin/sh -c "printf '%s' '{\"access_key_id\":\"AKIAWORKOSFIXTURE\",\"secret_access_key\":\"fixture-only-material\"}' | /usr/local/bin/workosctl credential put --consumer aws-deploy --purpose cloud-credential.v1 --label 'cloud fixture' --idempotency-key 'kind-cloud-$$(date +%s%N)'" | sed -n 's/^id: //p' | head -1 )"; \
		fi; \
		test -n "$$github_cred" && test -n "$$cloud_cred"; \
		echo "== phase 2: kind grammar fails closed with zero side effects =="; \
		if docker compose exec -T workos-core /bin/sh -c "printf '%s' 'short' | /usr/local/bin/workosctl credential put --consumer github --purpose github-token.v1 --idempotency-key 'kind-invalid-$$(date +%s%N)'" >/dev/null 2>&1; then \
			echo "invalid github token was accepted"; exit 1; \
		fi; \
		if docker compose exec -T workos-core /bin/sh -c "printf '%s' 'not json' | /usr/local/bin/workosctl credential put --consumer aws-deploy --purpose cloud-credential.v1 --idempotency-key 'kind-invalid2-$$(date +%s%N)'" >/dev/null 2>&1; then \
			echo "invalid cloud credential was accepted"; exit 1; \
		fi; \
		echo "== phase 3: audited reveal =="; \
		revealed="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential reveal --credential "$$cloud_cred" | tail -1)"; \
		test "$$revealed" = '$(CLOUD_KIND_FIXTURE_SECRET)'; \
		echo "== phase 4: online master-key rotation + restart convergence =="; \
		mkdir -p tmp; \
		core_container="$$(docker compose ps -q workos-core)"; \
		docker exec "$$core_container" /bin/sh -c 'dd if=/dev/urandom of=/tmp/k2.key bs=32 count=1 status=none && chmod 600 /tmp/k2.key'; \
		docker cp "$$core_container:/tmp/k2.key" tmp/k2-rotation.key; \
		docker compose exec -T workos-core /usr/local/bin/workosctl credential rotate-master-key --new-key-file /tmp/k2.key; \
		docker exec "$$core_container" rm -f /tmp/k2.key; \
		echo "== phase 4b: successor-key process retires the rotation key =="; \
		docker run -d --rm --network host --user 0:0 --name workos-rotation-fixture \
			-e WORKOS_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable" \
			-e WORKOS_OWNER_ID=0198d7ea-2110-7c42-b659-c5e4d73bc337 \
			-e WORKOS_DEVICE_ID=0198d7ea-2110-7c42-b659-c5e4d73bc338 \
			-e WORKOS_HTTP_ADDRESS=127.0.0.1:18081 \
			-e WORKOS_CORE_EXECUTION_ADDRESS=127.0.0.1:18086 \
			-e WORKOS_CORE_EXECUTION_CA_FILE=/run/workos/execution/ca.crt \
			-e WORKOS_CORE_EXECUTION_CERT_FILE=/run/workos/execution/core.crt \
			-e WORKOS_CORE_EXECUTION_KEY_FILE=/run/workos/execution/core.key \
			-v workos_workos-credential-vault:/run/workos/vault:ro \
			-v workos_workos-core-execution:/run/workos/execution:ro \
			workos:dev sh -c 'sleep 300' >/dev/null; \
		docker cp tmp/k2-rotation.key workos-rotation-fixture:/run/k2.key; \
		docker exec -u 0 workos-rotation-fixture chown 10001:10001 /run/k2.key; \
		docker exec -u 10001 workos-rotation-fixture sh -c 'mkdir -p /tmp/rotadmin && chmod 700 /tmp/rotadmin && WORKOS_CREDENTIAL_MASTER_KEY_FILE=/run/k2.key WORKOS_CREDENTIAL_ADMIN_SOCKET=/tmp/rotadmin/admin.sock WORKOS_HTTP_ADDRESS=127.0.0.1:18081 WORKOS_CORE_EXECUTION_ADDRESS=127.0.0.1:18086 WORKOS_CORE_EXECUTION_CA_FILE=/run/workos/execution/ca.crt WORKOS_CORE_EXECUTION_CERT_FILE=/run/workos/execution/core.crt WORKOS_CORE_EXECUTION_KEY_FILE=/run/workos/execution/core.key WORKOS_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable" WORKOS_OWNER_ID=0198d7ea-2110-7c42-b659-c5e4d73bc337 WORKOS_DEVICE_ID=0198d7ea-2110-7c42-b659-c5e4d73bc338 /usr/local/bin/workos-core >/tmp/rotation-core.log 2>&1 & sleep 4; WORKOS_CREDENTIAL_ADMIN_SOCKET=/tmp/rotadmin/admin.sock /usr/local/bin/workosctl credential rotate-master-key --new-key-file /run/workos/vault/vault-master.key'; \
		docker stop workos-rotation-fixture >/dev/null; \
		rm -f tmp/k2-rotation.key; \
		docker compose restart workos-core >/dev/null; \
		sleep 3; \
		revealed="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential reveal --credential "$$cloud_cred" | tail -1)"; \
		test "$$revealed" = '$(CLOUD_KIND_FIXTURE_SECRET)'; \
		noop="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential rotate-master-key --new-key-file /run/workos/vault/vault-master.key)"; \
		echo "$$noop" | grep -q "already current"; \
		echo "== phase 5: in-process rotation protocol on scratch databases =="; \
		$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestVaultCredentialKindsLifecycle|TestVaultRevealAuditsAndFailsClosed|TestVaultMasterKeyRotationConverges|TestVaultRotationIsAllOrNothing|TestVaultKindsAreOwnerScoped' -v ./tests/integration; \
		echo "test-credential-vault-expansion: PASS"

# The review-artifact-as-Agent-context gate (ADR-0010): real PostgreSQL +
# Core + harness-host + Gateway + Chromium through Task Router context
# verification, private lease-bound ResolveTaskContext, and the provider's
# deterministic context receipt — plus the digest-mismatch / foreign /
# unsupported fail-closed paths in the Go layer.
test-artifact-context: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestResolveTaskContext' -v ./tests/integration
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test artifact-context.spec.ts

# Regenerates the project knowledge-search deterministic evidence: expanded
# results, pinned Agent context chip, granted app surface, and compact
# results. Uses fixed fixture data only.
capture-knowledge-visual: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway indexer
	mkdir -p docs/ui/desktop-web/changes/20260901-project-knowledge-search/after
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-e WORKOS_CAPTURE_DIR=$(WORKDIR)/docs/ui/desktop-web/changes/20260901-project-knowledge-search/after \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test knowledge-visual.spec.ts
	cp docs/ui/desktop-web/changes/20260901-project-knowledge-search/after/knowledge-center--results--1440x900.png docs/ui/desktop-web/current/knowledge-center--results--1440x900.png
	cp docs/ui/desktop-web/changes/20260901-project-knowledge-search/after/knowledge-center--results--390x844.png docs/ui/desktop-web/current/knowledge-center--results--390x844.png
	cp docs/ui/desktop-web/changes/20260901-project-knowledge-search/after/agent-center--context-chip--1440x900.png docs/ui/desktop-web/current/agent-center--context-chip--1440x900.png
	cp docs/ui/desktop-web/changes/20260901-project-knowledge-search/after/app-knowledge-search--results--1440x900.png docs/ui/desktop-web/current/app-knowledge-search--results--1440x900.png

capture-artifact-context-visual: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host workos-gateway
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-e WORKOS_CAPTURE_DIR=$(ARTIFACT_CONTEXT_CAPTURE_DIR) \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test artifact-context-visual.spec.ts
	cp docs/ui/desktop-web/changes/20260830-artifact-agent-context/after/artifact-center--use-as-context--1440x900.png docs/ui/desktop-web/current/artifact-center--use-as-context--1440x900.png
	cp docs/ui/desktop-web/changes/20260830-artifact-agent-context/after/agent-center--context-chip--1440x900.png docs/ui/desktop-web/current/agent-center--context-chip--1440x900.png

# The DeepSeek structured review gate (ADR-0011): real PostgreSQL + Core
# private mTLS listener + harness-host + workosctl admin socket + local
# DeepSeek fixture + Gateway + Chromium. Exercises the full chain: DeepSeek
# bound with a server-derived credential ref, structured run requesting both
# canonical outputs, strict JSON parsing, atomic batch materialization, the
# inert viewers, reload durability, and the malformed-output fail-closed path.
test-deepseek-structured-review: e2e-image
	@set -eu; \
		cleanup() { docker compose --profile deepseek-fixture stop deepseek-api-fixture >/dev/null 2>&1 || true; }; \
		trap cleanup EXIT INT TERM; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		WORKOS_DEEPSEEK_ENABLED=true \
		WORKOS_DEEPSEEK_BASE_URL=http://127.0.0.1:18086 \
		docker compose --profile deepseek-fixture up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway deepseek-api-fixture; \
		cred_id="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$$2} /^consumer: /{consumer=$$2} /^status: /{status=$$2} consumer=="deepseek" && status=="ACTIVE"{print id; exit}')"; \
		if [ -z "$$cred_id" ]; then \
			printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential put --consumer deepseek --purpose provider-api-key.v1 --label 'structured fixture' --idempotency-key 'structured-fixture-$$(date +%s%N)'"; \
		else \
			cred_rev="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^consumer: /{consumer=$$2} /^revision: /{revision=$$2} /^status: /{status=$$2} consumer=="deepseek" && status=="ACTIVE"{print revision; exit}')"; \
			printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential rotate --credential '$$cred_id' --expected-revision '$$cred_rev' --label 'structured fixture' --idempotency-key 'structured-fixture-reseal-$$(date +%s%N)'" >/dev/null; \
		fi; \
		docker run --rm --network host $(USER_FLAGS) \
			-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
			-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
			-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
			-e WORKOS_DEEPSEEK_STRUCTURED_E2E=true \
			-v $(CURDIR):$(WORKDIR) \
			-w $(WORKDIR)/apps/desktop-web \
			$(E2E_IMAGE) pnpm exec playwright test deepseek-structured-review.spec.ts

test-deepseek-fixture: e2e-image
	@set -eu; \
		cleanup() { docker compose --profile deepseek-fixture stop deepseek-api-fixture >/dev/null 2>&1 || true; }; \
		trap cleanup EXIT INT TERM; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		WORKOS_DEEPSEEK_ENABLED=true \
		WORKOS_DEEPSEEK_BASE_URL=http://127.0.0.1:18086 \
		docker compose --profile deepseek-fixture up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway deepseek-api-fixture; \
		cred_id="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$$2} /^consumer: /{consumer=$$2} /^status: /{status=$$2} consumer=="deepseek" && status=="ACTIVE"{print id; exit}')"; \
		if [ -z "$$cred_id" ]; then \
			printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential put --consumer deepseek --purpose provider-api-key.v1 --label 'deepseek fixture' --idempotency-key 'deepseek-fixture-$$(date +%s%N)'"; \
		else \
			cred_rev="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^consumer: /{consumer=$$2} /^revision: /{revision=$$2} /^status: /{status=$$2} consumer=="deepseek" && status=="ACTIVE"{print revision; exit}')"; \
			printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential rotate --credential '$$cred_id' --expected-revision '$$cred_rev' --label 'deepseek fixture' --idempotency-key 'deepseek-fixture-reseal-$$(date +%s%N)'" >/dev/null; \
		fi; \
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

# The Codex harness gate (ADR-0015): the versioned app-server fixture runs
# as a real child of harness-host over the Core mTLS execution channel with a
# codex-auth.v1 task-bound credential lease. Proves catalog honesty, the
# streaming canonical event mapping, bounded usage, task durability across a
# Core+harness restart, and the browser binding/run journey.
test-codex-harness: e2e-image
	@set -eu; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		WORKOS_CODEX_ENABLED=true \
		docker compose up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway; \
		cred_id="$$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$$2} /^consumer: /{cons=$$2} /^purpose: /{pur=$$2} /^status: /{if (cons=="codex" && pur=="codex-auth.v1" && $$2=="ACTIVE") {print id; exit}}')"; \
		if [ -z "$$cred_id" ]; then \
			cred_id="$$( docker compose exec -T workos-core /bin/sh -c "printf '%s' 'codex-fixture-key-not-a-real-credential' | /usr/local/bin/workosctl credential put --consumer codex --purpose codex-auth.v1 --label 'codex fixture' --idempotency-key 'codex-fixture-$$(date +%s%N)'" | sed -n 's/^id: //p' | head -1 )"; \
		fi; \
		test -n "$$cred_id"; \
		$(GO_HOST_RUN) go test -tags='integration codexfixture' -count=1 -run '^TestCodexProjectBindingFixtureVerticalSlice$$' -v ./tests/integration; \
		task_id="$$( $(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=codex go run ./tests/restart seed' )"; \
		docker compose restart workos-core harness-host >/dev/null; \
		$(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=codex go run ./tests/restart verify "$$1"' _ "$$task_id"; \
		docker run --rm --network host $(USER_FLAGS) \
			-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
			-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
			-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
			-e WORKOS_CODEX_FIXTURE_E2E=true \
			-v $(CURDIR):$(WORKDIR) \
			-w $(WORKDIR)/apps/desktop-web \
			$(E2E_IMAGE) pnpm exec playwright test codex-fixture.spec.ts; \
		echo "test-codex-harness: PASS"

# The MCP harness gate (ADR-0015): the versioned stdio fixture runs as a
# real child of harness-host. Proves the honest degraded capability subset,
# credential-free binding, the deterministic blocking tool-call run, and
# durable completion across a Core+harness restart.
test-mcp-harness: e2e-image
	@set -eu; \
		WORKOS_MCP_ENABLED=true \
		docker compose up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway; \
		$(GO_HOST_RUN) go test -tags='integration mcpfixture' -count=1 -run '^TestMCPProjectBindingFixtureVerticalSlice$$' -v ./tests/integration; \
		task_id="$$( $(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=mcp go run ./tests/restart seed' )"; \
		docker compose restart workos-core harness-host >/dev/null; \
		$(GO_HOST_RUN) sh -c 'WORKOS_TEST_PROVIDER=mcp go run ./tests/restart verify "$$1"' _ "$$task_id"; \
		echo "test-mcp-harness: PASS"

PROVIDER_CATALOG_CAPTURE_DIR := $(WORKDIR)/docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/after

capture-provider-catalog: e2e-image
	@set -eu; \
		mkdir -p docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/before docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/after; \
		echo "== before frame: baseline provider set (adapters disabled) =="; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		docker compose up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway; \
		docker run --rm --network host $(USER_FLAGS) \
			-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright -e WORKOS_E2E_URL=http://127.0.0.1:8080 \
			-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
			-e WORKOS_CODEX_FIXTURE_E2E=true -e WORKOS_PROVIDERS_EXPANDED=false \
			-e WORKOS_CAPTURE_DIR=$(WORKDIR)/docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/before \
			-v $(CURDIR):$(WORKDIR) -w $(WORKDIR)/apps/desktop-web \
			$(E2E_IMAGE) pnpm exec playwright test provider-catalog-visual.spec.ts; \
		echo "== after frame: expanded provider set =="; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" WORKOS_CODEX_ENABLED=true WORKOS_MCP_ENABLED=true \
		docker compose up -d --build --force-recreate harness-host >/dev/null; \
		sleep 3; \
		docker run --rm --network host $(USER_FLAGS) \
			-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright -e WORKOS_E2E_URL=http://127.0.0.1:8080 \
			-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
			-e WORKOS_CODEX_FIXTURE_E2E=true -e WORKOS_PROVIDERS_EXPANDED=true \
			-e WORKOS_CAPTURE_DIR=$(PROVIDER_CATALOG_CAPTURE_DIR) \
			-v $(CURDIR):$(WORKDIR) -w $(WORKDIR)/apps/desktop-web \
			$(E2E_IMAGE) pnpm exec playwright test provider-catalog-visual.spec.ts; \
		cp docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/after/harness-settings--provider-catalog-expanded--1440x900.png docs/ui/desktop-web/current/harness-settings--provider-catalog--1440x900.png; \
		echo "capture-provider-catalog: PASS"

# The REAL rootless Podman + cgroup v2 acceptance gate (ADR-0006/ADR-0016).
# Like test-podman-fixture, it is compiled in the pinned toolchain container
# and executed on the host. It fails loudly - never silently passes - on
# hosts without the full acceptance precondition set, recording the exact
# probe output as the task's BLOCKED evidence.
test-rootless-runtime:
	@set -eu; \
		echo "== rootless acceptance probe (ADR-0016 §1) =="; \
		probe_ok=1; \
		if command -v podman >/dev/null 2>&1; then \
			echo "podman: $$(command -v podman)"; \
			podman info --format '{{.Host.Security.Rootless}} {{.Host.CgroupsVersion}}' || probe_ok=0; \
		else \
			echo "podman: NOT AVAILABLE (command -v podman failed)"; \
			probe_ok=0; \
		fi; \
		if [ -f /sys/fs/cgroup/cgroup.controllers ]; then \
			echo "cgroup v2: yes ($$(head -c 120 /sys/fs/cgroup/cgroup.controllers))"; \
		else \
			echo "cgroup v2: no"; \
			probe_ok=0; \
		fi; \
		echo "user namespaces: $$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo unknown)"; \
		if [ "$$probe_ok" != "1" ]; then \
			echo "test-rootless-runtime: BLOCKED - the acceptance host does not satisfy the rootless Podman preconditions above."; \
			echo "The container-runner capability stays unavailable; no host software is installed."; \
			exit 1; \
		fi; \
		trap 'rm -f tmp/rootlessruntime.test tmp/workos-web-fixture' EXIT HUP INT TERM; \
		$(GO_RUN) sh -c 'go test -c -o tmp/rootlessruntime.test -tags podmanfixture ./tests/podmanfixture && CGO_ENABLED=0 go build -o tmp/workos-web-fixture ./tests/podmanfixture/fixture'; \
		WORKOS_PODMAN_FIXTURE_BINARY="$$(pwd)/tmp/workos-web-fixture" tmp/rootlessruntime.test -test.v; \
		echo "test-rootless-runtime: PASS"

# The REAL supervision chain gate (ADR-0016 §3): the six-process stack with
# the bounded fixture engine (no Podman on this host). Proves observation ->
# incident (exactly one per occurrence) -> restart action advancing the
# workload generation -> deterministic stop at the restart limit -> the
# incident lands in the owner-visible projection.
test-real-supervision: e2e-image
	@set -eu; \
		mkdir -p tmp; \
		printf '# fake engine scenario (ADR-0016): name=ok|crash|oom|flap\n' > tmp/fake-engine-scenario.conf; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		docker compose -f compose.yaml -f deploy/compose.supervision.yaml up -d --build --force-recreate postgres bootstrap workos-core harness-host runtime-host reliability-host workos-gateway; \
		$(GO_HOST_RUN) go test -tags='integration realsupervision' -count=1 -run '^TestRealSupervisionChain$$' -v ./tests/integration; \
		echo "test-real-supervision: PASS"

# The telemetry gate (ADR-0016 §4): the six-process stack exporting OTLP to
# the collector, whose file export feeds the reliability collector component.
# Proves real traffic produces real sanitized aggregates (per-service spans,
# errors, durations), that the in-process attribute budget bounds the export,
# and that the summary projection stays a numeric whitelist - the submitted
# goal marker never appears.
test-telemetry: e2e-image
	@set -eu; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		docker compose -f compose.yaml -f deploy/compose.observability.yaml up -d --build --force-recreate postgres bootstrap otel-collector workos-core harness-host runtime-host reliability-host indexer workos-gateway; \
		$(GO_HOST_RUN) go test -tags='integration telemetryfixture' -count=1 -run '^TestTelemetryPipeline$$' -v ./tests/integration; \
		echo "test-telemetry: PASS"

# The repair/deployment gate (ADR-0016 sections 5-6): the fixture-engine
# supervision chain produces a real incident, the reliability repair
# orchestrator submits the repair task through Core's private admission RPC
# (standard queue/lease/terminal chain), and the incident projects the
# repair task idempotently.
test-repair-deployment: e2e-image
	@set -eu; \
		mkdir -p tmp; \
		printf '# fake engine scenario (ADR-0016): name=ok|crash|oom|flap\n' > tmp/fake-engine-scenario.conf; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		docker compose -f compose.yaml -f deploy/compose.supervision.yaml up -d --build --force-recreate postgres bootstrap workos-core harness-host runtime-host reliability-host workos-gateway; \
		$(GO_HOST_RUN) go test -tags='integration repairdeployment' -count=1 -run '^TestRepairOrchestratorTurnsIncidentIntoTask$$' -v ./tests/integration; \
		echo "test-repair-deployment: PASS"

# The full App Bridge capability gate (ADR-0016 §5 / structure 10.5): a
# granted web bundle exercises the shell-side bridge methods (project.current
# with the project.read grant, theme.get, window.setTitle, window.close) and
# the fail-closed path for an ungranted capability, end to end through the
# opaque-origin surface, app-host shell dispatch, and the runtime/Core chain.
test-app-bridge-full: e2e-image
	@set -eu; \
		WORKOS_UID="$$(id -u)" WORKOS_GID="$$(id -g)" \
		docker compose up -d --build --force-recreate postgres bootstrap workos-core runtime-host workos-gateway; \
		docker run --rm --network host $(USER_FLAGS) \
			-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
			-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
			-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
			-v $(CURDIR):$(WORKDIR) \
			-w $(WORKDIR)/apps/desktop-web \
			$(E2E_IMAGE) pnpm exec playwright test app-bridge-full.spec.ts; \
		echo "test-app-bridge-full: PASS"

e2e-image:
	docker build \
		--build-arg DEBIAN_MIRROR=$(DEBIAN_MIRROR) \
		--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
		--build-arg PLAYWRIGHT_DOWNLOAD_HOST=$(PLAYWRIGHT_DOWNLOAD_HOST) \
		--build-arg PLAYWRIGHT_VERSION=$(PLAYWRIGHT_VERSION) \
		-t $(E2E_IMAGE) \
		-f deploy/e2e/Dockerfile deploy/e2e

# The opt-in REAL rootless Podman + cgroup v2 gate (ADR-0006). The test
# binary and the fixture payload are compiled in the pinned toolchain
# container, then EXECUTED ON THE HOST — podman, the user session, and the
# delegated cgroup subtree live on the host, not inside a Go image. It fails
# loudly — never silently passes — on hosts without podman.
test-podman-fixture:
	@command -v podman >/dev/null 2>&1 || { \
		echo "test-podman-fixture: BLOCKED — podman is not available on this host."; \
		echo "Install rootless podman (with unprivileged user namespaces and cgroup v2) and re-run."; \
		exit 1; \
	}
	@set -eu; \
		trap 'rm -f tmp/podmanfixture.test tmp/workos-web-fixture' EXIT HUP INT TERM; \
		$(GO_RUN) sh -c 'go test -c -o tmp/podmanfixture.test -tags podmanfixture ./tests/podmanfixture && CGO_ENABLED=0 go build -o tmp/workos-web-fixture ./tests/podmanfixture/fixture'; \
		WORKOS_PODMAN_FIXTURE_BINARY="$$(pwd)/tmp/workos-web-fixture" tmp/podmanfixture.test -test.v

# The adaptive shell gate (docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md):
# real PostgreSQL + Core + harness-host + runtime-host + Gateway + Chromium
# at the three canonical viewports (390x844 compact, 820x1180 medium,
# 1440x900 expanded) plus an injected fold-segment fixture. Drives project,
# App Registry/Installation, the fake-harness Agent chain, and the
# Reliability-backed System Monitor through the phone/tablet shells and
# re-asserts the expanded desktop.
test-adaptive-shell: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host reliability-host workos-gateway
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test adaptive-shell.spec.ts

# The owner-triggered version transition/rollback gate (ADR-0012): real
# PostgreSQL + Core + Gateway + Chromium through two immutable web-bundle
# versions, the App Library consent flow, the Versions dialog, surface
# invalidation, exact first-response replay, and fail-closed conflicts.
test-app-version-rollback: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test app-version-rollback.spec.ts

# The project knowledge-search gate (ADR-0013): real PostgreSQL + Core +
# harness-host + indexer + Gateway + Chromium through publication, durable
# ingestion, owner lexical search, Agent-context pinning, isolation, and
# restart convergence. Not a route-mocked test.
test-project-knowledge-search: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway indexer
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestProjectKnowledgeSearchStack' -v ./tests/integration
	@set -eu; \
	i=0; until curl -sf http://127.0.0.1:8080/healthz >/dev/null 2>&1; do i=$$((i+1)); [ $$i -le 60 ] || { echo 'gateway readiness timed out' >&2; exit 1; }; sleep 1; done; \
	i=0; until curl -sf http://127.0.0.1:8085/readyz >/dev/null 2>&1; do i=$$((i+1)); [ $$i -le 60 ] || { echo 'indexer readiness timed out' >&2; exit 1; }; sleep 1; done
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test '(^|/)knowledge-search\.spec\.ts$$'
	@set -eu; \
		cleanup() { docker compose start indexer >/dev/null 2>&1 || true; }; \
		trap cleanup EXIT INT TERM; \
		docker compose stop indexer >/dev/null; \
		docker run --rm --network host $(USER_FLAGS) \
			-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
			-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
			-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
			-e WORKOS_KNOWLEDGE_OUTAGE_E2E=true \
			-v $(CURDIR):$(WORKDIR) \
			-w $(WORKDIR)/apps/desktop-web \
			$(E2E_IMAGE) pnpm exec playwright test knowledge-unavailable.spec.ts; \
		docker compose start indexer >/dev/null; \
		i=0; until curl -sf http://127.0.0.1:8085/readyz >/dev/null 2>&1; do i=$$((i+1)); [ $$i -le 60 ] || { echo 'indexer recovery timed out' >&2; exit 1; }; sleep 1; done; \
		trap - EXIT INT TERM
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestProjectKnowledgeSearchStack' -v ./tests/integration

# The granted-app knowledge-read gate (ADR-0013): real Core/Runtime/Indexer/
# Gateway/PostgreSQL/Chromium with an opaque-origin web bundle. Proves the
# negotiated method, the scoped results, isolation, and revoke fail-closed.
test-app-knowledge-search: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway indexer
	@set -eu; \
	i=0; until curl -sf http://127.0.0.1:8080/healthz >/dev/null 2>&1; do i=$$((i+1)); [ $$i -le 60 ] || { echo 'gateway readiness timed out' >&2; exit 1; }; sleep 1; done; \
	i=0; until curl -sf http://127.0.0.1:8085/readyz >/dev/null 2>&1; do i=$$((i+1)); [ $$i -le 60 ] || { echo 'indexer readiness timed out' >&2; exit 1; }; sleep 1; done
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test '(^|/)app-knowledge-search\.spec\.ts$$'

# The disaster-recovery rebuild gate (ADR-0013): Core-authoritative shadow
# generation rebuild with live traffic, crash resume at every phase, and the
# destroy-and-restore golden equivalence in a temporary indexer-owned schema.
test-project-knowledge-rebuild: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway indexer
	@set -eu; \
	i=0; until curl -sf http://127.0.0.1:8080/healthz >/dev/null 2>&1; do i=$$((i+1)); [ $$i -le 60 ] || { echo 'gateway readiness timed out' >&2; exit 1; }; sleep 1; done; \
	i=0; until curl -sf http://127.0.0.1:8085/readyz >/dev/null 2>&1; do i=$$((i+1)); [ $$i -le 60 ] || { echo 'indexer readiness timed out' >&2; exit 1; }; sleep 1; done
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestProjectKnowledgeSearchStack' -v ./tests/integration
	$(GO_HOST_RUN) go test -tags=integration -count=1 -run 'TestProjectKnowledgeRebuild' -v ./tests/integration
	@set -eu; \
	status="$$(docker compose exec -T indexer /usr/local/bin/workosctl index status --json)"; \
	active="$$(printf '%s' "$$status" | sed -n 's/.*"generation_id":"\([^"]*\)".*/\1/p')"; \
	test -n "$$active"; \
	key="project-knowledge-rebuild-gate-$$active"; \
	started="$$(docker compose exec -T indexer /usr/local/bin/workosctl index rebuild --all --idempotency-key "$$key")"; \
	job="$$(printf '%s\n' "$$started" | sed -n 's/^job: //p')"; \
	test -n "$$job"; \
	replayed="$$(docker compose exec -T indexer /usr/local/bin/workosctl index rebuild --all --idempotency-key "$$key")"; \
	replay_job="$$(printf '%s\n' "$$replayed" | sed -n 's/^job: //p')"; \
	test "$$job" = "$$replay_job"; \
	docker compose restart indexer >/dev/null; \
	i=0; while :; do \
		view="$$(docker compose exec -T indexer /usr/local/bin/workosctl index job get --job "$$job" --json 2>/dev/null || true)"; \
		case "$$view" in *'"state":"completed"'*) break ;; *'"state":"failed"'*|*'"state":"canceled"'*) printf '%s\n' "$$view" >&2; exit 1 ;; esac; \
		i=$$((i+1)); [ $$i -le 90 ] || { echo 'real indexer rebuild timed out' >&2; exit 1; }; sleep 1; \
	done; \
	case "$$view" in *owner_user_id*|*project_id*) echo 'admin job output leaked raw scope identifiers' >&2; exit 1 ;; esac; \
	printf 'real Core-authoritative rebuild completed after indexer restart: %s\n' "$$job"

test-e2e: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host reliability-host workos-gateway
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm test:e2e

# The review-artifact vertical gate (ADR-0008): real PostgreSQL + Core +
# harness-host (Fake provider structured output) + Gateway + Chromium, all
# the way through Task Router → private lease-bound AppendTaskArtifact →
# Artifact PostgreSQL facts → public ArtifactService reads → the read-only
# viewer. Not a route-mocked test.
test-artifact-review: e2e-image
	docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_URL=http://127.0.0.1:8080 \
		-e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test artifact-review.spec.ts

# Regenerates the task's deterministic 1440x900 Auth Gate and Device Center
# evidence. Device Center uses browser-intercepted Connect fixtures; no live
# ticket, credential, provider, or persistent database row appears in it.
capture-lan-pairing-visual: e2e-image
	@set -eu; \
	certdir="$$(mktemp -d)"; \
	cleanup() { docker compose --profile lan-pairing stop workos-gateway-tls >/dev/null 2>&1 || true; rm -rf "$$certdir"; }; \
	trap cleanup EXIT HUP INT TERM; \
	docker run --rm $(USER_FLAGS) -e HOME=/tmp -e GOPATH=/tmp/workos-go \
		-e GOMODCACHE=/go/pkg/mod -e GOPROXY=$(GOPROXY) \
		-v $(CURDIR):$(WORKDIR) -w $(WORKDIR) -v workos-go-cache:/go/pkg/mod \
		-v "$$certdir":/certs \
		$(GO_IMAGE) go run ./tests/lanpairing/gencert -out /certs; \
	export WORKOS_TLS_CERT="$$certdir/leaf.crt" WORKOS_TLS_KEY="$$certdir/leaf.key"; \
	docker compose --profile lan-pairing up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway-tls; \
	mkdir -p "$(LAN_PAIRING_CAPTURE_DIR)"; \
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_TLS_URL=https://localhost:8443 \
		-e WORKOS_CAPTURE_DIR=/captures \
		-v "$(LAN_PAIRING_CAPTURE_DIR)":/captures \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test lan-pairing-visual.spec.ts; \
	cp "$(LAN_PAIRING_CAPTURE_DIR)"/*.png docs/ui/desktop-web/current/; \
	echo "capture-lan-pairing-visual: PASS"

# The production-auth acceptance gate (ADR-0007): a real TLS gateway
# (temporarily generated leaf + SAN localhost), real PostgreSQL, the real
# admin Unix socket via workosctl, and a real Chromium profile that pairs,
# keeps its session across a gateway restart, re-proves from IndexedDB, and
# fails closed after revocation. All fixture material lives in temp
# directories removed on exit; nothing is committed.
test-lan-pairing: e2e-image
	@set -eu; \
	certdir="$$(mktemp -d)"; \
	profiledir="$$(mktemp -d)"; \
	profiledir_b="$$(mktemp -d)"; \
	stamp="$$(date +%s)"; \
	cleanup() { docker compose --profile lan-pairing stop workos-gateway-tls >/dev/null 2>&1 || true; rm -rf "$$certdir" "$$profiledir" "$$profiledir_b"; }; \
	trap cleanup EXIT HUP INT TERM; \
	echo "== generating temporary TLS fixture =="; \
	docker run --rm $(USER_FLAGS) -e HOME=/tmp -e GOPATH=/tmp/workos-go \
		-e GOMODCACHE=/go/pkg/mod -e GOPROXY=$(GOPROXY) \
		-v $(CURDIR):$(WORKDIR) -w $(WORKDIR) -v workos-go-cache:/go/pkg/mod \
		-v "$$certdir":/certs \
		$(GO_IMAGE) go run ./tests/lanpairing/gencert -out /certs; \
	test -f "$$certdir/leaf.crt" -a -f "$$certdir/leaf.key"; \
	export WORKOS_TLS_CERT="$$certdir/leaf.crt" WORKOS_TLS_KEY="$$certdir/leaf.key"; \
	echo "== starting the production-auth gateway =="; \
	docker compose --profile lan-pairing up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway-tls; \
	echo "== rotating an operator pairing ticket over the admin socket =="; \
	pair_url="$$(docker compose exec -T workos-gateway-tls /usr/local/bin/workosctl device pair | grep '^https://')"; \
	test -n "$$pair_url"; \
	echo "== phase: pair =="; \
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_TLS_URL=https://localhost:8443 \
		-e WORKOS_LAN_PHASE=pair \
		-e WORKOS_LAN_PROFILE=/lan-profile \
		-e WORKOS_LAN_DEVICE_NAME='E2E LAN Device' \
		-e WORKOS_LAN_PROJECT="E2E LAN $$stamp" \
		-e WORKOS_E2E_PAIRING_URL="$$pair_url" \
		-v "$$profiledir":/lan-profile \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test lan-pairing.spec.ts; \
	echo "== phase: gateway restart persistence =="; \
	docker compose restart workos-gateway-tls >/dev/null; \
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_TLS_URL=https://localhost:8443 \
		-e WORKOS_LAN_PHASE=persist \
		-e WORKOS_LAN_PROFILE=/lan-profile \
		-e WORKOS_LAN_DEVICE_NAME='E2E LAN Device' \
		-e WORKOS_LAN_PROJECT="E2E LAN $$stamp" \
		-v "$$profiledir":/lan-profile \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test lan-pairing.spec.ts; \
	echo "== phase: session proof re-authentication =="; \
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_TLS_URL=https://localhost:8443 \
		-e WORKOS_LAN_PHASE=reauth \
		-e WORKOS_LAN_PROFILE=/lan-profile \
		-e WORKOS_LAN_DEVICE_NAME='E2E LAN Device' \
		-e WORKOS_LAN_PROJECT="E2E LAN $$stamp" \
		-v "$$profiledir":/lan-profile \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test lan-pairing.spec.ts; \
	echo "== phase: two paired-device notification convergence =="; \
	pair_url_b="$$(docker compose exec -T workos-gateway-tls /usr/local/bin/workosctl device pair | grep '^https://')"; \
	test -n "$$pair_url_b"; \
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_TLS_URL=https://localhost:8443 \
		-e WORKOS_LAN_PHASE=paired-notifications \
		-e WORKOS_LAN_PROFILE=/lan-profile \
		-e WORKOS_LAN_PROFILE_B=/lan-profile-b \
		-e WORKOS_LAN_DEVICE_NAME='E2E LAN Device' \
		-e WORKOS_LAN_DEVICE_NAME_B='E2E LAN Device B' \
		-e WORKOS_LAN_PROJECT="E2E LAN $$stamp" \
		-e WORKOS_E2E_PAIRING_URL="$$pair_url_b" \
		-v "$$profiledir":/lan-profile \
		-v "$$profiledir_b":/lan-profile-b \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test lan-pairing.spec.ts; \
	echo "== phase: revocation fails closed =="; \
	docker run --rm --network host $(USER_FLAGS) \
		-e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
		-e WORKOS_E2E_TLS_URL=https://localhost:8443 \
		-e WORKOS_LAN_PHASE=revoke \
		-e WORKOS_LAN_PROFILE=/lan-profile \
		-e WORKOS_LAN_DEVICE_NAME='E2E LAN Device' \
		-e WORKOS_LAN_PROJECT="E2E LAN $$stamp" \
		-v "$$profiledir":/lan-profile \
		-v $(CURDIR):$(WORKDIR) \
		-w $(WORKDIR)/apps/desktop-web \
		$(E2E_IMAGE) pnpm exec playwright test lan-pairing.spec.ts; \
	echo "test-lan-pairing: PASS (temp TLS + admin ticket + two browser pairings + paired notification convergence + cookie + restart + re-auth + revoke)"

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
