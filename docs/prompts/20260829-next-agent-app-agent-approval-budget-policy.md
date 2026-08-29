# 下一位智能体 Prompt：App Agent 持久预算策略与运行前审批

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、UI 视觉证据、文档和聚焦提交，
> 不是只输出计划。

## 你的角色与最终结果

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。ProjectService 契约加固及三轮
审核修复已合入本地 `main`。你的任务是实现一个严格限定的 App Agent 安全纵向切片：

**为 Project 中已安装 App 的 Agent 调用建立持久、有限、可审计的预算策略；策略可以直接允许、
要求每次运行前由用户审批，或阻止新任务。审批、配额预留、任务入队、Usage 记账和重启恢复必须有
真实 PostgreSQL 与跨进程证据。**

最终链路必须闭合：

```text
manifest requested permission
  → installation current grant + grant revision（已有）
  → Agent-owned effective App policy（finite limits + execution mode）
  → fresh App run replay/policy/provider/quota adjudication
      ├── allow：task + provenance + quota reservation + outbox 原子提交
      ├── require approval：waiting task + approval request，尚不入队也不占额度
      └── block/quota exhausted：fail closed，不消费 run key
  → owner-only Agent Center approval decision
      ├── approve：重验授权并原子 reserve + queue + outbox
      └── reject：原子 terminal，不执行、不占额度
  → Harness worker 独立执行 runtime deadline，adapter 执行 token cap
  → UsageRecorded 与任务事件同事务形成审计投影
  → App watch、Agent Center、重启后的 policy/approval/quota/usage 一致
```

持续推进到实现、生成物、测试、UI 前后截图、ADR、任务记录、架构文档、状态事实源和聚焦提交全部
完成。不要 merge 或 push。只有遇到以下情况才停止并留下证据与选项：必须破坏已有 v1 字段/编号、
修改已执行 migration、改变六进程所有权、需要 Core 读取其他进程数据库、无法在不暴露凭据的前提下
实现，或要把 Provider/tool approval 与本任务的 WorkOS 运行前审批混为一谈。

## 为什么现在做这个

当前 App Agent 链路已经完成：

```text
requested permission
  → explicit installation grant
  → mutable grant revision + old Surface fail closed
  → Project-scoped App Bridge run/watch
  → durable task provenance/idempotency
  → provider snapshot + persisted event stream
```

但 grant 只回答“这个 App 能否调用 `agent.task.run` / `agent.event.watch`”，尚未回答：

- 每个任务最多允许多少输出 token、运行多久；
- 每个 App 每个 UTC 日最多提交多少任务、预留多少输出 token；
- 哪些 App 可以自动运行，哪些必须逐次由用户确认；
- 并发请求如何不超卖额度；
- pending approval、quota 和 usage 在 Core/Harness 重启后如何保持；
- Provider 不支持硬预算或 usage reporting 时如何明确 fail closed；
- Usage 超出已授权 reservation 时如何触发确定性切断，而不是依赖模型自觉。

`docs/structure.md` 明确要求 App 不接触 Provider Key、可配置调用权限与预算，并按 app/project/
harness/task/user/model 统计 AI 消耗；`docs/tasks/20260829-mutable-project-app-grants.md` 与
`docs/tasks/20260829-project-service-contract-hardening.md` 都把 approval/quota-budget policy 留作下一条
产品主线。因此本任务优先于新增 container/native runner，也不再继续扩张 Project CRUD。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时本地 `main` 为 `005f0e9`，工作树干净且与 `origin/main` 一致。执行时必须重新
  检查，以执行时本地 `main` 为基准，不能从旧远端提交重建或丢弃本地历史。
- `docs/status.json` 是进度事实源：Agent Task Router、Project App Installation、Runtime / Surface、
  Desktop Shell、Harness Broker 和 Fake/DeepSeek adapter 均有 working 证据；Access Gateway 的生产
  device auth、Reliability enforcement、container/native runner 仍未完成。
- App Bridge public body 当前只允许 `idempotency_key`、`role`、`goal`；owner/device/project/
  app instance/grant revision 从可信 Gateway identity、bridge token 与 Surface session 派生。App 不得
  提交 budget、policy revision、quota、approval decision 或 Provider ID。
- `AgentTaskInput.budget` 已有 `max_tokens`、`max_cost_decimal`、`max_runtime_seconds`，但 App 路径
  明确不接受这些字段，当前 App task payload 也未注入系统预算。
