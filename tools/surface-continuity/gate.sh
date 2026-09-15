#!/bin/sh
# The surface continuity gate (ADR-0031, B06/B07 + B09 A13): program
# execution and device access are separate lifecycles on a real stack. One
# real PTY shell survives detach with output accumulating, an explicit
# takeover switches the single controller server-side, the PTY data path
# enforces the control lease for two independent device identities, the
# bounded sweep expires elapsed attachments, stop deterministically reaps
# the program, and a runtime-host stop/start proves dead sessions are
# reconciled to honest terminal states instead of staying "running".
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/surface-continuity.XXXXXX")
export WORKOS_SURFACE_GATE_DIR="$task_dir"
export WORKOS_SURFACE_GATE_USER="$(id -u):$(id -g)"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_surface_$stamp"
project="workos-surface-$stamp"
export WORKOS_SURFACE_GATE_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" << 'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','RUNTIME','EXECUTION']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_SURFACE_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$project" -f tools/surface-continuity/compose.yaml "$@"; }
owned_database=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    printf 'surface-continuity diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ] && [ "$result" -eq 0 ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  docker run --rm -v "$task_dir:/gate" busybox:latest sh -c 'rm -rf /gate/core-execution /gate/harness-execution /gate/vault /gate/run' >/dev/null 2>&1 || true
  if [ "$result" -eq 0 ]; then printf 'surface-continuity: PASS (detach keeps programs, single controller enforced, runtime restart reconciles dead workloads)\n'; fi
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
    code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_SURFACE_GATE_CORE_PORT/workos.project.v1.ProjectService/GetProject" 2>/dev/null || true)
    case "$code" in 200|400|401|403|404|409|422|429|500|501|503) return 0;; esac
    sleep 1
  done
  return 1
}
wait_ready
# Control-lease state machine against the real scratch postgres (migration
# 059): attach/first-control, renewal, atomic takeover, detach, sweeps, and
# concurrent-takeover convergence at the repository level.
docker run --rm --network host --user "$WORKOS_SURFACE_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e WORKOS_SURFACE_CONTINUITY_TEST_DATABASE_URL="$WORKOS_SURFACE_GATE_DATABASE_URL" -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm go test -count=1 -run 'TestContinuityStore' ./internal/runtime/surface/adapters/postgres/
# End-to-end chain through the gateway and the runtime listener.
if ! docker run --rm --network host --user "$WORKOS_SURFACE_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e WORKOS_SURFACE_GATE_GATEWAY_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_GATEWAY_PORT" -e WORKOS_SURFACE_GATE_CORE_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_CORE_PORT" -e WORKOS_SURFACE_GATE_RUNTIME_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_RUNTIME_PORT" -e WORKOS_SURFACE_GATE_DATABASE_URL="$WORKOS_SURFACE_GATE_DATABASE_URL" -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm go test -tags='integration continuitygate' -count=1 -run "^TestSurfaceContinuity$" -v ./tests/integration > "$task_dir/phase1.log" 2>&1; then
  cat "$task_dir/phase1.log"
  exit 1
fi
cat "$task_dir/phase1.log"

# A13 restart phase: a live PTY session is created and pinned, then the
# runtime-host container is stopped and started again. Its child processes
# die with the container; the startup reconcile must finalize the durable row
# so ListProjectSurfaces never reports the dead program as running.
if ! docker run --rm --network host --user "$WORKOS_SURFACE_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e WORKOS_SURFACE_GATE_GATEWAY_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_GATEWAY_PORT" -e WORKOS_SURFACE_GATE_CORE_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_CORE_PORT" -e WORKOS_SURFACE_GATE_RUNTIME_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_RUNTIME_PORT" -e WORKOS_SURFACE_GATE_DATABASE_URL="$WORKOS_SURFACE_GATE_DATABASE_URL" -e WORKOS_SURFACE_GATE_RESTART_PHASE=prepare -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm go test -tags='integration continuitygate' -count=1 -run "^TestSurfaceContinuityRestartReconcile$" -v ./tests/integration > "$task_dir/phase2a.log" 2>&1; then
  cat "$task_dir/phase2a.log"
  exit 1
fi
cat "$task_dir/phase2a.log"
restart_workload="$(sed -n 's/^WORKOS_SURFACE_GATE_RESTART_WORKLOAD=//p' "$task_dir/phase2a.log" | tail -n 1)"
restart_project="$(sed -n 's/^WORKOS_SURFACE_GATE_RESTART_PROJECT=//p' "$task_dir/phase2a.log" | tail -n 1)"
test -n "$restart_workload" || { echo "restart prepare never reported the workload id" >&2; exit 1; }
test -n "$restart_project" || { echo "restart prepare never reported the project id" >&2; exit 1; }
compose stop runtime >/dev/null
compose up -d runtime >/dev/null
for attempt in $(seq 1 90); do
  code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_SURFACE_GATE_RUNTIME_PORT/workos.surface.v1.SurfaceContinuityService/ListProjectSurfaces" 2>/dev/null || true)
  case "$code" in 200|400|401|403|404|409|422|429|500|501|503) break;; esac
  sleep 1
done
if ! docker run --rm --network host --user "$WORKOS_SURFACE_GATE_USER" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY=https://goproxy.cn,direct -e WORKOS_SURFACE_GATE_GATEWAY_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_GATEWAY_PORT" -e WORKOS_SURFACE_GATE_CORE_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_CORE_PORT" -e WORKOS_SURFACE_GATE_RUNTIME_URL="http://127.0.0.1:$WORKOS_SURFACE_GATE_RUNTIME_PORT" -e WORKOS_SURFACE_GATE_DATABASE_URL="$WORKOS_SURFACE_GATE_DATABASE_URL" -e WORKOS_SURFACE_GATE_RESTART_PHASE=verify -e WORKOS_SURFACE_GATE_RESTART_WORKLOAD="$restart_workload" -e WORKOS_SURFACE_GATE_RESTART_PROJECT="$restart_project" -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace golang:1.26.7-bookworm go test -tags='integration continuitygate' -count=1 -run "^TestSurfaceContinuityRestartReconcile$" -v ./tests/integration > "$task_dir/phase2b.log" 2>&1; then
  cat "$task_dir/phase2b.log"
  exit 1
fi
cat "$task_dir/phase2b.log"
