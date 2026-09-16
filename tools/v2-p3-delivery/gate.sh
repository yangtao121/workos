#!/bin/sh
# Real P3 gate: isolated PostgreSQL, current checkout binaries, all six hosts,
# real build volume/container, operator import, gateway Surface A/B/A and replay.
set -eu
umask 077
mode=${1:-bundle}
case "$mode" in bundle|legacy) ;; *) exit 2 ;; esac
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/v2-p3-delivery.XXXXXX")
export WORKOS_P3_GATE_DIR="$task_dir"
export WORKOS_P3_GATE_USER="$(id -u):$(id -g)"
export WORKOS_P3_GATE_DOCKER_GID="$(stat -c %g /var/run/docker.sock)"
export WORKOS_P3_GATE_NAMESPACE="p3-$(basename "$task_dir" | tr '[:upper:].' '[:lower:]-')"
project="$WORKOS_P3_GATE_NAMESPACE"
python3 - "$task_dir/ports.env" <<'PY'
import socket,sys
sockets=[]; lines=[]
for name in ['DATABASE','GATEWAY','CORE','HARNESS','EXECUTION','RUNTIME','RELIABILITY','INDEXER']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    lines.append(f'export WORKOS_P3_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(lines)+'\n')
PY
. "$task_dir/ports.env"
export WORKOS_P3_GATE_DATABASE_URL="postgres://workos:gate-only-fixture@127.0.0.1:$WORKOS_P3_GATE_DATABASE_PORT/workos?sslmode=disable"
compose() {
 if [ "$mode" = legacy ]; then
  docker compose -p "$project" -f tools/v2-p3-delivery/compose.yaml -f "$task_dir/legacy.yaml" "$@"
 else
  docker compose -p "$project" -f tools/v2-p3-delivery/compose.yaml "$@"
 fi
}
cleanup() {
  result=$?
  trap - EXIT INT TERM
  compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
  compose down --timeout 5 --volumes > "$task_dir/cleanup.log" 2>&1 || result=1
  docker ps -aq --filter "label=workos.runtime=$WORKOS_P3_GATE_NAMESPACE" | xargs -r docker rm -f >> "$task_dir/cleanup.log" 2>&1 || result=1
  docker volume ls -q --filter "label=workos.runtime=$WORKOS_P3_GATE_NAMESPACE" | xargs -r docker volume rm >> "$task_dir/cleanup.log" 2>&1 || result=1
  docker network ls -q --filter "label=workos.runtime=$WORKOS_P3_GATE_NAMESPACE" | xargs -r docker network rm >> "$task_dir/cleanup.log" 2>&1 || result=1
  printf 'P3 gate evidence: %s\n' "$task_dir"
  if [ "$result" -eq 0 ]; then printf 'v2-p3-delivery: PASS (%s profile)\n' "$mode"; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$task_dir/bin" "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run" "$task_dir/cli" "$task_dir/artifacts" "$task_dir/buildtest"
# Missing prerequisites fail the gate. No success path is allowed to skip them.
docker image inspect workos:dev golang:1.26.7-bookworm pgvector/pgvector:pg18 > /dev/null
docker run --rm --user "$WORKOS_P3_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e CGO_ENABLED=0 -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm sh -c 'go build -o "$1/" ./cmd/... && go build -o "$1/workos-dev-fixture" ./tests/devauth' sh "${task_dir#"$repo"/}/bin" > "$task_dir/build.log" 2>&1
cat > "$task_dir/cli/config.yaml" <<'YAML'
harness:
  generic_cli:
    enabled: true
    executable: /fixture/cli/run
    timeout: 60s
YAML
cat > "$task_dir/cli/run" <<'SH'
#!/bin/sh
exec /gate-bin/generic-harness-fixture
SH
chmod 700 "$task_dir/cli/run"
if [ "$mode" = legacy ]; then
 cat > "$task_dir/legacy.yaml" <<'YAML'
services:
  runtime:
    image: workos-buildtest-runtime:dev
    environment:
      PATH: /gate-bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
      WORKOS_RUNTIME_WORKLOAD_ENGINE: fake-fixture
      WORKOS_RUNTIME_BUILDTEST_ENGINE: process
      WORKOS_RUNTIME_ARTIFACT_ROOT: ""
      WORKOS_RUNTIME_ADMIN_SOCKET: ""
      WORKOS_RUNTIME_FIXTURE_SCENARIO_FILE: /run/workos/fake-engine/scenario.conf
      WORKOS_RUNTIME_BUILDTEST_TIMEOUT: 2m
      WORKOS_RUNTIME_BUILDTEST_PROCESS_LIMIT: "65534"
    volumes:
      - ${WORKOS_P3_GATE_DIR:?}/scenario.conf:/run/workos/fake-engine/scenario.conf
YAML
 printf '# gate-owned fake engine scenarios\n' > "$task_dir/scenario.conf"
fi
compose up -d > "$task_dir/up.log" 2>&1
wait_ready() {
  for attempt in $(seq 1 90); do
    if curl --noproxy '*' --silent --fail --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_P3_GATE_GATEWAY_PORT/workos.harness.v1.HarnessCatalogService/GetHarnessCatalog" > "$task_dir/catalog.json"; then return 0; fi
    sleep 1
  done
  return 1
}
wait_ready
run_test() {
 docker run --rm --user "$WORKOS_P3_GATE_USER" --network host --group-add "$WORKOS_P3_GATE_DOCKER_GID" \
 -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct \
 -e WORKOS_REPAIR_BUILDTEST_DIR="$task_dir" \
 -e WORKOS_REPAIR_BUILDTEST_EXECUTION_URL="https://127.0.0.1:$WORKOS_P3_GATE_EXECUTION_PORT" \
 -e WORKOS_P3_GATE_DIR="$task_dir" -e WORKOS_P3_GATE_NAMESPACE \
 -e WORKOS_REPAIR_BUILDTEST_GATEWAY_URL="http://127.0.0.1:$WORKOS_P3_GATE_GATEWAY_PORT" \
 -e WORKOS_REPAIR_BUILDTEST_CORE_URL="http://127.0.0.1:$WORKOS_P3_GATE_CORE_PORT" \
 -e WORKOS_REPAIR_BUILDTEST_RUNTIME_URL="http://127.0.0.1:$WORKOS_P3_GATE_RUNTIME_PORT" \
 -e WORKOS_REPAIR_BUILDTEST_DATABASE_URL="$WORKOS_P3_GATE_DATABASE_URL" \
 -e WORKOS_P3_RELIABILITY_URL="http://127.0.0.1:$WORKOS_P3_GATE_RELIABILITY_PORT" \
 -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -v "$task_dir:$task_dir" -w /workspace \
 golang:1.26.7-bookworm go test -tags='integration repairbuildtest p3delivery' -count=1 -timeout 15m -run "^$1$" -v ./tests/integration
}
if [ "$mode" = legacy ]; then
 run_test TestRepairBuildTestChain
 run_test TestRepairBuildTestRestartSeed
 compose restart runtime
 run_test TestRepairBuildTestRestartRestore
 run_test TestRepairRecoveryFallback
 run_test TestRepairRecoveryAwaitingManual
 run_test TestRepairBuildTestMatrixSeed
 compose stop reliability
 run_test TestRepairBuildTestDeploymentMatrix
 exit 0
fi
run_test TestP3RealDelivery
docker run --rm --network host --user "$WORKOS_P3_GATE_USER" -e HOME=/tmp \
 -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_P3_GATE_GATEWAY_PORT" -e WORKOS_P3_GATE_DIR="$task_dir" \
 -v "$repo:/workspace" -v "$task_dir:$task_dir" -w /workspace/apps/desktop-web \
 workos-playwright:1.62.1 node node_modules/@playwright/test/cli.js test e2e/p3-delivery.spec.ts --workers=1
compose restart core runtime reliability > "$task_dir/restart.log" 2>&1
wait_ready
run_test TestP3RestartReplay
