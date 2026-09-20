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
capture_dir=${WORKOS_V2_CAPTURE_DIR:-"$WORKOS_V2_DIR/visuals"}
mkdir -p "$capture_dir"
docker run --rm --network host --user "$WORKOS_V2_USER" -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
 -e WORKOS_V2_CAPTURE_DIR=/workos-visual-capture -e WORKOS_V2_E2E=true -e WORKOS_V2_PROJECT_ID \
 -e PLAYWRIGHT_JSON_OUTPUT_NAME=/workos-gate/browser-results.json \
 -e WORKOS_E2E_URL="http://127.0.0.1:$WORKOS_V2_GATEWAY_PORT" -e WORKOS_E2E_OUTPUT_DIR=/tmp/v2-results \
 -v "$repo:/workspace" -v "$WORKOS_V2_DIR:/workos-gate" -v "$capture_dir:/workos-visual-capture" \
 -w /workspace/apps/desktop-web "${E2E_IMAGE:-workos-playwright:1.62.1}" \
 node node_modules/@playwright/test/cli.js test v2-completion --workers=1 --reporter=line,json
python3 - "$WORKOS_V2_DIR/browser-results.json" <<'PY'
import json,sys
report=json.load(open(sys.argv[1]))
stats=report['stats']
assert stats['expected'] > 0 and stats['skipped'] == 0 and stats['unexpected'] == 0 and stats['flaky'] == 0, stats
def specs(suites):
    for suite in suites:
        yield from suite.get('specs', [])
        yield from specs(suite.get('suites', []))
passed={s['title'] for s in specs(report['suites']) if s['ok']}
for size in ('1440x900','820x1180','390x844'):
    assert 'development states '+size in passed, 'missing visual acceptance: '+size
print('V2 browser and three-viewport visual acceptance: PASS')
PY
