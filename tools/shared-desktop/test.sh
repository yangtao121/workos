#!/bin/sh
set -eu
repo=$(pwd)
mkdir -p "$WORKOS_V2_DIR/shared-visuals"
compose() { docker compose -p "$WORKOS_V2_NAMESPACE" -f tools/v2-completion/compose.yaml "$@"; }
docker run --rm --network host --user "$WORKOS_V2_USER" -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
 -e WORKOS_SHARED_DESKTOP_E2E=true -e WORKOS_V2_PROJECT_ID -e WORKOS_CAPTURE_DIR=/workos-gate/shared-visuals \
 -e PLAYWRIGHT_JSON_OUTPUT_NAME=/workos-gate/shared-browser-results.json \
 -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" -e WORKOS_E2E_OUTPUT_DIR=/workos-gate/playwright \
 -v "$repo:/workspace" -v "$WORKOS_V2_DIR:/workos-gate" \
 -w /workspace/apps/desktop-web "$E2E_IMAGE" \
 node node_modules/@playwright/test/cli.js test --config playwright.shared.config.ts --reporter=line,json
python3 - "$WORKOS_V2_DIR/shared-browser-results.json" <<'PY'
import json,sys
stats=json.load(open(sys.argv[1]))['stats']
assert stats['expected'] >= 20 and stats['skipped'] == 0 and stats['unexpected'] == 0 and stats['flaky'] == 0,stats
print('Shared desktop Chromium/WebKit real-stack gate: PASS', stats)
PY
sh tools/shared-desktop/apps.sh
# Persisted desktop reference facts survive real Core and Gateway restarts.
python3 tools/shared-desktop/restart.py seed
compose restart core gateway
python3 tools/v2-completion/prepare.py ready >/dev/null
python3 tools/shared-desktop/restart.py verify
