# Task: App Agent 持久预算策略与运行前审批

- 状态：done（2026-08-29 四轮审查阻断项全部修复：第一轮 4×P1 + 4×P2，第二轮
  2×P1 + 3×P2，第三轮 2×P1 + 1×P2，第四轮 2×P1，全部门禁重新通过；见执行记录内四份
  审查修复记录）
- Owner/Agent：AquaTao + WorkOS 实现智能体
- 进程/模块：workos-core（Agent：policy/approval/quota/usage 事实 owner）、harness-host
  （capability + worker deadline + adapter token cap）、runtime-host（无契约变化）、
  workos-gateway（allowlist）、desktop-web（policy 编辑器 + Approvals/Usage 视图）
- 依赖：Project App Installation grant（ADR-0003）、App Agent provenance（009）、
  Harness Catalog（approvals/usage_reporting capability）、Fake/DeepSeek adapter

## 目标与范围

为 active App installation 建立 Agent-owned 持久策略：

- `execution_mode = allow | require_approval | block`；
- 每任务 `max_output_tokens` / `max_runtime_seconds`；
- 每 UTC 日 `max_tasks` / `max_reserved_output_tokens`；
- 版本化 policy revision + canonical digest；system default 版本化且有限（绝 unlimited）。

fresh App run 裁决顺序固定：replay 优先 → grant/epoch 授权（既有）→ effective policy →
provider snapshot → provider capability（hard token budget + runtime deadline + usage
reporting 全部显式支持）→ mode/quota 裁决。`allow` 在一个 Agent 事务内原子提交
task + provenance + policy/budget 快照 + daily reservation + outbox；`require_approval`
创建 waiting task + pending approval + `approval_required` 事件，不入队、不占额度；
`block`/quota exhausted/capability 缺失 fail closed，不消费 run key。

Owner-only 运行前审批：public approval read/list/decide 契约；decide 带独立 idempotency
key；approve 前重验 installation/grant/policy/provider capability，随后 decision +
reservation + `waiting → queued` + task event + outbox 单事务；reject 原子 terminal；
并发相反 decision 恰一个 winner。

配额是入队前确定性 reservation：bucket = (owner, app_instance, UTC date)，入队后本
UTC 日不退款；`UsageRecorded` 与 task event 同事务累计 Agent-owned usage projection；
reported output 超过 task reservation → 记录 breach + 确定性 cancellation/circuit
break（bucket 后续 fresh run fail closed）。reserved allowance 与 reported actual
usage 分开展示；不实现货币硬上限，`max_cost_decimal` 不被 Core 硬编码价格。

### 非目标

Provider/tool-call 中途审批、Harness `approve()`、steer/resume/persistent session；
vendor-neutral 金额硬上限与真实定价同步；App 自选模型/Provider；自动取消 policy/grant
变化前已入队的 durable task；Credential Vault、生产 device auth、container/native
runner、Reliability enforcement、Indexer/RAG。

## 协议/数据影响

- Proto：`api/proto/workos/agent/v1/app_policy.proto`（新 public services：
  `AgentAppPolicyService` Get/Set、`AgentApprovalService` List/Get/Decide、
  `AgentAppUsageService` GetUsage）；`agent.proto` additive oneof 字段
  `approval_decided = 22` / `approval_expired = 23`；`harness.proto`
  `HarnessCapabilities` additive `hard_token_budget = 12` / `hard_runtime_deadline = 13`。
- Migration：`014_agent_app_policy_quota.sql`（owner：workos-core Agent）：policy、
  policy request（幂等 + 结果快照）、approval、daily reservation bucket、daily usage
  projection、per-task usage 表 + `agent_tasks` additive budget/policy 快照列 +
  provenance task 反查索引。001–013 逐字节不变。
- 事件：`approval_required`（已 canonical）；新增 Core 侧系统事件
  `approval_approved` / `approval_rejected` / `approval_expired`（canonical oneof
  additive 字段）。
- Gateway：三个新 public service 进入 allowlist；private `AppAgentService` 仍不公开；
  App Bridge request、MessageChannel envelope、iframe SDK 不新增任何 budget/policy/
  approval/quota 字段。

## 验收

- [x] Domain/application：policy full replacement、system default、revision/no-op、
      digest/idempotency、边界值、replay 优先、mode 裁决、quota 边界、approval
      并发/失效、usage breach/circuit、净化错误
- [x] PostgreSQL/migration：pristine/forward/no-op/checksum、双 pool 并发、事务原子性、
      注入失败回滚、restart 持久、scratch cleanup
