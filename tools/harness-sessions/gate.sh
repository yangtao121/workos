#!/bin/sh
# The harness continuous-session gate (ADR-0030, A04): the pinned official
# DeepSeek runtime with tools enabled runs as a persistent per-session child
# of harness-host; turn one drives a real bash tool write, turn two proves
# the same native context; refresh and replay keep the same session facts.
set -eu
umask 077
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
mkdir -p tmp
task_dir=$(mktemp -d "$repo/tmp/harness-sessions.XXXXXX")
state_dir="$task_dir/session-state"
mkdir -p "$state_dir"
chmod 700 "$state_dir"
# The harness container runs as the image user (10001) and must both read
# its mTLS material and write the session state tree; hand the state
# directory to that uid inside a throwaway root container.
docker run --rm -v "$state_dir:/s" busybox:latest chown -R 10001:10001 /s >/dev/null 2>&1
export WORKOS_HARNESS_SESSION_STATE_DIR="$state_dir"
stamp=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:16])')
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [ "$result" -ne 0 ]; then
    docker compose --profile deepseek-fixture logs --no-color workos-core harness-host > "$task_dir/stack.log" 2>&1 || true
    printf 'harness-sessions diagnostics: %s\n' "$task_dir" >&2
  fi
  # The gate shares the default compose project (like test-deepseek-fixture):
  # only the fixture profile is stopped; the shared stack keeps running.
  docker compose --profile deepseek-fixture stop deepseek-api-fixture >/dev/null 2>&1 || true
  if [ "$result" -eq 0 ]; then printf 'harness-sessions: PASS (official runtime, two native turns, real tools)\n'; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
export WORKOS_UID="$(id -u)" WORKOS_GID="$(id -g)"
export WORKOS_DEEPSEEK_ENABLED=true
export WORKOS_DEEPSEEK_BASE_URL=http://127.0.0.1:18086
compose() { docker compose -f compose.yaml -f deploy/compose.harness-sessions.yaml --profile deepseek-fixture "$@"; }
compose up -d --build --force-recreate postgres bootstrap workos-core harness-host workos-gateway deepseek-api-fixture
# Store the fixture credential over the admin socket (same as the
# test-deepseek-fixture gate; never a real key).
cred_id="$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$2} /^consumer: /{consumer=$2} /^status: /{status=$2; if (consumer=="deepseek" && status=="ACTIVE") { print id; exit }}')"
if [ -z "$cred_id" ]; then
  printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential put --consumer deepseek --purpose provider-api-key.v1 --label 'session fixture' --idempotency-key 'session-fixture-$stamp'" >/dev/null
else
  cred_rev="$(docker compose exec -T workos-core /usr/local/bin/workosctl credential list 2>/dev/null | awk '/^id: /{id=$2} /^consumer: /{consumer=$2} /^revision: /{revision=$2} /^status: /{status=$2; if (consumer=="deepseek" && status=="ACTIVE") { print revision; exit }}')"
  printf '%s' 'workos-fixture-only-not-a-real-key' | docker compose exec -T workos-core /bin/sh -c "/usr/local/bin/workosctl credential rotate --credential '$cred_id' --expected-revision '$cred_rev' --label 'session fixture' --idempotency-key 'session-fixture-reseal-$stamp'" >/dev/null
fi
for attempt in $(seq 1 90); do
  code=$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' --max-time 2 -H 'Content-Type: application/json' -d '{}' "http://127.0.0.1:8080/workos.agent.v1.AgentSessionService/GetSession" 2>/dev/null || true)
  case "$code" in 200|400|401|403|404|409|422|429|500|501|503) break;; esac
  sleep 1
done
owner="01999999-9999-7999-8999-000000000b01"
device="01999999-9999-7999-8999-000000000b02"
# Root inside the throwaway test container: the session state tree belongs
# to the harness image user (10001, mode 0700) and the assertions must read
# the native tool's real file effects.
docker run --rm --network host --user 0:0 \
  -e HOME=/tmp -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod \
  -e GOPROXY=https://goproxy.cn,direct \
  -v "$repo:/workspace" -w /workspace -v workos-go-cache:/go/pkg/mod \
  -v "$state_dir:$state_dir" \
  -e WORKOS_HARNESS_SESSION_GATE_URL="http://127.0.0.1:8080" \
  -e WORKOS_HARNESS_SESSION_GATE_OWNER="$owner" \
  -e WORKOS_HARNESS_SESSION_GATE_DEVICE="$device" \
  -e WORKOS_HARNESS_SESSION_GATE_STATE_DIR="$state_dir" \
  golang:1.26.7-bookworm go test -tags='integration harnesssessiongate' -count=1 -run '^TestHarnessContinuousSessions$' -v ./tests/integration
