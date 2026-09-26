#!/usr/bin/env bash
# Real CA-trusted Chromium A08 lifecycle gate. Run after the auxiliary app
# isolation gate, using the successful Code P0 gate's owner-only handoff.
set -euo pipefail

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
image=workos-playwright:1.62.1
lan_ip=${WORKOS_LAN_IP:-192.168.5.5}
tls_dir=${WORKOS_LAN_TLS_DIR:-"$repo/.workos/lan-tls"}
secret_file=${WORKOS_LAN_E2E_PASSWORD_FILE:-}
pointer=${WORKOS_LAN_P0_RESULTS_POINTER:-}
username=${WORKOS_LAN_E2E_USERNAME:-owner}

if [[ -z "$pointer" || "$pointer" != /* || ! -f "$pointer" || -L "$pointer" ||
      $(stat -c '%u' -- "$pointer") != "$(id -u)" || -n $(find "$pointer" -maxdepth 0 -perm /077 -print) ]]; then
    echo 'test-code-a08: pass an owner-only WORKOS_LAN_P0_RESULTS_POINTER from a passed Code P0 gate' >&2
    exit 2
fi
results=$(cat -- "$pointer")
if [[ "$results" != "$repo"/.workos/lan-p0-e2e.* || ! -d "$results" || -L "$results" ||
      $(stat -c '%u' -- "$results") != "$(id -u)" || -n $(find "$results" -maxdepth 0 -perm /077 -print) ]]; then
    echo 'test-code-a08: Code P0 evidence directory is invalid' >&2
    exit 2
fi
if [[ -z "$secret_file" || "$secret_file" != /* || ! -f "$secret_file" || -L "$secret_file" ||
      $(stat -c '%u' -- "$secret_file") != "$(id -u)" || -n $(find "$secret_file" -maxdepth 0 -perm /077 -print) ]]; then
    echo 'test-code-a08: owner-only password fixture is required' >&2
    exit 2
fi
if [[ ! "$lan_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ || ! -f "$tls_dir/ca.crt" ||
      ! -d "$repo/apps/desktop-web/node_modules" ]]; then
    echo 'test-code-a08: LAN HTTPS CA and pinned web dependencies are required' >&2
    exit 2
fi
for artifact in state.json restart.json child-restarted.txt; do
    if [[ ! -f "$results/$artifact" || -L "$results/$artifact" ]]; then
        echo "test-code-a08: missing successful Code P0 artifact $artifact" >&2
        exit 2
    fi
done

project_id=$(python3 - "$results/state.json" "$results/restart.json" <<'PY'
import json
from pathlib import Path
import re
import sys

state = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
restart = json.loads(Path(sys.argv[2]).read_text(encoding="utf-8"))
uuid = r"[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}"
project = state.get("projectId")
if not isinstance(project, str) or not re.fullmatch(uuid, project) or state.get("workloadId") != restart.get("workloadId"):
    raise SystemExit("test-code-a08: Code P0 identity handoff is invalid")
if int(restart.get("generation", 0)) <= int(state.get("generation", 0)):
    raise SystemExit("test-code-a08: Code P0 restart handoff is stale")
print(project)
PY
)

run_phase() {
    local phase=$1
    echo "LAN Code A08 phase: $phase"
    docker run --rm --network host \
        --user "$(id -u):$(id -g)" \
        -e HOME=/tmp/workos-lan-code-a08 \
        -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
        -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-lan-code-a08-results \
        -e WORKOS_E2E_TLS_URL="https://$lan_ip:8443" \
        -e WORKOS_LAN_CODE_P0_E2E=true \
        -e WORKOS_LAN_P0_PHASE="$phase" \
        -e WORKOS_LAN_E2E_USERNAME="$username" \
        -e WORKOS_LAN_E2E_PASSWORD_FILE=/run/workos/lan-e2e-password \
        -e WORKOS_LAN_P0_PROJECT_ID="$project_id" \
        -e WORKOS_LAN_P0_STATE_FILE=/run/workos/p0-results/state.json \
        -e WORKOS_LAN_P0_RESTART_FILE=/run/workos/p0-results/restart.json \
        -e WORKOS_LAN_P0_EXIT_RESTART_FILE=/run/workos/p0-results/exit-restart.json \
        -e WORKOS_LAN_P0_FAILED_RESTART_FILE=/run/workos/p0-results/failed-restart.json \
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
    raise SystemExit("test-code-a08: invalid workload identity from browser phase")
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
        echo 'test-code-a08: resident child is not a live Docker process' >&2
        exit 1
    fi
}

if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo 'test-code-a08: pinned Playwright image is unavailable' >&2
    exit 2
fi
before_child=$(child_name "$results/restart.json")
inspect_child "$before_child" "$results/child-before-a08.txt"
if ! cmp -s "$results/child-restarted.txt" "$results/child-before-a08.txt"; then
    echo 'test-code-a08: auxiliary stage changed the Code child before lifecycle testing' >&2
    exit 1
fi

run_phase app-exit
after_exit_child=$(child_name "$results/exit-restart.json")
inspect_child "$after_exit_child" "$results/child-after-app-exit-restart.txt"
if cmp -s "$results/child-before-a08.txt" "$results/child-after-app-exit-restart.txt"; then
    echo 'test-code-a08: Code File > Exit Restart reused the old child identity' >&2
    exit 1
fi
echo 'A08: Code File > Exit persisted stopped and Restart created a new real child.'

# Kill only the exact generation just recorded by the browser phase. The
# following phase must observe failed, then restart again with a new process.
read -r exact_child_id _ < "$results/child-after-app-exit-restart.txt"
docker kill --signal KILL "$exact_child_id" > "$results/killed-child-id.txt"
run_phase forced-child-death
after_failure_child=$(child_name "$results/failed-restart.json")
inspect_child "$after_failure_child" "$results/child-after-failure-restart.txt"
if cmp -s "$results/child-after-app-exit-restart.txt" "$results/child-after-failure-restart.txt"; then
    echo 'test-code-a08: failed child Restart reused the killed child identity' >&2
    exit 1
fi
echo "A08: forced child death persisted failed and Restart created a new child. Evidence: $results"
