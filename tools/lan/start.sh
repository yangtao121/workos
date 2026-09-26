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
mount_file="$repo/.workos/lan-code-workspace-mount"
if [ "$action" = up ] && [ "${WORKOS_RUNTIME_WORKSPACE_MOUNTS+x}" != x ] && [ -f "$mount_file" ]; then
    [ ! -L "$mount_file" ] && [ "$(stat -c %u:%a "$mount_file")" = "$(id -u):600" ] || {
        echo 'start.sh: Code workspace mount file must be owner-only and not a symlink' >&2
        exit 1
    }
    WORKOS_RUNTIME_WORKSPACE_MOUNTS=$(cat "$mount_file")
    case "$WORKOS_RUNTIME_WORKSPACE_MOUNTS" in
        *';'*|*'
'*|'') echo 'start.sh: Code workspace mount file is invalid' >&2; exit 1 ;;
    esac
fi
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

if [ "$action" = set-password ]; then
    # Password administration changes neither certificates nor the service
    # graph. Require the already-running private Gateway admin socket before
    # prompting, so an unavailable stack cannot consume a typed secret.
    gateway_compose config --quiet
    if ! gateway_compose exec -T workos-gateway sh -ec 'test -S "$WORKOS_AUTH_ADMIN_SOCKET"'; then
        echo 'start.sh: Gateway admin socket is unavailable; start the LAN stack first' >&2
        exit 1
    fi
    gateway_compose exec workos-gateway workosctl auth set-password
    exit
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
    # The prior invocation leaves the IPC root owned by uid 10001 with mode
    # 0700. The host operator can resolve that directory but cannot cd into it.
    WORKOS_GREENFIELD_IPC_ROOT=$(realpath -e -- "$WORKOS_GREENFIELD_IPC_ROOT")
    WORKOS_WORKSPACE_ROOTS=$(realpath -e -- "$WORKOS_WORKSPACE_ROOTS")
    export WORKOS_GREENFIELD_IPC_ROOT WORKOS_WORKSPACE_ROOTS
    # The trusted Runtime and isolated child run as uid 10001. The one-shot
    # helper changes only the mount root, never project files or TLS keys.
    docker run --rm --user 0:0 \
        -v "$WORKOS_GREENFIELD_IPC_ROOT:/run/workos/greenfield-ipc" \
        debian:bookworm-slim sh -ec 'chown 10001:10001 /run/workos/greenfield-ipc && chmod 0700 /run/workos/greenfield-ipc'
    # Keep a checksummed source archive outside the image build. The local
    # cache makes repeat starts independent of a slow source mirror, while a
    # clean host still downloads the exact pinned Greenfield commit.
    greenfield_commit=6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57
    greenfield_sha256=97e0a72b0e139c8b22088fa4acde199d65d5794f8ee7f85590c435e9231cf433
    greenfield_source="$repo/.workos/build-cache/greenfield-$greenfield_commit"
    mkdir -p "$greenfield_source"
    chmod 0700 "$greenfield_source"
    [ ! -L "$greenfield_source/source.tar.gz" ] || { echo 'start.sh: Greenfield source cache must not be a symlink' >&2; exit 1; }
    if [ ! -f "$greenfield_source/source.tar.gz" ]; then
        temporary=$(mktemp "$greenfield_source/source.XXXXXX")
        if ! curl -fsSL --connect-timeout 10 --max-time 180 --retry 2 \
            "https://codeload.github.com/udevbe/greenfield/tar.gz/$greenfield_commit" -o "$temporary"; then
            rm -f "$temporary"
            echo 'start.sh: pinned Greenfield source download failed' >&2
            exit 1
        fi
        if ! printf '%s  %s\n' "$greenfield_sha256" "$temporary" | sha256sum --check --strict >/dev/null; then
            rm -f "$temporary"
            echo 'start.sh: pinned Greenfield source checksum failed' >&2
            exit 1
        fi
        mv "$temporary" "$greenfield_source/source.tar.gz"
    fi
    printf '%s  %s\n' "$greenfield_sha256" "$greenfield_source/source.tar.gz" | sha256sum --check --strict >/dev/null || {
        echo 'start.sh: cached Greenfield source checksum failed' >&2
        exit 1
    }
    # Build both stages from this checkout so a clean host cannot silently
    # reuse an older local base image under the same tag.
    docker build --build-context "greenfield-source=$greenfield_source" \
        -t workos-greenfield-runtime:p0 -f "$repo/deploy/greenfield-runtime.Dockerfile" "$repo"
    docker build -t workos-greenfield-child:dev -f "$repo/deploy/greenfield-child.Dockerfile" "$repo"
    # The supervised PTY uses a separate no-network workspace container.
    # Rebuild it with the resident child so Terminal is available on LAN P0.
    docker build -t workos-workspace-runtime:dev -f "$repo/deploy/workspace.Dockerfile" "$repo"
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