- `ApprovalRequired` 与 `UsageRecorded` 已是 canonical AgentEvent；当前只有事件持久化，没有
  owner-only approval decision authority、durable policy/quota ledger 或 UI 操作闭环。
- 当前 Harness Provider 协议有通用 `approvals` 与 `usage_reporting` capability；Fake/DeepSeek 均不
  声称 provider approvals。DeepSeek 可映射 token/runtime budget 和 usage；Generic CLI 不能被 Core
  按 Provider ID 特判为“支持预算”。
- WorkOS 运行前审批与 Provider 在执行途中产生的 tool approval 是两种不同事实。本任务只实现前者；
  后者需要未来扩展 Harness `approve` 协议和具体 adapter，不得伪装成已完成。
- migrations `001`–`013` 已执行并受 checksum/forward 测试保护，禁止修改。新数据变更从 `014`
  开始，owner 应为 `workos-core Agent`；不得把 policy/quota/approval 表塞进 Project 或 runtime 表。
- Desktop Agent Center 当前只有 task composer/timeline；App Library 已有 installation grant 管理。
  `docs/ui/README.md` 规定所有 UI 变化必须保存 before/after/current 视觉记录。

## 凭据与不可突破的边界

- **本任务不需要任何真实 DeepSeek、OpenAI、Codex、GitHub 或其他 Provider API Key。**
- 不得使用、保存、转述、验证或尝试恢复聊天中曾出现的真实 Key；不得扫描 shell history、环境变量、
  本机私有文件或聊天记录搜集凭据。
- Agent E2E 必须使用 Fake Harness；DeepSeek 回归只允许仓库 keyless/假凭据 fixture，禁止请求真实
  Provider 网络或产生费用。
- Policy 永远不能扩大 installation grant；`agent.task.run` 未 grant、grant revision stale、Project
  archived 或 installation inactive 时，policy 即使是 allow 也不能执行。
- App/iframe 不得获得 Provider credential、bridge token、Gateway identity header、通用 Connect
  client、Core/runtime 私有地址、policy/quota 内部写接口或 approval decision 能力。
- Provider 类型和价格只能存在对应 adapter/未来定价 adapter；Core 不得硬编码 DeepSeek/Codex
  价格、模型表或 Provider-specific request。
- 任务目标、事件正文、approval description、credential、token、SQL、DSN、constraint、provider raw
  error 不得进入日志或未净化错误。Owner UI 可以显示其自身受限任务内容，但测试截图只使用 fixture。
- 预算和 deadline 必须由确定性代码执行；不能依赖 Harness/模型“自觉遵守”。无法硬执行的能力必须
  报告 unsupported/unavailable，不能假装 working。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`CONTRIBUTING.md`、`docs/ui/README.md`；
   - `docs/structure.md` 中 1.3、5、6.3、7、9.6、10、11.5、17、18；
   - `docs/architecture/implementation.md`、`docs/status.json`；
   - `docs/decisions/0001-foundation-boundaries.md`、`0002-app-bridge-trust-boundary.md`、
     `0003-mutable-app-grants.md`、`0004-project-create-idempotency.md`；
   - `docs/tasks/20260828-minimal-project-agent-app-bridge.md`；
   - `docs/tasks/20260829-mutable-project-app-grants.md`；
   - `docs/tasks/20260829-review-hardening-mutable-project-app-grants.md`；
   - `docs/tasks/20260829-project-service-contract-hardening.md`；
   - `api/proto/workos/agent/v1/{agent,app_agent}.proto`；
   - `api/proto/workos/bridge/v1/bridge.proto`；
   - `api/proto/workos/harness/v1/harness.proto`；
   - `api/proto/workos/taskexecution/v1/execution.proto`；
   - `api/proto/workos/app/v1/installation.proto`；
   - `internal/core/agent` 的 domain/application/ports/postgres/transport 与全部测试；
   - `internal/core/orchestration/{app_agent,task_router}.go` 及测试；
   - Project installation resolver/grant revalidation 与 harness catalog；
   - `internal/harness/{broker,worker}`、Fake/DeepSeek/Generic CLI adapters；
   - runtime App Bridge、Surface coreclient、Gateway 路由/header 清洗；
   - `sdk/{protocol,agent-sdk,app-sdk,surface-sdk}`、`clients/{agent-center,app-host}`；
   - `apps/desktop-web` 的 Agent Center、App Library、permissions、window lifecycle 与 E2E；
   - migrations `009`–`013`、全部 checksum/forward/integration/restart 测试。

