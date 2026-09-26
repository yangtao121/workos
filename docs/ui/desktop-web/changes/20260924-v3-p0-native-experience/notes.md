# Greenfield Code diagnostic pixels (2026-09-26 UTC)

- Task: `docs/tasks/20260924-v3-p0-native-experience.md`; gate details: `docs/tasks/evidence/20260924-v3-p0-native-experience/greenfield-gate.md`.
- Surface/route: the isolated `/greenfield-proof.html` canvas connected to a pinned Greenfield compositor-proxy. It is a diagnostic view of official Linux VS Code; it is **not** the finished WorkOS per-window desktop integration.
- Fixture: dedicated `/fixture/note.txt`, initial bytes `line-one\n`, edited through the Code GUI to `line-one-edited\n` and saved with Ctrl+S. No account or real project data appears.
- Capture: Playwright Chromium 151.0.7922.34, viewport 1440×900, device scale factor 1, `--enable-webgl --ignore-gpu-blocklist`. The server used NVIDIA RTX 2080 Ti, driver 595.91.07, EGL 1.5 with DMA-BUF, H.264 x264 encoding. The container process ran as UID 10001 with render group 991; the Docker daemon remained trusted.
- `before/greenfield-code--editor--1440x900.png` is the earlier same proof canvas without an initialized render node: blank despite a live Code process. `after/` and `current/` show the same route after XWayland authentication and WebCodecs coded-rectangle fixes. These are visual diagnostics, not A02 window-level acceptance.
- The Code process stayed alive after browser close, but a new browser received a white canvas. `code-unsaved-before-detach.png` and `code-same-pid-reconnect-white.png` in task evidence record the A06 failure. The current screenshot does not imply reconnect works.
- Reproduce the image and fixture with the commands in `greenfield-gate.md`; capture used Playwright `page.screenshot()` after a 12-second first paint wait and Ctrl+S. The original and after screenshots are 1440×900 PNGs.
