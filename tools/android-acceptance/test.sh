#!/bin/sh
set -eu
docker image inspect workos-network-test:local >/dev/null 2>&1 || docker build -t workos-network-test:local -f tools/network-continuity/Dockerfile tools/network-continuity
docker image inspect workos-android-test:local >/dev/null 2>&1 || docker build -t workos-android-test:local -f tools/android-acceptance/Dockerfile tools/android-acceptance
docker run --rm --user "$WORKOS_V2_USER" -e HOME=/tmp -v "$(pwd):/workspace" -w /workspace node:24.19.0-bookworm-slim sh -ec 'corepack pnpm --filter @workos/mobile-shell build; corepack pnpm --filter @workos/mobile-shell exec cap sync android'
exec python3 tools/android-acceptance/fixture.py
