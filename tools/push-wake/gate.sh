#!/bin/sh
# Only a fresh database, temp bind mounts, and a unique Compose project are owned here.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/push-wake.XXXXXX")
export WORKOS_PUSH_GATE_DIR="$task_dir"
export WORKOS_PUSH_GATE_USER="$(id -u):$(id -g)"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_push_$stamp"
project="workos-push-$stamp"
export WORKOS_PUSH_GATE_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" <<'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','HARNESS','RUNTIME','RELIABILITY','INDEXER','EXECUTION','RELAY']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_PUSH_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$project" -f tools/push-wake/compose.yaml "$@"; }
go_run() {
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace "${GO_IMAGE:-golang:1.26.7-bookworm}" "$@"
}
relay_url="https://127.0.0.1:$WORKOS_PUSH_GATE_RELAY_PORT"
relay() { curl --noproxy '*' --silent --show-error --fail --max-time 5 --cacert "$task_dir/tls/ca.crt" "$relay_url/$1"; }
owned_database=false
browser_pid=""
cleanup() {
  result=$?
  trap - EXIT INT TERM
  docker stop -t 2 "$project-browser" >/dev/null 2>&1 || true
  if [ -n "$browser_pid" ]; then wait "$browser_pid" || true; fi
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    if [ -f "$task_dir/tls/ca.crt" ]; then
      relay state > "$task_dir/relay-final.json" 2>/dev/null || true
    fi
    printf 'push-wake diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  rm -rf "$task_dir/core" "$task_dir/relay" "$task_dir/public" "$task_dir/tls" "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/push-relay" "$task_dir/run"
  if [ "$result" -eq 0 ]; then printf 'test-push-wake: PASS (isolated Core retry → encrypted TLS receiver → Chromium wake → Core catch-up)\n'; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# No existing service or subscription is reconfigured. Build the shared binary image only.
docker compose build workos-core
mkdir -p "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/tls" "$task_dir/run"
go_run go run ./tests/lanpairing/gencert -out "/workspace/${task_dir#"$repo/"}/tls"
go_run go build -o "/workspace/${task_dir#"$repo/"}/push-relay" ./tests/fixtures/pushrelay
"$task_dir/push-relay" -init -dir "$task_dir" -address "127.0.0.1:$WORKOS_PUSH_GATE_RELAY_PORT"
docker compose exec -T postgres createdb -U workos -T template0 "$database"
owned_database=true
compose up -d
for port in "$WORKOS_PUSH_GATE_GATEWAY_PORT" "$WORKOS_PUSH_GATE_CORE_PORT" "$WORKOS_PUSH_GATE_HARNESS_PORT"; do
  i=0
  until curl --noproxy '*' -sf --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null; do i=$((i+1)); [ "$i" -lt 60 ] || exit 1; sleep 1; done
done
i=0
until relay state > "$task_dir/relay-state.json"; do i=$((i+1)); [ "$i" -lt 30 ] || exit 1; sleep 1; done
docker run --rm --name "$project-browser" --network host --user "$(id -u):$(id -g)" -e HOME=/tmp -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_PUSH_GATE_GATEWAY_PORT" -e WORKOS_PUSH_WAKE_RELAY="$relay_url" -e WORKOS_PUSH_WAKE_DIR="/workspace/${task_dir#"$repo/"}" -e WORKOS_E2E_OUTPUT_DIR="/workspace/${task_dir#"$repo/"}/results" -v "$repo:/workspace" -w /workspace/apps/desktop-web "${E2E_IMAGE:-workos-playwright:1.62.1}" node node_modules/@playwright/test/cli.js test push-wake.spec.ts --workers=1 > "$task_dir/browser.log" 2>&1 &
browser_pid=$!
i=0
while :; do
  kill -0 "$browser_pid" || { cat "$task_dir/browser.log"; exit 1; }
  relay state > "$task_dir/relay-state.json"
  if [ -f "$task_dir/browser-ready" ] && python3 - "$task_dir/relay-state.json" <<'PY'
import json,sys
state=json.load(open(sys.argv[1])); raise SystemExit(0 if state['deliveries'] and not state['accepted'] and state['invalid']==0 else 1)
PY
  then break; fi
  i=$((i+1)); [ "$i" -lt 90 ] || { cat "$task_dir/browser.log"; exit 1; }; sleep 1
done
compose restart core
curl --noproxy '*' --silent --show-error --fail --max-time 5 --cacert "$task_dir/tls/ca.crt" -X POST "$relay_url/allow" >/dev/null
printf 'restarted\n' > "$task_dir/core-restarted"
if wait "$browser_pid"; then browser_pid=""; else browser_pid=""; cat "$task_dir/browser.log"; exit 1; fi
cat "$task_dir/browser.log"
relay state > "$task_dir/relay-state.json"
docker compose exec -T postgres psql -U workos -d "$database" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) = 2 AND bool_and(state = 'delivered') AND max(attempts) >= 2 FROM workos_core.push_deliveries" > "$task_dir/outbox-verified"
[ "$(cat "$task_dir/outbox-verified")" = t ]