- [x] Harness：capability truthful、Fake/DeepSeek budget+usage、Generic CLI 不支持、
      worker deadline、单一 terminal、breach circuit
- [x] Transport/Gateway：identity、owner isolation、pagination、wire cap、错误矩阵、
      private service 不公开
- [x] 跨进程 Fake Harness E2E：require approval → waiting → approve → 执行 → usage；
      reject 永不执行；quota 边界；policy 变更使 pending 永不排队；restart 跨 pending
      approval 与已消费 quota
- [x] UI 视觉证据：before/after/current + notes.md（固定 1440×900 Chromium fixture）
- [x] `make check`、`make generate` 无漂移、`buf breaking`、`go test -race`、
      `make test-integration`、`make test-deepseek-fixture`、`make test-e2e`
- [x] 文档：ADR-0005、implementation.md、模块 README、`docs/status.json`

## 交接

见本文件末尾「执行记录」（随实现追加）。不以聊天记录代替仓库事实。

## 执行记录

- 基线（本地 `main` = `4159789`，工作树干净，分支
  `feat/app-agent-approval-budget-policy` 自该提交创建）：
  - `git status --short --branch`：clean；`git log -15`、`git branch -vv` 正常；
    `git diff --check` 无输出。
  - `make bootstrap` exit 0；`make check` exit 0；`make test-integration` exit 0
    （含 seed → `docker compose restart workos-core harness-host runtime-host` →
    task/app/installation/surface/bridge/grants 全部 verify）；`make test-e2e`
    exit 0（5 passed，1 skipped = deepseek fixture 门控）。
- 决策记录：ADR-0005（policy 归 Agent、quota 入队前 reservation、WorkOS pre-run
  approval ≠ provider tool approval、不声称金额硬上限）。

### 实现（本分支 `feat/app-agent-approval-budget-policy`）

- Proto：`api/proto/workos/agent/v1/app_policy.proto`（AgentAppPolicyService /
  AgentApprovalService / AgentAppUsageService + policy/approval/usage 消息）；
  `agent.proto` additive oneof `approval_decided=22` / `approval_expired=23`；
  `harness.proto` additive `hard_token_budget=12` / `hard_runtime_deadline=13`；
  Gateway allowlist 增加三个新 public service（private AppAgentService 仍不公开）。
- Migration：`014_agent_app_policy_quota.sql`（owner：workos-core Agent）：6 张
  新表 + `agent_tasks` additive 快照列 + provenance 反查索引；001–013 逐字节
  不变（`pinnedPolicyMigrationChecksums` 钉住含 013）。
- Core Agent：domain（policy/approval/usage 语法、digest、错误哨兵）、application
  （PolicyService/ApprovalService/UsageService，decide 前中立端口重验 fail
  closed）、postgres（SetPolicy/DecideApproval/CreateForApp+guarded reservation/
  CreateForAppApproval/usage projection 同事务）；TaskRouter 固定裁决序；
  ExecutionHandler 校验 usage 事件语法。
- Harness：Fake/DeepSeek 真实声明并证明 budget contract；Fake 增加 token cap
  截断与预算校验；worker 以 `budget.max_runtime_seconds` 建立独立 deadline，
  deadline 命中时合成恰一个 `RunFailed` terminal 并正确结束 lease；owner 取消
  路径不合成重复 terminal。
- Desktop：App Library policy 摘要/编辑器（full replacement + optimistic
  revision + conflict reload）、Agent Center Tasks/Approvals/Usage 视图、App
  窗口默认位置调整 + 窗口 rect 定位 + 头部拖拽；surface-sdk/bridgeErrors 增加
  `resource_exhausted`/`failed_precondition` 错误码。

### 验证记录（分支工作树，全部实测）

| 命令                                                                                                        | 结果                 |
| ----------------------------------------------------------------------------------------------------------- | -------------------- |
| `make bootstrap`                                                                                            | exit 0               |
| `make generate` ×2                                                                                          | 第二次无生成漂移     |
| `make check`（proto/go/web + status 渲染）                                                                  | exit 0               |
| `buf breaking --against '.git#branch=main'`                                                                 | 通过（仅 additive）  |
| `go test -race ./internal/core/agent/... ./internal/core/orchestration/... ./internal/harness/...`          | ok                   |
| `make test-integration`（含 014 migration 链、双池并发、breach、`policy-seed` → restart → `policy-verify`） | exit 0               |
| `make test-deepseek-fixture`                                                                                | 见门禁记录           |
| `make test-e2e`（6 specs 含新增 `approval-policy.spec.ts`）                                                 | 6 passed             |
| 视觉验收（judge 逐张）                                                                                      | after/ 7 张全部 pass |

