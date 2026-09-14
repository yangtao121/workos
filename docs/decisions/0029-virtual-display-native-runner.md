# ADR-0029：虚拟显示原生运行器与回环 WebRTC 远程面

- 状态：Accepted
- 日期：2026-09-14
- 关系：structure §8 Human Native Workspace / Native Runner；ADR-0027/0028 进程纪律。

## 裁决

Native Runner 是 runtime-host 拥有的受监督虚拟显示会话：每个 owner 会话一个真实
`Xvfb` 显示 + 一个配置的原生 X 客户端（默认 `xterm`）+ 一个 `ffmpeg -f x11grab`
捕获/VP8 编码子进程（migration 056 持久 owner-scoped 会话行）。视频经 pion
WebRTC 以回环拓扑（仅 host candidates，无 STUN/TURN）推给桌面 `<video>` 渲染；
输入经 Data Channel（`workos.input`）回传，由引擎映射为 `xdotool` XTEST 注入。
Gateway 负责鉴权与信令路由（owner 身份注入，与 ADR-0027/0028 相同）。

控制面 `workos.surface.v1.NativeSessionService`：

- `CreateNativeSession`：幂等（key + project/尺寸摘要），固定初始尺寸，启动
  Xvfb + 客户端 + 捕获；并发上限 2/owner、4/实例，TTL 30 分钟。
- `ConnectNativeSession`：信令段——客户端提交完整 SDP offer（候选已收集），
  服务端返回 answer（同样完整收集）；同一会话重连时旧 peer 关闭，帧轨道与
  Data Channel 重建。会话身份与信令分离，重试不破坏幂等键。
- `GetNativeSession` / `CloseNativeSession`：读取与终态回收（杀进程组、删私有
  scratch 目录）。

输入事件为有界 JSON（≤2 KiB）：`text`（≤256 可打印字符 + \n\t）、`key`（有限
键名允许表）、`pointer`（move/down/up/click + 0..1 归一坐标）。令牌桶限速
64 事件/秒，超出丢弃并计数；未声明能力（剪贴板、文件选择、宿主窗口输入）不
协商、不转发。

诚实边界：进程组 kill + Pdeathsig；无 cgroup/命名空间隔离、无显示级 auth
cookie（单 uid 镜像内 `-nolisten tcp` + Unix socket）、无 TURN 中继（仅回环/
同主机拓扑）、无 cgroup 级资源上限。引擎事实如实上报。宿主缺少 Xvfb/ffmpeg/
xdotool 时能力 unavailable，不装作可用。

## 验收

`make test-native-surface`（tools/native-surface，真实 PostgreSQL/Core/Runtime/
Gateway + Xvfb/ffmpeg/xdotool/xterm 镜像）：

- RPC 矩阵：Create 幂等重放/摘要漂移 Aborted、非法尺寸/超限输入 fail-closed、
  会话上限（恰好 2，第 3 拒绝）、外 owner 直连 runtime 隔离、Close 后 Connect
  拒绝、引擎不可用（未配置）如实 Unavailable。
- 真实视频链：Go pion 对等端 Connect 后收到 VP8 RTP 帧；帧哈希在静止期稳定、
  在 Data Channel 键入文本后变化（xterm 回显进入虚拟显示 → x11grab → VP8 →
  RTP → 对等端），证明输入回传与渲染内容真实变化，非黑屏/固定图片。
- Chromium E2E：桌面 Native 窗口 `<video>` 收到真实轨道（videoWidth>0、
  readyState≥2），canvas 像素读回方差 >0 且键入后变化，窗口关闭即 Close。

默认栈不携带 X 显示工具链；能力按配置如实报告。
