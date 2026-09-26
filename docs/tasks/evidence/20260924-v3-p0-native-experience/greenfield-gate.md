# Greenfield direct-path technical gate, 2026-09-26 UTC

This is a diagnostic of the pinned direct browser compositor path. It is **not** P0 acceptance or a production LAN deployment. The official Code process and trusted Runtime currently share a Docker container, and the browser compositor cannot reconstruct native state after its page is destroyed. The resident compositor design must move Code and the compositor into Runtime-managed isolated child containers.

## Reproduction

Build from this checkout (the Dockerfile verifies both source and Code checksums and builds `runtime-host` from the same source tree):

```sh
docker build -t workos-greenfield-runtime:p0 -f deploy/greenfield-runtime.Dockerfile .
mkdir -p /tmp/workos-gf-image-fixture
printf 'line-one\n' > /tmp/workos-gf-image-fixture/note.txt
chmod 666 /tmp/workos-gf-image-fixture/note.txt
cat > /tmp/workos-gf-image-apps.json <<'JSON'
{"/code":{"name":"Code","executable":"/usr/share/code/code","args":["--ozone-platform=x11","--disable-gpu","--no-sandbox","--user-data-dir","/tmp/code-data","/fixture/note.txt"],"env":{"HOME":"/tmp/code-home"}}}
JSON
docker run -d --name workos-gf-p0-image --gpus all \
  --device /dev/dri/renderD128:/dev/dri/renderD128 \
  --group-add "$(stat -c %g /dev/dri/renderD128)" \
  -e NVIDIA_DRIVER_CAPABILITIES=graphics,display,utility,compute \
  -p 127.0.0.1:18082:8081 \
  -v /tmp/workos-gf-image-fixture:/fixture \
  -v /tmp/workos-gf-image-apps.json:/tmp/apps.json:ro \
  --entrypoint sh workos-greenfield-runtime:p0 \
  -c 'mkdir -p /tmp/code-home; sleep infinity'
docker exec -d -e HOME=/tmp/code-home workos-gf-p0-image sh -c \
  'exec node /opt/greenfield/packages/compositor-proxy-cli/dist/main.js --bind-ip 0.0.0.0 --bind-port 8081 --allow-origin http://127.0.0.1:5173 --base-url ws://127.0.0.1:18082 --encoder x264 --render-device /dev/dri/renderD128 --applications /tmp/apps.json > /tmp/proxy.log 2>&1'
curl --retry 10 --retry-connrefused --retry-delay 1 -fsS -H 'Origin: http://127.0.0.1:5173' \
  -H 'X-Compositor-Session-Id: workos-p0-image' \
  http://127.0.0.1:18082/code -o /tmp/workos-gf-image-launch.json
```

The launch JSON contains an ephemeral signaling key. Keep it local and do not print it or include proxy URL logs in task artifacts. Start the Vite fixture on `127.0.0.1:5173` from `apps/desktop-web` with `pnpm exec vite --host 127.0.0.1 --port 5173 --force`; the checked-in `public/greenfield-proof.html` supplies the canvas. With dependencies installed, the browser proof command is:

```sh
docker run --rm --network host -e NODE_PATH=/usr/local/lib/node_modules \
  -v "$PWD":/repo:ro -v /tmp:/hosttmp \
  --entrypoint node workos-playwright-shared:1.62.1 \
  /repo/tools/v3-p0-native-experience/greenfield-browser-proof.cjs
```

The browser proof intercepts only the fixture `/code` launcher GET and fulfills it from the launch JSON. It never requests Core on port 8081 or starts a second Code process. It captures first paint, GUI save, unsaved edit, and remount at 1440×900/DPR 1. It asserts disk bytes after save; the remount screenshot requires visual review because the upstream helper reports `ready` even when no window pixels are present. Use a fresh container/profile for each run. This fixture runs through the proxy directly and does not prove Gateway/Runtime integration.

## Observed facts

