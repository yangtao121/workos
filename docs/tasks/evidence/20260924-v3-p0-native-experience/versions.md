# 固定版本

| 组件 | 值 |
| --- | --- |
| Greenfield | `6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57` |
| 源码镜像 | `https://ghfast.top/https://github.com/udevbe/greenfield/archive/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57.tar.gz` |
| 补丁 | `deploy/patches/greenfield/0001-keep-clients-without-browser.patch`（去掉浏览器断开后 600 秒 SIGHUP） |
| Debian | `http://mirrors.huaweicloud.com/debian` bookworm |
| npm | `https://registry.npmmirror.com` |
| 镜像 | `workos-greenfield-runtime:p0`，id `sha256:73e2cc90af28fdbf0b2e3b3e7b8b96d6b4ba9c48e9da8ce2c476fa0731d9c78f` |
| VS Code | 1.139.0，`2242ebbb54efeeb0129e08e919e7e8d43033cd83` |
| deb sha256 | `5031849ea13d2297ec7c8f1af70c0bc7d585250cae306b53d668cb6a7a900623` |
| 基础镜像 | `node:24.19.0-bookworm-slim` |
| 编码 | 容器内无 `/dev/dri/renderD128`，EGL/dmabuf 未初始化，软件路径未完成出画 |

上游默认 `onDisconnect` 会在 600 秒后 SIGHUP 应用。补丁去掉该定时器，进程寿命改由 Runtime Stop 和应用自身退出决定。
