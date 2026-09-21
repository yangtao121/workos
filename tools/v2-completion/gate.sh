#!/bin/sh
# Fully isolated six-process fixture; no shared database, Vault, or bootstrap.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
export WORKOS_V2_DIR=$(mktemp -d "$repo/tmp/v2-completion.XXXXXX")
export WORKOS_V2_USER="$(id -u):$(id -g)"
export WORKOS_V2_DOCKER_GID="$(stat -c %g /var/run/docker.sock)"
export WORKOS_V2_NAMESPACE="$(basename "$WORKOS_V2_DIR" | tr '[:upper:].' '[:lower:]-')"
export WORKOS_V2_BIN="$repo/tmp/v2-completion-intake/bin"
mkdir -p "$WORKOS_V2_BIN" "$repo/tmp/go-build-cache"
python3 - "$WORKOS_V2_DIR/env" <<'PY'
import socket,sys
held=[]; lines=[]
for name in ['DATABASE','GATEWAY','CORE','HARNESS','RUNTIME','EXECUTION','RELIABILITY','INDEXER','MODEL']:
    s=socket.socket(); s.bind(('127.0.0.1',0)); held.append(s)
    lines.append(f'export WORKOS_V2_{name}_PORT={s.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(lines)+'\n')
PY
. "$WORKOS_V2_DIR/env"
export WORKOS_V2_DATABASE_URL="postgres://workos:workos@127.0.0.1:$WORKOS_V2_DATABASE_PORT/workos?sslmode=disable"
compose() { docker compose -p "$WORKOS_V2_NAMESPACE" -f tools/v2-completion/compose.yaml "$@"; }
cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [ "$result" -ne 0 ]; then compose logs --no-color > "$WORKOS_V2_DIR/stack.log" 2>&1 || true; fi
 if [ "${WORKOS_V2_KEEP:-}" != 1 ]; then
  compose down --timeout 5 >/dev/null 2>&1 || true
  docker ps -aq --filter "label=workos.runtime=$WORKOS_V2_NAMESPACE" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker ps -aq --filter "label=workos.network=$WORKOS_V2_NAMESPACE" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network ls -q --filter "label=workos.network=$WORKOS_V2_NAMESPACE" | xargs -r docker network rm >/dev/null 2>&1 || true
  docker rm -f "$WORKOS_V2_NAMESPACE-db" >/dev/null 2>&1 || true
 fi
 printf 'V2 fixture result=%s evidence=%s\n' "$result" "$WORKOS_V2_DIR"
 exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$WORKOS_V2_DIR/core-execution" "$WORKOS_V2_DIR/harness-execution" "$WORKOS_V2_DIR/vault" "$WORKOS_V2_DIR/run" "$WORKOS_V2_DIR/sessions" "$WORKOS_V2_DIR/project" "$WORKOS_V2_DIR/bridges" "$WORKOS_V2_DIR/delegations" "$WORKOS_V2_DIR/x11" "$WORKOS_V2_DIR/indexer-run"
chmod 1777 "$WORKOS_V2_DIR/x11"
chmod 700 "$WORKOS_V2_DIR/indexer-run"
printf '%s\n' 'Fixture workspace. No private data.' > "$WORKOS_V2_DIR/project/README.md"
if [ "${WORKOS_V2_SKIP_BUILD:-}" != 1 ]; then
 docker image inspect "${E2E_IMAGE:-workos-playwright:1.62.1}" >/dev/null 2>&1 || docker build --build-arg PLAYWRIGHT_VERSION=1.62.1 -t "${E2E_IMAGE:-workos-playwright:1.62.1}" -f deploy/e2e/Dockerfile deploy/e2e
 docker image inspect workos:dev >/dev/null 2>&1 || docker build -t workos:dev .
 docker image inspect workos-native-runtime:dev >/dev/null 2>&1 || docker build -t workos-native-runtime:dev -f tools/native-surface/runtime.Dockerfile .
 docker image inspect workos-workspace-runtime:dev >/dev/null 2>&1 || docker build -t workos-workspace-runtime:dev -f deploy/workspace.Dockerfile .
 docker run --rm --user "$WORKOS_V2_USER" -e HOME=/tmp -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/tmp/go-cache -e CGO_ENABLED=0 -v "$repo:/workspace" -v workos-go-cache:/go/pkg/mod -v "$repo/tmp/go-build-cache:/tmp/go-cache" -w /workspace golang:1.26.7-bookworm sh -c 'go build -o tmp/v2-completion-intake/bin/ ./cmd/workos-core ./cmd/runtime-host ./cmd/harness-host ./cmd/workos-gateway ./cmd/reliability-host ./cmd/indexer ./cmd/workosctl && go build -o tmp/v2-completion-intake/bin/workos-dev-fixture ./tests/devauth && go build -o tmp/v2-completion-intake/bin/deepseek-fixture ./tests/fixtures/deepseekapi'
 docker run --rm --user "$WORKOS_V2_USER" -e HOME=/tmp -e COREPACK_NPM_REGISTRY=https://registry.npmmirror.com -v "$repo:/workspace" -w /workspace node:24.19.0-bookworm-slim corepack pnpm --filter @workos/desktop-web build
fi
docker run -d --rm --name "$WORKOS_V2_NAMESPACE-db" -p "127.0.0.1:$WORKOS_V2_DATABASE_PORT:5432" -e POSTGRES_USER=workos -e POSTGRES_PASSWORD=workos -e POSTGRES_DB=workos pgvector/pgvector:pg18 >/dev/null
for attempt in $(seq 1 60); do docker exec "$WORKOS_V2_NAMESPACE-db" pg_isready -U workos >/dev/null 2>&1 && break; sleep 1; done
compose up -d
export WORKOS_V2_PROJECT_ID=$(python3 tools/v2-completion/prepare.py project)
export WORKOS_V2_WORKSPACE_MOUNTS="01999999-9999-7999-8999-000000000b01:$WORKOS_V2_PROJECT_ID:$WORKOS_V2_DIR/project"
compose up -d --no-deps --force-recreate runtime
printf '%s' workos-fixture-only-not-a-real-key | compose exec -T core workosctl credential put --consumer deepseek --purpose provider-api-key.v1 --label 'V2 fixture only' --idempotency-key v2-fixture > "$WORKOS_V2_DIR/credential-result.txt"
python3 tools/v2-completion/prepare.py bind
# Persist only fixture configuration for restart phases and local diagnostics.
python3 - "$WORKOS_V2_DIR/env" <<'PY'
import os,shlex,sys
with open(sys.argv[1],'a') as f:
 for key,value in os.environ.items():
  if key.startswith('WORKOS_V2_'): f.write('export '+key+'='+shlex.quote(value)+'\n')
PY
if [ "${WORKOS_V2_PREPARE_ONLY:-}" = 1 ]; then exit 0; fi
if [ "${WORKOS_NETWORK_AUTOMATION:-}" = 1 ]; then
 sh tools/network-continuity/test.sh
elif [ "${WORKOS_NATIVE_AUTOMATION:-}" = 1 ]; then
 sh tools/native-automation/test.sh
elif [ "${WORKOS_V2_REAL:-}" = 1 ]; then
 sh tools/real-model-acceptance/run.sh
else
 sh tools/v2-completion/test.sh
fi
