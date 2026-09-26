#!/bin/sh
# Check source files without walking private runtime directories. Prettier's
# directory expansion opens .workos before applying ignore patterns, but the
# resident compositor IPC root is deliberately owned by its separate UID.
set -eu

find . \( \
    -path './.git' -o \
    -path './.workos' -o \
    -path './data' -o \
    -path './tmp' -o \
    -path './.cache' -o \
    -path './.tools' -o \
    -name node_modules -o \
    -name dist -o \
    -name build -o \
    -name coverage -o \
    -name .pnpm-store -o \
    -name playwright-report -o \
    -name test-results \
\) -prune -o -type f -print0 |
    xargs -0 -r corepack pnpm exec prettier --check --ignore-unknown
