#!/bin/sh
# The existing isolated six-process fixture owns all resources and cleanup.
set -eu
export WORKOS_SHARED_DESKTOP_AUTOMATION=1
export E2E_IMAGE=${E2E_IMAGE:-workos-playwright-shared:1.62.1}
exec sh tools/v2-completion/gate.sh
