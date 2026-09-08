#!/bin/sh
# The Remote Browser Pool gate (ADR-0027): a real Chromium worker per
# session inside the Playwright-image runtime-host. Proves real rendering,
# idempotent sessions, drift aborts, navigation, crash recovery with
# bounded restarts, close reaping, the session cap and scheme refusal.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/browser-pool.XXXXXX")
export WORKOS_BROWSERPOOL_GATE_DIR="$task_dir"
export WORKOS_BROWSERPOOL_GATE_USER="$(id -u):$(id -g)"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_browserpool_$stamp"
project="workos-browserpool-$stamp"
export WORKOS_BROWSERPOOL_GATE_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" << 'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','RUNTIME','EXECUTION']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_BROWSERPOOL_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$project" -f tools/browser-pool/compose.yaml "$@"; }
owned_database=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    printf 'browser-pool diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ] && [ "$result" -eq 0 ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  docker run --rm -v "$task_dir:/gate" busybox:latest sh -c 'rm -rf /gate/core-execution /gate/harness-execution /gate/vault /gate/run' >/dev/null 2>&1 || true
  if [ "$result" -eq 0 ]; then printf 'browser-pool: PASS (real chromium workers)\n'; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run"
printf '# gate scratch\n' > "$task_dir/.keep"
docker compose build workos-core
compose build runtime
docker compose exec -T postgres createdb -U workos "$database"
owned_database=true
compose up -d
wait_ready() {
  for attempt in $(seq 1 90); do
    code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_BROWSERPOOL_GATE_GATEWAY_PORT/workos.project.v1.ProjectService/GetProject" 2>/dev/null || true)
    case "$code" in 200|400|401|403|404|409|422|429|500|501|503) return 0;; esac
    sleep 1
  done
  return 1
}
wait_ready
run_test() {
  docker run --rm --network host --user "$WORKOS_BROWSERPOOL_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e WORKOS_BROWSERPOOL_GATE_DIR="/workspace/${task_dir#"$repo"/}" -e WORKOS_BROWSERPOOL_GATE_GATEWAY_URL="http://127.0.0.1:$WORKOS_BROWSERPOOL_GATE_GATEWAY_PORT" -e WORKOS_BROWSERPOOL_GATE_RUNTIME_URL="http://127.0.0.1:$WORKOS_BROWSERPOOL_GATE_RUNTIME_PORT" -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm go test -tags='integration browserpool' -count=1 -run "^$1$" -v ./tests/integration
}
run_test 'TestBrowserPoolRealChromium'
# Crash injection: seed a live session, kill its real Chromium child inside
# the runtime container, then verify bounded recovery.
run_test 'TestBrowserPoolCrashSeed'
found=$(docker exec "${project}-runtime-1" sh -c 'for p in /proc/[0-9]*; do cmd=$(tr "\0" " " < "$p/cmdline" 2>/dev/null); case "$cmd" in "/ms-playwright/chromium"*) basename "$p";; esac; done' 2>/dev/null || true)
if [ -z "$found" ]; then
  echo "browser-pool: no live chromium worker found for crash injection" >&2
  exit 1
fi
# Kill every chromium process in the worker's group: chrome is multi-process,
# and a partial kill leaves rendering alive.
for pid in $found; do
  docker exec "${project}-runtime-1" sh -c "kill -9 $pid" 2>/dev/null || true
done
run_test 'TestBrowserPoolCrashRestore'
