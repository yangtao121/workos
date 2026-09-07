#!/bin/sh
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
cache=${WORKOS_EMBEDDING_CACHE_DIR:-$repo/tmp/embedding-model}
python3 tools/embedding/fetch.py "$cache"
docker build --network host --build-arg HTTPS_PROXY --build-arg HTTP_PROXY \
  -t workos-embedding:dev --target embedding-runtime .
directory=$(mktemp -d "$repo/tmp/embedding-postgres.XXXXXX")
trap 'rm -f "$directory/integration.test"' EXIT
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod \
  -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
  -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace \
  golang:1.26.7-bookworm go test -c -race -tags integration \
  -o "/workspace/${directory#"$repo/"}/integration.test" ./tests/integration
# Host networking reaches only the local scratch PostgreSQL fixture. The separate
# test-local-embedding gate proves that inference itself needs no network.
if ! docker run --rm --network host --read-only --cpus 2 --memory 2g \
  --user "$(id -u):$(id -g)" --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -e WORKOS_LOCAL_EMBEDDING_MODEL_DIR=/model \
  -v "$cache:/model:ro" -v "$repo:/workspace:ro" \
  -w /workspace/tests/integration workos-embedding:dev \
  "/workspace/${directory#"$repo/"}/integration.test" -test.v -test.run '^TestOfflineModelPostgres$' > "$directory/result.log" 2>&1; then
  cat "$directory/result.log"
  exit 1
fi
cat "$directory/result.log"
if grep -q -- '--- SKIP:' "$directory/result.log"; then
  echo 'real model PostgreSQL test was skipped' >&2
  exit 1
fi
echo 'test-model-postgres: PASS (real pinned model, pgvector, cross-language search and restart backfill)'
