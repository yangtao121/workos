#!/bin/sh
# F26 regression: mutable grants + Web Bundle surfaces against the isolated
# v2-completion fixture (prepare-only mode). Never touches the shared dev
# stack; every command's exit status is recorded before the private stack is
# torn down. Reproduce with: sh tools/v2-p3-delivery/f26-grants-webbundle.sh
set -eu
repo=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
cd "$repo"
task_dir=$(mktemp -d "$repo/tmp/v2-p3-f26.XXXXXX")
result=0
record() { printf '%s exit=%s\n' "$1" "$2" | tee -a "$task_dir/results.txt"; }

teardown() {
  exit_status=$?
  trap - EXIT INT TERM
  [ "$exit_status" -eq 0 ] || result=$exit_status
  if [ "${WORKOS_P3_F26_KEEP:-}" = 1 ]; then exit "$result"; fi
  if [ -n "${WORKOS_V2_NAMESPACE:-}" ]; then
    docker compose -p "$WORKOS_V2_NAMESPACE" -f tools/v2-completion/compose.yaml down --timeout 5 >/dev/null 2>&1 || result=1
    docker ps -aq --filter "label=workos.runtime=$WORKOS_V2_NAMESPACE" | xargs -r docker rm -f >/dev/null 2>&1 || true
    docker rm -f "$WORKOS_V2_NAMESPACE-db" >/dev/null 2>&1 || result=1
  fi
  printf 'v2-p3-f26: result=%s evidence=%s\n' "$result" "$task_dir"
  exit "$result"
}
trap teardown EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 1) Isolated six-process stack, fixture credentials only.
status=0
WORKOS_V2_KEEP=1 WORKOS_V2_PREPARE_ONLY=1 sh tools/v2-completion/gate.sh > "$task_dir/prepare.log" 2>&1 || status=$?
record prepare-only "$status"
if [ "$status" -ne 0 ]; then result=1; exit 1; fi
line=$(tail -n 1 "$task_dir/prepare.log")
case "$line" in
  "V2 fixture result=0 evidence="*) ;;
  *) printf 'unexpected prepare trailer: %s\n' "$line" >&2; result=1; exit 1 ;;
esac
# The persisted env carries every WORKOS_V2_* fact including ports, user,
# docker gid, database URL and the fixture namespace.
# shellcheck disable=SC1090
. "${line##*evidence=}/env"

run_go() {
  docker run --rm --network host --user "$WORKOS_V2_USER" --group-add "$WORKOS_V2_DOCKER_GID" \
    -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/tmp/go-cache -e GOPROXY=off \
    -e WORKOS_TEST_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" \
    -e WORKOS_TEST_RUNTIME_URL="http://127.0.0.1:$WORKOS_V2_RUNTIME_PORT" \
    -e WORKOS_TEST_DATABASE_URL="$WORKOS_V2_DATABASE_URL" \
    -e WORKOS_TEST_OWNER_ID=01999999-9999-7999-8999-000000000b01 \
    -e WORKOS_WORKSPACE_TEST_ROOT="$WORKOS_V2_DIR/project" \
    -e WORKOS_RUNTIME_CONTAINER_NAMESPACE="$WORKOS_V2_NAMESPACE-f26" \
    -v "$repo:$repo" -v /var/run/docker.sock:/var/run/docker.sock \
    -v workos-go-cache:/go/pkg/mod -v "$repo/tmp/go-build-cache:/tmp/go-cache" -w "$repo" \
    golang:1.26.7-bookworm "$@"
}

# 2) Existing parameterized regression suite (grants vertical slice, session
# tool authorization/revocation, workspace host adapters).
status=0
sh tools/v2-completion/regression.sh > "$task_dir/regression.log" 2>&1 || status=$?
record regression.sh "$status"
[ "$status" -eq 0 ] || result=1

# 3) The two grants/web-bundle chains that the p3 closeout must not regress.
status=0
run_go go test -tags integration -count=1 \
  -run 'TestMutableGrantRevocationChain|TestWebBundleSurfaceVerticalSlice|TestAppVersionTransitionAndRollback|TestRuntimeStoreOutageIsUnavailableNotMissing|TestCoreResolverDependencyOutageExposesUnavailable|TestRuntimeAssetOutageServes503' -v ./tests/integration \
  > "$task_dir/grants-webbundle-go.log" 2>&1 || status=$?
record grants-webbundle-go "$status"
if [ "$status" -eq 0 ]; then
  for test_name in TestMutableGrantRevocationChain TestWebBundleSurfaceVerticalSlice TestAppVersionTransitionAndRollback TestRuntimeStoreOutageIsUnavailableNotMissing TestCoreResolverDependencyOutageExposesUnavailable TestRuntimeAssetOutageServes503; do
    grep -q -- "--- PASS: $test_name " "$task_dir/grants-webbundle-go.log" || { record "$test_name-zeromatch" 1; result=1; }
  done
  if grep -q -- '--- SKIP:' "$task_dir/grants-webbundle-go.log"; then record grants-webbundle-go-skip 1; result=1; fi
else
  result=1
fi

# 4) Desktop-facing behavior against the same isolated gateway.
status=0
docker run --rm --network host --user "$WORKOS_V2_USER" \
  -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
  -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" \
  -e WORKOS_E2E_OUTPUT_DIR=/tmp/v2-p3-f26-results \
  -v "$repo:/workspace" -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 node node_modules/@playwright/test/cli.js \
  test mutable-grants.spec.ts web-bundle-surface.spec.ts app-version-rollback.spec.ts --workers=1 \
  > "$task_dir/playwright.log" 2>&1 || status=$?
record playwright-grants-webbundle "$status"
if [ "$status" -eq 0 ]; then
  grep -Eq '[1-9][0-9]* passed' "$task_dir/playwright.log" \
    || { record playwright-zeromatch 1; result=1; }
else
  result=1
fi

exit "$result"