已执行迁移 014 的持久验收 volume checksum 已记录（`dd92c010…`），与文件一致。

### 未决风险与下一步

- provider tool approval（Harness `approve()` 协议）与 vendor-neutral 金额硬
  上限仍为显式未实现；`max_cost_decimal` 仅作可选已验证观测。
- per-stream 中途 token 切断依赖 provider usage 上报粒度；当前 Fake/DeepSeek
  为 run 末聚合，circuit break 生效点在 usage 事件事务内。
- 生产 device auth、container/native runner、Reliability enforcement 仍为既有
  未完成项，不因本任务改变。

### 2026-08-29 第二轮审查修复记录（2×P1 + 3×P2）

- P1 breach 确定性终止：usage breach 不再只置 `cancellation_requested` 等
  worker 心跳观察——`projectUsage` 在检测超预算的同一事务内直接把任务推进为
  `cancelled`、写入恰一条系统 `run_cancelled` 事件（reason：超出预留 token
  预算），并 `FinishPendingTaskRequest` 收尾 outbox。竞态的 provider
  `RunCompleted` 由 terminal 状态在 `LockTaskEventStream` 处拒绝，超额任务
  不可能再完成。
- P1 worker terminal 写入次序：emit 回调只在 terminal 事件 append 成功后才
  置 `sawTerminal`——写入丢失时 fallback 的 `run_failed` 仍会补写；append
  失败路径不再提前 return，始终尝试 `FinishTaskLease`（服务端仅在任务已
  terminal 时接受，未 terminal 时保持租约恰好对应"terminal 事件仍缺失"）。
  新增 `TestWorkerRepairsALostTerminalAppend`（fakeCore 支持 append 注入
  失败 + terminal-required finish）。
- P2 损坏改为内部事实：新增 `domain.ErrPolicyCorrupt`；`policyFromDB`、
  `DecodePolicyResult` 的损坏一律包装该哨兵，transport 映射净化后的
  Internal（不再是 InvalidArgument，也不外泄存储细节）。`SetPolicy` 现在在
  任何裁决前无条件验证存量行——损坏行不会被不同 spec 静默覆盖，而是对所有
  调用方 fail closed。
- P2 cost unknown→known：两处 cost 累计改为
  `CASE WHEN 双方皆 NULL THEN NULL ELSE COALESCE(旧,0)+COALESCE(新,0) END`，
  首次有成本不再被存量 NULL 吞掉（unknown 语义保留：从未有成本仍为 NULL）。
- P2 ApprovalsView 二次串台：catch 路径在 `await refresh()` 之后复查项目
  归属再写反馈；切换项目时清除既有反馈。成功路径此前已有复查。
- 回归测试补齐：`TestAppAgentApprovalCreationVerifiesPolicyChain`（stale
  revision / default-behind-explicit / 匹配快照 / policy 变更后旧快照永久
  失效 + 零消费断言）、`TestAppAgentApprovalCreationUnderSystemDefault`、
  `TestAppAgentUsageCostAccumulatesAcrossUnknownAndKnown`（unknown→known→
  累计、跨任务桶累计）、breach 测试升级为确定性终止契约（terminal 状态、
  恰一条 run_cancelled、outbox 收尾、后续 append 被拒）、worker terminal
  修补用例、ApprovalsView deferred-switch 与切换清反馈两个 UI 用例。

#### 第二轮修复后验证（全部实测）

| 命令                                                                                                      | 结果                                 |
| --------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| `make check`（proto/go/web + status 渲染）                                                                | exit 0                               |
| `go test -race ./internal/harness/worker/... ./internal/core/agent/... ./internal/core/orchestration/...` | ok                                   |
| `make test-integration`（63 用例 + seed → restart → 全部 verify）                                         | exit 0                               |
| `make test-e2e`                                                                                           | 6 passed（1 skipped = fixture 门控） |
| `make test-deepseek-fixture`                                                                              | exit 0                               |

### 2026-08-29 第三轮审查修复记录（2×P1 + 1×P2）

- P1 策略绑定一致性：`EffectivePolicy` 与 usage 读路径统一走
  `effectivePolicy` 共享读——存储行的 `project_id` 必须与 active
  installation 的项目一致，漂移按 `ErrPolicyCorrupt` fail closed（净化后的
  Internal），不再被请求项目 ID 静默覆盖；allow 路径、usage 展示与审批
  重验都不能再使用错误绑定的策略。
