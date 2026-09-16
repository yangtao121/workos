#!/bin/sh
set -eu
repo=$(pwd)
run_go() {
 docker run --rm --network host --user "$WORKOS_V2_USER" --group-add "$WORKOS_V2_DOCKER_GID" -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/tmp/go-cache -e GOPROXY=off -e WORKOS_TEST_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" -e WORKOS_TEST_DATABASE_URL="$WORKOS_V2_DATABASE_URL" -e WORKOS_TEST_OWNER_ID=01999999-9999-7999-8999-000000000b01 -e WORKOS_WORKSPACE_TEST_ROOT="$WORKOS_V2_DIR/project" -e WORKOS_RUNTIME_CONTAINER_NAMESPACE="$WORKOS_V2_NAMESPACE-regression" -v "$repo:$repo" -v /var/run/docker.sock:/var/run/docker.sock -v workos-go-cache:/go/pkg/mod -v "$repo/tmp/go-build-cache:/tmp/go-cache" -w "$repo" golang:1.26.7-bookworm "$@"
}
run_go go test -tags integration -count=1 -run 'TestSessionTransactionsAndInterruptedLease|TestSessionToolAuthorizationAndRevocation|TestPtyContainerRestartKeepsIdentity|TestPtyContainerReadonlyAndRevocation|TestPreviewRealProcessContinuityAndRevocation|TestAppBridgeVerticalSlice|TestAppArtifacts$|TestProjectAppInstallationVerticalSlice|TestMutableProjectAppGrantsVerticalSlice|TestAppAgentRequireApprovalVerticalSlice|TestAppAgentRejectNeverExecutes|TestTaskSubmissionIdentityArbitratesConcurrentInputs' -v ./tests/integration
run_go go test -count=1 -v ./internal/runtime/workspacehost/adapters/...
