#!/bin/sh
# A15 uses an isolated six-process stack and an acceptance-only budget proxy.
set -eu
umask 077
cd "$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
if [ "${WORKOS_REAL_DEEPSEEK:-}" != 1 ] || [ ! -f "${WORKOS_REAL_DEEPSEEK_KEY_FILE:-/nonexistent}" ]; then
 echo 'A15 BLOCKED: set WORKOS_REAL_DEEPSEEK=1 and WORKOS_REAL_DEEPSEEK_KEY_FILE to an owner-only secret file. Total reservation ceiling: CNY 1.90.' >&2
 exit 1
fi
python3 - "$WORKOS_REAL_DEEPSEEK_KEY_FILE" <<'PY'
import os,stat,sys
s=os.lstat(sys.argv[1])
assert stat.S_ISREG(s.st_mode) and s.st_uid==os.getuid() and not s.st_mode & 0o077, 'secret file must be owner-only and not a symlink'
PY
export WORKOS_V2_REAL=1
exec sh tools/v2-completion/gate.sh