- Host: NVIDIA RTX 2080 Ti, driver 595.91.07, `/dev/dri/renderD128` group 991. The container process ran as UID 10001 with group 991. The Docker daemon was privileged/trusted. Proxy logged `Using EGL 1.5`, `EGL vendor: NVIDIA`, and DMA-BUF format modifier support. Browser: Playwright Chromium 151.0.7922.34, WebGL enabled; encoder: x264/H.264.
- Official Code 1.139.0 initially showed `/fixture/note.txt` as `line-one`. On the canvas, click editor, press End, type `-edited`, then Ctrl+S: disk became exactly `line-one-edited\n` (SHA-256 `359160b592172969325483946a0815fc1f5a8f47b766b571e183183177c143f8`). [Saved Code pixels](code-gui-saved.png) and [correct-color first paint](code-pixels-correct-color.png) show real application content. This passes the _isolated A01 GUI edit/save probe_; WorkOS window-level A02 remains unmet.
- Typing `-unsaved` showed `line-one-edited-unsaved` and an unsaved dot in Code while disk stayed `line-one-edited\n`: [unsaved buffer](code-unsaved-before-detach.png). At that point the Code main process was PID 91 with `/proc/91/stat` starttime 439302 ticks (`Sat Sep 26 10:21:35 UTC`). After closing every browser page, the same PID/starttime was still present. A second browser using the same launch key reported `ready=1` but displayed an empty white canvas: [same-PID reconnect](code-same-pid-reconnect-white.png). The buffer could not be viewed or edited after remount. **A06 failed**; process survival alone is insufficient.
- A separate actual XWayland selection probe selected all text in Code and pressed Ctrl+C. The browser source was `XDataSource` with `text/plain;charset=utf-8`; `source.send(...)` followed by `readFD.readBlob()` returned `line-one-edited-unsaved\n` within the 3-second bound. The pinned proxy's `writeFdAsStream` closes the write stream, so an additional XWindowManager `fd.close()` patch was not added. Headless Chromium denied `navigator.clipboard.write`, and Mac→Code was not tested. **A05 remains partial.** The older `TransferNativeClipboard` RPC used only an in-memory string; this patch returns Connect `Unimplemented` until a real selection bridge owns it.
- Pinned compositor rc1 originally uploaded WebCodecs buffers sized for `visibleRect` as `codedWidth`/`codedHeight`, yielding WebGL insufficient-buffer errors and green tint. The pnpm patch copies the coded rectangle; the clean screenshot has correct colors and no buffer-size errors. The XWayland patch permits the private compositor FD connection without an Xauth cookie; before it, Code pixels were absent.
- Upstream `SessionController` allows one signaling socket and sends channel descriptors only at client creation. On page close, ARQ channels release state; a fresh browser compositor has no Wayland object graph or frame history. The two server-side disconnect patches keep Code alive but cannot replay it. A02 per-window WorkOS mapping, A03 DPR/quality, A07 observer/fan-out and takeover, A10 latency/resources, and A11 physical Mac remain unpassed. Black border and unused canvas area are visible in the diagnostic screenshot.
- The direct proxy now requires the current device and live control generation on each HTTP request and browser→upstream WebSocket frame, revoking existing sockets after takeover/expiry. Unit tests cover observer denial and a live socket closing on revocation. This prevents a reusable signaling key from bypassing the lease but does not provide observer display or state replay.
- The proxy/Code child environment is an explicit allowlist excluding `WORKOS_DATABASE_URL` and service credentials. This is defense in depth only: same-container process/network access still makes the direct overlay unsuitable for a LAN product deployment.

## Verification

- `docker build -t workos-greenfield-runtime:p0 -f deploy/greenfield-runtime.Dockerfile .` — passed after source/Code checksum checks and pinned patches.
- `go test ./internal/runtime/nativehost/adapters/greenfield ./internal/runtime/nativehost/application ./cmd/runtime-host` using `golang:1.26.7-bookworm`, cached modules, and `GOPROXY=https://goproxy.cn,direct` — passed.
- `pnpm install --frozen-lockfile` in the Node container — passed with patched `@gfld/compositor@1.0.0-rc1`.
- `make check` and Mac/physical-device acceptance are not represented by this isolated proof; the overall P0 gate remains open.