2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -15
   git branch -vv
   git diff --check
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败必须如实记录归属。禁止删 volume、TRUNCATE、broad DELETE、放宽断言、删除历史测试或固定
   成功响应绕过。

3. 从执行时本地 `main` 创建独立 branch/worktree，建议：

   ```text
   feat/app-agent-approval-budget-policy
   ```

   禁止直接在 `main` 实现，不要 merge 或 push。保留所有不属于本任务的改动；不得 reset/rebase/
   checkout 覆盖用户文件。

4. 从 `docs/tasks/TEMPLATE.md` 创建：

   ```text
   docs/tasks/20260829-app-agent-approval-budget-policy.md
   ```

   初始状态为 active，写清 policy owner、默认策略、approval/quota/usage 事实、Proto、migration、
   idempotency、并发、错误、UI、测试和非目标。

5. 新增聚焦 ADR，建议：

   ```text
   docs/decisions/0005-app-agent-approval-budget-policy.md
   ```

   ADR 必须固定本 Prompt 下述语义，特别是：为什么 policy 属于 Agent 而不是 Project grant；为什么
   quota 采用入队前 reservation；为什么 WorkOS pre-run approval 不等于 provider tool approval；
   为什么本任务不声称 vendor-neutral 金额硬上限。

6. 按 `docs/ui/README.md` 从当前基线建立：

   ```text
   docs/ui/desktop-web/changes/20260829-app-agent-approval-budget-policy/before/
   ```

   至少覆盖 App Library 已安装行/权限管理和 Agent Center 当前任务界面。使用 Chromium、1440×900、
   deviceScaleFactor 1、确定性 fixture，不得含真实内容或凭据。

## 必须固定的领域事实

不要把下列事实折叠成一个布尔值：

| 事实                               | owner                | 含义                                                       |
| ---------------------------------- | -------------------- | ---------------------------------------------------------- |
| manifest requested permission      | App Registry         | App 声明想要什么，绝不是授权                               |
| installation current grant + epoch | Project Installation | 用户允许哪些 bridge method；变化使旧 Surface 失效          |
| App Agent policy + revision/digest | Core Agent           | 新 App task 的执行模式和有限预算，不会扩大 grant           |
| approval request/decision          | Core Agent           | 某个 waiting task 是否可进入队列，只有 owner 可决定        |
| UTC daily reservation bucket       | Core Agent           | 并发入队前原子占用 task/output-token allowance             |
| actual usage projection            | Core Agent           | 从持久 UsageRecorded 事件累计的观测事实，不充当授权        |
| effective task budget snapshot     | Agent task           | 创建时的 server-derived budget，App 与后续 policy 不能改写 |

Policy 变化只影响新 task 和尚未决定的 approval，不隐式取消已经 queued/running/terminal 的 durable
task，也不复用 grant revision、不关闭 Surface、不修改 Project revision。Grant 变化仍按 ADR-0003
即时阻止旧 Surface 的新 run/watch；两条安全边界必须同时成立。

## 必须实现的策略契约

### 1. 有限、版本化的 per-installation policy

为 active App installation 提供 Agent-owned policy，至少表达：

```text
execution_mode = allow | require_approval | block
max_output_tokens_per_task
max_runtime_seconds_per_task
max_tasks_per_utc_day
max_reserved_output_tokens_per_utc_day
policy revision / canonical digest
```

要求：

- key 是 owner + app instance，并持久绑定 project snapshot；无跨模块 FK/SQL。
- public Set 是 full replacement，带 owner-scoped idempotency key 和 optimistic policy revision/etag；
  same key/same canonical request 精确重放第一次结果，different request 稳定 `Aborted`。
- 所有限额必须是正数、有限、有明确上界；零、负数、溢出、未知 enum、控制字符、非 canonical UUID
  一律 `InvalidArgument`。不能用零表示 unlimited。
- 没有 explicit row 时使用一个**版本化、有限、代码与 ADR 钉住的 system default**，默认应保持已由
  grant 明确授权的既有 App run 能继续工作，但绝不能是 unlimited。每个新 task 必须持久快照其
  effective policy source/digest/budget，不能日后按新 policy 重解释历史任务。
