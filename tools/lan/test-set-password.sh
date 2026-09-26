#!/bin/sh
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM
mkdir -p "$temp_dir/bin"
cat >"$temp_dir/bin/docker" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$WORKOS_LAN_FAKE_DOCKER_LOG"
case " $* " in
    *' exec -T workos-gateway '*)
        [ "${WORKOS_LAN_FAKE_ADMIN_UNAVAILABLE:-0}" != 1 ] || exit 1
        ;;
esac
EOF
chmod 0700 "$temp_dir/bin/docker"

export WORKOS_LAN_IP=192.168.5.5
export WORKOS_LAN_TLS_DIR="$temp_dir/tls"
export WORKOS_GREENFIELD_IPC_ROOT="$temp_dir/ipc"
export WORKOS_WORKSPACE_ROOTS="$temp_dir/workspaces"
export WORKOS_LAN_FAKE_DOCKER_LOG="$temp_dir/docker.log"
PATH="$temp_dir/bin:$PATH" "$here/start.sh" set-password >"$temp_dir/success.log" 2>&1
test ! -e "$WORKOS_LAN_TLS_DIR/leaf.crt"
test "$(wc -l <"$WORKOS_LAN_FAKE_DOCKER_LOG")" -eq 3
grep -Fq ' config --quiet' "$WORKOS_LAN_FAKE_DOCKER_LOG"
grep -Fq ' exec -T workos-gateway sh -ec test -S' "$WORKOS_LAN_FAKE_DOCKER_LOG"
grep -Fq ' exec workos-gateway workosctl auth set-password' "$WORKOS_LAN_FAKE_DOCKER_LOG"

: >"$WORKOS_LAN_FAKE_DOCKER_LOG"
if WORKOS_LAN_FAKE_ADMIN_UNAVAILABLE=1 PATH="$temp_dir/bin:$PATH" "$here/start.sh" set-password >"$temp_dir/failure.log" 2>&1; then
    echo 'test-set-password: missing admin socket was accepted' >&2
    exit 1
fi
grep -Fq 'Gateway admin socket is unavailable' "$temp_dir/failure.log"
test "$(wc -l <"$WORKOS_LAN_FAKE_DOCKER_LOG")" -eq 2
if grep -Fq ' exec workos-gateway workosctl auth set-password' "$WORKOS_LAN_FAKE_DOCKER_LOG"; then
    echo 'test-set-password: CLI was invoked without admin socket' >&2
    exit 1
fi
echo 'test-lan-set-password: PASS (admin preflight, no certificate changes, no premature prompt)'
