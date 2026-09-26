# V3 P0 diagnostic pins

| Component          | Pinned value                                                                                                                                                  |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Greenfield source  | commit `6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57`; source archive SHA-256 `97e0a72b0e139c8b22088fa4acde199d65d5794f8ee7f85590c435e9231cf433`                  |
| Server patches     | `deploy/patches/greenfield/0001-keep-clients-without-browser.patch`, `0002-preserve-client-on-navigation.patch`, `0003-private-xwayland-without-cookie.patch` |
| Browser compositor | published `@gfld/compositor@1.0.0-rc1` with `patches/@gfld__compositor@1.0.0-rc1.patch` (WebCodecs coded rectangle)                                           |
| VS Code            | official Linux desktop `.deb` 1.139.0; SHA-256 `5031849ea13d2297ec7c8f1af70c0bc7d585250cae306b53d668cb6a7a900623`                                             |
| Runtime build      | `golang:1.26.7-bookworm`, built from this checkout inside `deploy/greenfield-runtime.Dockerfile`                                                              |
| Browser proof      | Playwright Chromium 151.0.7922.34, 1440×900/DPR 1, WebGL enabled                                                                                              |
| Host GPU           | NVIDIA RTX 2080 Ti, driver 595.91.07, renderD128 group 991; EGL 1.5/NVIDIA, DMA-BUF supported                                                                 |
| Container process  | UID 10001, GID 10001 plus render group 991; Docker daemon trusted/privileged                                                                                  |

The Docker image ID changes with local source edits; run `docker image inspect workos-greenfield-runtime:p0 --format '{{.Id}}'` to record the exact build under test. These pins describe an isolated technical probe. They do not make the direct same-container composition a supported LAN deployment.
