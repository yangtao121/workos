#!/bin/sh
set -eu
umask 077
test_spec=${1:-generic-cli.spec.ts}
case "$test_spec" in generic-cli.spec.ts|app-build-inputs.spec.ts) ;; *) exit 2 ;; esac
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/generic-cli.XXXXXX")
export WORKOS_CLI_GATE_DIR="$task_dir"
export WORKOS_CLI_GATE_USER="$(id -u):$(id -g)"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_cli_$stamp"
project="workos-cli-$stamp"
export WORKOS_CLI_GATE_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" <<'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','HARNESS','EXECUTION']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_CLI_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$project" -f tools/generic-cli/compose.yaml "$@"; }
owned_database=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    printf 'generic-cli diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  rm -rf "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run" "$task_dir/cli"
  if [ "$result" -eq 0 ]; then printf '%s: PASS (isolated Core/Harness fixture)\n' "$test_spec"; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run" "$task_dir/cli"
cat > "$task_dir/cli/config.yaml" <<'YAML'
harness:
  generic_cli:
    enabled: true
    executable: /fixture/cli/run
    timeout: 10s
YAML
cat > "$task_dir/cli/run" <<'SH'
#!/bin/sh
exec /usr/local/bin/generic-harness-fixture
SH
chmod 700 "$task_dir/cli/run"
docker compose build workos-core
docker compose exec -T postgres createdb -U workos "$database"
owned_database=true
compose up -d
# Poll the public API, never depend on a fixed startup delay.
wait_ready() {
  for attempt in $(seq 1 60); do
    if curl --noproxy '*' --silent --fail --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_CLI_GATE_GATEWAY_PORT/workos.harness.v1.HarnessCatalogService/GetHarnessCatalog" > "$task_dir/catalog.json"; then return 0; fi
    sleep 1
  done
  return 1
}
run_test() {
  docker run --rm --network host --user "$WORKOS_CLI_GATE_USER" -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_CLI_GATE_GATEWAY_PORT" -e WORKOS_CLI_GATE_EXECUTABLE="/workspace/${task_dir#"$repo/"}/cli/run" -e WORKOS_APP_SOURCE_CORE_URL="http://127.0.0.1:$WORKOS_CLI_GATE_CORE_PORT" -e WORKOS_APP_SOURCE_STATE="/workspace/${task_dir#"$repo/"}/source-state.json" -e WORKOS_E2E_OUTPUT_DIR="/workspace/${task_dir#"$repo/"}/results" -v "$repo:/workspace" -w /workspace/apps/desktop-web "${E2E_IMAGE:-workos-playwright:1.62.1}" node node_modules/@playwright/test/cli.js test "$test_spec" --workers=1 "$@"
}
wait_ready
if [ "$test_spec" = app-build-inputs.spec.ts ]; then
  run_test --grep 'source seed'
  compose restart core
  wait_ready
  run_test --grep 'source restore'
else
  run_test
fi
