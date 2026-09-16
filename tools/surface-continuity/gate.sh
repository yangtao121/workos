#!/bin/sh
# ADR-0032 supersedes the unbound-workspace/host-process gate. Keep the public
# entry point, exercising the current contract on an isolated six-process stack.
set -eu
exec sh "$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)/tools/v2-completion/gate.sh"
