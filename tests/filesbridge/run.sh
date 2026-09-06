#!/bin/sh
set -eu
fixture=$(mktemp -d /tmp/workos-filesbridge.XXXXXX)
cleanup() {
  docker compose up -d --no-deps --force-recreate runtime-host >/dev/null 2>&1 || true
  docker run --rm --user 0 -v "$fixture:/fixture" node:24.19.0-bookworm-slim rm -rf /fixture/workspace
  rm -rf "$fixture"
}
trap cleanup EXIT HUP INT TERM
docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD:/workspace:ro" -v "$fixture:/fixture" node:24.19.0-bookworm-slim node /workspace/tools/filesbridge/seed.mjs
docker run --rm --user 0 -v "$fixture:/fixture" node:24.19.0-bookworm-slim chown -R 10001:10001 /fixture/workspace
cat > "$fixture/runtime.yaml" <<YAML
services:
  runtime-host:
    environment:
      WORKOS_CONFIG_FILE: /run/workos/files.yaml
    volumes:
      - "$fixture/config.yaml:/run/workos/files.yaml:ro"
      - "$fixture/workspace:/workspaces"
YAML
docker compose -f compose.yaml -f "$fixture/runtime.yaml" up -d --no-deps --force-recreate runtime-host
for i in $(seq 1 50); do
  if curl -fsS http://127.0.0.1:8083/readyz >/dev/null 2>&1; then break; fi
  sleep 1
done
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD:/workspace" -w /workspace/apps/desktop-web node:24.19.0-bookworm-slim corepack pnpm exec vite build --config e2e/fixtures/files-bridge.config.ts
docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright -e WORKOS_FILES_FIXTURE=/fixture/state.json -e WORKOS_FILES_CAPTURE_DIR="${WORKOS_FILES_CAPTURE_DIR:-}" -e WORKOS_FILES_BEFORE_DIR="${WORKOS_FILES_BEFORE_DIR:-}" -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results -v "$PWD:/workspace" -v "$fixture:/fixture:ro" -w /workspace/apps/desktop-web workos-playwright:1.62.1 pnpm exec playwright test app-files.spec.ts
docker run --rm --user 0 -v "$fixture:/fixture:ro" node:24.19.0-bookworm-slim node -e 'const fs=require("node:fs");if(fs.readFileSync("/fixture/workspace/notes.txt","utf8")!=="Saved in WorkOS")throw new Error("workspace write did not persist");'
echo 'test-app-files: PASS'
