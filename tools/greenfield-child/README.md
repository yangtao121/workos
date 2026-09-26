# Resident Greenfield child

This child owns the long-lived Greenfield compositor session, headless Chromium,
the pinned Greenfield proxy and official Code process for one Runtime workload.
Runtime Go is the trusted broker. The child runs as UID 10001 in a separate
container with `NetworkMode=none`, a read-only root, a private `/tmp` tmpfs,
explicit render devices, one authorized workspace mount, and a per-workload
Unix socket directory. No database credentials or Docker socket enter it.

`src/browser.ts` creates the native Greenfield session and a canvas for each
published top-level window. The pinned `@gfld/compositor@1.0.0-rc1` patch in
`../../patches` filters each scene to its window surface tree and projects
its global coordinates into the window canvas. Real canvas pixels are read
into 512×512 PNG tiles. Changed tiles are sent between complete frames;
initial connection, broker reconnect and a one-second timer force complete
frames. The first frame waits for a native buffer and visible center detail,
with a four-second fallback for intentionally blank applications.

`src/bridge.ts` is the only child IPC entry. Runtime listens on
`/run/workos/greenfield/bridge.sock`; the child connects and reconnects without
restarting Chromium or Code. IPC is ADR-0040's four-byte big-endian length
plus `GreenfieldChildEnvelope` Protobuf, at most 2,200,000 bytes per record.
Notifications use request ID zero; one-event input and clipboard requests use
nonzero IDs. The resident input ledger suppresses duplicate native actions
after an ACK is lost and fails closed after an uncertain partial action.

Build and start from the repo root:

```sh
./tools/lan/start.sh up
```

The launcher caches the exact Greenfield source archive under owner-only
`.workos/build-cache/`, supplies it as a named Docker build context, and
checks SHA-256 on both sides of the build. The base Dockerfile pins the proxy
source commit and Code package SHA-256.
This package pins the published compositor npm rc1 plus the checked-in patch.
The child Dockerfile installs Chromium through pinned `playwright-core@1.62.1`.
The broker specifies the entrypoint and checks image ID, UID, mounts, network,
tmpfs, environment and devices before launch.

The manual `test/smoke.ts` fixture verifies a real Code window, PNG tiles,
broker reconnect with the same window ID, and a sequenced native focus ACK.
`test/menu-smoke.ts` exercises the File menu and records whether popup pixels
and window relations survive the scene filter. See the [task record](../../docs/tasks/20260926-greenfield-resident-child.md)
for the current measured verdict and evidence.
