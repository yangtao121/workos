#!/bin/sh
# Start the same six-process development stack with a trusted LAN HTTPS edge.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
umask 077
[ "$(id -u)" -ne 0 ] || { echo 'start.sh: run as an unprivileged Docker-enabled user, not root' >&2; exit 1; }
action=${1:-up}
case "$action" in
    up|set-password|renew-leaf|config) ;;
    *) echo 'usage: start.sh [up|set-password|renew-leaf|config]' >&2; exit 2 ;;
esac
[ "$#" -le 1 ] || { echo 'start.sh: too many arguments' >&2; exit 2; }

export WORKOS_LAN_IP=${WORKOS_LAN_IP:-192.168.5.5}
export WORKOS_LAN_TLS_DIR=${WORKOS_LAN_TLS_DIR:-"$repo/.workos/lan-tls"}
export WORKOS_LAN_UID=$(id -u)
export WORKOS_LAN_GID=$(id -g)
export WORKOS_GREENFIELD_IPC_ROOT=${WORKOS_GREENFIELD_IPC_ROOT:-"$repo/.workos/greenfield-ipc"}
export WORKOS_WORKSPACE_ROOTS=${WORKOS_WORKSPACE_ROOTS:-"$repo/.workos/workspaces"}
export WORKOS_RUNTIME_WORKSPACE_MOUNTS=${WORKOS_RUNTIME_WORKSPACE_MOUNTS:-}
[ -S /var/run/docker.sock ] || { echo 'start.sh: Docker socket is required for the isolated Greenfield child' >&2; exit 1; }
export WORKOS_DOCKER_GID=$(stat -c %g /var/run/docker.sock)
if [ -c /dev/dri/renderD128 ]; then
    export WORKOS_GREENFIELD_RENDER_GID=$(stat -c %g /dev/dri/renderD128)
else
    # Keep Gateway/LAN administration available; Runtime reports native
    # rendering unavailable instead of falling back to an older display.
    export WORKOS_GREENFIELD_RENDER_GID=-1
fi

case "$WORKOS_GREENFIELD_IPC_ROOT:$WORKOS_WORKSPACE_ROOTS" in
    /*:/*) ;;
    *) echo 'start.sh: IPC and workspace roots must be absolute paths' >&2; exit 1 ;;
esac
[ ! -L "$WORKOS_GREENFIELD_IPC_ROOT" ] || { echo 'start.sh: IPC root must not be a symlink' >&2; exit 1; }

mkdir -p "$WORKOS_LAN_TLS_DIR"
WORKOS_LAN_TLS_DIR=$(CDPATH= cd -- "$WORKOS_LAN_TLS_DIR" && pwd -P)
export WORKOS_LAN_TLS_DIR

gateway_compose() {
    docker compose -f "$repo/compose.yaml" -f "$repo/deploy/compose.observability.yaml" \
        -f "$repo/deploy/compose.lan-https.yaml" "$@"
}

resident_compose() {
    docker compose -f "$repo/compose.yaml" -f "$repo/deploy/compose.observability.yaml" \
        -f "$repo/deploy/compose.greenfield-resident.yaml" \
        -f "$repo/deploy/compose.lan-https.yaml" "$@"
}

if [ "$action" = config ]; then
    if [ -f "$repo/deploy/compose.greenfield-resident.yaml" ]; then
        resident_compose config --quiet
        echo 'LAN HTTPS resident Compose configuration is valid.'
    else
        gateway_compose config --quiet
        echo 'LAN HTTPS Gateway Compose configuration is valid; resident P0 overlay is pending.'
    fi
    exit 0
fi

old_fingerprint=
if [ -f "$WORKOS_LAN_TLS_DIR/leaf.crt" ]; then
    old_fingerprint=$(openssl x509 -in "$WORKOS_LAN_TLS_DIR/leaf.crt" -noout -fingerprint -sha256 2>/dev/null || true)
fi

if [ "$action" = renew-leaf ]; then
    "$here/cert.sh" "$WORKOS_LAN_IP" "$WORKOS_LAN_TLS_DIR" --renew-leaf
else
    "$here/cert.sh" "$WORKOS_LAN_IP" "$WORKOS_LAN_TLS_DIR"
fi
new_fingerprint=$(openssl x509 -in "$WORKOS_LAN_TLS_DIR/leaf.crt" -noout -fingerprint -sha256)

if [ "$action" = set-password ]; then
    # The CLI reads both values from this terminal; no password enters env,
    # command arguments, Compose config, logs or the shell history.
    gateway_compose config --quiet
    gateway_compose exec workos-gateway workosctl auth set-password
    exit
fi

if [ "$action" = renew-leaf ]; then
    gateway_compose config --quiet
    gateway_compose up -d --no-deps --force-recreate workos-gateway
else
    [ -f "$repo/deploy/compose.greenfield-resident.yaml" ] || {
        echo 'start.sh: resident P0 Compose overlay is not built yet' >&2
        exit 1
    }
    resident_compose config --quiet
    mkdir -p "$WORKOS_GREENFIELD_IPC_ROOT" "$WORKOS_WORKSPACE_ROOTS"
    WORKOS_GREENFIELD_IPC_ROOT=$(CDPATH= cd -- "$WORKOS_GREENFIELD_IPC_ROOT" && pwd -P)
    WORKOS_WORKSPACE_ROOTS=$(CDPATH= cd -- "$WORKOS_WORKSPACE_ROOTS" && pwd -P)
    export WORKOS_GREENFIELD_IPC_ROOT WORKOS_WORKSPACE_ROOTS
    # The trusted Runtime and isolated child run as uid 10001. The one-shot
    # helper changes only the mount root, never project files or TLS keys.
    docker run --rm --user 0:0 \
        -v "$WORKOS_GREENFIELD_IPC_ROOT:/run/workos/greenfield-ipc" \
        debian:bookworm-slim sh -ec 'chown 10001:10001 /run/workos/greenfield-ipc && chmod 0700 /run/workos/greenfield-ipc'
    # The child inherits the pinned Greenfield proxy and official Code image.
    # Build both stages from this checkout so a clean host cannot accidentally
    # reuse an older local base image under the same tag.
    docker build -t workos-greenfield-runtime:p0 -f "$repo/deploy/greenfield-runtime.Dockerfile" "$repo"
    docker build -t workos-greenfield-child:dev -f "$repo/deploy/greenfield-child.Dockerfile" "$repo"
    resident_compose up -d --build
    # Compose does not hash bind-mounted file contents; a previously running
    # collector needs a restart to load the new loopback-only receiver config.
    resident_compose up -d --no-deps --force-recreate otel-collector
    # A bind mount of an already running gateway may still hold the old inode.
    # Restart only Gateway when automatic renewal replaced the leaf files.
    if [ -n "$old_fingerprint" ] && [ "$old_fingerprint" != "$new_fingerprint" ]; then
        resident_compose up -d --no-deps --force-recreate workos-gateway
    fi
fi

attempt=0
until curl --noproxy '*' --silent --output /dev/null --connect-timeout 2 --max-time 3 \
    --cacert "$WORKOS_LAN_TLS_DIR/ca.crt" "https://$WORKOS_LAN_IP:8443/"; do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 30 ] || {
        echo 'start.sh: Gateway did not serve a CA-verified HTTPS response; check docker compose logs workos-gateway' >&2
        exit 1
    }
    sleep 1
done

echo "LAN URL: https://$WORKOS_LAN_IP:8443/"
echo "Trust this CA on each client: $WORKOS_LAN_TLS_DIR/ca.crt"
echo 'Set or change the owner password with: ./tools/lan/start.sh set-password'