- P1 审批分页：`ApprovalService.List` 以 effectiveLimit+1 探测（默认 50、
  上限 100），返回 `next` token——仅当确实还有下一页时才产生，满页不再
  虚报；transport 删除基于原始 `limit=0`/未钳制值的 `len(items)==limit`
  判断，统一消费 service 的分页结果。
- P2 测试 act 警告：deferred project switch 用例的手动 promise 决议包进
  `act(...)`，无 React 未包裹告警。
- 回归测试：`TestAppAgentPolicyBindingFailsClosedEverywhere` 的第三轮初版（默认/
  显式读取 + SQL 注入绑定漂移 → EffectivePolicy 与 usage 读路径双双
  `ErrPolicyCorrupt`；第四轮继续扩展）、`TestApprovalServiceListPagination`（默认页探测 51、
  满末页无 token、oversize 钳制后仍按 101 探测）。

#### 第三轮修复后验证（全部实测）

| 命令                                                                        | 结果                                 |
| --------------------------------------------------------------------------- | ------------------------------------ |
| `make check`（proto/go/web + status 渲染）                                  | exit 0                               |
| `go test -race ./internal/core/agent/... ./internal/core/orchestration/...` | ok                                   |
| `make test-integration`（含绑定漂移回归，64 用例）                          | exit 0                               |
| `make test-e2e`                                                             | 6 passed（1 skipped = fixture 门控） |
| `make test-deepseek-fixture`                                                | exit 0                               |

### 2026-08-29 第四轮审查修复记录（2×P1）

- P1 策略写路径绑定一致性：`SetPolicy` 在持有 policy-chain lock 并读取存量
  policy 后、插入幂等 request 前，比较存量 `project_id` 与已重验 active
  installation 的项目。格式合法但绑定漂移的行按 `ErrPolicyCorrupt` fail
  closed；相同 spec 的 no-op 与不同 spec 的替换都不能消费 key、返回错误绑定或
  静默覆盖损坏事实。
- P1 审批批准完整重验：应用层 approve 复用 `effectivePolicy`，同时比较项目绑定、
  revision 与完整 spec；决定事务先进入与 `SetPolicy` 相同的 per-installation
  policy-chain lock，再锁 approval/task，从 durable task 恢复 policy
  source/digest/budget snapshot，并在 reservation/outbox 前再次用
  `verifyApprovalPolicyChain` 比较当前 policy。损坏或漂移时 approval/task 保持
  pending/waiting，零 reservation、零 outbox。approval 重载时用 durable task 的
  source/digest 恢复并校验完整 policy mode，而不是猜测或新增同义快照。
- 回归测试：`TestApprovalServiceDecideFailsClosedOnDriftedWorld` 增加错误项目绑定和
  同 revision/spec 漂移；`TestAppAgentPolicyBindingFailsClosedEverywhere` 以真实
  PostgreSQL 注入合法 UUID 的错误绑定，覆盖 EffectivePolicy、usage、Set no-op、
  Set replacement、应用层 approve 与事务内 approve，并断言 key/row/state/quota/
  outbox 均无非预期变化；`TestAppAgentApprovalDecisionLocksPolicyChainBeforeRows`
  用 PostgreSQL advisory-lock waiter + `FOR UPDATE NOWAIT` 固定证明 approve 的锁序是
  chain → approval row，策略替换不能与旧快照批准交错。正常 require-approval
  纵向链路继续通过。

#### 第四轮修复后验证（全部实测）

| 命令                                                                                               | 结果                                 |
| -------------------------------------------------------------------------------------------------- | ------------------------------------ |
| `go test -race ./internal/core/agent/... ./internal/core/orchestration/... ./internal/harness/...` | ok                                   |
| `make test-integration`（66 用例 + seed → restart → 全部 verify）                                  | exit 0                               |
| `make test-e2e`                                                                                    | 6 passed（1 skipped = fixture 门控） |
| `make test-deepseek-fixture`（本地 keyless fixture，不使用真实凭据）                               | exit 0                               |
| `make generate`、`make check`、`buf breaking --against '.git#branch=main'`、`git diff --check`     | exit 0 / 无漂移                      |

### 2026-08-29 审查修复记录（4×P1 + 4×P2）

- P1 政策线性化：`SetPolicy` 与 `CreateForAppApproval` 两个事务现在都先取
  per-installation 的 transaction-scoped advisory lock（`LockAgentAppPolicyChain`，
  命名空间常量 + FNV-1a(owner, installation) 键），锁内在审批创建事务中用
  `verifyApprovalPolicyChain` 重验快照：system default 仅在无显式行时成立，
  显式行要求 revision、spec、project binding 全等。任何先提交的 SetPolicy
  要么被本次重验看见（revision 漂移 → `ErrPolicyStale`，整个事务回滚、run key
  不消费），要么其失效扫描必然能看到后提交的 pending approval。空缺行首次
  SetPolicy 的竞态同样被该锁覆盖。
