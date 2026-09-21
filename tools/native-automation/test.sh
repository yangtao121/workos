#!/bin/sh
set -eu
repo=$(pwd)
compose() { docker compose -p "$WORKOS_V2_NAMESPACE" -f tools/v2-completion/compose.yaml "$@"; }
python3 tools/native-automation/seed.py
python3 tools/native-automation/journey.py
compose restart core harness gateway
python3 tools/v2-completion/prepare.py ready
export WORKOS_NATIVE_SESSION_ID=$(python3 -c 'import json,os;print(json.load(open(os.environ["WORKOS_V2_DIR"]+"/native-automation.json"))["session"])')
capture_dir=${WORKOS_NATIVE_CAPTURE_DIR:-"$WORKOS_V2_DIR/visuals"}
mkdir -p "$capture_dir"
docker run --rm --network host --user "$WORKOS_V2_USER" -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
 -e WORKOS_NATIVE_SESSION_ID -e WORKOS_NATIVE_E2E=true -e WORKOS_NATIVE_CAPTURE_DIR=/workos-visual-capture \
 -e PLAYWRIGHT_JSON_OUTPUT_NAME=/workos-gate/native-browser-results.json \
 -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" -e WORKOS_E2E_OUTPUT_DIR=/tmp/native-results \
 -v "$repo:/workspace" -v "$WORKOS_V2_DIR:/workos-gate" -v "$capture_dir:/workos-visual-capture" \
 -w /workspace/apps/desktop-web "${E2E_IMAGE:-workos-playwright:1.62.1}" \
 node node_modules/@playwright/test/cli.js test native-automation --workers=1 --reporter=line,json
python3 - "$WORKOS_V2_DIR/native-browser-results.json" <<'PY'
import json,sys
report=json.load(open(sys.argv[1])); stats=report['stats']
assert stats['expected']==4 and stats['skipped']==0 and stats['unexpected']==0 and stats['flaky']==0, stats
print('NATIVE_BROWSER_AND_THREE_VIEWPORTS_PASS')
PY
python3 tools/native-automation/exceptions.py
printf 'Native automation six-process acceptance: PASS\n'
