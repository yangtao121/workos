#!/bin/sh
set -eu
export WORKOS_NETWORK_AUTOMATION=1
exec sh "$(dirname "$0")/../v2-completion/gate.sh"
