#!/bin/sh
# Run after the browser phase, before restart checks. The parent gate owns cleanup.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
: "${WORKOS_V2_DIR:?source the isolated V2 fixture env first}"
: "${WORKOS_V2_NAMESPACE:?}"
: "${WORKOS_V2_DATABASE_URL:?}"
: "${WORKOS_V2_USER:?}"
: "${WORKOS_V2_GATEWAY_PORT:?}"
: "${WORKOS_V2_RUNTIME_PORT:?}"
case "$WORKOS_V2_DIR" in "$repo"/tmp/v2-completion.*) ;; *) echo 'expected an isolated V2 fixture directory' >&2; exit 1;; esac
case "$WORKOS_V2_NAMESPACE" in v2-completion-*) ;; *) echo 'expected the isolated V2 namespace' >&2; exit 1;; esac
image=golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514
docker image inspect "$image" >/dev/null
mkdir -p "$WORKOS_V2_DIR/artifacts" "$repo/tmp/go-build-cache"
compose() {
 docker compose -p "$WORKOS_V2_NAMESPACE" -f tools/v2-completion/compose.yaml -f tools/shared-desktop/compose.override.yaml "$@"
}
compose up -d --no-deps runtime
python3 - "$WORKOS_V2_DIR/run/artifact-admin.sock" "$WORKOS_V2_RUNTIME_PORT" <<'PY'
import socket, sys, time
for attempt in range(60):
    try:
        with socket.socket(socket.AF_UNIX) as s:
            s.settimeout(1); s.connect(sys.argv[1])
        with socket.create_connection(('127.0.0.1', int(sys.argv[2])), timeout=1):
            pass
        break
    except OSError:
        time.sleep(1)
else:
    raise SystemExit('runtime and artifact admin did not become ready')
PY
result=0
docker run --rm --network host --user "$WORKOS_V2_USER" \
 -e HOME=/tmp -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/tmp/go-cache -e CGO_ENABLED=0 \
 -e WORKOS_TEST_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" \
 -e WORKOS_TEST_RUNTIME_URL="http://127.0.0.1:$WORKOS_V2_RUNTIME_PORT" \
 -e WORKOS_SHARED_DESKTOP_DATABASE_URL="$WORKOS_V2_DATABASE_URL" \
 -e WORKOS_SHARED_DESKTOP_ARTIFACT_SOCKET="$WORKOS_V2_DIR/run/artifact-admin.sock" \
 -v "$repo:/workspace" -v "$WORKOS_V2_DIR:$WORKOS_V2_DIR" \
 -v workos-go-cache:/go/pkg/mod -v "$repo/tmp/go-build-cache:/tmp/go-cache" \
 -w /workspace golang:1.26.7-bookworm \
 go test -tags=integration,shareddesktopapps -count=1 -v -timeout=4m \
 -run '^TestSharedDesktopInstalledAppContinuity$' ./tests/integration \
 > "$WORKOS_V2_DIR/shared-desktop-apps.log" 2>&1 || result=$?
cat "$WORKOS_V2_DIR/shared-desktop-apps.log"
if [ "$result" -ne 0 ]; then compose logs --no-color runtime > "$WORKOS_V2_DIR/shared-desktop-apps-runtime.log" 2>&1 || true; fi
# Docker app networks use workos.runtime, unlike workspace execution networks.
# Keep them and the services alive for the enclosing gate's namespace cleanup.
printf 'Installed app continuity result=%s evidence=%s\n' "$result" "$WORKOS_V2_DIR/shared-desktop-apps.log"
exit "$result"
