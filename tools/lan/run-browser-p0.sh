#!/bin/sh
# Owner-run, CA-trusted LAN P0 browser acceptance. Password bytes stay local.
set +x
set +a
set -eu
umask 077
unset password

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
private="$repo/.workos"
lan_ip=${WORKOS_LAN_IP:-192.168.5.5}
tls_dir=${WORKOS_LAN_TLS_DIR:-"$private/lan-tls"}
username=${WORKOS_LAN_E2E_USERNAME:-aquatao}
secret_file=
tty_saved=
stage_file=
current_stage=

if [ "$#" -ne 0 ]; then
    echo 'usage: ./tools/lan/run-browser-p0.sh' >&2
    exit 2
fi
if [ "$(id -u)" -eq 0 ]; then
    echo 'run-browser-p0: run as the unprivileged Docker-enabled owner, not root' >&2
    exit 2
fi
if ! ( : </dev/tty ) 2>/dev/null; then
    echo 'run-browser-p0: an interactive local TTY is required; no password was read' >&2
    exit 2
fi
exec 3</dev/tty 4>/dev/tty

cleanup() {
    result=$?
    trap - 0 1 2 15
    if [ -n "$tty_saved" ]; then
        if ! stty "$tty_saved" <&3 2>/dev/null; then
            echo 'run-browser-p0: could not restore terminal settings' >&2
            result=1
        fi
        printf '\n' >&4
    fi
    if [ -n "$current_stage" ] && [ -n "$stage_file" ]; then
        printf '%s\tINTERRUPTED\t%s\t%s\n' "$current_stage" "$result" "$(date -u +%FT%TZ)" >> "$stage_file" || :
    fi
    if [ -n "$secret_file" ] && ! rm -f -- "$secret_file"; then
        echo 'run-browser-p0: temporary password file cleanup failed' >&2
        result=1
    fi
    exit "$result"
}
trap cleanup 0
trap 'exit 129' 1
trap 'exit 130' 2
trap 'exit 143' 15

for required in git python3 docker curl mktemp stty stat find date; do
    if ! command -v "$required" >/dev/null 2>&1; then
        echo "run-browser-p0: missing required command: $required" >&2
        exit 2
    fi
done
if [ ! -S /var/run/docker.sock ] || [ ! -d "$repo/apps/desktop-web/node_modules" ]; then
    echo 'run-browser-p0: Docker socket and pinned desktop-web dependencies are required' >&2
    exit 2
fi
if ! git -C "$repo" diff --quiet || ! git -C "$repo" diff --cached --quiet; then
    echo 'run-browser-p0: commit tracked checkout changes before preparing the exact HEAD snapshot' >&2
    exit 2
fi
if ! python3 - "$lan_ip" <<'PY'
import ipaddress
import sys

try:
    ipaddress.IPv4Address(sys.argv[1])
except ValueError:
    raise SystemExit(1)
PY
then
    echo 'run-browser-p0: WORKOS_LAN_IP must be an IPv4 address' >&2
    exit 2
fi

commit=$(git -C "$repo" rev-parse --verify HEAD)
project_file="$private/lan-code-project-$(printf '%s' "$commit" | cut -c 1-12).json"
if [ -L "$private" ]; then
    echo 'run-browser-p0: private evidence directory must not be a symlink' >&2
    exit 2
fi
mkdir -p -m 700 "$private"
if [ "$(stat -c %u -- "$private")" != "$(id -u)" ] ||
   [ -n "$(find "$private" -maxdepth 0 -perm /077 -print)" ]; then
    echo 'run-browser-p0: private evidence directory must be owned by this user and owner-only' >&2
    exit 2
fi
evidence_dir=$(mktemp -d "$private/lan-browser-p0.XXXXXXXX")
stage_file="$evidence_dir/stages.tsv"
printf 'stage\tresult\texit_code\tutc\n' > "$stage_file"
printf 'commit\t%s\n' "$commit" > "$evidence_dir/context.tsv"
echo "LAN P0 nonsecret stage evidence: $evidence_dir"

record_stage() {
    printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$(date -u +%FT%TZ)" >> "$stage_file"
}

run_stage() {
    current_stage=$1
    shift
    record_stage "$current_stage" START 0
    echo "run-browser-p0: $current_stage"
    if "$@"; then
        record_stage "$current_stage" PASS 0
        current_stage=
    else
        stage_result=$?
        record_stage "$current_stage" FAIL "$stage_result"
        echo "run-browser-p0: $current_stage failed (exit $stage_result); inspect $stage_file" >&2
        current_stage=
        exit "$stage_result"
    fi
}

gateway_ready() {
    [ -f "$tls_dir/ca.crt" ] &&
        curl --noproxy '*' --silent --output /dev/null --connect-timeout 2 --max-time 5 \
            --cacert "$tls_dir/ca.crt" "https://$lan_ip:8443/"
}

# Project preparation authenticates through the public Gateway. Bootstrap an
# unavailable stack before prompting, then restart it again after prepare has
# written the exact snapshot mount record.
unset WORKOS_RUNTIME_WORKSPACE_MOUNTS
if ! gateway_ready; then
    run_stage bootstrap-lan "$here/start.sh" up
fi

tty_saved=$(stty -g <&3)
printf 'Password for WorkOS owner %s: ' "$username" >&4
stty -echo <&3
if ! IFS= read -r password <&3; then
    echo 'run-browser-p0: password input was interrupted' >&2
    exit 2
fi
stty "$tty_saved" <&3
tty_saved=
printf '\n' >&4
exec 3<&- 4>&-
if [ -z "$password" ]; then
    echo 'run-browser-p0: empty password; no test stage started' >&2
    exit 2
fi
secret_file=$(mktemp /tmp/workos-lan-p0-password.XXXXXXXX)
printf '%s' "$password" > "$secret_file"
unset password
chmod 600 "$secret_file"

export WORKOS_LAN_E2E_USERNAME="$username"
export WORKOS_LAN_E2E_PASSWORD_FILE="$secret_file"
export WORKOS_LAN_P0_PROJECT_FILE="$project_file"

run_stage prepare python3 "$here/prepare-code-project.py" prepare \
    --username "$username" --password-file "$secret_file"

verify_project_record() {
    if [ ! -f "$project_file" ] || [ -L "$project_file" ] ||
       [ "$(stat -c %u -- "$project_file")" != "$(id -u)" ] ||
       [ -n "$(find "$project_file" -maxdepth 0 -perm /077 -print)" ]; then
        echo 'run-browser-p0: current-commit project record is missing or not owner-only' >&2
        return 2
    fi
    python3 - "$project_file" "$commit" <<'PY'
import json
from pathlib import Path
import sys

try:
    record = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
except (OSError, ValueError):
    raise SystemExit("run-browser-p0: project record could not be validated") from None
if record.get("version") != 1 or record.get("commit") != sys.argv[2]:
    raise SystemExit("run-browser-p0: project record does not match current HEAD")
PY
}
run_stage verify-project-record verify_project_record
run_stage resident-up "$here/start.sh" up
run_stage bind python3 "$here/prepare-code-project.py" bind \
    --username "$username" --password-file "$secret_file"
run_stage password-browser "$here/test-browser.sh"
run_stage resident-code-browser "$here/test-code-p0.sh"

echo "LAN P0 browser self-test passed. Stage evidence: $stage_file"
