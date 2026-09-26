#!/usr/bin/env bash
# Owner-run, CA-trusted long Code control-renewal gate.
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
        echo 'test-code-control-renewal: pass absolute, regular password and project files' >&2
        exit 2
    fi
    if [[ $(stat -c '%u' -- "$required") != "$(id -u)" || -n $(find "$required" -maxdepth 0 -perm /077 -print) ]]; then
        echo 'test-code-control-renewal: password and project files must be owner-only' >&2
        exit 2
    fi
done
if [[ ! "$lan_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ || ! -f "$tls_dir/ca.crt" ]]; then
    echo 'test-code-control-renewal: configure LAN IP and local CA first' >&2
    exit 2
fi
if [[ ! -d "$repo/apps/desktop-web/node_modules" ]]; then
    echo 'test-code-control-renewal: pinned desktop-web dependencies are required' >&2
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
if record.get("version") != 1 or record.get("commit") != sys.argv[2]:
    raise SystemExit("test-code-control-renewal: project record is not for current HEAD")
if not isinstance(project, str) or not re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", project):
    raise SystemExit("test-code-control-renewal: prepared project ID is invalid")
if not isinstance(snapshot, str) or not snapshot.startswith("/") or any(value in snapshot for value in ("\n", "\r", "\t", ":")):
    raise SystemExit("test-code-control-renewal: snapshot path is invalid")
path = Path(snapshot)
if path.is_symlink() or not path.is_dir() or path.resolve() != path or (path / ".git").exists() or (path / ".workos").exists():
    raise SystemExit("test-code-control-renewal: isolated tracked-source snapshot is missing or unsafe")
print(project)
PY
)

if ! docker image inspect "$image" >/dev/null 2>&1 ||
   ! docker run --rm "$image" sh -c 'command -v certutil >/dev/null'; then
    docker build -t "$image" -f "$repo/deploy/e2e/Dockerfile" "$repo/deploy/e2e"
fi

if [[ -L "$repo/.workos" ]]; then
    echo 'test-code-control-renewal: private evidence directory must not be a symlink' >&2
    exit 2
fi
mkdir -p -m 700 "$repo/.workos"
if [[ $(stat -c '%u' -- "$repo/.workos") != "$(id -u)" ||
      -n $(find "$repo/.workos" -maxdepth 0 -perm /077 -print) ]]; then
    echo 'test-code-control-renewal: private evidence directory must be owner-only' >&2
    exit 2
fi
results=$(mktemp -d "$repo/.workos/lan-p0-renew.XXXXXXXX")
chmod 700 "$results"
echo "Testing automatic Code control renewal (about 30 minutes); nonsecret evidence: $results"

docker run --rm --network host \
    --user "$(id -u):$(id -g)" \
    -e HOME=/tmp/workos-lan-code-renew \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-lan-code-renew-results \
    -e WORKOS_E2E_TLS_URL="https://$lan_ip:8443" \
    -e WORKOS_LAN_CODE_RENEW_E2E=true \
    -e WORKOS_LAN_E2E_USERNAME="$username" \
    -e WORKOS_LAN_E2E_PASSWORD_FILE=/run/workos/lan-e2e-password \
    -e WORKOS_LAN_P0_PROJECT_ID="$project_id" \
    -e WORKOS_LAN_P0_RENEW_RESULTS_FILE=/run/workos/p0-renew-results/results.json \
    -v "$repo:/workspace:ro" \
    -v "$tls_dir/ca.crt:/run/workos/lan-ca.crt:ro" \
    -v "$secret_file:/run/workos/lan-e2e-password:ro" \
    -v "$results:/run/workos/p0-renew-results:rw" \
    -w /workspace/apps/desktop-web \
    "$image" sh -ec '
        mkdir -p "$HOME/.pki/nssdb"
        certutil -N -d "sql:$HOME/.pki/nssdb" --empty-password
        certutil -A -d "sql:$HOME/.pki/nssdb" -n workos-lan-ca -t "C,," -i /run/workos/lan-ca.crt
        node node_modules/@playwright/test/cli.js test lan-code-control-renewal.spec.ts --workers=1
    '

if [[ ! -f "$results/results.json" || -L "$results/results.json" ||
      $(stat -c '%u' -- "$results/results.json") != "$(id -u)" ||
      -n $(find "$results/results.json" -maxdepth 0 -perm /077 -print) ]]; then
    echo 'test-code-control-renewal: private PASS record missing or unsafe' >&2
    exit 1
fi
echo "A07 real Code control renewal passed. Nonsecret result: $results/results.json"
