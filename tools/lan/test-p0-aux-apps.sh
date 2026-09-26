#!/usr/bin/env bash
# Owner-run, CA-trusted Mousepad and Terminal gate after Code failure cases.
set -euo pipefail

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
image=workos-playwright:1.62.1
lan_ip=${WORKOS_LAN_IP:-192.168.5.5}
tls_dir=${WORKOS_LAN_TLS_DIR:-"$repo/.workos/lan-tls"}
secret_file=${WORKOS_LAN_E2E_PASSWORD_FILE:-}
project_file=${WORKOS_LAN_P0_PROJECT_FILE:-}
username=${WORKOS_LAN_E2E_USERNAME:-aquatao}

for required in "$secret_file" "$project_file"; do
    if [[ -z "$required" || "$required" != /* || ! -f "$required" || -L "$required" ]]; then
        echo 'test-p0-aux-apps: pass absolute, regular WORKOS_LAN_E2E_PASSWORD_FILE and WORKOS_LAN_P0_PROJECT_FILE' >&2
        exit 2
    fi
    if [[ $(stat -c '%u' -- "$required") != "$(id -u)" || -n $(find "$required" -maxdepth 0 -perm /077 -print) ]]; then
        echo 'test-p0-aux-apps: credential and project record must be owned by this user and mode 0600' >&2
        exit 2
    fi
done
if [[ ! "$lan_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ || ! -f "$tls_dir/ca.crt" ]]; then
    echo 'test-p0-aux-apps: configure WORKOS_LAN_IP and run tools/lan/start.sh to create the local CA' >&2
    exit 2
fi
if [[ ! -d "$repo/apps/desktop-web/node_modules" ]]; then
    echo 'test-p0-aux-apps: install pinned workspace dependencies before the browser gate' >&2
    exit 2
fi

project_id=$(python3 - "$project_file" "$(git -C "$repo" rev-parse --verify HEAD)" <<'PY'
import json
from pathlib import Path
import re
import sys

record = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
project = record.get("projectId")
snapshot = record.get("snapshot")
if record.get("version") != 1 or record.get("commit") != sys.argv[2] or not isinstance(project, str) or not re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", project):
    raise SystemExit("test-p0-aux-apps: prepared project record does not match this checkout")
if not isinstance(snapshot, str) or not snapshot.startswith("/") or any(value in snapshot for value in ("\n", "\r", "\t", ":")):
    raise SystemExit("test-p0-aux-apps: isolated snapshot path is invalid")
path = Path(snapshot)
if path.is_symlink() or not path.is_dir() or path.resolve() != path or (path / ".git").exists() or (path / ".workos").exists():
    raise SystemExit("test-p0-aux-apps: isolated tracked-source snapshot is missing or unsafe")
print(project)
PY
)

if ! docker image inspect "$image" >/dev/null 2>&1 ||
   ! docker run --rm "$image" sh -c 'command -v certutil >/dev/null'; then
    docker build -t "$image" -f "$repo/deploy/e2e/Dockerfile" "$repo/deploy/e2e"
fi

mkdir -p "$repo/.workos"
results=$(mktemp -d "$repo/.workos/lan-p0-aux-apps.XXXXXXXX")
chmod 700 "$results"
echo "LAN P0 auxiliary app evidence directory: $results"
echo "Testing Code, Mousepad and Terminal in project $project_id through https://$lan_ip:8443/"

docker run --rm --network host \
    --user "$(id -u):$(id -g)" \
    -e HOME=/tmp/workos-lan-p0-aux-apps \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-lan-p0-aux-results \
    -e WORKOS_E2E_TLS_URL="https://$lan_ip:8443" \
    -e WORKOS_LAN_P0_AUX_E2E=true \
    -e WORKOS_LAN_E2E_USERNAME="$username" \
    -e WORKOS_LAN_E2E_PASSWORD_FILE=/run/workos/lan-e2e-password \
    -e WORKOS_LAN_P0_PROJECT_ID="$project_id" \
    -e WORKOS_LAN_P0_AUX_RESULTS_FILE=/run/workos/p0-aux-results/results.json \
    -v "$repo:/workspace:ro" \
    -v "$tls_dir/ca.crt:/run/workos/lan-ca.crt:ro" \
    -v "$secret_file:/run/workos/lan-e2e-password:ro" \
    -v "$results:/run/workos/p0-aux-results:rw" \
    -w /workspace/apps/desktop-web \
    "$image" sh -ec '
        mkdir -p "$HOME/.pki/nssdb"
        certutil -N -d "sql:$HOME/.pki/nssdb" --empty-password
        certutil -A -d "sql:$HOME/.pki/nssdb" -n workos-lan-ca -t "C,," -i /run/workos/lan-ca.crt
        node node_modules/@playwright/test/cli.js test lan-p0-aux-apps.spec.ts --workers=1
    '

echo "LAN P0 auxiliary app gate passed. Nonsecret facts: $results/results.json"
