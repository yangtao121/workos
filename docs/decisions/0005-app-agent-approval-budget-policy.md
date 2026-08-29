# ADR-0005：App Agent 持久预算策略、运行前审批与入队前配额预留

- 状态：Accepted
- 日期：2026-08-29
- 关系：落实 `docs/structure.md` 1.3/1.4/7/9.6（App 可配置调用权限与预算、确定性
  资源保护、按维度统计 AI 消耗、异常调用可切断）；沿用 ADR-0002 的信任边界与
  ADR-0003 的 grant epoch 语义，均不修改；沿用 ADR-0004 确立的"mapping 表是唯一
  幂等事实源 + 版本化首次响应快照"模式。

## 背景

installation grant 只回答"App 能否调用 `agent.task.run` / `agent.event.watch`"，
不回答：单任务允许多少输出 token、运行多久；每 App 每 UTC 日最多提交多少任务、
预留多少 token；哪些 App 自动运行、哪些必须逐次审批；并发如何不超卖；pending
approval/quota/usage 如何跨重启；provider 不支持硬预算时如何 fail closed；usage
超过授权时如何确定性切断。本 ADR 固定这些语义。

## 决策

### 1. Policy 属于 Core Agent，而不是 Project grant

manifest requested permission 表达"App 想要什么"；installation grant 表达"用户允许
哪些 bridge method"；二者都不是预算。预算/执行模式是运行治理事实：它随用户对
"这个 App 每天能花多少"的判断变化，与"允许它调用哪个方法"无关，且必须能被 Agent
域独立审计与演进。因此：

- key 是 `(owner_user_id, app_instance_id)`，持久绑定 project snapshot（仅 ID 快照，
  无跨模块 FK/SQL）；owner 是 `workos-core Agent`（migration `014`）。
- policy 是 full replacement + `expected_policy_revision` optimistic 并发 + owner-scoped
  idempotency key（独立 request 表 + canonical digest + 版本化结果快照，ADR-0004 模式）；
  same key/same request 精确重放第一次结果，different request 稳定 `Aborted`，失败
  不消费 key。
- policy 永远不能扩大 grant：`agent.task.run` 未 grant、grant revision stale、
  Project archived、installation inactive 时，即使 policy=allow 也 fail closed。两条
  安全边界（grant epoch 即时失效、policy 治理新任务）同时成立、互不替代。
- policy 真实变化递增自身 revision，并原子失效该 App 的全部 pending approval（其
  waiting task 终止为 cancelled + `approval_expired` 事件）；相同 policy 是确定性
  no-op 但仍消费 idempotency key。policy 变化不影响已 queued/running/terminal 的
  durable task，不改 Project revision/grant revision/Surface session，不发 Project 事件。

### 2. 版本化 system default，绝不 unlimited

无 explicit policy row 时使用代码与测试钉死的 system default v1：
`allow`、每任务 4096 输出 token、120 秒 runtime、每 UTC 日 50 任务、204800 保留
token（= 50 × 4096）。选择依据：保持已被 grant 明确授权的既有 App run 可继续工作，
同时对齐现有 adapter 事实边界（DeepSeek 单任务 cap ≤ 384000 token、≤ 600s；
Fake 输出恒 ≤ 4 token），远小于 provider 最大值；数值全部有限、非零，默认拒绝
"配置缺失 = 无限"的隐式语义。default 版本化（`system_default revision = 1`），
未来调整需要新版本与显式迁移决定，不静默改变历史任务的快照。每个新 task 在创建
事务内持久化 effective policy source/revision/digest 与 server-derived budget
snapshot；历史任务不按新 policy 重新解释；014 之前的既有 task 列为 NULL，按
legacy/unknown 如实标记，绝不回填为"已计费 0"。

### 3. Quota 采用入队前 reservation，而不是事后核算

第一版硬配额必须是确定性的：每个真正入队（queued）的 task 在入队事务内对
`(owner, app_instance, UTC date)` bucket 原子 `tasks_reserved += 1`、
`output_tokens_reserved += effective max_output_tokens_per_task`，带 guarded UPDATE
（越界即整个事务失败）。理由：

- usage 是观测、reservation 是授权：provider usage reporting 存在缺失/迟到/聚合
  粒度问题（DeepSeek 在 run 结束才发聚合值），事后核算无法阻止并发超卖；
- bucket 不含 policy revision：换 policy 不能绕过当日已消费额度；
- reservation 一旦入队当日内不退款（provider failure/cancel 也不退）：退款需要
  可靠的 task→bucket 反查与补偿语义，是并发重复消费的温床；fail-closed 更符合
  资源保护的确定性要求；waiting/rejected task 从未入队，天然不占额度；
- 并发由 PostgreSQL 行级仲裁（`INSERT … ON CONFLICT DO UPDATE … WHERE` guarded），
  两个 Core 实例并发 fresh run 在最后一格额度上恰有一个 winner，无 orphan task。

### 4. WorkOS 运行前审批 ≠ provider tool approval

两者是不同事实、不同协议面：

- WorkOS pre-run approval（本 ADR）：任务入队**之前**，owner 对"这个 App task 可以
  运行"的授权决定，由 Core Agent 拥有、public approval service 承载，是排队门禁；
