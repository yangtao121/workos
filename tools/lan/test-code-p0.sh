#!/usr/bin/env bash
# Real, CA-trusted Chromium gate for the isolated resident Code project.
set -euo pipefail

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
image=workos-playwright:1.62.1
lan_ip=${WORKOS_LAN_IP:-192.168.5.5}
tls_dir=${WORKOS_LAN_TLS_DIR:-"$repo/.workos/lan-tls"}
secret_file=${WORKOS_LAN_E2E_PASSWORD_FILE:-}
project_file=${WORKOS_LAN_P0_PROJECT_FILE:-}
username=${WORKOS_LAN_E2E_USERNAME:-owner}

for required in "$secret_file" "$project_file"; do
    if [[ -z "$required" || "$required" != /* || ! -f "$required" || -L "$required" ]]; then
        echo 'test-code-p0: pass absolute, regular WORKOS_LAN_E2E_PASSWORD_FILE and WORKOS_LAN_P0_PROJECT_FILE' >&2
        exit 2
    fi
    if [[ $(stat -c '%u' -- "$required") != "$(id -u)" || -n $(find "$required" -maxdepth 0 -perm /077 -print) ]]; then
        echo 'test-code-p0: password fixture and project record must be owned by this user and mode 0600' >&2
        exit 2
    fi
done
if [[ ! "$lan_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ || ! -f "$tls_dir/ca.crt" ]]; then
    echo 'test-code-p0: configure WORKOS_LAN_IP and run tools/lan/start.sh to create the local CA' >&2
    exit 2
fi
if [[ ! -d "$repo/apps/desktop-web/node_modules" ]]; then
    echo 'test-code-p0: install pinned workspace dependencies before the browser gate' >&2
    exit 2
fi

facts=$(python3 - "$project_file" <<'PY'
import json
from pathlib import Path
import re
import sys

record = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
project = record.get("projectId")
snapshot = record.get("snapshot")
if record.get("version") != 1 or not isinstance(project, str) or not re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", project):
    raise SystemExit("test-code-p0: prepared project record is invalid")
if not isinstance(snapshot, str) or not snapshot.startswith("/") or any(value in snapshot for value in ("\n", "\r", "\t", ":")):
    raise SystemExit("test-code-p0: prepared snapshot path is invalid")
path = Path(snapshot)
if path.is_symlink() or not path.is_dir() or path.resolve() != path or (path / ".git").exists() or (path / ".workos").exists():
    raise SystemExit("test-code-p0: isolated tracked-source snapshot is missing or unsafe")
print(project)
print(snapshot)
PY
)
project_id=${facts%%$'\n'*}
snapshot=${facts#*$'\n'}

if ! docker image inspect "$image" >/dev/null 2>&1 ||
   ! docker run --rm "$image" sh -c 'command -v certutil >/dev/null'; then
    docker build -t "$image" -f "$repo/deploy/e2e/Dockerfile" "$repo/deploy/e2e"
fi

mkdir -p "$repo/.workos"
results=$(mktemp -d "$repo/.workos/lan-p0-e2e.XXXXXXXX")
chmod 700 "$results"
echo "LAN Code P0 evidence directory: $results"
echo "Testing prepared project $project_id through https://$lan_ip:8443/"

snapshot_check() {
    docker run --rm --network none --user 0:0 \
        -v "$snapshot:/snapshot:ro" \
        -v "$results:/results:ro" \
        -v "$repo/tools/lan/check-code-p0-snapshot.mjs:/opt/check.mjs:ro" \
        node:24.19.0-bookworm-slim node /opt/check.mjs "$1"
}
original_sha256=$(snapshot_check hash)
if [[ ! "$original_sha256" =~ ^[0-9a-f]{64}$ ]]; then
    echo 'test-code-p0: isolated snapshot README.md is unreadable' >&2
    exit 2
fi

run_phase() {
    local phase=$1
    echo "LAN Code P0 phase: $phase"
    docker run --rm --network host \
        --user "$(id -u):$(id -g)" \
        -e HOME=/tmp/workos-lan-code-p0 \
        -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
        -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-lan-code-p0-results \
        -e WORKOS_E2E_TLS_URL="https://$lan_ip:8443" \
        -e WORKOS_LAN_CODE_P0_E2E=true \
        -e WORKOS_LAN_P0_PHASE="$phase" \
        -e WORKOS_LAN_E2E_USERNAME="$username" \
        -e WORKOS_LAN_E2E_PASSWORD_FILE=/run/workos/lan-e2e-password \
        -e WORKOS_LAN_P0_PROJECT_ID="$project_id" \
        -e WORKOS_LAN_P0_ORIGINAL_SHA256="$original_sha256" \
        -e WORKOS_LAN_P0_STATE_FILE=/run/workos/p0-results/state.json \
        -e WORKOS_LAN_P0_SAVED_FILE=/run/workos/p0-results/saved.json \
        -e WORKOS_LAN_P0_RESTART_FILE=/run/workos/p0-results/restart.json \
        -e WORKOS_LAN_P0_PERFORMANCE_FILE=/run/workos/p0-results/performance.json \
        -v "$repo:/workspace:ro" \
        -v "$tls_dir/ca.crt:/run/workos/lan-ca.crt:ro" \
        -v "$secret_file:/run/workos/lan-e2e-password:ro" \
        -v "$results:/run/workos/p0-results:rw" \
        -w /workspace/apps/desktop-web \
        "$image" sh -ec '
            mkdir -p "$HOME/.pki/nssdb"
            certutil -N -d "sql:$HOME/.pki/nssdb" --empty-password
            certutil -A -d "sql:$HOME/.pki/nssdb" -n workos-lan-ca -t "C,," -i /run/workos/lan-ca.crt
            node node_modules/@playwright/test/cli.js test lan-code-p0.spec.ts --workers=1
        '
}

child_name() {
    python3 - "$1" <<'PY'
import json
from pathlib import Path
import re
import sys

facts = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
session = facts.get("workloadId")
generation = str(facts.get("generation", ""))
if not isinstance(session, str) or not re.fullmatch(r"[0-9a-f-]{36}", session) or not re.fullmatch(r"[1-9][0-9]*", generation):
    raise SystemExit("test-code-p0: invalid workload identity from browser phase")
print(f"workos-gf-{session.replace('-', '')}-g{generation}")
PY
}

inspect_child() {
    local name=$1
    local output=$2
    docker inspect --format '{{.Id}} {{.State.StartedAt}} {{.State.Pid}} {{.State.Running}}' "$name" > "$output"
    local container_id started_at pid running
    read -r container_id started_at pid running < "$output"
    if [[ ! "$container_id" =~ ^[0-9a-f]{64}$ || ! "$pid" =~ ^[1-9][0-9]*$ || "$running" != true ]]; then
        echo 'test-code-p0: resident child is not a live Docker process' >&2
        exit 1
    fi
}

run_phase unsaved
if [[ $(snapshot_check hash) != "$original_sha256" ]]; then
    echo 'test-code-p0: unsaved Code buffer changed snapshot bytes' >&2
    exit 1
fi
first_child=$(child_name "$results/state.json")
inspect_child "$first_child" "$results/child-before.txt"
run_phase dialog-close
inspect_child "$first_child" "$results/child-after-dialog-close.txt"
if ! cmp -s "$results/child-before.txt" "$results/child-after-dialog-close.txt"; then
    echo 'test-code-p0: Code child identity changed while closing its Open File dialog' >&2
    exit 1
fi
echo 'A02: native Open File child closed; Code top level and exact resident child stayed alive.'
run_phase continuity
saved_verified=false
for attempt in {1..30}; do
    if snapshot_check saved >/dev/null 2>&1; then
        saved_verified=true
        break
    fi
    sleep 1
done
if [[ "$saved_verified" != true ]]; then
    echo 'test-code-p0: saved Code text did not reach the isolated snapshot within 30 s' >&2
    exit 1
fi
echo 'A01/A05: Code GUI save and mixed native clipboard bytes match the isolated snapshot.'
inspect_child "$first_child" "$results/child-after.txt"
if ! cmp -s "$results/child-before.txt" "$results/child-after.txt"; then
    echo 'test-code-p0: child ID, started instant or host PID changed across all-client disconnect' >&2
    exit 1
fi
echo 'A06: exact Docker child identity and process start persisted across all-client disconnect.'

run_phase restart
snapshot_check saved
new_child=$(child_name "$results/restart.json")
inspect_child "$new_child" "$results/child-restarted.txt"
if cmp -s "$results/child-before.txt" "$results/child-restarted.txt"; then
    echo 'test-code-p0: Restart did not create a new resident child identity' >&2
    exit 1
fi
echo 'A08: Stop and Restart produced a new real child identity.'

# The performance phase measures Code's real canvas, not a service ack or
# synthetic fixture. Retain raw samples and resource facts even if p95 misses.
(
    while :; do
        docker stats --no-stream --format '{{json .}}' "$new_child" || break
        sleep 2
    done
) > "$results/child-resource-samples.jsonl" 2>/dev/null &
resource_sampler=$!
set +e
run_phase latency
latency_status=$?
set -e
kill "$resource_sampler" 2>/dev/null || true
wait "$resource_sampler" 2>/dev/null || true
docker stats --no-stream --format '{{json .}}' "$new_child" > "$results/child-resource.json" || true
uname -srm > "$results/host-kernel.txt"
grep -m 1 '^model name' /proc/cpuinfo > "$results/host-cpu.txt" || true
grep -m 1 '^MemTotal:' /proc/meminfo > "$results/host-memory.txt" || true
if command -v nvidia-smi >/dev/null 2>&1; then
    nvidia-smi --query-gpu=name,utilization.gpu,memory.used,memory.total --format=csv,noheader > "$results/gpu-resource.txt" || true
fi
if [[ $latency_status -ne 0 ]]; then
    echo "A10: real Code latency gate failed; inspect $results/performance.json for measured samples or NOT_RUN." >&2
    exit "$latency_status"
fi
echo "LAN Code P0 Chromium gate passed. Raw performance and child identity facts: $results"