- Set 前通过中立 port/application orchestration 重验 owner、Project、active installation 和 pinned
  facts；policy 不得导入 Project adapter/internal package，也不得查询 Project 表。
- `block` 只阻止新的 run；watch 既有 App-owned task 仍由 provenance + current grant 决定。
- policy 真实变化递增自身 revision，并使该 App 所有 pending approval 原子失效/终止；相同 policy
  是确定性 no-op，但 idempotency key 仍消费并精确 replay。
- policy 更新不改 Project revision/grant revision，不产生 Project event。若需要事件/outbox，使用
  Agent-owned、稳定、非敏感的 policy event。

具体安全默认数值由实现者根据现有 DeepSeek/Fake 上限在 ADR 中选择并以测试固定；不得把 Provider
最大值直接当默认值，也不得引入配置缺失时的隐式无限。

### 2. Fresh App run 的原子裁决

同一个 `(owner, app_instance, client idempotency key)` 的既有 replay 必须先于当前 policy、quota、
Project binding 和 provider catalog 重解析：历史 key 返回第一次 task（包括 waiting/approved/rejected/
terminal 状态），不能因后来 policy 或 binding 改变生成第二个 task/approval/reservation。

fresh key 的顺序固定为：

```text
validate bounded role/goal/key
  → current installation/grant/epoch authorization（已有）
  → resolve effective finite policy
  → snapshot Project provider
  → verify provider explicitly supports required budget + usage contract
  → execution_mode / quota adjudication
```

- `allow`：task、App provenance mapping、policy/budget snapshot、daily reservation、task outbox 在一个
  Agent-owned PostgreSQL transaction 提交。
- `require_approval`：创建 `waiting` task、provenance mapping、pending approval 与
  `approval_required` task event；**不创建可 claim 的 task outbox，也不预留 daily quota**。
- `block`：固定 `PermissionDenied`/`FailedPrecondition`（在 ADR 选择并全链路一致），不创建 task、
  mapping、approval、event、outbox、reservation。
- quota exhausted：`ResourceExhausted`，失败不消费 App run key；UTC 日切换后同 key 可重试。
- App Bridge request、private runtime→Core request 和 MessageChannel envelope 不增加 budget/policy/
  approval/quota 字段。`AgentTaskInput.budget` 只能由 Core 根据 effective policy 注入。
- policy digest/revision、provider snapshot、budget、reservation 进入任务审计事实，但不要把 server policy
  混入 client request digest；client digest 仍只覆盖 bounded role/goal，重放语义保持稳定。
- 两个 Core 实例并发 fresh run 不得超卖 daily bucket，也不得产生 orphan task/mapping/outbox。

### 3. Owner-only 运行前审批

新增 public、Gateway identity 保护的 approval read/list/decide 契约。可按实际 Proto 拆为独立 service，
但必须满足：

- approval ID 是 UUIDv7 的普通资源 ID，不是 bearer token；App Bridge/iframe 只能从任务事件知道
  “等待用户”，永远不能决定。
- List/Get 只返回当前 owner 的记录，支持确定性分页；foreign/unknown 统一 `NotFound`，不泄露存在性。
- Decide 带独立 idempotency key，`approve`/`reject` 是唯一明确 decision；并发相反 decision 恰一个
  winner，loser 得到稳定 conflict/replay，不可二次执行。
- approve 前重新验证 active installation、current run grant、policy identity 和 provider capability；
  然后 approval decision、quota reservation、task `waiting → queued`、task event、task outbox 在一个
  Agent transaction 提交。任何失败保持 pending 且不占额度。
- reject 将 approval、task terminal 状态和明确 canonical event 原子提交；不创建 outbox、不占额度。
- policy 变化后旧 pending approval 必须失效且永不排队；grant/uninstall/Project archive 在 approve
  重验时 fail closed。已经在授权线性化点前通过并入队的任务可完成，这是已有并发边界，不做虚假
  “追溯取消”承诺。
- approval 文本是 untrusted App task 内容：有严格尺寸/UTF-8/控制字符边界，React 只按文本渲染；
  错误与日志不得复制 goal/description。
- 如果给 `AgentEvent` 增加 approval decision/expired 事件，只能 additive 使用新 oneof 字段号；不得
  重用或更改已有 10–21。

### 4. Durable quota reservation 与 usage

第一版硬配额采用确定性的 reservation，而不是事后猜测费用：

