#!/bin/sh
# The V2 development journey gate (ADR-0030 B05/B08): the real desktop Agent
# Sessions window against a real, isolated stack whose provider is the
# deterministic fake and whose runtime serves real supervised shells. Proves
# the persistent-session loop end to end (create -> submit -> fake-provider
# run -> refresh/resume -> second input), the busy-state UI contract
# (running cancel action, queued inputs, cancel is not close), and — when
# WORKOS_CAPTURE_DIR is exported — records the UI visual evidence for the
# agent session window, the Terminal/Native stop controls, and the Running
# apps list at the three standard viewports. No real model is ever called.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/v2-journey.XXXXXX")
export WORKOS_V2JOURNEY_DIR="$task_dir"
export WORKOS_V2JOURNEY_USER="$(id -u):$(id -g)"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_v2journey_$stamp"
project="workos-v2journey-$stamp"
export WORKOS_V2JOURNEY_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" << 'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','HARNESS','RUNTIME','EXECUTION']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_V2JOURNEY_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$project" -f tools/v2-development-journey/compose.yaml "$@"; }
# The gate reuses the shared dev postgres instance (like the terminal and
# surface gates) but owns a throwaway database inside it.
docker compose up -d --build postgres
i=0
until docker compose exec -T postgres pg_isready -U workos >/dev/null 2>&1; do
  i=$((i+1)); [ "$i" -le 60 ] || { echo 'postgres readiness timed out' >&2; exit 1; }; sleep 1
done
owned_database=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    printf 'v2-development-journey diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ] && [ "$result" -eq 0 ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  docker run --rm -v "$task_dir:/gate" busybox:latest sh -c 'rm -rf /gate/core-execution /gate/harness-execution /gate/vault /gate/run' >/dev/null 2>&1 || true
  if [ "$result" -eq 0 ]; then
    printf 'v2-development-journey: PASS (agent sessions window, fake-provider loop, resume, busy-state contract)\n'
    rm -rf "$task_dir"
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$task_dir/core-execution" "$task_dir/harness-execution" "$task_dir/vault" "$task_dir/run"
docker compose build workos-core
docker compose exec -T postgres createdb -U workos "$database"
owned_database=true
compose up -d
for attempt in $(seq 1 90); do
  code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_V2JOURNEY_GATEWAY_PORT/workos.agent.v1.AgentSessionService/GetSession" 2>/dev/null || true)
  case "$code" in 200|400|401|403|404|409|422|429|500|501|503) break;; esac
  sleep 1
done
# The desktop bundle is baked into the gateway image (WORKOS_STATIC_DIR).
capture_dir=""
if [ -n "${WORKOS_CAPTURE_DIR:-}" ]; then
  capture_dir="${WORKOS_CAPTURE_DIR#"$repo"}"
  capture_dir="/workspace${capture_dir}"
  mkdir -p "${WORKOS_CAPTURE_DIR:?}"
fi
docker run --rm --network host --user "$WORKOS_V2JOURNEY_USER" \
  -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
  -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_V2JOURNEY_GATEWAY_PORT" \
  -e WORKOS_E2E_OUTPUT_DIR="/tmp/workos-playwright-results" \
  -e WORKOS_AGENT_SESSIONS_E2E=true \
  -e WORKOS_CAPTURE_DIR="$capture_dir" \
  -v "$repo:/workspace" -w /workspace/apps/desktop-web \
  "${E2E_IMAGE:-workos-playwright:1.62.1}" node node_modules/@playwright/test/cli.js test agent-sessions.spec.ts --workers=1
