# V2 原生目标、项目 Skills 与子 Agent

- 状态：done
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

## 实现进展（2026-09-20，未完成）

- Core：持久化目标投影/显式命令、可重放暂停请求、每个 Task 最多两个 delegation，
  与事件同事务提交；取消/失租约撤销自动目标并将未结算子任务标为 needs_review。
- Runtime：专属 bare repository + Git worktree；源仅准备时只读挂载，子命令只挂载自己
  的工作树与 Git 元数据。拒绝 dirty 项目、重复准备、scope 漂移与未知准备结果；产出
  含新增文件的有界 diff。生产 overlay 使用 preview bridge root 下独立 delegation 目录。
- Harness：仍使用官方 goal driver、skill/filesystem/tool 和 spawn-in-process；项目技能
  禁止默认根和 watcher；原生子 session 经 AsyncLocalStorage 绑定 canonical delegation。
  并行工具 RPC 保持帧串行写入；父子模型请求预留同一个 Task 的剩余额度。
- 已通过：Core Agent/Harness/Runtime workspace 相关单测；真实 PostgreSQL 的命令幂等、
  暂停重放、容量竞争、取消回收（`tmp/native-automation-transactions.log`）；真实锁定
  Harness + 本地模型的目标暂停/恢复/预算、两个前台子 Agent 并发及作用域/合计预算、
  项目技能调用（`tmp/native-all-probe.log`）；真实 Docker 两个 Git 工作树隔离、dirty
  拒绝、hook/fsmonitor 隔离及新增文件 diff（`tmp/native-worktree-docker.log`）。
- UI 修改前已复制 [before](../ui/desktop-web/changes/20260920-v2-native-goals-skills-subagents/before/)。
  目标控制、子任务与结果查看正在接入；六进程浏览器 E2E、after/current、异常矩阵、
  有界真实模型验收和全仓检查尚未完成，不得标记 done/working。

## 2026-09-21 验收进展

- P3 最终证据 `b232993` 已纳入本分支（cherry-pick `8d507cc`）。
- 完整 `sh tools/native-automation/gate.sh` **PASS**，`tmp/v2-completion.XoC8R3`：
  真实两个原生子 session / Docker worktree / 成果；原生技能；目标创建、幂等暂停；
  Core/Harness/Gateway 重启后浏览器审阅和恢复到轮数上限；取消/撤权回收两个运行容器。
  四个 Chromium 用例（含三尺寸视觉）无 skipped/flaky；自有栈已清理。
- 联调修复两项：工作区来源是 opaque `ws_…`，不能用 UUID 类型存储；普通 Task 的
  单类型成果名额须与每个 Core 授权子任务分开，新增 Artifact-owned migration 075。
  PostgreSQL 回归证明独立成果/幂等重放/未知 grant 拒绝，普通输出限制不变。
- 官方二进制 probes 与 race 再次通过，含缺 usage 拒绝后续付费请求、子 Agent 不可
  派生或调用目标/提问工具；UI 十一个组件用例通过，全仓 ESLint/Prettier 通过。
- [before](../ui/desktop-web/changes/20260920-v2-native-goals-skills-subagents/before/)、
  [after](../ui/desktop-web/changes/20260920-v2-native-goals-skills-subagents/after/)、
  [采集说明](../ui/desktop-web/changes/20260920-v2-native-goals-skills-subagents/notes.md)；current 已更新。
- 真实 DeepSeek 已证明两名隔离子 Agent、原生 Skill 加载与 get_goal/update_goal 完成。
  后续持续开发 oracle 发现提示允许具名导出、断言只接受直接函数导出的歧义；首次该子用例
  不算通过，已明确提示后单独重跑。没有降低独立执行测试或函数行为的断言。
- 价格于 2026-09-21 核对 [官方定价](https://api-docs.deepseek.com/zh-cn/quick_start/pricing/)：
  使用兼容模型名，实际由 V4.1-Flash 服务。预留按高峰输入 2/输出 8 元每百万 tokens，
  无缓存折扣、失败不退款，所有本轮调用共用仓库外台账，上限 19 元（用户授权 20 元）。
  首轮 18 请求保守预留 2.866172 元，观察到的 token 按无折扣峰值估算 0.189456 元。
  日志仅存用量，真实凭据经 stdin 导入隔离 Vault，执行结束已撤销。
- 待收口：真实持续两轮重跑、最终全仓 check/generate、长期结果摘要和代码审查。

## 最终验证（2026-09-21）

完整六进程门禁 `tmp/v2-completion.N4kvoP` 通过，新增运行中 Harness 强杀/重启
证明中断资源回收和持久化 needs_review。修复 expired-lease Claim 的遗漏，并通过
取消/失租约 PostgreSQL 回归；已完成子成果不被覆盖，不重放未知执行。
真实原生自动化最终完整 PASS（`tmp/v2-completion.vTQzkZ`）；同一会话 double/triple
持续开发重跑 PASS（`tmp/v2-completion.KX05lJ`）。当前累计保守预留 6.055478 元，
观察用量按峰值估算 0.398880 元；所有真实 Vault 凭据已撤销。
`make generate` 前后 211 个生成文件哈希相同。完整证据见
[长期验收记录](evidence/20260920-v2-native-goals-skills-subagents/results.md)。
最后 `make check` 全部通过；Go vet/单测、Proto/sqlc、架构、ESLint/Prettier、全部 TypeScript 工作区检查及桌面构建通过。进入独立网络任务。
