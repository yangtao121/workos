#!/bin/sh
set -eu
docker image inspect workos-network-test:local >/dev/null 2>&1 || docker build -t workos-network-test:local -f tools/network-continuity/Dockerfile .
exec python3 tools/network-continuity/topology.py
