# Task: V3 P0 Greenfield resident child compositor

- 状态：in_progress
- Owner/Agent：Codex greenfield_reconnect_audit
- 分支/worktree：`feat/greenfield-resident-child` / `/home/aquatao/workos-greenfield-child`
- 基线：`bf6edc4`，开始时工作树干净；其他 worktree 改动保留
- 依赖：[ADR-0040](../decisions/0040-resident-greenfield-window-media.md)、[窗口契约任务](20260926-greenfield-window-contract.md)、[Greenfield 重连审计](20260926-greenfield-reconnect-audit.md)
- 范围：固定发布包 `@gfld/compositor@1.0.0-rc1` 的逐 top-level scene patch、同 child 容器内 resident Chromium/Node bridge、Proto Unix socket IPC、child 镜像和相关测试
- 不在本任务修改：Runtime Go/Gateway/Desktop/Proto；其 producer 和 consumer 由并行任务处理
- 验收：可重复安装/构建固定包补丁和 child 镜像；真实 Code top-level 出现时发窗口 snapshot、逐窗口无损 PNG full-refresh tiles；Go socket 重连后不重启 Code/Chromium 并重发快照/帧；输入/clipboard 真正作用于 Greenfield seat/selection，序号重试不能双输；没有 GPU/渲染/隔离时明确失败；记录构建和实测证据

## 进程与 IPC 约定

与 Runtime producer 约定：镜像 `workos-greenfield-child:dev`，入口 `node /opt/workos/greenfield-child/bridge.mjs --socket /run/workos/greenfield/bridge.sock --session-id <uuidv7> --generation <int64> --width <int> --height <int> [--workspace /workspace] --render-device /dev/dri/renderD128`。Runtime Go 在 per-workload `0700` host-visible 目录监听并 bind 到 child `/run/workos/greenfield:rw`；child UID 10001，`NetworkMode=none`。child 内 Code、proxy、Chromium、Node 只用 loopback；仅挂授权 workspace、IPC 目录和 render node，无 Runtime DB env、Docker socket、公网/host network。

IPC 使用 `GreenfieldChildEnvelope` v1，4 字节大端长度 + Protobuf，单 record ≤2,200,000 字节。child 连接 Go listener，重连后主动送完整窗口 snapshot 与每窗口 full-refresh frame。Go 的 input/clipboard 请求用非零 `request_id`；窗口/帧通知为 0。child 按 `attachment_id + workload_generation` 保留实际应用的输入 sequence 与结果，ACK 丢失后的重复请求不得二次注入。

## 设计与限制

发布包的多 scene 原本默认全局 viewStack；补丁给每窗口 scene 绑定一个 native top-level，包含关联 child view，转换投影原点。child 对 512×512 tile 做逐像素差异检测，只发送变化块；首次、broker 重连及每秒发送完整 `full_refresh`。首帧要求 native buffer、非透明像素和中心区域可见细节；对有意空白的 app 在 4 秒后退化为真实 native buffer。冷启动门禁仍需独立验证，真实 Code A10 p95 未测。

## 验证与交接

`docker build -t workos-greenfield-child:dev -f deploy/greenfield-child.Dockerfile .` 通过，镜像 ID `sha256:8079b180bb4cacaae538de59b5367b1090c8360a2f972086750bf53c61ee0718`，Chromium 151.0.7922.34 / Playwright 1.62.1。`docker image inspect` 显示 UID 10001，Env 仅 PATH、Node/Yarn 版本、npm registry、Electron/XDG/Playwright 路径，无 `WORKOS_*`。Runtime producer 独立用同镜像通过 Docker profile/GPU inspect probe。

网络隔离、read-only root、`/tmp` tmpfs、1 GiB shm、NVIDIA/renderD128、UID 10001 实际运行后，私有 IPC broker 收到一个 Code native window、6 块完整首帧 PNG；销毁 broker socket 并重新监听后，child 保持同 `window_id`、重新发送 snapshot/full frame，native focus 输入返回 APPLIED。稍后完整窗口画面清晰显示真实 Code UI：[完整窗口](evidence/20260926-greenfield-resident-child/code-window-full.png)。初次 Code 启动画面的第一块曾几乎全黑；因此增加内容门禁，尚需重新执行冷启动验证。

真实 Code 首次引导关闭后，File 顶栏点击在 PNG 中高亮，但下拉菜单没有进入对应 window canvas；snapshot 仍只有一个 main window，`parent_window_id` 空。证据：[File 高亮但无下拉](evidence/20260926-greenfield-resident-child/code-file-highlight-no-popup.png)。尚不能确定是 pointer UP/坐标语义还是 popup/transient scene 过滤：下一步须同一点击后同时截全局 `#display`，记录 `topLevelViews`、`viewStack`、surface parent/role 结构，再修正确因。A02 不能判定通过；不能用固定成功或忽略菜单代替。

已验证 `pnpm --filter @workos/greenfield-child build`、`tsc --noEmit` 和 5 个 IPC/序号单测。冷启动首帧、菜单/dialog、真实 Code 输入到 viewer 像素 p95、内存/CPU/GPU、Mac E2E 仍是未决门禁。契约已合并，不因 child 单独可运行把 P0 标为 done。
