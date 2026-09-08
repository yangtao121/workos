#!/bin/sh
# The Repair Build/Test chain gate (ADR-0026): real Core/Harness/Runtime/
# Reliability/Gateway/PostgreSQL with the toolchain-image runtime-host, the
# real CLI fixture producing candidates, real go build/test execution, staged
# registration, canary and publication. Covers success, test failure, build
# failure, restart recovery, duplicate submission and user version change.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/repair-buildtest.XXXXXX")
export WORKOS_BUILDTEST_GATE_DIR="$task_dir"
export WORKOS_BUILDTEST_GATE_USER="$(id -u):$(id -g)"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_buildtest_$stamp"
project="workos-buildtest-$stamp"
export WORKOS_BUILDTEST_GATE_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" << 'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','HARNESS','EXECUTION','RUNTIME','RELIABILITY']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_BUILDTEST_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$project" -f tools/repair-buildtest/compose.yaml "$@"; }
owned_database=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    printf 'repair-buildtest diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ] && [ "$result" -eq 0 ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  # The runtime container writes mount-scoped state as its own uid; remove
  # it with a helper container instead of failing the host-side cleanup.
  docker run --rm -v "$task_dir:/gate" busybox:latest sh -c 'rm -rf /gate/core-execution /gate/harness-execution /gate/vault /gate/run' >/dev/null 2>&1 || true
  if [ "$result" -eq 0 ]; then printf 'repair-buildtest: PASS (isolated six-process chain)\n'; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run" "$task_dir/cli"
printf '# gate-local fake engine scenario (ADR-0016)\n' > "$task_dir/scenario.conf"
cat > "$task_dir/cli/config.yaml" << 'YAML'
harness:
  generic_cli:
    enabled: true
    executable: /fixture/cli/run
    timeout: 60s
YAML
cat > "$task_dir/cli/run" << 'SH'
#!/bin/sh
exec /usr/local/bin/generic-harness-fixture
SH
chmod 700 "$task_dir/cli/run"
docker compose build workos-core
compose build runtime
docker compose exec -T postgres createdb -U workos "$database"
owned_database=true
compose up -d
wait_ready() {
  for attempt in $(seq 1 90); do
    if curl --noproxy '*' --silent --fail --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_BUILDTEST_GATE_GATEWAY_PORT/workos.harness.v1.HarnessCatalogService/GetHarnessCatalog" > "$task_dir/catalog.json"; then return 0; fi
    sleep 1
  done
  return 1
}
wait_ready
run_test() {
  docker run --rm --network host --user "$WORKOS_BUILDTEST_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e WORKOS_REPAIR_BUILDTEST_DIR="/workspace/${task_dir#"$repo"/}" -e WORKOS_REPAIR_BUILDTEST_GATEWAY_URL="http://127.0.0.1:$WORKOS_BUILDTEST_GATE_GATEWAY_PORT" -e WORKOS_REPAIR_BUILDTEST_CORE_URL="http://127.0.0.1:$WORKOS_BUILDTEST_GATE_CORE_PORT" -e WORKOS_REPAIR_BUILDTEST_RUNTIME_URL="http://127.0.0.1:$WORKOS_BUILDTEST_GATE_RUNTIME_PORT" -e WORKOS_REPAIR_BUILDTEST_EXECUTION_URL="https://127.0.0.1:$WORKOS_BUILDTEST_GATE_EXECUTION_PORT" -e WORKOS_REPAIR_BUILDTEST_DATABASE_URL="$WORKOS_BUILDTEST_GATE_DATABASE_URL" -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm go test -tags='integration repairbuildtest' -count=1 -run "^$1$" -v ./tests/integration
}
run_test 'TestRepairBuildTestChain'
# Restart recovery: the seed test leaves a running slow-build job behind;
# restarting runtime-host proves the durable ledger plus lease takeover.
run_test 'TestRepairBuildTestRestartSeed'
compose restart runtime
run_test 'TestRepairBuildTestRestartRestore'
# The deployment fault matrix seeds its verified candidates first, then the
# reliability loop stops so the scripted fault driver owns the ledger
# exclusively (the real loop would race the same rows to promotion).
run_test 'TestRepairBuildTestMatrixSeed'
compose stop reliability
run_test 'TestRepairBuildTestDeploymentMatrix'
