#!/bin/sh
# Real P3 gate: isolated PostgreSQL, current checkout binaries, all six hosts,
# real build volume/container, operator import, gateway Surface A/B/A and replay.
set -eu
umask 077
mode=${1:-bundle}
case "$mode" in bundle|legacy|faults) ;; *) exit 2 ;; esac
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/v2-p3-delivery.XXXXXX")
export WORKOS_P3_GATE_DIR="$task_dir"
export WORKOS_P3_GATE_USER="$(id -u):$(id -g)"
export WORKOS_P3_GATE_DOCKER_GID="$(stat -c %g /var/run/docker.sock)"
export WORKOS_P3_GATE_NAMESPACE="p3-$(basename "$task_dir" | tr '[:upper:].' '[:lower:]-')"
project="$WORKOS_P3_GATE_NAMESPACE"
tests_completed=0
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
  # KEEP leaves the isolated stack up for targeted diagnostics; the caller
  # owns tearing it down with the printed compose project.
  if [ "${WORKOS_V2_KEEP:-}" != 1 ]; then
    compose down --timeout 5 --volumes > "$task_dir/cleanup.log" 2>&1 || result=1
    docker ps -aq --filter "label=workos.runtime=$WORKOS_P3_GATE_NAMESPACE" | xargs -r docker rm -f >> "$task_dir/cleanup.log" 2>&1 || result=1
    # Reconciliation sentinels deliberately use a foreign runtime namespace.
    # Their separate test-ownership label permits exact cleanup on signals.
    docker ps -aq --filter "label=workos.acceptance=$WORKOS_P3_GATE_NAMESPACE" | xargs -r docker rm -f >> "$task_dir/cleanup.log" 2>&1 || result=1
    docker volume ls -q --filter "label=workos.runtime=$WORKOS_P3_GATE_NAMESPACE" | xargs -r docker volume rm >> "$task_dir/cleanup.log" 2>&1 || result=1
    docker network ls -q --filter "label=workos.runtime=$WORKOS_P3_GATE_NAMESPACE" | xargs -r docker network rm >> "$task_dir/cleanup.log" 2>&1 || result=1
  fi
  printf 'P3 fixture result=%s evidence=%s project=%s\n' "$result" "$task_dir" "$project"
  printf 'P3 gate evidence: %s\n' "$task_dir"
  if [ "$result" -eq 0 ] && [ "$tests_completed" -eq 1 ]; then printf 'v2-p3-delivery: PASS (%s profile)\n' "$mode"; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$task_dir/bin" "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run" "$task_dir/cli" "$task_dir/artifacts" "$task_dir/buildtest" "$task_dir/faults" "$task_dir/tests"
# Missing prerequisites fail the gate. No success path is allowed to skip them.
docker image inspect workos:dev golang:1.26.7-bookworm pgvector/pgvector:pg18 > /dev/null
if [ "$mode" = legacy ]; then
 docker image inspect workos-buildtest-runtime:dev > /dev/null
else
 docker image inspect workos-playwright:1.62.1 golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 > /dev/null
