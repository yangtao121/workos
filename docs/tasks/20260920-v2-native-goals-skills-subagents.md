# V2 原生目标、项目 Skills 与子 Agent

- 状态：in_progress
- Branch：`feat/v2-native-goals-skills-subagents`
- Worktree：`/home/aquatao/workos-v2-native`
- 基线：`3f51fa4`；P3 完整门禁在独立 worktree 收尾，最终合入其证据提交。
- 设计：[V2](../structure-v2.md)、[现有会话/工作区](../architecture/v2-agent-workspace-web-continuity.md)。

## 范围

复用锁定的 DeepSeek Harness `0.1.1rc1`：接入官方 goal/goal-round-driver/tool-goal、
skill/tool-skill registry 和 spawn-in-process。WorkOS 只实现生命周期、canonical 投影、
授权、预算和受限工具后端，不另写 Agent 循环。

目标状态持久化由原生 session log 拥有，Core 保存可恢复的展示投影及用户命令；暂停、
显式恢复和崩溃后的 needs_review 不混淆。所有自动轮次和子 Agent 共用当前 Task 的凭据
租约、输出预算、截止时间与项目授权。

Skills 仅来自已绑定项目的 `.dsh/skills`、`.agents/skills`；不扫描宿主 HOME，不获得额外
权限。子 Agent 使用原生独立 session、最多同时两个、深度一，并由 Runtime 提供独立
可写 Git worktree；工具不能选择任意宿主路径或扩权。取消/撤权必须回收运行资源，保留
可核对的结果，禁止任意后台 Bash。

不增加 Codex/MCP、iOS、FCM、KasmVNC 或新的进程边界。真实模型验收继续共享用户授权
的人民币 20 元总预算，经 Vault 使用仓库外凭据；不得写入此 worktree 或日志。

## 顺序与验收

1. 阅读锁定上游源码、既有 Proto 和会话/工具测试，形成 ADR 与 additive canonical 契约，运行 `make generate` 后提交契约。
2. Core 授权/投影、Runtime worktree、Harness adapter 和 UI 顺序实现，更新各模块文档。
3. 官方原生 runtime + 本地模型 fixture 验证目标自动多轮、暂停/恢复、进程重启、Skills、两个子 Agent 隔离与合计预算；不得以 mock Harness 替代。
4. 真实六进程浏览器链路及三尺寸 before/after/current；错误路径含失租约、撤权、超预算、子 Agent 失败和重启。
5. 经 Vault 的有界真实 DeepSeek 验收、`make generate` 无生成差异、`make check`、受影响 race，随后更新状态事实。

## 当前事实

- 新 worktree 起始干净；[ADR-0034](../decisions/0034-native-session-goals-and-delegated-worktrees.md) 与 additive 契约已加入，运行 `make generate` 生成 Go/TypeScript。
- 契约提交阶段的新控制与 delegation 明确返回 unavailable/Unimplemented，原 Provider capability 仍为 false；不能把本提交当作功能完成。接下来顺序实现，替换这些入口拒绝。
- 契约验证：`make generate`、buf lint/相对 `3f51fa4` 的 breaking 检查、Core Agent/orchestration 与 Runtime workspace 单测、protocol TypeScript 检查通过。
- 已读取锁定 tag `dsh-v0.1.1-rc.1` 的目标、技能、子 Agent、Agent 和工具源码，研究副本在主 worktree 的 gitignored `tmp/v2-native-upstream/`。
- 上游 goal activation 不持久化；恢复原生 session 不会自动继续，必须显式 resume。round driver 自己负责多轮，WorkOS 不补写调度循环。
- 官方 `tools/execute` 和 `agent/request` 提供调用 Agent 的作用域，可用于后端隔离和合计预算；每个原生子 Agent 自有 session。实际二进制组合仍需动态验证。
