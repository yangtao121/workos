#!/bin/sh
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM
cert_dir=$temp_dir/tls

"$here/cert.sh" 192.168.5.5 "$cert_dir" >"$temp_dir/issue.log" 2>&1
test "$(stat -c %a "$cert_dir")" = 700
test "$(stat -c %a "$cert_dir/ca.key")" = 600
test "$(stat -c %a "$cert_dir/leaf.key")" = 600
openssl verify -purpose sslserver -CAfile "$cert_dir/ca.crt" "$cert_dir/leaf.crt" >/dev/null
openssl verify -purpose sslserver -verify_ip 192.168.5.5 -CAfile "$cert_dir/ca.crt" "$cert_dir/leaf.crt" >/dev/null
if openssl verify -purpose sslserver -verify_ip 192.168.5.6 -CAfile "$cert_dir/ca.crt" "$cert_dir/leaf.crt" >/dev/null 2>&1; then
    echo 'test.sh: certificate verified for the wrong IP' >&2
    exit 1
fi
openssl x509 -in "$cert_dir/leaf.crt" -noout -ext subjectAltName | grep -Fq 'IP Address:192.168.5.5'
ca_before=$(openssl x509 -in "$cert_dir/ca.crt" -noout -fingerprint -sha256)
leaf_before=$(openssl x509 -in "$cert_dir/leaf.crt" -noout -fingerprint -sha256)

"$here/cert.sh" 192.168.5.5 "$cert_dir" >"$temp_dir/reuse.log" 2>&1
test "$ca_before" = "$(openssl x509 -in "$cert_dir/ca.crt" -noout -fingerprint -sha256)"
test "$leaf_before" = "$(openssl x509 -in "$cert_dir/leaf.crt" -noout -fingerprint -sha256)"

"$here/cert.sh" 192.168.5.6 "$cert_dir" >"$temp_dir/new-ip.log" 2>&1
test "$ca_before" = "$(openssl x509 -in "$cert_dir/ca.crt" -noout -fingerprint -sha256)"
leaf_after=$(openssl x509 -in "$cert_dir/leaf.crt" -noout -fingerprint -sha256)
test "$leaf_before" != "$leaf_after"
openssl x509 -in "$cert_dir/leaf.crt" -noout -ext subjectAltName | grep -Fq 'IP Address:192.168.5.6'
openssl verify -purpose sslserver -CAfile "$cert_dir/ca.crt" "$cert_dir/leaf.crt" >/dev/null

# An IP that merely prefixes the existing SAN must still trigger reissuance.
"$here/cert.sh" 192.168.5.60 "$cert_dir" >"$temp_dir/prefix-ip.log" 2>&1
openssl x509 -in "$cert_dir/leaf.crt" -noout -ext subjectAltName | grep -Fq 'IP Address:192.168.5.60'
leaf_prefix=$(openssl x509 -in "$cert_dir/leaf.crt" -noout -fingerprint -sha256)
"$here/cert.sh" 192.168.5.6 "$cert_dir" >"$temp_dir/restore-ip.log" 2>&1
openssl x509 -in "$cert_dir/leaf.crt" -noout -ext subjectAltName | grep -Fq 'IP Address:192.168.5.6'
leaf_restored=$(openssl x509 -in "$cert_dir/leaf.crt" -noout -fingerprint -sha256)
test "$leaf_prefix" != "$leaf_restored"
"$here/cert.sh" 192.168.5.6 "$cert_dir" --renew-leaf >"$temp_dir/renew.log" 2>&1
test "$leaf_restored" != "$(openssl x509 -in "$cert_dir/leaf.crt" -noout -fingerprint -sha256)"
test "$ca_before" = "$(openssl x509 -in "$cert_dir/ca.crt" -noout -fingerprint -sha256)"
if "$here/cert.sh" '192.168.5.6;touch /tmp/workos-bad-ip' "$temp_dir/rejected" >"$temp_dir/bad-ip.log" 2>&1; then
    echo 'test.sh: unsafe IP was accepted' >&2
    exit 1
fi
test ! -e "$temp_dir/rejected"
mkdir "$temp_dir/incomplete"
cp "$cert_dir/ca.key" "$temp_dir/incomplete/ca.key"
if "$here/cert.sh" 192.168.5.6 "$temp_dir/incomplete" >"$temp_dir/incomplete.log" 2>&1; then
    echo 'test.sh: incomplete CA was silently replaced' >&2
    exit 1
