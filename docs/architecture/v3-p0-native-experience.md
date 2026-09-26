# V3 P0 Greenfield diagnostic boundary

The default Native engine remains Xvfb/WebRTC. Explicit `WORKOS_RUNTIME_NATIVE_ENGINE=greenfield` selects Greenfield; a failed launch reports unavailable rather than falling back. The direct image described below is a historical technical probe: it runs Code beside the privileged Runtime process in one container and is **not suitable for the LAN product deployment**. The LAN stack now uses the [Runtime-owned resident compositor](../decisions/0040-resident-greenfield-window-media.md): Code and Chromium run in a private child container, while the trusted Go broker remains in runtime-host. The child receives neither database credentials nor Docker socket access. Its actual browser acceptance remains tracked in the [P0 integration task](../tasks/20260926-lan-p0-integration.md).

```sh
WORKOS_RUNTIME_NATIVE_ENGINE=greenfield
WORKOS_RUNTIME_NATIVE_GREENFIELD_PROXY=/opt/greenfield/packages/compositor-proxy-cli/dist/main.js
WORKOS_RUNTIME_NATIVE_GREENFIELD_APP=/usr/share/code/code
WORKOS_RUNTIME_NATIVE_RENDER_DEVICE=/dev/dri/renderD128
WORKOS_RUNTIME_NATIVE_SCRATCH=/var/lib/workos/greenfield
WORKOS_AUTH_PUBLIC_ORIGIN=https://<LAN gateway origin>
```

`deploy/greenfield-runtime.Dockerfile` builds the Runtime binary from this checkout, verifies pinned Greenfield and official VS Code archive checksums, applies server patches, and installs the GPU/XWayland toolchain. The image is exercised only through the isolated loopback Docker fixture in the gate record; there is no Compose overlay that enables this direct engine on the LAN. The fixture requires NVIDIA Container Toolkit, `/dev/dri/renderD128`, and its group ID. There is no software-renderer override. The proxy binds `127.0.0.1` inside Runtime. It receives its final public WebSocket base URL before Code starts because Greenfield embeds that URL inside signaling frames and file-descriptor messages; HTTP response replacement alone cannot repair it.

Runtime launches one Code PID per display. `/native/greenfield/{session}/code` returns that existing PID/key; forwarding the GET upstream would launch another Code instance. Gateway authenticates the device session before forwarding. Runtime requires the current controller device and live control generation on every HTTP request and browser-to-proxy WebSocket frame; an old socket is closed after lease loss. The proxy and Code child receive an explicit environment allowlist, not Runtime's database URL or service credentials. Explicit Stop kills the process group; browser close only drops its channels. `TransferNativeClipboard` reports unavailable because its former string field was not a Wayland selection bridge. Browser-side selection is a separate integration surface.

The pinned direct browser compositor can display and edit official VS Code on a real GPU. Its WebCodecs copy path needs the package patch in `patches/` to preserve coded YUV padding. XWayland needs the private-FD auth patch. A GUI test saved fixture bytes successfully, but after closing all pages a new page showed a blank canvas while the same Code PID remained alive. Upstream has one signaling socket and no replay of Wayland object state, so this direct path does not satisfy P0 same-instance recovery, observer fan-out, or per-window WorkOS mapping. See [the gate record](../tasks/evidence/20260924-v3-p0-native-experience/greenfield-gate.md) and [version pins](../tasks/evidence/20260924-v3-p0-native-experience/versions.md).
