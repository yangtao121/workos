#!/bin/sh
# The REAL-MODEL acceptance gate (A15) — OPERATOR-GATED, never part of the
# ordinary suite. This is the only sanctioned entry point for spending real
# DeepSeek API quota: it drives the harness-sessions flow (two native turns +
# usage + a real tool write) against the live DeepSeek API instead of the
# local fixture. It FAILS LOUDLY — never silently passes — whenever an
# operator precondition is missing:
#
#   1. WORKOS_REAL_DEEPSEEK=1 must be exported (explicit opt-in).
#   2. An ACTIVE deepseek provider-api-key.v1 credential must already live in
#      the Core Credential Vault over the workosctl admin socket. Store it
#      once with (never in YAML/git/logs):
#        printf '%s' 'the-real-key' | docker compose exec -T workos-core \
#          /bin/sh -c "/usr/local/bin/workosctl credential put \
#          --consumer deepseek --purpose provider-api-key.v1 \
#          --label 'real model acceptance' \
#          --idempotency-key real-model-$(date +%s)"
#   3. WORKOS_REAL_DEEPSEEK_BASE_URL (default https://api.deepseek.com) must
#      not be a loopback fixture origin.
#   4. The stack stays on the shared dev compose project; the gate creates
#      its own fresh, non-sensitive test project per run and never touches
#      existing projects.
#
# Expected evidence to record (the test prints the markers): session id,
# project id, both task ids, per-turn usage, and the evidence file path.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
fail() {
  echo "test-real-model-acceptance: BLOCKED - $1" >&2
  echo "This gate never silently passes; fix the precondition and re-run." >&2
  exit 1
}
if [ "${WORKOS_REAL_DEEPSEEK:-}" != "1" ]; then
  fail "WORKOS_REAL_DEEPSEEK=1 is not set. This flag is the operator's explicit authorization to spend real DeepSeek API quota on a fresh non-sensitive test project."
fi
base_url="${WORKOS_REAL_DEEPSEEK_BASE_URL:-https://api.deepseek.com}"
case "$base_url" in
  http://127.0.0.1*|http://localhost*|http://\[*:1]*) fail "WORKOS_REAL_DEEPSEEK_BASE_URL points at a loopback fixture ($base_url); the real-model gate must target the live API." ;;
esac
task_dir=$(mktemp -d "$repo/tmp/real-model.XXXXXX")
state_dir="$task_dir/session-state"
mkdir -p "$state_dir"
chmod 700 "$state_dir"
# The harness container runs as the image user (10001) and must write the
# session state tree; hand it over inside a throwaway root container.
docker run --rm -v "$state_dir:/s" busybox:latest chown -R 10001:10001 /s >/dev/null 2>&1
export WORKOS_HARNESS_SESSION_STATE_DIR="$state_dir"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    docker compose logs --no-color workos-core harness-host > "$task_dir/stack.log" 2>&1 || true
    printf 'real-model-acceptance diagnostics: %s\n' "$task_dir" >&2
  else
    printf 'real-model-acceptance: PASS (live DeepSeek API, two native turns, real tool writes, usage reported)\n'
    printf 'Record the printed session/task/usage markers and the evidence file in the task record (A15).\n'
    docker run --rm -v "$task_dir:/g" busybox:latest sh -c 'rm -rf /g/session-state' >/dev/null 2>&1 || true
    rm -rf "$task_dir"
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export WORKOS_UID="$(id -u)" WORKOS_GID="$(id -g)"
export WORKOS_DEEPSEEK_ENABLED=true
export WORKOS_DEEPSEEK_BASE_URL="$base_url"
# No --profile deepseek-fixture: this gate must never talk to the fixture.
compose() { docker compose -f compose.yaml -f deploy/compose.harness-sessions.yaml "$@"; }
compose up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway
cred_id="$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$2} /^consumer: /{consumer=$2} /^purpose: /{purpose=$2} /^status: /{status=$2; if (consumer=="deepseek" && purpose=="provider-api-key.v1" && status=="ACTIVE") { print id; exit }}')"
if [ -z "$cred_id" ]; then
  fail "no ACTIVE deepseek provider-api-key.v1 credential in the Core Credential Vault. Store the real key over the workosctl admin socket (see the header of this script), then re-run. Never paste the key into logs, YAML, or the task record."
fi
echo "using vault credential: $cred_id (secret never leaves the vault; the gate only prints its id)"
for attempt in $(seq 1 90); do
  code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:8080/workos.agent.v1.AgentSessionService/GetSession" 2>/dev/null || true)
  case "$code" in 200|400|401|403|404|409|422|429|500|501|503) break;; esac
  sleep 1
done
owner="01999999-9999-7999-8999-000000000b01"
device="01999999-9999-7999-8999-000000000b02"
if ! docker run --rm --network host --user 0:0 \
  -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod \
  -e GOPROXY=https://goproxy.cn,direct \
  -v "$repo:/workspace" -w /workspace -v workos-go-cache:/go/pkg/mod \
  -v "$state_dir:$state_dir" \
  -e WORKOS_REAL_MODEL_GATE_URL="http://127.0.0.1:8080" \
  -e WORKOS_REAL_MODEL_GATE_OWNER="$owner" \
  -e WORKOS_REAL_MODEL_GATE_DEVICE="$device" \
  -e WORKOS_REAL_MODEL_GATE_STATE_DIR="$state_dir" \
  -e WORKOS_REAL_MODEL_GATE_BASE_URL="$base_url" \
  golang:1.26.7-bookworm go test -tags='integration realmodelgate' -count=1 -run '^TestRealModelDeepSeekContinuousSession$' -v -timeout 25m ./tests/integration; then
  echo "real-model acceptance failed; diagnostics kept in $task_dir" >&2
  exit 1
fi