fi
test ! -e "$temp_dir/incomplete/ca.crt"

WORKOS_LAN_IP=192.168.5.6 \
WORKOS_LAN_TLS_DIR="$cert_dir" \
WORKOS_LAN_UID=$(id -u) \
WORKOS_LAN_GID=$(id -g) \
    docker compose -f "$repo/compose.yaml" -f "$repo/deploy/compose.observability.yaml" \
    -f "$repo/deploy/compose.lan-https.yaml" \
    config --format json >"$temp_dir/compose.json"
python3 - "$temp_dir/compose.json" "$cert_dir" <<'PY'
import json
import os
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    services = json.load(source)["services"]
gateway = services["workos-gateway"]
environment = gateway["environment"]
assert environment["WORKOS_HTTP_ADDRESS"] == "192.168.5.6:8443"
assert environment["WORKOS_AUTH_PUBLIC_ORIGIN"] == "https://192.168.5.6:8443"
assert environment["WORKOS_AUTH_MODE"] == "password"
assert environment["WORKOS_DEV_AUTH_BYPASS"] == "false"
assert gateway["user"] == f"{os.getuid()}:{os.getgid()}"
sources = {volume["source"] for volume in gateway["volumes"]}
assert sources == {sys.argv[2] + "/leaf.crt", sys.argv[2] + "/leaf.key"}
assert services["postgres"]["command"] == ["postgres", "-c", "listen_addresses=127.0.0.1"]
assert services["otel-collector"]["network_mode"] == "host"
for name in ("workos-core", "harness-host", "runtime-host", "reliability-host", "indexer"):
    assert services[name]["environment"]["WORKOS_HTTP_ADDRESS"].startswith("127.0.0.1:")
PY

if [ -f "$repo/deploy/compose.greenfield-resident.yaml" ]; then
    WORKOS_LAN_IP=192.168.5.6 \
    WORKOS_LAN_TLS_DIR="$cert_dir" \
    WORKOS_LAN_UID=$(id -u) \
    WORKOS_LAN_GID=$(id -g) \
    WORKOS_DOCKER_GID=1001 \
    WORKOS_GREENFIELD_RENDER_GID=1002 \
    WORKOS_GREENFIELD_IPC_ROOT="$temp_dir/greenfield-ipc" \
    WORKOS_WORKSPACE_ROOTS="$temp_dir/workspaces" \
        docker compose -f "$repo/compose.yaml" -f "$repo/deploy/compose.observability.yaml" \
        -f "$repo/deploy/compose.greenfield-resident.yaml" \
        -f "$repo/deploy/compose.lan-https.yaml" \
        config --format json >"$temp_dir/resident-compose.json"
    python3 - "$temp_dir/resident-compose.json" "$cert_dir" "$temp_dir" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    services = json.load(source)["services"]
gateway = services["workos-gateway"]
runtime = services["runtime-host"]
assert runtime["environment"]["WORKOS_RUNTIME_NATIVE_ENGINE"] == "greenfield"
assert runtime["environment"]["WORKOS_RUNTIME_NATIVE_GREENFIELD_CHILD_IMAGE"] == "workos-greenfield-child:dev"
assert runtime["environment"]["WORKOS_RUNTIME_NATIVE_GREENFIELD_IPC_ROOT"] == sys.argv[3] + "/greenfield-ipc"
runtime_sources = {volume["source"] for volume in runtime["volumes"]}
assert "/var/run/docker.sock" in runtime_sources
assert sys.argv[3] + "/greenfield-ipc" in runtime_sources
assert sys.argv[3] + "/workspaces" in runtime_sources
assert not any(source.startswith(sys.argv[2]) for source in runtime_sources)
assert {volume["source"] for volume in gateway["volumes"]} == {
    sys.argv[2] + "/leaf.crt", sys.argv[2] + "/leaf.key"
}
assert "/var/run/docker.sock" not in {volume["source"] for volume in gateway["volumes"]}
assert "greenfield-child" not in services
PY
fi

grep -Fq 'endpoint: 127.0.0.1:4318' "$repo/deploy/otel-collector.yaml"
echo 'test-lan-https: PASS (CA persistence, leaf renewal, permissions, SAN, Compose binds)'
