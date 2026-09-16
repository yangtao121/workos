#!/bin/sh
# C00 host capability probe for the V2 P3 formal Docker App runner.
#
# Probes, in an isolated label namespace only this script creates:
#   1. docker/kernel/cgroup facts and the pinned base image digest
#   2. pull=never fail-closed behavior
#   3. an internal network: container reachable from the host netns by bridge
#      IP, container egress kernel-unreachable, external DNS fails,
#      port publishing NOT supported on internal networks (recorded fact)
#   4. a read-only /app bind mount running a Go HTTP server; swapping the
#      bundle changes the HTTP response (real package-swap evidence)
#   5. cgroup v2 readback: memory.max, memory.high, pids.max, cpu.max
#   6. capability drop / no-new-privileges / read-only rootfs enforcement
#   7. label-based container ownership survives client restarts
#
# Everything created here is labeled workos.probe=<stamp>; cleanup removes
# exactly those containers/networks and the private temp dir. Set
# WORKOS_P3_KEEP=1 to keep the work dir for diagnostics.
set -eu

KEEP=${WORKOS_P3_KEEP:-0}
STAMP=$(date -u +%Y%m%d%H%M%S)-$$
GATE_DIR=$(mktemp -d /tmp/workos-p3-probe.XXXXXX)
NET_NAME=workos-p3-probe-$STAMP
LABEL="workos.probe=$STAMP"
BASE_TAG=golang:1.26.7-bookworm
APP_LABEL_OWNER=workos.owner=runtime-app-probe

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "PROBE FAILED (status $status); leftover labeled containers:" >&2
    docker ps -a --filter "label=$LABEL" --format '{{.ID}} {{.Names}} {{.Status}}' >&2 || true
    echo "workdir kept for diagnosis: $GATE_DIR" >&2
  fi
  docker rm -f $(docker ps -aq --filter "label=$LABEL") >/dev/null 2>&1 || true
  docker network rm "$NET_NAME" >/dev/null 2>&1 || true
  if [ "$KEEP" = "1" ] && [ "$status" -eq 0 ]; then
    echo "kept probe dir: $GATE_DIR"
  else
    rm -rf "$GATE_DIR"
  fi
}
trap cleanup EXIT INT TERM

pass() { printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; FAILED=1; }
note() { printf 'NOTE %s\n' "$1"; }
FAILED=0

echo "=== 1. versions and pinned base image ==="
DOCKER_VERSION=$(docker version --format '{{.Client.Version}}/{{.Server.Version}}')
KERNEL=$(uname -r)
CGROUP_FS=$(stat -fc %T /sys/fs/cgroup)
echo "docker=$DOCKER_VERSION kernel=$KERNEL cgroup_fs=$CGROUP_FS"
BASE_DIGEST=$(docker image inspect "$BASE_TAG" --format '{{index .RepoDigests 0}}')
echo "base_image=$BASE_DIGEST"
case "$BASE_DIGEST" in
  *@sha256:[0-9a-f]*) pass "base image pinned by real repo digest" ;;
  *) fail "base image missing repo digest: $BASE_DIGEST" ;;
esac
BASE_REF=$(echo "$BASE_TAG" | cut -d: -f1)@$(echo "$BASE_DIGEST" | cut -d@ -f2)

echo "=== 2. pull=never fail closed ==="
if docker run --pull=never --rm "$BASE_REF-nope-$$" true >/dev/null 2>&1; then
  fail "nonexistent image ran (implicit pull?!) "
else
  pass "nonexistent image refused with --pull=never"
fi