- provider tool approval：任务执行途中 provider 请求工具授权，需要未来扩展 Harness
  `approve()` 协议与具体 adapter（`docs/structure.md` 5.1 的 HarnessConnection.approve），
  本任务不实现、不声称、也不把 WorkOS approval 映射成 `HarnessCapabilities.approvals`。

approval 事实：`waiting` task + pending approval row + `approval_required` 事件
（复用 canonical 事件，状态机已映射 waiting）在创建事务内原子提交；**不创建可
claim 的 outbox、不预留额度**。decide 带独立 idempotency key；`approve`/`reject`
是唯一合法 decision；并发相反 decision 恰一个 winner（数据库行锁仲裁），loser 得到
稳定 `Aborted`/replay。approve 前重验 active installation、current grant、policy
identity（revision 一致）与 provider capability；任何失败保持 pending 且不占额度。
approve 成功 = decision + reservation + `waiting → queued` + `approval_approved`
事件 + outbox 单事务。reject = decision + task terminal + `approval_rejected` 事件
单事务，零 outbox、零 reservation。policy 变更使 pending approval 失效且永不排队。
approval ID 是普通资源 ID（UUIDv7），不是 bearer token；App/iframe 只能从任务事件
知道"等待用户"，永远不能决定。

### 5. Provider capability 必须显式声明硬预算

Core 不得以 `provider_id == "deepseek"` 特判预算支持。`HarnessCapabilities` 增加
additive `hard_token_budget` 与 `hard_runtime_deadline`：只有 adapter 的测试证明其
真实执行 token cap 与 runtime deadline 后才声明 true；Generic CLI 等如实 false，
fresh App run 在入队前验证 provider snapshot 的三项能力
（hard token budget + hard runtime deadline + usage reporting），缺失为净化
`FailedPrecondition`，不创建 task、不消费 run key、不 fallback 到其他 provider。
worker 用 server-derived `max_runtime_seconds` 建立独立 context deadline：即使
adapter 忽略 deadline，worker 也取消 run 并合成恰好一个 terminal 事件、正确结束
lease。token cap 由声明支持的 adapter 映射进 provider request；Core 只认识
canonical `AgentBudget`。

### 6. Usage 是观测事实；breach 触发确定性 circuit break

`UsageRecorded` 事件与 task event append 在同一事务内累计到 Agent-owned per-task
usage 行与 per-bucket usage projection；input/output token 验证非负、有界，model
为有界文本，cost 仅作可选已验证观测（本任务无版本化价格源/currency/model pricing，
`max_cost_decimal` 不参与任何 enforcement，Core 不硬编码价格——vendor-neutral 金额
硬上限被显式声明为未实现）。reported output usage 超过该 task 的 reservation
snapshot 时：同事务记录 bucket breach 并置 task `cancellation_requested`（worker
续租路径确定性取消）；breached bucket 的后续 fresh run fail closed
（`ResourceExhausted`）。reserved allowance 与 reported actual usage 在 API/UI 中
分离展示；cost 不可用时显示 unavailable，绝不显示为 0。

### 7. 线性化点与错误语义

- 所有事务边界在 Core Agent PostgreSQL；fresh run 的线性化点是 allow 路径的
  reservation/task/mapping/outbox 提交；approval 的线性化点是 decide 事务提交。
- `block` = `PermissionDenied`；provider capability 缺失与已失效 pending approval
  的 decide = `FailedPrecondition`；quota/circuit break = `ResourceExhausted`；
  stale policy revision / 已决定 approval / same key different request = `Aborted`；
  quota 失败不消费 App run key（UTC 日切换后同 key 可重试）。
- 客户端请求 digest 仍只覆盖 bounded role/goal（`workos.agent-app-run.v1` 不变，
  replay 语义跨升级稳定）；policy digest/revision/provider/budget 是服务端审计
  快照，不混入 client digest。App Bridge request、private runtime→Core request 与
  MessageChannel envelope 不增加任何 budget/policy/approval/quota 字段；
  `AgentTaskInput.budget` 只由 Core 注入。

## 后果

- migration `014`（owner：workos-core Agent）：`agent_app_policies`、
  `agent_app_policy_requests`、`agent_app_approvals`、`agent_app_daily_reservations`、
  `agent_app_daily_usage`、`agent_task_usage` 六张新表 + `agent_tasks` additive
  budget/policy 快照列 + provenance task 反查索引；001–013 逐字节不变（checksum
  钉住）。legacy task 的 policy/usage 事实保持 NULL/unknown，不伪造历史。
- 三个新 public Connect service（policy/approval/usage）进入 Gateway allowlist，
  全部 identity 保护的 owner-scoped 读取与决策；private `AppAgentService` 保持不公开。
- Desktop：App Library 已安装行增加 `Agent policy` 入口与编辑器（system
  default/explicit、mode、限额、UTC 语义、"policy 不能代替 permissions"）；Agent
  Center 在现有窗口内增加 Approvals 与 Usage 视图。
- Harness：`HarnessCapabilities` 两个 additive bool；Fake/DeepSeek 更新 capability
  声明并有测试证明；worker 强制 runtime deadline；三处 capability 映射同步。
- 明确不实现并如实报告 unavailable：provider tool approval（未来 Harness `approve()`
  协议）、金额硬上限（需版本化定价源）、per-stream 中途 token 切断（当前 adapter
  usage 为 run 末聚合，circuit break 依赖 usage 事件与 worker 续租检查点的组合）。
