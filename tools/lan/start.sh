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

mkdir -p "$WORKOS_LAN_TLS_DIR"
WORKOS_LAN_TLS_DIR=$(CDPATH= cd -- "$WORKOS_LAN_TLS_DIR" && pwd -P)
export WORKOS_LAN_TLS_DIR

compose() {
    docker compose -f "$repo/compose.yaml" -f "$repo/deploy/compose.observability.yaml" \
        -f "$repo/deploy/compose.lan-https.yaml" "$@"
}

if [ "$action" = config ]; then
    compose config --quiet
    echo 'LAN HTTPS Compose configuration is valid.'
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
compose config --quiet

if [ "$action" = set-password ]; then
    # The CLI reads both values from this terminal; no password enters env,
    # command arguments, Compose config, logs or the shell history.
    exec docker compose -f "$repo/compose.yaml" -f "$repo/deploy/compose.observability.yaml" \
        -f "$repo/deploy/compose.lan-https.yaml" \
        exec workos-gateway workosctl auth set-password
fi

if [ "$action" = renew-leaf ]; then
    compose up -d --no-deps --force-recreate workos-gateway
else
    compose up -d --build
    # Compose does not hash bind-mounted file contents; a previously running
    # collector needs a restart to load the new loopback-only receiver config.
    compose up -d --no-deps --force-recreate otel-collector
    # A bind mount of an already running gateway may still hold the old inode.
    # Restart only Gateway when automatic renewal replaced the leaf files.
    if [ -n "$old_fingerprint" ] && [ "$old_fingerprint" != "$new_fingerprint" ]; then
        compose up -d --no-deps --force-recreate workos-gateway
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
