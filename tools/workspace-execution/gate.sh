#!/bin/sh
# The workspace execution gate (ADR-0030, B02): one real git repository
# shared by the owner's terminal and the host-side file view. The PTY shell
# proves the bound working directory, git identity, and bidirectional file
# flow against the real disk tree.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/workspace-execution.XXXXXX")
export WORKOS_WORKSPACE_GATE_DIR="$task_dir"
export WORKOS_WORKSPACE_GATE_USER="$(id -u):$(id -g)"
repo_tree="$task_dir/project-tree"
mkdir -p "$repo_tree"
git -C "$repo_tree" init -q
git -C "$repo_tree" -c user.name=gate -c user.email=gate@invalid commit -q --allow-empty -m "gate seed"
printf 'gate-marker\n' > "$repo_tree/marker.txt"
git -C "$repo_tree" add marker.txt
git -C "$repo_tree" -c user.name=gate -c user.email=gate@invalid commit -q -m "marker"
head=$(git -C "$repo_tree" rev-parse HEAD)
owner="01999999-9999-7999-8999-000000000b01"
project=$(python3 - << 'PY'
import time, random
ms = int(time.time() * 1000)
tail = random.getrandbits(64)
hexed = f"{ms:012x}7{random.getrandbits(12):03x}8{random.getrandbits(12):03x}{random.getrandbits(48):012x}"
print(f"{hexed[0:8]}-{hexed[8:12]}-{hexed[12:16]}-{hexed[16:20]}-{hexed[20:32]}")
PY
)
export WORKOS_WORKSPACE_GATE_REPO="$repo_tree"
export WORKOS_WORKSPACE_GATE_REPO_CONTAINER_PATH="$repo_tree"
export WORKOS_WORKSPACE_GATE_PROJECT_ID="$project"
export WORKOS_WORKSPACE_GATE_GIT_HEAD="$head"
export WORKOS_WORKSPACE_GATE_MOUNTS="$owner:$project:$repo_tree"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
database="workos_workspace_$stamp"
composeproject="workos-workspace-$stamp"
export WORKOS_WORKSPACE_GATE_DATABASE_URL="postgres://workos:workos@127.0.0.1:5432/$database?sslmode=disable"
python3 - "$task_dir/ports.env" << 'PY'
import socket,sys
sockets=[]; values=[]
for name in ['GATEWAY','CORE','RUNTIME','EXECUTION']:
    sock=socket.socket(); sock.bind(('127.0.0.1',0)); sockets.append(sock)
    values.append(f'export WORKOS_WORKSPACE_GATE_{name}_PORT={sock.getsockname()[1]}')
open(sys.argv[1],'w').write('\n'.join(values)+'\n')
PY
. "$task_dir/ports.env"
compose() { docker compose -p "$composeproject" -f tools/workspace-execution/compose.yaml "$@"; }
owned_database=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    compose logs --no-color > "$task_dir/stack.log" 2>&1 || true
    printf 'workspace-execution diagnostics: %s\n' "$task_dir" >&2
  fi
  compose down --timeout 5 || result=1
  if [ "$owned_database" = true ] && [ "$result" -eq 0 ]; then
    docker compose exec -T postgres dropdb -U workos --force "$database" || result=1
  fi
  docker run --rm -v "$task_dir:/gate" busybox:latest sh -c 'rm -rf /gate/core-execution /gate/harness-execution /gate/vault /gate/run' >/dev/null 2>&1 || true
  if [ "$result" -eq 0 ]; then printf 'workspace-execution: PASS (shared real directory)\n'; fi
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
    code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:$WORKOS_WORKSPACE_GATE_CORE_PORT/workos.project.v1.ProjectService/GetProject" 2>/dev/null || true)
    case "$code" in 200|400|401|403|404|409|422|429|500|501|503) return 0;; esac
    sleep 1
  done
  return 1
}
wait_ready
repo="$repo" GO_HOST_IMAGE=golang:1.26.7-bookworm docker run --rm --network host \
  --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod \
  -e GOPROXY=https://goproxy.cn,direct \
  -v "$repo:/workspace" -w /workspace -v workos-go-cache:/go/pkg/mod \
  -v "$repo_tree:$repo_tree" \
  -e WORKOS_WORKSPACE_GATE_GITHUB_OWNER="$owner" \
  -e WORKOS_WORKSPACE_GATE_PROJECT_ID -e WORKOS_WORKSPACE_GATE_GIT_HEAD \
  -e WORKOS_WORKSPACE_GATE_REPO="$repo_tree" \
  -e WORKOS_WORKSPACE_GATE_URL="http://127.0.0.1:$WORKOS_WORKSPACE_GATE_GATEWAY_PORT" \
  -e WORKOS_WORKSPACE_GATE_RUNTIME_URL="http://127.0.0.1:$WORKOS_WORKSPACE_GATE_RUNTIME_PORT" \
  -e WORKOS_WORKSPACE_GATE_DIR \
  golang:1.26.7-bookworm go test -tags='integration workspacegate' -count=1 -run '^TestWorkspaceExecution$' -v ./tests/integration
