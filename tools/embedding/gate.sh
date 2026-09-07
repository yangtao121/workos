#!/bin/sh
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo"
cache=${WORKOS_EMBEDDING_CACHE_DIR:-$repo/tmp/embedding-model}
python3 tools/embedding/fetch.py "$cache"
docker build --network host --build-arg HTTPS_PROXY --build-arg HTTP_PROXY \
  -t workos-embedding:dev -f deploy/embedding/Dockerfile .
directory=$(mktemp -d "$repo/tmp/embedding-gate.XXXXXX")
trap 'rm -f "$directory/embedding.test"' EXIT
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e GOPATH=/tmp/workos-go -e GOMODCACHE=/go/pkg/mod \
  -e GOCACHE=/workspace/tmp/go-build-cache -e GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
  -v workos-go-cache:/go/pkg/mod -v "$repo:/workspace" -w /workspace \
  golang:1.26.7-bookworm sh -c 'go test -race -count=1 "$2" && go test -c -race -o "$1" "$2"' \
  sh "/workspace/${directory#"$repo/"}/embedding.test" ./internal/indexer/adapters/localembedding
docker run --rm --network none --read-only --cpus 2 --memory 2g \
  --user "$(id -u):$(id -g)" --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -e WORKOS_LOCAL_EMBEDDING_MODEL_DIR=/model \
  -v "$cache:/model:ro" -v "$repo:/workspace:ro" \
  -w /workspace/internal/indexer/adapters/localembedding workos-embedding:dev \
  "/workspace/${directory#"$repo/"}/embedding.test" -test.v -test.run TestPinnedOfflineModel
echo 'test-local-embedding: PASS (offline CPU model and child failure matrix; storage/search integration is separate)'