- P1 运行前能力上界：proto `HarnessCapabilities` additive 增加
  `max_output_tokens = 14` / `max_runtime_seconds = 15`（buf breaking 通过）；
  Fake/DeepSeek adapter 如实上报各自强制上界（1,000,000/86,400 与
  384,000/600）；catalog domain、harnesshost source、catalog transport、
  orchestration adapter 全链路透传；`ProviderCapabilities.Supports(tokens,
seconds)` 在 `TaskRouter.adjudicateAppRun` 与 `ApprovalService.revalidate`
  中把超出上界的 policy/spec 判为 `ErrProviderCapabilityMissing`，在入队与
  占额度之前 fail closed。
- P1 worker deadline 弃绝：deadline 触发后 worker 给 adapter 一个有界宽限
  （默认 5s，`abandonAfter` 可注入）；宽限内未返回则放弃等待，补上恰一个
  `run_failed`（"provider run exceeded its runtime deadline"）terminal 事件并
  结束 lease，孤儿 run 的后续 append 由服务端按已结束 lease 拒绝。新增
  `TestWorkerAbandonsProviderThatIgnoresCancellation`：provider 完全不响应
  context（`<-neverClosed`），worker 仍在有界时间内终止。
- P1 Reject 不再重验：`ApprovalService.Decide` 只对 Approve 执行
  installation/grant/policy/provider 重验；Reject 直接进入 decide 事务，卸载、
  撤销 grant 或 Provider 不可用后审批仍可拒绝，不再滞留 pending。新增
  application 子测试「reject skips the world revalidation」与集成测试
  `TestAppAgentRejectSurvivesUninstalledWorld`（approve 判 FailedPrecondition
  且 approval 保持 pending，reject 成功且任务 cancelled，reject key 幂等重放）。
- P2 cost 累计：`UpsertAgentAppDailyUsage` 与 `UpsertAgentTaskUsage` 的
  cost 由 COALESCE 覆盖改为 `存量 + COALESCE(增量, 0)`，多任务/多事件成本
  正确累计；无 cost 观测仍保持 NULL（集成断言 unavailable-cost 语义不变）。
- P2 未知 state 过滤：`approvalStateFromProto` 对 UNSPECIFIED 之外未知枚举
  返回包装 `ErrInvalid` 的错误，transport 映射 InvalidArgument，不再静默
  变成全状态通配。
- P2 持久 policy 读取校验：`policyFromDB` 返回错误并校验 revision > 0、
  project binding 为合法 UUID、spec 语法、重算 spec digest 与存储值一致，
  任何漂移按 corruption 报错（`GetPolicy`、`SetPolicy` no-op 路径、
  `verifyApprovalPolicyChain` 共用）。
- P2 ApprovalsView 串台：`refresh` 改为按调用分配 generation（新 refresh
  使旧响应全部失效），`decide` 结束后通过 latest-project ref 刷新当前
  project 的列表；decision 发生后的 project 切换不再用旧 project 数据覆盖
  新列表，feedback/onDecided 仅在 project 未变时呈现。无视觉变化，无需新
  截图（existing before/after/current 记录仍准确）。
- 受影响测试同步：router/application 集成 fakes（`fakeProviders`、
  `completeCapabilities`、`staticFullCapabilities`、`outageProviders`）补
  上界字段；router 新增 over-bound policy 用例；worker 新增不配合取消用例；
  集成新增卸载后 reject 用例。

#### 审查修复后验证（全部实测）

| 命令                                                                                                                                  | 结果                                             |
| ------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| `make generate`                                                                                                                       | 后工作树无生成漂移                               |
| `make check`（proto/go/web + status 渲染）                                                                                            | exit 0                                           |
| `buf breaking --against '.git#branch=main'`                                                                                           | 通过（仅 additive）                              |
| `go test -race ./internal/core/agent/... ./internal/core/orchestration/... ./internal/core/harnesscatalog/... ./internal/harness/...` | ok                                               |
| `make test-integration`（60 用例 + seed → restart → task/app/install/surface/bridge/grants/policy verify）                            | exit 0                                           |
| `make test-e2e`                                                                                                                       | 6 passed（1 skipped = deepseek fixture 门控）    |
| `make test-deepseek-fixture`                                                                                                          | exit 0（Catalog/binding E2E 通过 local fixture） |