- bucket key 至少包含 owner、app instance、UTC date；policy revision 变化不能通过新 bucket 绕过
  当日已消费额度。
- 每个真正入队的 task 原子增加 `tasks_reserved += 1` 与
  `output_tokens_reserved += effective max_output_tokens_per_task`。
- reservation 一旦 task 入队，本 UTC 日内不退款，即使 provider failure/cancel；这是有意的
  fail-closed 语义，避免并发退款/重试重复消费。waiting/rejected task 不占额度。
- daily task/output reservation 任一将越界时整个入队事务失败；并发 N 个请求必须证明不会超卖。
- `UsageRecorded` 的 input/output token、model 和可选 cost 是实际观测；必须验证非负、有界、合法
  decimal/文本，并与 task event append 在同一事务更新 Agent-owned usage projection。
- actual usage 不替代 reservation，不允许缺失/迟到 usage 释放额度。若 provider 报告的 output usage
  超过 task reservation，必须记录可审计 breach，并触发确定性的 cancellation/circuit-break 行为，
  使该 bucket 后续 fresh run fail closed；不能只打日志继续无限运行。
- UI/API 分开显示 reserved allowance 与 reported actual usage，不把 unavailable cost 显示成 0。
- 本任务不实现货币硬上限。`max_cost_decimal` 保持空或只作已验证观测；在没有版本化价格源、currency、
  model pricing 与 reservation 算法前，禁止在 Core 硬编码价格并声称 cost enforcement。

### 5. Provider capability 与 worker enforcement

- Core 不能通过 `provider_id == "deepseek"` 判断预算支持。若当前 HarnessCapabilities 不能准确表达
  hard token budget/runtime deadline，需要 additive capability 字段并先改 Proto、`make generate`，
  再更新 catalog/domain/UI/adapter 测试。
- Fake 与 DeepSeek 只有在测试证明其实际执行 token cap/runtime/usage contract 后才声明支持；Generic
  CLI 或其他不支持者必须如实 false。不得把 WorkOS pre-run approval 映射成 provider
  `approvals=true`。
- fresh App task 在入队前验证 provider snapshot 的必要能力。capability 不足返回固定
  `FailedPrecondition`/`Unavailable`，不创建 task 或消费 key。
- harness worker 必须用 server-derived `max_runtime_seconds` 建立独立 context deadline；即使 adapter
  忽略 deadline，worker/broker 也会 cancel，最终只产生一个 terminal event 并正确结束 lease。
- token cap 由声明支持的 adapter 映射到 provider request；adapter 拒绝非法/超界 budget。Core 只认识
  canonical AgentBudget，不导入厂商 DTO。
- Provider health/capability 在 task 入队后变化可能导致执行失败，但不能改写 task snapshot或重新路由
  到另一 Provider；错误必须净化且 reservation 不退款。

## 协议与数据要求

- 优先在 `api/proto/workos/agent/v1` 新增聚焦 policy/approval/usage Proto，复用已有
  `AgentBudget`/`AgentTaskState`/`AgentEvent`；不要在 App Installation Proto 再造同义 policy DTO。
- 新 public service 只进 Core，经 Gateway allowlist 暴露；Gateway 继续清洗用户伪造 identity 与 bridge
  token。private AppAgentService 仍不在 public allowlist。
- 所有 public request 设置推导出的解压后 wire cap，gzip bomb/oversize 在业务层前
  `ResourceExhausted`；application 仍做完整字段语义验证。
- migration 使用下一个未占用编号（预期 `014`），owner 写明 `workos-core Agent`。可以包含 Agent-owned
  policy、idempotency、approval、quota/usage 表或必要 task/provenance additive 列；不能修改
  `001`–`013`，不能查询/外键引用 runtime schema，也不能跨模块读取 Project 表。
- 所有表/列有 CHECK、owner-scoped key、UTC 时间、UUIDv7、正数 revision 和必要唯一约束。migration
  必须支持 pristine、forward、no-op；既有 task/mapping 不可伪造 policy/usage 历史，缺失历史按 ADR
  明确标记 legacy/unknown，不得填成“已计费 0”。
- SQLC 生成文件只经 `make generate` 更新，禁止手改 `gen/`、`src/gen/`、sqlc 产物或 README 状态区块。
- policy/approval/quota mutation 与 task event/outbox 使用稳定非敏感事件名；consumer 仍按
  at-least-once、持久 cursor 设计。

