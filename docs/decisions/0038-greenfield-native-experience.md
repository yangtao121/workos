# ADR-0038：Greenfield 原生体验实验接入

- 状态：实验中（不是 P0 通过后的生产方案）
- 日期：2026-09-24
- 关系：structure-v3 P0；ADR-0029 继续作为默认 Xvfb/WebRTC 路线。

## 裁决

`WORKOS_RUNTIME_NATIVE_ENGINE=greenfield` 时，runtime-host 监督 compositor-proxy
和官方桌面应用进程组。浏览器只持有临时合成连接。断开、Detach 不清进程；Stop
结束进程组。缺二进制时 Create 返回 unavailable，不回退到 Xvfb。

连接使用 `OpenGreenfieldDisplay`，不把 WebSocket 地址放进 SDP。
`TransferNativeClipboard` 只传纯文本，上限 256 KiB，失败不返回旧内容。
Wayland 帧留在 Greenfield 适配器内。

本裁决只覆盖实验接入。生产拓扑、编码器和网关代理在 P0 门禁通过后另做 ADR。

## 固定版本

- Greenfield commit `6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57`
- 补丁 `deploy/patches/greenfield/0001-keep-clients-without-browser.patch`
- VS Code `1.139.0`，commit `2242ebbb54efeeb0129e08e919e7e8d43033cd83`
- deb sha256 `5031849ea13d2297ec7c8f1af70c0bc7d585250cae306b53d668cb6a7a900623`

## 后果

默认引擎仍是 xvfb。Greenfield 镜像 `workos-greenfield-runtime:p0` 只在实验环境构建。
Mac 输入法和第二台物理设备不在本 ADR 的已验证范围内。
