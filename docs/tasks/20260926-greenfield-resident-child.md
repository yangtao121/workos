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

发布包的多 scene 原本默认全局 viewStack；补丁给每窗口 scene 绑定一个 native top-level，包含关联 child view，转换投影原点。child 对 512×512 tile 做逐像素差异检测，只发送变化块；首次、broker 重连及每秒发送完整 `full_refresh`。首帧要求 native buffer、非透明像素和中心区域可见细节；对有意空白的 app 在 4 秒后退化为真实 native buffer。真实 Code A10 p95 未测。

## 验证与交接

`docker build -t workos-greenfield-child:dev -f deploy/greenfield-child.Dockerfile .` 通过，镜像 ID `sha256:8079b180bb4cacaae538de59b5367b1090c8360a2f972086750bf53c61ee0718`，Chromium 151.0.7922.34 / Playwright 1.62.1。`docker image inspect` 显示 UID 10001，Env 仅 PATH、Node/Yarn 版本、npm registry、Electron/XDG/Playwright 路径，无 `WORKOS_*`。Runtime producer 独立用同镜像通过 Docker profile/GPU inspect probe。

网络隔离、read-only root、`/tmp` tmpfs、1 GiB shm、NVIDIA/renderD128、UID 10001 实际运行后，私有 IPC broker 收到一个 Code native window、6 块完整首帧 PNG；销毁 broker socket 并重新监听后，child 保持同 `window_id`、重新发送 snapshot/full frame，native focus 输入返回 APPLIED。稍后完整窗口画面清晰显示真实 Code UI：[完整窗口](evidence/20260926-greenfield-resident-child/code-window-full.png)。

最终 `:dev` 镜像的独立冷启动试验：child Docker `StartedAt=2026-09-26T11:48:37.135Z`，首个 PNG tile 创建时间 `11:48:45.259Z`，约 8.12 秒；完整首帧为真实 Code 编辑器像素，[冷启动首帧](evidence/20260926-greenfield-resident-child/code-cold-first-full-frame.png)，不是曾见到的黑色启动缓冲。broker 从启动监听到首帧为 16.25 秒，其中前约 8 秒用于手工启动 child，不代表用户可见首帧时延。broker 重连仍得相同窗口 ID、完整刷新和 native focus ACK。此轮手工诊断容器 `workos-gf-child-cold*` 已停止并删除。

真实 Code File 菜单诊断：冷 profile 有“Continue without Signing In”和“Get Started”两页引导；完成引导后以 MOVE/DOWN/UP 单次点击 File。原实现 DOWN 时全局 `#display` 显示 File 下拉，UP 后全局和窗口 canvas 都没有菜单。结构记录中 `topLevelViews` 和 `viewStack` 均只有一个 mapped XWayland Code surface（`parent=null`），菜单绘制在此 surface 内，没有窗口 scene 漏滤。固定 rc1 的 `dist/browser/input.js` 在 pointer UP 只调用 `queueButton`；child 原实现额外调用 `notifyMotion`。移除该 motion 后，冷 profile 完成引导再单击，真实菜单在 UP 后持续显示于全局和逐窗口 canvas：[File 菜单](evidence/20260926-greenfield-resident-child/code-file-menu-after-pointer-up.png)。先前失败证据仍保留：[File 高亮但无下拉](evidence/20260926-greenfield-resident-child/code-file-highlight-no-popup.png)。Code 的此菜单没有单独 native surface，故其 `parent_window_id` 空是预期。

从上述 File 菜单点击“Open File...”后，child 真实收到第二个 `Open File` XWayland 窗口及完整 PNG 帧：[对话框画面](evidence/20260926-greenfield-resident-child/code-open-file-dialog.png)。对话框展示 fixture `note.txt`，证明独立窗口 scene 可渲染真实 native 对话框。固定 rc1 的 `XWindow.js` 读取 `WM_TRANSIENT_FOR`；`XWaylandShellSurface.setParent`/`DesktopSurface.setParent` 将关系写到 native `Surface.parent`。child 从 `topLevelViews` 取实际 native surface，沿 parent 链或 XWindow `transientFor` 找到已发布窗口，修正 `parent_window_id`。复测的[窗口快照](evidence/20260926-greenfield-resident-child/code-open-file-window-snapshot.json)中，`Open File.parentWindowId` 等于 Code 主窗口 ID，两个窗口均收到完整帧。Code 菜单和对话框的窗口级路径已验证；其他 native app 与整条浏览器 E2E 仍由总 P0 gate 判定。此轮手工诊断容器 `workos-gf-child-menu*` 已停止并删除。

另修复仅 DPR 改变时的 scene 更新：`record.pixelRatio` 是请求值，`record.scenePixelRatio` 是已应用值；不同则重投影、提升 window revision、强制完整帧并发布新 DPR。DPR1→2 的真实 UI 链路仍须在 A03 E2E 验证。

独立诊断镜像 `workos-greenfield-child:dpr-probe`（`sha256:a179a5a210bf2f8bfc251d85899b0f67141b7503c76cd1576b7dd7f9c41c65af`）上的真实 Code 子进程 probe 使用隔离、无网络、只读 root 的 child 及私有 UDS broker。broker 在首次完整原生帧后发送只改变 DPR 为 2000 的 `GreenfieldWindowResize`。输入 ACK 为 `APPLIED`，同一窗口 revision 从 3 增至 7，snapshot 报告 DPR 2000，随后收到 3152×2064 的完整刷新帧（35 tiles）；初始完整帧为 1512×968：[结构化结果](evidence/20260926-greenfield-resident-child/code-dpr2-window.json)。原生 Code 在 configure 后略微调整了逻辑窗口尺寸，因此物理帧并非初始尺寸的精确 2 倍。这证明 child 的输入→原生重配置→逐窗口场景→帧 IPC 链路；浏览器 viewer 绘制仍需 A03 端到端验证。probe 用的 `workos-gf-dpr-child` 和 `workos-gf-dpr-broker` 诊断容器已停止并删除，正式 `:dev` 镜像未被覆盖。

已验证 `pnpm --filter @workos/greenfield-child build`、`tsc --noEmit`、child ESLint 和 5 个 IPC/序号单测，Prettier 与 `git diff --check` 通过。全仓 `make check` 的 Proto、Go vet/test 阶段通过；Web 阶段曾在 pnpm 的非 TTY `install --production` 自动清理提示处终止，需由集成基线重跑。其他 native app、DPR 真实 viewer 链路、真实 Code 输入到 viewer 像素 p95、内存/CPU/GPU、两种 Chromium profile 的端到端测试仍是 P0 未决门禁；物理设备另行验证。契约已合并，不因 child 单独可运行把 P0 标为 done。
