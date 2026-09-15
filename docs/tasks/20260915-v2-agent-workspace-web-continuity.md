# V2 持续开发会话与 Web 应用接续（B00–B10）

- 任务提示词：[docs/prompts/20260915-glm-5.3-v2-p0-p2-goal.md](../prompts/20260915-glm-5.3-v2-p0-p2-goal.md)
- 基线：`main@a33e71f`；分支：`feat/v2-agent-workspace-web-continuity`
- 依据：[V2 架构](../structure-v2.md) §3–4
- 状态：in_progress（B00 已完成，B01 进行中）

## 工作包状态

工作包 | 状态 | 依赖 | 实现入口/提交 | 验收编号 | 实际命令与结果 | 长期证据路径 | 未决问题/下一步
------ | ---- | ---- | -------------- | -------- | -------------- | ------------ | ---------------
B00 | done | — | `docs/architecture/v2-harness-workspace-baseline.md`（本分支首个提交） | A01 | 见下"B00 证据" | 基线文档 + tmp/b00 探测脚本输出 | 上游无 wire 级 session resume；B03 采用"每会话常驻官方 runtime 进程 + jsonl 持久化"
B01 | in_progress | B00 | ADR-0030/0031、`api/proto/workos/` 新增 session 与 surface continuity 契约 | — | — | — | 进行中
B02 | todo | B00、B01 | — | A02、A03 | — | — | —
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

- 当前包：B01（契约与持久事实）。
- 已完成动作：分支创建、任务记录、B00 基线文档与提交。
- 正在运行的进程/日志：无（探测 fixture 已停止；tmp/b00/ 保存探测脚本与输出）。
- 下一条具体动作：写 ADR-0030/0031，修改 `api/proto/workos/` 新增 harness session 与
  surface continuity 契约，`make generate` 后进入 B02。
- 阻塞：无。

## 验收矩阵（A01–A18）

按提示词 §6 逐行补齐实际命令、结果与证据路径；当前仅登记 B00 已覆盖行。

| 编号 | 状态 | 证据 |
| ---- | ---- | ---- |
| A01 | pass（软件侧） | 基线文档能力矩阵 + 动态探测记录（tmp/b00 摘要已写入基线文档） |
| A02–A18 | todo | — |