echo "=== 3. internal network reachability model ==="
docker network create --internal "$NET_NAME" >/dev/null
pass "internal network created: $NET_NAME"
mkdir -p "$GATE_DIR/bundle-a" "$GATE_DIR/bundle-b" "$GATE_DIR/src"
cat >"$GATE_DIR/src/main-a.go" <<'EOF'
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "WORKOS-P3-PROBE-A")
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	if err := http.ListenAndServe("0.0.0.0:8080", nil); err != nil {
		os.Exit(1)
	}
}
EOF
sed 's/WORKOS-P3-PROBE-A/WORKOS-P3-PROBE-B/' "$GATE_DIR/src/main-a.go" >"$GATE_DIR/src/main-b.go"
docker run --rm --pull=never --network none -v "$GATE_DIR/src:/src" -v "$GATE_DIR/bundle-a:/out" \
  -w /src -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod -e GOPROXY=off -e GOCACHE=/tmp/gocache \
  --label "$LABEL" "$BASE_REF" go build -o /out/server main-a.go >/dev/null
docker run --rm --pull=never --network none -v "$GATE_DIR/src:/src" -v "$GATE_DIR/bundle-b:/out" \
  -w /src -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod -e GOPROXY=off -e GOCACHE=/tmp/gocache \
  --label "$LABEL" "$BASE_REF" go build -o /out/server main-b.go >/dev/null
pass "fixture servers built offline inside the pinned image"

run_app() { # $1=bundle dir, $2=container name
  docker rm -f "$2" >/dev/null 2>&1 || true
  docker run -d --pull=never --name "$2" --network "$NET_NAME" \
    --read-only --cap-drop=all --security-opt no-new-privileges \
    --pids-limit 64 --memory 256m --memory-reservation 128m --cpus 1.5 \
    --tmpfs /tmp:rw,size=33554432,noexec,nodev,nosuid \
    --label "$LABEL" --label "$APP_LABEL_OWNER" --label workos.artifact.digest=probe \
    -v "$1:/app:ro" -w /app "$BASE_REF" /app/server >/dev/null
}

CID_A=workos-p3-probe-app-$STAMP-a
run_app "$GATE_DIR/bundle-a" "$CID_A"
IP_A=$(docker inspect "$CID_A" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
echo "container_ip=$IP_A"
[ -n "$IP_A" ] && pass "container received a bridge IP on the internal network" || fail "no bridge IP"
BODY_A=$(curl -fsS --max-time 5 "http://$IP_A:8080/")
[ "$BODY_A" = "WORKOS-P3-PROBE-A" ] && pass "host netns reaches app by bridge IP: $BODY_A" \
  || fail "expected probe A marker, got: $BODY_A"
curl -fsS --max-time 5 "http://$IP_A:8080/health" | grep -qx ok \
  && pass "health endpoint served from container" || fail "health endpoint failed"

if docker exec "$CID_A" timeout 5 bash -c 'exec 3<>/dev/tcp/1.1.1.1/80' >/dev/null 2>&1; then
  fail "container on internal network reached external TCP 1.1.1.1:80"
else
  pass "container egress kernel-unreachable (bash /dev/tcp 1.1.1.1:80 failed)"
fi
if docker exec "$CID_A" timeout 5 bash -c 'exec 3<>/dev/tcp/93.184.216.34/80' >/dev/null 2>&1; then
  fail "container on internal network reached external TCP 93.184.216.34:80"
else
  pass "container egress blocked for second external IP"
fi
if docker exec "$CID_A" getent hosts example.com >/dev/null 2>&1; then
  fail "external DNS resolution works from internal network (leak)"
else
  pass "external DNS resolution fails from internal network"
fi
if docker port "$CID_A" 2>/dev/null | grep -q .; then
  note "docker port reported published ports unexpectedly"
else
  note "port publishing is unsupported on internal networks (docker silently ignores -p); the app endpoint is the container bridge IP reachable only from the host netns"
fi

echo "=== 4. read-only /app + package swap changes behavior ==="
if docker exec "$CID_A" sh -c 'touch /app/nope' >/dev/null 2>&1; then
  fail "wrote into read-only /app mount"
else
  pass "/app mount is read-only inside the container"
fi
echo "inspect image: $(docker inspect "$CID_A" --format '{{.Image}}')"
echo "inspect mounts: $(docker inspect "$CID_A" --format '{{range .Mounts}}{{.Source}}:{{.Destination}}:{{.RW}} {{end}}')"
echo "inspect security: $(docker inspect "$CID_A" --format '{{json .HostConfig.SecurityOpt}} capdrop={{json .HostConfig.CapDrop}} readonly={{.HostConfig.ReadonlyRootfs}}')"
docker rm -f "$CID_A" >/dev/null
CID_B=workos-p3-probe-app-$STAMP-b
run_app "$GATE_DIR/bundle-b" "$CID_B"
IP_B=$(docker inspect "$CID_B" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
BODY_B=$(curl -fsS --max-time 5 "http://$IP_B:8080/")
[ "$BODY_B" = "WORKOS-P3-PROBE-B" ] && pass "bundle B served: $BODY_B (behavior differs by bundle content)" \
  || fail "expected probe B marker, got: $BODY_B"
docker rm -f "$CID_B" >/dev/null

echo "=== 5. cgroup v2 readback ==="
CG=$(docker run --rm --pull=never --memory 256m --memory-reservation 128m --pids-limit 64 --cpus 1.5 \
  --label "$LABEL" "$BASE_REF" sh -c \
  'cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.high /sys/fs/cgroup/pids.max /sys/fs/cgroup/cpu.max')
MEM_MAX=$(echo "$CG" | sed -n 1p)
MEM_HIGH=$(echo "$CG" | sed -n 2p)
PIDS_MAX=$(echo "$CG" | sed -n 3p)
CPU_MAX=$(echo "$CG" | sed -n 4p)
echo "memory.max=$MEM_MAX memory.high=$MEM_HIGH pids.max=$PIDS_MAX cpu.max='$CPU_MAX'"
[ "$MEM_MAX" = "268435456" ] && pass "memory.max enforced (256m)" || fail "memory.max=$MEM_MAX"
[ "$PIDS_MAX" = "64" ] && pass "pids.max enforced (64)" || fail "pids.max=$PIDS_MAX"
[ "$CPU_MAX" = "150000 100000" ] && pass "cpu.max enforced (1.5 cores)" || fail "cpu.max=$CPU_MAX"
case "$MEM_HIGH" in
  134217728) pass "memory.high settable via --memory-reservation (128m)" ;;
  max) note "memory.high NOT settable via --memory-reservation (reported max); soft high-water mark unavailable on this driver" ;;
  *) note "memory.high=$MEM_HIGH (unexpected value); treat soft high-water mark as unavailable" ;;