fi
docker run --rm --user "$WORKOS_P3_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e CGO_ENABLED=0 -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm sh -c 'go build -tags faultinject -o "$1/" ./cmd/... && go build -o "$1/workos-dev-fixture" ./tests/devauth' sh "${task_dir#"$repo"/}/bin" > "$task_dir/build.log" 2>&1
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
# PREPARE_ONLY stops after the six processes are ready (no tests): paired
# with KEEP it hands an isolated stack to targeted diagnostics, which must
# run their own tests and tear the project down afterwards.
if [ "${WORKOS_V2_PREPARE_ONLY:-}" = 1 ]; then exit 0; fi
run_test() {
 pattern=$1
 timeout=${2:-15m}
 package=${3:-./tests/integration}
 log="$task_dir/tests/$pattern.log"
 # Stream output live while keeping the container's real exit status: a plain
 # `docker | tee` pipeline would mask go test failures with tee's status 0,
 # and the subshell must not inherit errexit or it dies before recording it.
 ( set +e; docker run --rm --user "$WORKOS_P3_GATE_USER" --network host --group-add "$WORKOS_P3_GATE_DOCKER_GID" \
 -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct \
 -e WORKOS_REPAIR_BUILDTEST_DIR="$task_dir" \
 -e WORKOS_REPAIR_BUILDTEST_EXECUTION_URL="https://127.0.0.1:$WORKOS_P3_GATE_EXECUTION_PORT" \
 -e WORKOS_P3_GATE_DIR="$task_dir" -e WORKOS_P3_GATE_NAMESPACE \
 -e WORKOS_REPAIR_BUILDTEST_GATEWAY_URL="http://127.0.0.1:$WORKOS_P3_GATE_GATEWAY_PORT" \
 -e WORKOS_REPAIR_BUILDTEST_CORE_URL="http://127.0.0.1:$WORKOS_P3_GATE_CORE_PORT" \
 -e WORKOS_REPAIR_BUILDTEST_RUNTIME_URL="http://127.0.0.1:$WORKOS_P3_GATE_RUNTIME_PORT" \
 -e WORKOS_REPAIR_BUILDTEST_DATABASE_URL="$WORKOS_P3_GATE_DATABASE_URL" \
 -e WORKOS_P3_RELIABILITY_URL="http://127.0.0.1:$WORKOS_P3_GATE_RELIABILITY_PORT" \
 -e WORKOS_FAULT_DIR="$task_dir/faults" \
 -e WORKOS_RUNTIME_DOCKER_SOCKET=/var/run/docker.sock \
 -e WORKOS_TEST_DOCKER_BUILD=1 -e WORKOS_RUNTIME_CONTAINER_NAMESPACE="$WORKOS_P3_GATE_NAMESPACE" \
 -v /var/run/docker.sock:/var/run/docker.sock \
 -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -v "$task_dir:$task_dir" -w /workspace \
 golang:1.26.7-bookworm go test -tags='integration repairbuildtest p3delivery' -count=1 -timeout "$timeout" -run "^$pattern$" -v "$package" 2>&1; \
   printf '%s\n' "$?" > "$task_dir/tests/$pattern.status" ) | tee "$log"
 status=$(cat "$task_dir/tests/$pattern.status" 2>/dev/null || printf '1')
 if [ "$status" -ne 0 ]; then exit "$status"; fi
 # Zero matched tests must fail the gate instead of silently passing.
 grep -q -- "--- PASS: $pattern " "$log" || { printf 'v2-p3-delivery: no passing test matched %s\n' "$pattern" >&2; exit 1; }
 if grep -q -- '--- SKIP:' "$log"; then printf 'v2-p3-delivery: required test skipped\n' >&2; exit 1; fi
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
 tests_completed=1
 exit 0
fi
run_e2e() {
 spec=$1
 log="$task_dir/tests/e2e-$(basename "$spec" .spec.ts).log"
 ( set +e; docker run --rm --network host --user "$WORKOS_P3_GATE_USER" -e HOME=/tmp \
 -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_P3_GATE_GATEWAY_PORT" -e WORKOS_P3_GATE_DIR="$task_dir" \
 -v "$repo:/workspace" -v "$task_dir:$task_dir" -w /workspace/apps/desktop-web \
 workos-playwright:1.62.1 node node_modules/@playwright/test/cli.js test "$spec" --workers=1 2>&1; \
   printf '%s\n' "$?" > "$task_dir/tests/e2e-$(basename "$spec" .spec.ts).status" ) | tee "$log"
 status=$(cat "$task_dir/tests/e2e-$(basename "$spec" .spec.ts).status" 2>/dev/null || printf '1')
 if [ "$status" -ne 0 ]; then exit "$status"; fi
 # A spec whose tests were all skipped or matched nothing must not pass the gate.
 grep -Eq '[1-9][0-9]* passed' "$log" || { printf 'v2-p3-delivery: no passed playwright tests in %s\n' "$spec" >&2; exit 1; }
 if grep -Eq '[1-9][0-9]* skipped' "$log"; then printf 'v2-p3-delivery: required playwright test skipped\n' >&2; exit 1; fi
}
run_test TestP3RealDelivery
run_test TestRealEngineFailureMatrix 5m ./internal/runtime/buildtest/adapters/dockerbuild
run_test TestP3Closeout 40m
run_test TestP3CloseoutWindows 60m
run_test TestP3CloseoutMatrix 60m
run_test TestP3FinalMatrix 60m
run_test TestP3FinalAuthority 40m
run_test TestP3FinalRuntime 40m
run_e2e e2e/p3-delivery.spec.ts
run_e2e e2e/p3-closeout.spec.ts
compose restart core runtime reliability > "$task_dir/restart.log" 2>&1
wait_ready
run_test TestP3RestartReplay
tests_completed=1
if [ "$mode" = faults ]; then
  printf 'v2-p3-delivery faults profile covered TestP3Closeout\n'
fi