## 错误与安全映射

至少固定以下 public 语义与短消息：

| 条件                                            | Connect code                                          |
| ----------------------------------------------- | ----------------------------------------------------- |
| 缺少可信 identity                               | `Unauthenticated`                                     |
| 非法字段/enum/UUID/limit/decision               | `InvalidArgument`                                     |
| unknown/foreign Project、installation、approval | `NotFound`                                            |
| grant 不允许或 policy=block                     | `PermissionDenied` 或 ADR 固定的 `FailedPrecondition` |
| stale policy/已决定 approval/同 key 不同请求    | `Aborted`                                             |
| provider 缺必要能力、旧 pending 已失效          | `FailedPrecondition`                                  |
| daily quota/circuit breaker exhausted           | `ResourceExhausted`                                   |
| PostgreSQL/catalog/必要依赖暂时不可用           | `Unavailable`                                         |
| stored invariant/usage/policy corruption        | `Internal`                                            |

所有判断使用 `errors.Is`/typed domain errors；PostgreSQL connection/resource/operator errors 走 Agent
共享 `ErrStoreUnavailable`。不得把 SQLSTATE、constraint、current quota、current policy、goal、model raw
error 或 Provider response 放进 public message。

## UI 与视觉证据

保持 `docs/structure.md` 的完整桌面 + 内部窗口模型，不增加永久侧栏：

- App Library 已安装行增加 `Agent policy` 状态与入口；编辑器显示 system default/explicit、execution
  mode、每任务 token/runtime、UTC daily task/token allowance，并明确 policy 不能代替 permissions。
- Agent Center 在现有窗口内增加 Approvals 与 Usage 视图/标签：pending 项可 Approve/Reject；显示 app、
  project、有限 task 摘要、policy snapshot、reserved/actual usage 与 UTC reset 语义。
- pending App task 在 App Surface/timeline 显示“等待用户审批”；approve 后同一 task 继续，reject 后得到
  terminal 状态；不得要求 App 重拿 bridge token 才看到决定。
- loading/empty/error/stale/conflict/quota exhausted/provider unsupported 状态有明确、净化、可访问文案；
  按钮防重复提交，迟到响应在 Project/窗口/installation 切换后 inert。
- UI 不显示 Provider credential、bridge token、内部 policy digest、数据库 ID 细节或 raw goal 到日志。

完成后采集至少以下稳定状态：

```text
app-library--agent-policy-default--1440x900.png
app-library--agent-policy-editor--1440x900.png
agent-center--approval-pending--1440x900.png
agent-center--approval-decided--1440x900.png
agent-center--usage-quota--1440x900.png
agent-center--quota-exhausted--1440x900.png
```

保存到任务 `after/`，用同名更新 `docs/ui/desktop-web/current/`，并在 `notes.md` 记录 fixture、路由、
viewport、浏览器、采集命令与有意差异。只用合成 fixture/Fake Harness。

## 必须补齐的测试

### Domain / application

- policy full replacement、system default、revision/no-op、canonical digest/idempotency；
- mode/limit 的零、负数、最大、超限、overflow、unknown enum；
- App 不能提交 budget/policy/decision 字段；policy 永远不能扩大 grant；
- replay 优先于当前 policy/provider/quota；same key different role/goal conflict；
- allow/require/block 三条路径的 repository 调用与零调用断言；
- UTC bucket 边界、quota exact full/one over、policy change不重置 bucket；
- pending policy change invalidation、approve/reject、相反并发 decision；
- usage validation/aggregation/breach/circuit behavior；
- fixed errors 不含 goal/policy/quota/raw cause。

### PostgreSQL / migration / concurrency

- 001→014 pristine、013→014 forward、重复 Run no-op、001–013 checksum 不变；
- 两个 pool/两个 repository 并发 policy Set、fresh App run、quota 最后一格、approval decision；
- allow task+mapping+reservation+outbox 原子性；require path 没有 claimable outbox/reservation；
- approve 的 decision+reservation+queue+outbox 原子性；reject 零 outbox/零 reservation；
- event/outbox/commit 注入失败全回滚且正确分类；refused pgx endpoint → `Unavailable`；
- restart 后 policy revision、pending/decided approval、daily bucket、task snapshot、usage 与 replay 保持；
- scratch database 精确 cleanup，不得污染验收 volume 或用 broad delete。

