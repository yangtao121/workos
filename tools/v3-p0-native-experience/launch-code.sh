#!/bin/sh
# Launch official desktop VS Code under the pinned Greenfield compositor-proxy
# and record whether the process survives with no browser attached.
set -eu
deb="${1:-/opt/code.deb}"
log=/tmp/v3-p0-launch.log
: > "$log"
if ! command -v xdotool >/dev/null 2>&1; then
  apt-get update
  apt-get install -y --no-install-recommends xdotool
fi
if ! command -v code >/dev/null 2>&1; then
  apt-get update >>"$log" 2>&1
  dpkg -i "$deb" >>"$log" 2>&1 || apt-get install -y -f >>"$log" 2>&1
fi
code_bin=/usr/share/code/code
if [ ! -x "$code_bin" ]; then
  echo "missing desktop binary $code_bin" >&2
  exit 1
fi
mkdir -p /fixture /tmp/code-data /tmp/v3-apps
printf 'line-one\n' > /fixture/note.txt
cat > /tmp/v3-apps/apps.json <<EOF
{
  "/code": {
    "name": "Code",
    "executable": "$code_bin",
    "args": ["--ozone-platform=x11", "--disable-gpu", "--no-sandbox", "--remote-debugging-port=9222", "--remote-allow-origins=*", "--user-data-dir", "/tmp/code-data", "/fixture/note.txt"],
    "env": { "HOME": "/tmp/code-home" }
  }
}
EOF
mkdir -p /tmp/code-home /tmp/runtime-dir /tmp/.X11-unix
chmod 700 /tmp/runtime-dir
chmod 1777 /tmp/.X11-unix
export XDG_RUNTIME_DIR=/tmp/runtime-dir
cd /opt/greenfield
node packages/compositor-proxy-cli/dist/main.js \
  --bind-ip 127.0.0.1 \
  --bind-port 8081 \
  --allow-origin "http://localhost" \
  --base-url "ws://127.0.0.1:8081" \
  --encoder x264 \
  --render-device /dev/dri/renderD128 \
  --applications /tmp/v3-apps/apps.json \
  > /tmp/v3-proxy.log 2>&1 &
proxy_pid=$!
i=0
while [ "$i" -lt 50 ]; do
  if grep -q "Listening on" /tmp/v3-proxy.log 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 0.2
done
if ! grep -q "Listening on" /tmp/v3-proxy.log 2>/dev/null; then
  echo "proxy did not listen" >&2
  cat /tmp/v3-proxy.log >&2
  exit 1
fi
reply=$(curl -sS -D /tmp/v3-launch.headers -H "x-compositor-session-id: workos-p0" -o /tmp/v3-launch.json -w "%{http_code}" http://127.0.0.1:8081/code || true)
echo "launch_http=$reply"
cat /tmp/v3-launch.json
echo
if [ "$reply" != "201" ]; then
  echo "proxy_log:" >&2
  cat /tmp/v3-proxy.log >&2
  exit 1
fi
pid=$(sed -n 's/.*"pid":"\([0-9]*\)".*/\1/p' /tmp/v3-launch.json)
echo "spawn_pid=$pid proxy_pid=$proxy_pid"
sleep 4
if [ ! -d "/proc/$pid" ]; then
  echo "spawned pid exited" >&2
  cat /tmp/v3-proxy.log >&2
  exit 1
fi
echo "exe=$(readlink /proc/$pid/exe)"
tr "\0" " " < "/proc/$pid/cmdline"
echo
echo "children:"
ps -o pid,ppid,cmd --ppid "$pid" || true
# No browser is connected. The unpatched proxy would SIGHUP after 600s;
# this check proves the process is still the same desktop binary immediately
# after launch with zero compositor clients.
sleep 3
if [ ! -d "/proc/$pid" ]; then
  echo "process died with no browser" >&2
  exit 1
fi
echo "alive_after_no_browser exe=$(readlink /proc/$pid/exe)"
i=0
while [ "$i" -lt 40 ]; do
  if curl -sf http://127.0.0.1:9222/json/list >/tmp/v3-cdp.json 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 0.5
done
echo "cdp_targets=$(wc -c < /tmp/v3-cdp.json 2>/dev/null || echo 0)"
head -c 800 /tmp/v3-cdp.json 2>/dev/null || true
echo
node -e '
const url = process.argv[1];
const ws = new WebSocket(url);
const timer = setTimeout(() => { console.log("ws_timeout"); process.exit(0); }, 2000);
ws.on("open", () => { console.log("ws_open"); ws.close(); });
ws.on("close", () => { clearTimeout(timer); console.log("ws_close"); process.exit(0); });
ws.on("error", (e) => { console.log("ws_error", e.message); process.exit(0); });
' "ws://127.0.0.1:8081/signal?compositorSessionId=workos-p0&key=$(sed -n 's/.*"key":"\([^"]*\)".*/\1/p' /tmp/v3-launch.json)"
sleep 1
if [ ! -d "/proc/$pid" ]; then
  echo "process died after websocket close" >&2
  exit 1
fi
echo "same_pid_after_disconnect exe=$(readlink /proc/$pid/exe)"
echo "proxy_log_tail:"
tail -n 40 /tmp/v3-proxy.log
