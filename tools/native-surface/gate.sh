#!/bin/sh
# The virtual-display native runner gate (ADR-0029): real Xvfb displays with
# ffmpeg x11grab/VP8 capture and loopback WebRTC, driven end to end by a Go
# peer and then by the real desktop Native window.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/native-surface.XXXXXX")
export WORKOS_NATIVE_GATE_DIR="$task_dir"
export WORKOS_NATIVE_GATE_USER="$(id -u):$(id -g)"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_native_$stamp"
project="workos-native-$stamp"
export WORKOS_NATIVE_GATE_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" << 'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','RUNTIME','EXECUTION']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_NATIVE_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$project" -f tools/native-surface/compose.yaml "$@"; }
owned_database=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    printf 'native-surface diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ] && [ "$result" -eq 0 ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  docker run --rm -v "$task_dir:/gate" busybox:latest sh -c 'rm -rf /gate/core-execution /gate/harness-execution /gate/vault /gate/run' >/dev/null 2>&1 || true
  if [ "$result" -eq 0 ]; then printf 'native-surface: PASS (real xvfb displays over loopback webrtc)\n'; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run"
docker compose build workos-core
compose build runtime
docker compose exec -T postgres createdb -U workos "$database"
owned_database=true
compose up -d
wait_ready() {
  for attempt in $(seq 1 90); do
    code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_NATIVE_GATE_CORE_PORT/workos.project.v1.ProjectService/GetProject" 2>/dev/null || true)
    case "$code" in 200|400|401|403|404|409|422|429|500|501|503) return 0;; esac
    sleep 1
  done
  return 1
}
wait_ready
run_test() {
  docker run --rm --network host --user "$WORKOS_NATIVE_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e WORKOS_NATIVE_GATE_DIR="/workspace/${task_dir#"$repo"/}" -e WORKOS_NATIVE_GATE_GATEWAY_URL="http://127.0.0.1:$WORKOS_NATIVE_GATE_GATEWAY_PORT" -e WORKOS_NATIVE_GATE_CORE_URL="http://127.0.0.1:$WORKOS_NATIVE_GATE_CORE_PORT" -e WORKOS_NATIVE_GATE_RUNTIME_URL="http://127.0.0.1:$WORKOS_NATIVE_GATE_RUNTIME_PORT" -e WORKOS_NATIVE_GATE_DATABASE_URL="$WORKOS_NATIVE_GATE_DATABASE_URL" -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm go test -tags='integration nativegate' -count=1 -run "^$1$" -v ./tests/integration
}
run_test 'TestNativeSessions'
# Desktop consumer: the Native system window renders the WebRTC video and
# types over the data channel.
docker run --rm --network host --user "$WORKOS_NATIVE_GATE_USER" \
  -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
  -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_NATIVE_GATE_GATEWAY_PORT" \
  -e WORKOS_E2E_OUTPUT_DIR="/tmp/workos-playwright-results" \
  -e WORKOS_NATIVE_E2E=true \
  -v "$repo:/workspace" -w /workspace/apps/desktop-web \
  "${E2E_IMAGE:-workos-playwright:1.62.1}" node node_modules/@playwright/test/cli.js test native-surface-desktop.spec.ts --workers=1
