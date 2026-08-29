# Task: App Agent 持久预算策略与运行前审批

- 状态：active
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
