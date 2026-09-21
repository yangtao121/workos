#!/bin/sh
set -eu
umask 077
compose() { docker compose -p "$WORKOS_V2_NAMESPACE" -f tools/v2-completion/compose.yaml "$@"; }
if [ "${WORKOS_REAL_NATIVE_AUTOMATION:-}" = 1 ]; then
 python3 tools/native-automation/seed.py
 export WORKOS_REAL_TEST_PATTERN='^TestRealModelDeepSeekNativeAutomation$'
fi
# The fixture credential is rotated only in this unique test Vault. Secret
# travels through stdin into the admin socket, never argv/environment/logs.
credential=$(sed -n 's/^id: //p' "$WORKOS_V2_DIR/credential-result.txt" | head -1)
test -n "$credential"
compose exec -T core workosctl credential rotate --credential "$credential" --expected-revision 1 --idempotency-key real-acceptance < "$WORKOS_REAL_DEEPSEEK_KEY_FILE" > "$WORKOS_V2_DIR/credential-rotation.txt"
proxy_pid=
cleanup() {
 result=$?
 trap - EXIT INT TERM
 if [ -n "$proxy_pid" ]; then kill "$proxy_pid" 2>/dev/null || true; wait "$proxy_pid" 2>/dev/null || true; fi
 compose exec -T core workosctl credential revoke --credential "$credential" --expected-revision 2 --idempotency-key acceptance-finished > "$WORKOS_V2_DIR/credential-revoked.txt" 2>/dev/null || true
 exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Refresh binding snapshots after rotation, before creating any real task.
python3 tools/real-model-acceptance/refresh_binding.py
compose stop model
python3 tools/real-model-acceptance/budget_proxy.py "$WORKOS_V2_MODEL_PORT" "${WORKOS_REAL_BUDGET_LEDGER:-$WORKOS_V2_DIR/budget.jsonl}" &
proxy_pid=$!
repo=$(pwd)
docker run --rm --network host --user "$WORKOS_V2_USER" --group-add "$WORKOS_V2_DOCKER_GID" -v /usr/bin/docker:/usr/local/bin/docker:ro -v /var/run/docker.sock:/var/run/docker.sock -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/tmp/go-cache -e GOPROXY=off -v "$repo:$repo" -w "$repo" -v workos-go-cache:/go/pkg/mod -v "$repo/tmp/go-build-cache:/tmp/go-cache" -e WORKOS_REAL_MODEL_GATE_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" -e WORKOS_REAL_MODEL_GATE_PROJECT="$WORKOS_V2_PROJECT_ID" -e WORKOS_REAL_MODEL_GATE_WORKSPACE="$WORKOS_V2_DIR/project" golang:1.26.7-bookworm go test -tags='integration realmodelgate' -count=1 -run "${WORKOS_REAL_TEST_PATTERN:-^TestRealModelDeepSeekContinuousSession$}" -v -timeout 20m ./tests/integration
printf 'Live DeepSeek acceptance: PASS. Budget ledger: %s\n' "${WORKOS_REAL_BUDGET_LEDGER:-$WORKOS_V2_DIR/budget.jsonl}"
