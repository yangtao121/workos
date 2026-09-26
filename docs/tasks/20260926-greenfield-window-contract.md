# Task: V3 P0 Greenfield 常驻合成器窗口契约

- 状态：done（仅契约；P0 功能仍未通过）
- Owner/Agent：Codex greenfield_reconnect_audit
- 分支/worktree：`feat/greenfield-window-contract` / `/home/aquatao/workos-greenfield-contract`
- 基线：创建后同步至主线 `c86e944`（包含 LAN 密码 Proto/生成物、审计与当前浏览器 adapter 提交）；本任务只新增独立 Proto 文件、文档和 SDK 契约导出，保留其他 worktree 改动
- 依赖：[V3 P0 任务](20260924-v3-p0-native-experience.md)、[Greenfield 重连与窗口审计](20260926-greenfield-reconnect-audit.md)、[ADR-0038](../decisions/0038-greenfield-native-experience.md)、[continuity.proto](../../api/proto/workos/surface/v1/continuity.proto)
- 范围：为 Runtime 常驻 Chromium/Greenfield 的窗口级媒体、viewer 授权、输入控制、同实例及故障语义提供最小 v1 Proto、ADR 和 SDK 契约导出；运行生成与契约校验
- 不在本任务实施：Greenfield package 补丁、Runtime/Gateway/desktop-web adapter、真实 Code 验收、数据库 migration 或用户界面
- 验收：v1 字段/服务新增且不改变既有 Native RPC，`make generate` 产生 Go/TS SDK，`@workos/protocol` 和 agent-sdk 可引用服务，`buf lint` 与格式检查通过；记录尚未实现的端到端门禁

## 设计决定

用户选择 Runtime 常驻 Chromium/Greenfield 合成器。[ADR-0040](../decisions/0040-resident-greenfield-window-media.md)定义 P0 最小媒体与安全边界。每窗口有界 PNG tile 经 Runtime fanout 和 Connect server-stream 输出；首帧必须由客户端收齐、解码和绘制完整 frame 后才算 ready。媒体传输没有 WebRTC/SDP，也不把 Greenfield 原始 proxy key 给客户端。仅 live SurfaceAttachment 可观看；控制和写入另用其单调 control generation 及事件 sequence 校验。Go broker 与独立 child 容器的 Node bridge 通过有版本/长度上限的 Protobuf Unix socket envelope 通信。

实际发布包 `@gfld/compositor@1.0.0-rc1` 的多 scene 默认重复全局视图，逐窗口过滤与投影平移需要独立实现。固定 proxy commit `6c578f4` 的 compositor TypeScript 源 API 与 npm rc1 发布物不同，实施前须分别定版和重建验证；参见审计。

## 后续实现与验收交接

1. Runtime 增加 viewer 中性授权 port：基于 Gateway 注入的 owner/device、当前 `SurfaceAttachment.id` 与 workload generation 验证 live attachment，不能复用要求控制权的 `AuthorizeInputGeneration`。当前 `NativeSessionHandle` 未保存 ID，UI wrapper 必须保留 `AttachSurfaceResponse.attachment.id`；它仅属于当前 attach，重连需重新 Attach。`WatchGreenfieldWindows`/`WatchGreenfieldWindowFrames` 开始时和运行中每帧/至少每秒重验；Detach/Stop/Restart/附件撤销至迟一秒终止，Gateway 的 30 秒 device session stream 再验证为独立一层。
2. Runtime 在 `NetworkMode=none` 的每 workload 子容器内监督 Code、proxy、Chromium/Node bridge；Go broker 留 runtime-host。child 仅挂授权工作区/fixture、私有 socket 和 GPU，绝不能继承数据库环境、Docker socket或访问宿主 loopback。隔离不可用即 unavailable。全部外部 viewer 离开不停止三个组件；renderer 故障不可报告同实例恢复，Restart 使用新 generation。逐窗口 scene 裁剪含 popup/subsurface，窗口位置/尺寸变化产生有序 snapshot。
3. Broker 对每个输入事件（含 Shell 到 native 焦点激活）核对控制代次和 sequence，并将接管与单事件 child 派发串行；丢弃旧代次排队操作。帧 tile ≤512×512/2 MiB，frame ≤64 tiles/24 MiB，snapshot ≤128 windows/1 MiB；慢 viewer 丢增量后强制全帧恢复，不能漏块仍报 ready。Gateway auth store outage 时新 Watch 流至迟 30 秒 fail closed。纯文本 selection 目标上限 1 MiB；当前 Runtime domain 为 256 KiB，实施须提升并真实接入 Wayland selection，读操作不得返回旧缓存。
4. 宿主 GPU/Code 像素、DPR 1/2、IME、双向真实剪贴板、两设备观察和接管、零 viewer 后未保存编辑接续，按 P0 A01–A12 逐项端到端验证。A10 必须测输入到像素可见 p95；合成 Code 样式 headless POC 在 1440×900 的 PNG 编/解码 p95 为 24/21 ms，DPR2 2880×1800 为 61/65 ms，后者仅编解码已超 100 ms；512×512 tile 编/解码 p95 为 11/6 ms（DPR1）、10/6 ms（DPR2），约 150 KiB/块。dirty tile 必须实测优化；POC 不是真实 Code/GPU/网络验收。

## 验证记录

在本独立 worktree、基线 `c86e944` 加本任务修改上：

- `make generate` 两次成功；只新增 `native_window.proto` 对应 Go、Connect Go 和 TS 生成文件，未手改 `gen/` 或 `src/gen/`。
- `make proto-check` 成功（Buf format/lint、sqlc vet）。
- `@workos/protocol` 与 `@workos/agent-sdk` check 成功；最终 `make check` 成功（Go vet/test、全 workspace ESLint/Prettier/typecheck/unit tests、desktop-web build、status render check）。宿主无 make，使用已有 `workos-make:local` 容器挂同路径工作树和 Docker socket执行相同 Makefile 目标。
- 首次 `make check` 发现主线既有两个 Prettier 格式问题；只格式化 Greenfield 输入测试 fixture 和本审计文档后复跑通过。

本任务未实现 Runtime/Gateway/浏览器 producer 或 consumer，未运行真实 Code A01–A12 验收。实施门禁与当前风险见 ADR-0040；`docs/status.json` 的 P0 完成度不因本契约而变化。
