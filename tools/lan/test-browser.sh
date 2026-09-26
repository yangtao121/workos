#!/bin/sh
# Real Chromium gate for the password-protected LAN deployment. No TLS bypass.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
image=workos-playwright:1.62.1
lan_ip=${WORKOS_LAN_IP:-192.168.5.5}
tls_dir=${WORKOS_LAN_TLS_DIR:-"$repo/.workos/lan-tls"}
secret_file=${WORKOS_LAN_E2E_PASSWORD_FILE:-}
username=${WORKOS_LAN_E2E_USERNAME:-}

if [ ! -f "$tls_dir/ca.crt" ]; then
    echo 'test-browser.sh: LAN CA missing; first run tools/lan/start.sh' >&2
    exit 2
fi
if [ -z "$username" ] || [ -z "$secret_file" ] || [ ! -f "$secret_file" ]; then
    echo 'test-browser.sh: set WORKOS_LAN_E2E_USERNAME and WORKOS_LAN_E2E_PASSWORD_FILE (a private fixture file)' >&2
    exit 2
fi
if [ -n "$(find "$secret_file" -maxdepth 0 -perm /077 -print)" ]; then
    echo 'test-browser.sh: password fixture must be owner-only (chmod 600)' >&2
    exit 2
fi
if [ ! -d "$repo/apps/desktop-web/node_modules" ]; then
    echo 'test-browser.sh: install workspace dependencies with pnpm install --frozen-lockfile first' >&2
    exit 2
fi

# The normal Playwright image includes Chromium. Rebuild once to add certutil,
# which imports the LAN CA into Chromium's ephemeral NSS profile.
if ! docker image inspect "$image" >/dev/null 2>&1 ||
   ! docker run --rm "$image" sh -c 'command -v certutil >/dev/null'; then
    docker build -t "$image" -f "$repo/deploy/e2e/Dockerfile" "$repo/deploy/e2e"
fi

echo "Testing trusted Chromium at https://$lan_ip:8443/"
docker run --rm --network host \
    --user "$(id -u):$(id -g)" \
    -e HOME=/tmp/workos-lan-browser \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-lan-test-results \
    -e WORKOS_E2E_URL="https://$lan_ip:8443" \
    -e WORKOS_E2E_TLS_URL="https://$lan_ip:8443" \
    -e WORKOS_LAN_PASSWORD_E2E=true \
    -e WORKOS_LAN_PASSWORD_EXPIRY_E2E="${WORKOS_LAN_PASSWORD_EXPIRY_E2E:-false}" \
    -e WORKOS_LAN_E2E_USERNAME="$username" \
    -e WORKOS_LAN_E2E_PASSWORD_FILE=/run/workos/lan-e2e-password \
    -v "$repo:/workspace" \
    -v "$tls_dir/ca.crt:/run/workos/lan-ca.crt:ro" \
    -v "$secret_file:/run/workos/lan-e2e-password:ro" \
    -w /workspace/apps/desktop-web \
    "$image" sh -ec '
        mkdir -p "$HOME/.pki/nssdb"
        certutil -N -d "sql:$HOME/.pki/nssdb" --empty-password
        certutil -A -d "sql:$HOME/.pki/nssdb" -n workos-lan-ca -t "C,," -i /run/workos/lan-ca.crt
        node node_modules/@playwright/test/cli.js test lan-password-browser.spec.ts
    '
