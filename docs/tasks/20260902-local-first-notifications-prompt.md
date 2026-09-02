# Task: Local-first notifications overnight prompt

- 状态：done
- Owner/Agent：Codex
- 进程/模块：docs/prompts；仓库级实现交接设计
- 依赖：`main` at `a877fac`；`docs/structure.md`、`docs/architecture/implementation.md`、
  `docs/status.json`、现有 Agent/Artifact/Incident/App Bridge/Gateway/Adaptive Shell 链路

## 目标与范围

为下一位实现智能体编写一个可无人值守执行至少七小时、预期十至十四小时的单分支 Prompt。Prompt
围绕一个明确的平台闭环：把 Agent、Artifact、Reliability Incident 与获得明确 grant 的 Web Bundle App
产生的有限事件，收敛为 Core 持有的 durable notification；owner 可以在已配对桌面/移动布局中实时接收、
断线补收、跨设备同步单调已读状态并通过 typed action 打开权威目标。

Prompt 本身只定义任务，不实现通知功能。它必须要求一个 branch、一个 worktree、一个实现任务记录和
一个写入智能体，全部阶段串行；不得把 APNs/FCM、假 WebSocket、直接跨 schema SQL、真实 Provider、
rootless Podman blocker 或固定成功 adapter 混入交付。

## 协议/数据影响

none（本任务只新增 Prompt/任务记录；未来实现任务应先 additive 修改 Proto，并按单一进程所有权新增
forward-only migrations）。

## 验收

- [x] 最终目标唯一，覆盖 system source、incident source、granted App、owner UI 和 paired device
- [x] 工作量分阶段达到十至十四小时，且不依赖 Podman、外部 Push 或真实 Provider
- [x] 单 branch/worktree/task record/writer 纪律及阶段提交顺序明确
- [x] Notification/事件流/已读/idempotency/cursor/retention/跨进程 publication 所有权明确
- [x] App grant-revision、配额、opaque iframe、Bridge/SDK/app-host 与 revoke fail-closed 边界明确
- [x] Gateway session、流重连、跨设备同步、adaptive UI、视觉证据和 typed action 边界明确
- [x] 三个专项门禁、restart/fault matrix、状态升级条件与交接格式完整
- [x] Prompt 明确 Reliability 真实 supervisor 能力仍受 Podman 证据限制，不得借通知测试升级

## 交接

- Prompt：`docs/prompts/20260902-next-agent-local-first-notifications.md`
- 唯一实现分支：`feat/v1-local-first-notifications`
- 唯一实现任务记录：`docs/tasks/20260902-v1-local-first-notifications.md`
- 实现主线：Core durable notification + atomic Agent/Artifact producers + Reliability durable publication +
  resumable owner stream/read sync + adaptive Notification Center + capability-scoped App notification creation。
- Prompt 要求新增三个真实门禁：`make test-notification-center`、`make test-app-notifications`、
  `make test-incident-notifications`，并扩展 restart battery。
- 编写验证：`make generate` 连续两次 PASS 且无生成漂移；`make check` PASS；两份 Markdown 的 Prettier
  检查、`git diff --check`、`docker compose config --quiet` 均 PASS。
- 本记录与 Prompt 不修改 Proto、migration、生成目录、README 状态区块或现有 capability；实现、ADR、
  UI evidence 与状态升级全部留给下一位 Agent 在唯一 feature branch 完成。