esac

echo "=== 6. capability drop / no-new-privs / read-only rootfs ==="
SEC=$(docker run --rm --pull=never --read-only --cap-drop=all --security-opt no-new-privileges \
  --label "$LABEL" "$BASE_REF" grep -E 'CapEff|NoNewPrivs' /proc/self/status)
echo "$SEC"
echo "$SEC" | grep -q 'CapEff:[[:space:]]*0000000000000000' \
  && pass "all capabilities dropped" || fail "CapEff non-zero"
echo "$SEC" | grep -q 'NoNewPrivs:[[:space:]]*1' \
  && pass "no-new-privileges active" || fail "NoNewPrivs not set"
if docker run --rm --pull=never --read-only --label "$LABEL" "$BASE_REF" sh -c 'touch /probe-ro' >/dev/null 2>&1; then
  fail "wrote into read-only rootfs"
else
  pass "read-only rootfs enforced"
fi

echo "=== 7. label-based ownership survives client restart ==="
CID_C=workos-p3-probe-app-$STAMP-c
run_app "$GATE_DIR/bundle-a" "$CID_C"
# a fresh docker CLI invocation (as a restarted runtime-host would) must still
# find exactly its labeled containers and nothing else
OWNED=$(docker ps -aq --filter "label=$APP_LABEL_OWNER" --filter "label=$LABEL" | wc -l)
[ "$OWNED" -ge 1 ] && pass "labeled container discovered by fresh client ($OWNED)" || fail "label lookup empty"
docker rm -f "$CID_C" >/dev/null

echo "=== summary ==="
if [ "$FAILED" -eq 0 ]; then
  echo "C00 PROBE: ALL CHECKS PASSED"
else
  echo "C00 PROBE: FAILURES PRESENT"
  exit 1
fi