### Harness / execution

- provider capability truthful mapping；Fake/DeepSeek budget+usage，Generic CLI unsupported；
- worker runtime deadline 即使 adapter 不主动停止也会 cancel；取消、lease renew、deadline、terminal race
  只有一个 terminal；
- max token 映射、invalid budget fail closed、usage breach 触发停止/circuit；
- 不支持必要能力时任务不入队，不 fallback 到另一 provider。

### Transport / Gateway / Bridge

- policy/approval/usage RPC 的 identity、owner isolation、pagination、wire cap、gzip bomb、固定错误矩阵；
- approval service public，private AppAgentService 仍不可公开；bridge token 仍只进 AppBridge RPC；
- App Bridge request 无新增敏感/策略字段；waiting/approved/rejected response/event 映射；
- real PostgreSQL outage 从 repository 到 public Connect 为 retryable `Unavailable`。

### UI / E2E

- policy editor 的默认值、full replacement、validation、save conflict refresh、Project/installation 切换；
- Approvals pending/approve/reject、防双击、stale/quota error；Usage reserved 与 reported 分开；
- 真实跨进程 Fake Harness E2E：安装并 grant → require approval → App run waiting → Agent Center approve →
  同一 task 执行并 watch terminal → usage 出现；第二个 task reject 后永不执行；
- quota exact boundary/next request blocked，UTC 新 bucket 或可控 clock 后恢复；
- policy change 使旧 pending 永不排队，新 key 按新 policy；grant revoke/uninstall 仍优先 fail closed；
- Core/Harness restart 跨越 pending approval 和已消费 quota 后继续正确。

## 非目标

- Provider/tool-call 中途审批、Harness `approve()`、steer、resume、persistent session；
- vendor-neutral 金额硬上限、账单支付、真实 Provider 定价同步；
- App 自选模型/Provider、global scope、context refs、output types、parent/incident、任意 capabilities；
- 自动取消在 policy/grant 变化前已入队的 durable task；
- Credential Vault、生产 device enrollment/LAN pairing；
- Web Service/container/native runner、cgroup/Reliability、Indexer/RAG；
- 通用 AgentTaskService 的全面 CRUD/idempotency/pagination 重写，除非本切片的共享安全路径必须最小
  修复；若发现大量基础缺陷，另建明确任务，不扩大本任务伪装完成。

## 文档与状态同步

完成后同步：

- ADR-0005 与任务记录；
- `docs/architecture/implementation.md`：policy/approval/quota/usage owner、调用链、事务、错误、未实现
  provider approval/cost enforcement；
- 受影响模块 README；
- `docs/status.json`：只能按真实 E2E 证据更新，不能把 monetary budget/provider approvals 标为 working；
- `README.md` 状态区块只通过工具生成；
- UI before/after/current/notes。

任务记录必须列出执行过的命令、结果、资源计数、未决风险和下一步，不能用聊天记录代替仓库事实。

## 完成门禁

至少运行并记录：

```sh
make bootstrap
make generate
make generate                 # 第二次必须无生成漂移
make check
buf breaking --against '.git#branch=main'
go test -race ./internal/core/agent/... ./internal/core/orchestration/... ./internal/harness/...
make test-integration
make test-integration         # 再跑一轮观察并发与资源卫生
make test-deepseek-fixture     # 只用仓库 keyless/假凭据 fixture
make test-e2e
git diff --check
git status --short
```

并额外证明：

- 生成后工作树只有本任务预期文件，无手改生成物；
- migrations 001–013 逐字节不变；
- repository diff、日志、截图、fixture、环境文件中没有 credential/token/真实用户内容；
- 没有 ELF、Playwright report/trace/video、临时数据库或 root-owned 构建产物进入提交；
- branch 基于执行时本地 `main`，提交聚焦，不 merge、不 push。

## 最终交付格式

实现完成后向审核者报告：

1. branch 与 commit；
2. policy/approval/quota/usage 的事实 owner 和线性化点；
3. Proto/migration/event 变更；
4. allow/require/block、approval、quota、usage breach、replay、并发、restart 的证据；
5. Provider capability 与 worker deadline 的真实边界；
6. UI 视觉记录路径；
7. 全部验证命令与结果；
8. 未决风险，尤其 provider tool approval、金额预算、生产认证；
9. 明确声明未使用真实 Provider 密钥、未 merge、未 push。
