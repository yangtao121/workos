#!/bin/sh
set -eu
compose() { docker compose -p "$WORKOS_V2_NAMESPACE" -f tools/v2-completion/compose.yaml "$@"; }
python3 tools/v2-completion/journey.py first
compose restart core harness gateway
python3 tools/v2-completion/prepare.py ready >/dev/null
python3 tools/v2-completion/journey.py second
python3 tools/v2-completion/continuity.py before
compose restart runtime
python3 tools/v2-completion/prepare.py runtime-ready
python3 tools/v2-completion/continuity.py after
sh tools/v2-completion/regression.sh
repo=$(pwd)
docker run --rm --network host --user "$WORKOS_V2_USER" -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright -e WORKOS_V2_CAPTURE_DIR -e WORKOS_V2_E2E=true -e WORKOS_V2_PROJECT_ID -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" -e WORKOS_E2E_OUTPUT_DIR=/tmp/v2-results -v "$repo:/workspace" -w /workspace/apps/desktop-web "${E2E_IMAGE:-workos-playwright:1.62.1}" node node_modules/@playwright/test/cli.js test v2-completion --workers=1
