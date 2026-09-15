# V2 持续开发会话与 Web 应用接续（B00–B10）

- 任务提示词：[docs/prompts/20260915-glm-5.3-v2-p0-p2-goal.md](../prompts/20260915-glm-5.3-v2-p0-p2-goal.md)
- 基线：`main@a33e71f`；分支：`feat/v2-agent-workspace-web-continuity`
- 依据：[V2 架构](../structure-v2.md) §3–4
- 状态：in_progress（B00 已完成，B01 进行中）

## 工作包状态

工作包 | 状态 | 依赖 | 实现入口/提交 | 验收编号 | 实际命令与结果 | 长期证据路径 | 未决问题/下一步
------ | ---- | ---- | -------------- | -------- | -------------- | ------------ | ---------------
B00 | done | — | `docs/architecture/v2-harness-workspace-baseline.md`（本分支首个提交） | A01 | 见下"B00 证据" | 基线文档 + tmp/b00 探测脚本输出 | 上游无 wire 级 session resume；B03 采用"每会话常驻官方 runtime 进程 + jsonl 持久化"
B01 | done | B00 | ADR-0030/0031；`api/proto/workos/agent/v1/session.proto`、`project/v1/workspace.proto`、`workload/v1/workspace.proto`、`surface/v1/continuity.proto`；native/pty 新增 Detach；migration 057–059；domain 状态机（core agent session、runtime surface continuity） | 契约层 | `buf lint` 通过；`go build ./...`、`go vet`、`go test ./internal/core/agent/... ./internal/runtime/surface/... ./internal/runtime/{ptyhost,nativehost}/...` 全绿 | ADR + 契约 + migration + 状态机测试 | B02 起按新契约实现持久化与业务链路
B02 | in_progress | B00、B01 | — | A02、A03 | — | — | —
B03 | todo | B00–B02 | — | A04–A07 | — | — | —
B04 | todo | B01–B03 | — | A08、A09 | — | — | —
B05 | todo | B01、B03、B04 | — | A17（部分） | — | — | —
B06 | todo | B01 | — | A10、A11、A13 | — | — | —
B07 | todo | B01、B06 | — | A12 | — | — | —
B08 | todo | B02、B05、B06、B07 | — | A17 | — | — | —
B09 | todo | B00–B08 | — | A01–A18 | — | — | —
B10 | todo | 其余包 | — | A18 | — | — | —

## B00 证据（2026-09-15，host probe）

对官方锁定 runtime `deepseek_harness_runtime_bin==0.1.1rc1`（x86_64 wheel sha256
`8eb31e3a…137b`，与 Dockerfile 一致）做了静态源码提取 + 动态协议探测。完整结论在
[基线文档](../architecture/v2-harness-workspace-baseline.md)。关键事实：

- `sdk-jsonrpc-server` 仅 dispatch `initialize` / `session/prompt` / `shutdown`；
  0.1.5rc1 相同 → 保持 0.1.1rc1 不升级。
- 同进程同 sessionId 多轮续聊：第二轮模型请求包含第一轮 user/assistant（
  `COUNT total=6 markers=TURN1`）→ 原生上下文累积成立。
- 运行中追加输入：官方语义为排队到下一轮（`agent/inbox/spliced target=next-turn`）。
- 官方 base 组合（非 agent-spine-demo）注册 `bash/read/write/edit/glob/grep`；
  bash 真实执行 `pwd` 返回工作区路径；参数校验、[exit code] 标记真实存在。
- `write` 工具越界写被 `workspace-write` sandbox 拒绝；工作区内成功。
- bash 子进程 env 中 `DEEPSEEK_API_KEY`/`DSH_CORDIS_CONFIG` 计数为 0（官方 env 隔离）。
- bash-local 执行器不 confine（`dsh-bash-sandbox` 未打入 runtime bin）→ bash 可越界写，
  WorkOS 必须自己实施进程级边界（B02/B03 设计约束）。
- `session-persistence-jsonl` 追加式持久化每会话事件日志；跨进程 wire 级 resume 在
  0.1.1rc1/0.1.5rc1 均不存在（服务层 `agents.resume` 未暴露）。

## 恢复入口

- 当前包：B02（真实开发工作区）。
- 已完成动作：B00 基线（f9cffa8）；B01 契约（ADR-0030/0031、四个新 proto、
  migration 057–059、domain 状态机与测试、native/pty Detach 实现与测试）。
- 正在运行的进程/日志：无。
- 下一条具体动作：B02——Core workspace binding 持久化 + runtime workspace source
  注册/PrepareWorkspace + 隔离边界验收。
- 阻塞：无。

## 验收矩阵（A01–A18）

按提示词 §6 逐行补齐实际命令、结果与证据路径；当前仅登记 B00 已覆盖行。

| 编号 | 状态 | 证据 |
| ---- | ---- | ---- |
| A01 | pass（软件侧） | 基线文档能力矩阵 + 动态探测记录（tmp/b00 摘要已写入基线文档） |
| A02–A18 | todo | — |
