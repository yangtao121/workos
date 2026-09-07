#!/bin/sh
# Real CLI/admin socket/Gateway/Chromium acceptance; owns only its fixture scope.
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/workspace-index.XXXXXX")
chmod 755 "$task_dir"
mkdir "$task_dir/files"
chmod 755 "$task_dir/files"
export WORKOS_WORKSPACE_GATE_ROOT="$task_dir/files"
scope_file="/workspace/${task_dir#"$repo/"}/scope.json"
cat > "$task_dir/compose.yaml" <<'YAML'
services:
  indexer:
    volumes:
      - type: bind
        source: ${WORKOS_WORKSPACE_GATE_ROOT:?}
        target: /run/workos/workspace-fixture
        read_only: true
YAML
python3 - "$task_dir/files" <<'PY'
import pathlib,sys
root=pathlib.Path(sys.argv[1])
for i in range(23):
    file=root/f'note-{i:02d}.md'
    file.write_text(f'# Workspace fixture {i:02d}\n\ndeterministic synthetic output\n')
    file.chmod(0o644)
PY
compose() { docker compose -f compose.yaml -f "$task_dir/compose.yaml" "$@"; }
admin() { compose exec -T indexer workosctl index "$@"; }
browser() {
  docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright -e WORKOS_E2E_URL=http://127.0.0.1:8080 \
    -e WORKOS_WORKSPACE_SCOPE_FILE="$scope_file" -e WORKOS_WORKSPACE_PHASE="$1" \
    -e WORKOS_E2E_OUTPUT_DIR="/workspace/${task_dir#"$repo/"}/results-$1" \
    -v "$repo:/workspace" -w /workspace/apps/desktop-web \
    "${E2E_IMAGE:-workos-playwright:1.62.1}" node node_modules/@playwright/test/cli.js test workspace-index.spec.ts --workers=1
}
field() { python3 -c 'import functools,json,sys; print(functools.reduce(lambda obj,key: obj[key], sys.argv[2].split("."), json.load(open(sys.argv[1]))))' "$1" "$2"; }
source_state() {
  admin workspace list --json > "$task_dir/sources.json"
  python3 - "$task_dir" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]); scope=json.loads((root/'scope.json').read_text())
sources=json.loads((root/'sources.json').read_text()).get('sources',[])
matching=[s for s in sources if s['project_id']==scope['id'] and s['owner_user_id']==scope['ownerUserId'] and s['root_path']=='/run/workos/workspace-fixture']
if len(matching)!=1: raise SystemExit('fixture source not uniquely bound')
(root/'source.json').write_text(json.dumps(matching[0]))
PY
}
stop_source() {
  source_state
  admin workspace stop --source "$(field "$task_dir/source.json" source_id)" --etag "$(field "$task_dir/source.json" etag)" --json > "$task_dir/stopped.json"
}
registered=false
job=""
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ -n "$job" ]; then
    if admin job get --job "$job" --json > "$task_dir/cleanup-job.json"; then
      state=$(field "$task_dir/cleanup-job.json" job.state)
      case "$state" in requested|snapshotting|catching_up|validating) admin job cancel --job "$job" || result=1 ;; esac
    else result=1; fi
  fi
  if [ "$registered" = true ]; then stop_source || result=1; fi
  if [ -f "$task_dir/scope.json" ]; then browser cleanup || result=1; fi
  docker compose up -d --no-deps indexer || result=1
  rm -rf "$task_dir/files"
  if [ "$result" -eq 0 ]; then printf 'test-workspace-browser: PASS\n'; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway indexer
for url in http://127.0.0.1:8080/healthz http://127.0.0.1:8085/readyz; do
  i=0
  until curl -sf "$url" >/dev/null; do i=$((i+1)); [ "$i" -lt 60 ] || exit 1; sleep 1; done
done
browser seed
registered=true
admin workspace register --owner "$(field "$task_dir/scope.json" ownerUserId)" --project "$(field "$task_dir/scope.json" id)" --root /run/workos/workspace-fixture
source_state
admin workspace sync --source "$(field "$task_dir/source.json" source_id)" --json > "$task_dir/sync.json"
browser indexed
# A genuine restart retains source facts and document snapshots.
compose restart indexer
sleep 2
browser restarted
admin rebuild --all --idempotency-key "workspace-gate-$(field "$task_dir/source.json" source_id)" > "$task_dir/rebuild.txt"
job=$(sed -n 's/^job: //p' "$task_dir/rebuild.txt")
[ -n "$job" ] || { cat "$task_dir/rebuild.txt"; exit 1; }
i=0
while :; do
  admin job get --job "$job" --json > "$task_dir/job.json"
  state=$(field "$task_dir/job.json" job.state)
  [ "$state" != completed ] || break
  [ "$state" != failed ] && [ "$state" != canceled ] || { cat "$task_dir/job.json"; exit 1; }
  i=$((i+1)); [ "$i" -lt 60 ] || exit 1
  sleep 1
done
browser rebuilt
stop_source
browser stopped
