# 下一位智能体 Prompt：ProjectService 持久幂等、分页与公开边界加固

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、文档和单一聚焦提交，
> 不是只输出计划。

## 你的角色与最终结果

你是 WorkOS 的下一位实现智能体。仓库位于 /home/aquatao/workos。Mutable Project App Grants
及其审核修复已经合入本地 main。你的任务是完成一个严格限定的基础契约加固任务：

**让 public ProjectService 的 Create/Get/List/Update/Archive 在请求尺寸、输入边界、持久幂等、
分页、PostgreSQL 暂时不可用、事务与错误净化方面形成可验证的 production-grade 契约。**

这不是新增产品功能，也不是只把几个 fmt.Errorf 换成 sentinel。最终链路必须闭合：

~~~text
bounded public wire request
  → canonical application validation
  → owner-scoped PostgreSQL transaction
  → exact Create idempotency adjudication
  → project + event + outbox atomic commit
  → deterministic page result
  → sanitized Connect error
~~~

持续推进到实现、测试、架构文档、任务记录、状态事实源和单一聚焦提交全部完成。不要 merge 或
push。只有遇到以下情况才停止并留下证据与选项：必须破坏现有 v1 字段/编号、修改已执行
migration、改变六进程所有权、无法对历史 Create key 采用诚实且 fail-closed 的兼容策略，或发现
任务必须扩展到新的产品能力。

## 为什么现在先做这个

当前授权生命周期已可工作，下一条产品主线可以进入 App Agent approval / durable quota-budget
policy。但 Project 是 installation、Surface、Agent task 和后续预算策略共同依赖的聚合根；它的基础
公开服务仍保留早期 foundation 语义：

- 项目 CRUD 的 PostgreSQL 错误使用裸 fmt.Errorf，真实断连会被误映射为 Internal，而不是可重试的
  Unavailable；
- ProjectService handler 没有解码前请求体上限；
- CreateProject 只按 owner + idempotency key 去重，不比较请求；相同 key 的不同请求会静默返回旧
  Project；
- 重放 Create key 时返回的是项目当前可变状态，而不是第一次 Create 的精确响应；
- ListProjects 的 next token 在 transport 根据原始 page_size 猜测：默认 page size 时不会发 token，
  最后一页恰好满页时又会发出伪 token；
- 负 page size、cursor/Project ID 形状和多处复合字段缺少统一边界。

这些问题应在引入审批、预算和更多 Project mutation 之前收敛，否则后续功能会继承错误的幂等、
重试与分页基础。

## 当前仓库事实

- 六个进程边界固定：workos-gateway、workos-core、harness-host、runtime-host、
  reliability-host、indexer。
- 本 Prompt 编写时，本地 main HEAD 为 a4f9ecfade2d；执行时必须重新检查，以执行时本地 main 为
  基准，不得从落后的远端分支重建或丢弃本地提交。
- docs/status.json 是进度事实源；Project、App Installation、App Agent、Surface 和 Desktop 的已有
  working 证据不得降级或伪造。
- public ProjectService 已经由 Gateway allowlist 暴露；本任务不新增 RPC，也不改变 Gateway 身份
  信任模型。
- api/proto/workos/project/v1/project.proto 已经足以表达本任务。预期不需要 Proto 变更；若实现
  过程中证明必须变更，只允许 proto-first 的 additive v1 变更，先记录理由并执行 make generate。
- workos_core.projects 当前把 idempotency_key 放在可变 Project row 上，并有
  UNIQUE(owner_user_id, idempotency_key)。该设计不能证明同 key/same request，也不能保存第一次
  Create 的精确结果快照。
- migrations 001–012 已执行并受 checksum/forward tests 保护，禁止修改。任何 Core-owned 新
  migration 必须从 013 开始。
- installation repository 已经有 storeError 与真实 pgx 断连测试，可复用其判定原则；不要复制出
  第二套语义不同的实现。
- Project 基础 List 当前按 owner_user_id + id 升序 keyset 分页；cursor 是最后一个 Project ID。
- 本任务没有 UI 视觉变化，默认不需要新增截图。只有实际修改了 UI 行为或视觉时，才必须遵循
  docs/ui/README.md 记录 before/after/current/notes。

## 凭据与最高优先级安全边界

- **本任务不需要任何真实 DeepSeek、OpenAI、GitHub 或其他 Provider API Key。**
- 不得使用、保存、转述、验证或尝试恢复聊天中曾出现的真实 Key；不得扫描 shell history、环境
  变量、本机文件或聊天历史来搜集凭据。
- Agent 回归只使用 Fake Harness；DeepSeek fixture 只允许 keyless/假凭据模式，禁止真实 Provider
  网络请求。
- App 仍不得读取模型或外部服务真实凭据。HarnessBinding.credential_ref 只是 opaque reference，
  本任务不能把它演进为 raw credential。
- 日志、Connect 错误和测试失败输出不得包含 DSN、SQL、constraint 名、request body、workspace URI
  全文、credential reference、stack 或 provider raw error。
- 不得让 public request 提交 owner_user_id、device identity 或其他可信上下文；owner 继续只来自
  Gateway 建立并由 Core 验证的 identity context。

## 开始前必须完成

1. 完整阅读：

   - AGENTS.md、README.md、CONTRIBUTING.md；
   - docs/structure.md 中 Project、App、Harness capability、Credential Vault、事件/outbox、数据
     存储和第一版产品边界；
   - docs/architecture/implementation.md、docs/status.json；
   - docs/decisions/0001-foundation-boundaries.md；
   - docs/decisions/0002-app-bridge-trust-boundary.md；
   - docs/decisions/0003-mutable-app-grants.md；
   - docs/tasks/20260823-foundation.md；
   - docs/tasks/20260823-harness-catalog-binding-ux.md；
   - docs/tasks/20260825-project-app-installation.md；
   - docs/tasks/20260829-mutable-project-app-grants.md；
   - docs/tasks/20260829-review-hardening-mutable-project-app-grants.md，尤其“未决风险与下一步”；
   - api/proto/workos/project/v1/project.proto 与 common pagination Proto；
   - internal/core/project 的 domain、application、ports、postgres、transport 及全部测试；
   - cmd/workos-core/main.go 中 ProjectService composition root；
   - internal/core/project/adapters/postgres/installation.go 中 storeError 的现有实现和测试；
   - internal/platform/migrations/files/001_foundation.sql 与 002–012；
   - migration checksum、forward、restart、integration 和 Gateway boundary tests。

2. 运行并记录：

   ~~~sh
   git status --short --branch
   git log --oneline --decorate -12
   git branch -vv
   git diff --check
   ~~~

   保留不属于本任务的改动；不得 reset、rebase、checkout 覆盖用户文件，也不得删除或重建用户的
   持久验收数据库。

3. 从执行时的本地 main 创建独立 branch/worktree，建议：

   ~~~text
   fix/project-service-contract-hardening
   ~~~

   禁止直接在 main 实现，不要 merge 或 push。

4. 从 docs/tasks/TEMPLATE.md 创建：

   ~~~text
   docs/tasks/20260829-project-service-contract-hardening.md
   ~~~

   初始状态设为 active，写清 public contract、表 owner、migration、幂等 digest/结果快照、历史
   key 兼容策略、分页、错误矩阵、请求上限、并发测试、非目标和验收。

5. 为 CreateProject 持久幂等建立聚焦 ADR，建议：

   ~~~text
   docs/decisions/0004-project-create-idempotency.md
   ~~~

   ADR 至少决定：

   - 为什么可变 projects row 不能作为 Create 第一次响应的幂等事实源；
   - canonical request digest 覆盖哪些规范化字段；
   - 第一次响应如何以版本化、可验证的内部结果快照持久化；
   - mapping、Project、project.created event 和 outbox 的单事务线性化点；
   - same key/same digest、same key/different digest 和失败请求各自语义；
   - 两个进程并发 Create 时如何只产生一个 winner，不依赖进程内 mutex；
   - migration 001 中既有 key 缺少历史原始请求时采用什么诚实、fail-closed 的兼容策略；
   - 为什么不能根据后来已 Update/Archive 的 Project row 伪造“第一次 Create 精确响应”。

6. 运行并记录基线：

   ~~~sh
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ~~~

   基线失败必须记录证据与归属。不得通过删 volume、TRUNCATE、broad DELETE、跳过测试、降低断言、
   固定成功响应或删除历史测试绕过。

## 必须固定的公开契约

### 1. CreateProject 的持久精确幂等

Create 请求的 canonical digest 必须覆盖所有影响首次 Project 内容的客户端输入，并明确版本：

~~~text
command/version marker
normalized name
icon
ordered workspace refs（含每个公开字段）
optional harness binding（含 presence 与每个公开 reference/policy 字段）
~~~

不要把 owner、idempotency key、服务端生成 ID、时间、revision 或当前数据库状态混入 request digest。
owner + key 是命名空间，不是请求内容。必须先按 application 语义完成规范化/验证，再以无歧义的
canonical encoding 计算 digest；不要拼接容易碰撞的裸字符串。

语义固定为：

- idempotency key 为 owner-scoped，必须 valid UTF-8、1–128 Unicode code points、无 C0/C1 控制字符；
- same owner + same key + same digest：跨请求、跨进程、跨重启精确返回第一次成功
  CreateProjectResponse 的 Project 快照；
- 即使该 Project 后来 Update 或 Archive，Create replay 仍返回第一次 response 的 revision、字段和
  timestamps，不得返回当前可变 row；
- same owner + same key + different digest：稳定 Aborted，不能泄漏原请求或结果；
- 不同 owner 的相同 key 互不冲突，也不能访问对方结果；
- validation、DB failure、event/outbox failure 或 commit failure不得消费 key；
- winner 的 Project、idempotency record/result snapshot、project.created.v1 event 和 outbox 必须在
  同一个 Core-owned PostgreSQL transaction 提交；
- 高并发下只有一个逻辑 winner、一个 Project、一个 created event、一个 outbox entry；loser 根据
  digest 精确 replay 或 Aborted；
- 不得用 in-memory map/mutex、固定 sleep、查询后再无约束 insert 或应用层“最佳努力”补写来冒充
  durable idempotency；
- 结果快照是内部持久模型，不得另造一套与 Proto 竞争的 public DTO，也不得存 raw secret。

新增 Core-owned forward migration 013。不要编辑 migration 001–012。建议引入独立
project_create_requests authority，或等价且能证明上述性质的设计；具体列设计由实现者在 ADR 中
论证。必须保留 projects 上现有数据与约束的兼容性，不能通过重建历史表或 broad data rewrite
解决。

历史 owner + key 在 migration 前只保留当前 Project row，原始 canonical request 与首次响应可能已
不可恢复。迁移不得虚构 digest 或首次快照。必须选择、记录并测试一个诚实的兼容策略，例如把 legacy
记录标记为不可精确裁决并对其重放 fail closed；如果选择其他策略，必须证明不会把 different request
误判为 replay，也不会把后来状态冒充首次结果。

现有 foundation integration 中“同 key、不同 name 仍成功并返回旧 Project”的断言必须改为 Aborted。
补齐同 key/same request、different request、Update 后 replay、Archive 后 replay、重启后 replay 和
真实并发矩阵。

### 2. 输入验证与 wire 上限

为基础 ProjectService 增加专用 Connect handler constructor，并在 cmd/workos-core/main.go 组合根
启用 connect.WithReadMaxBytes。上限必须由所有合法字段的最大值推导并写注释，不得直接复制
installation 的 288 KiB。限制针对解压后的 body；测试必须证明：

- 合法最大或接近最大请求可以进入业务层；
- oversized identity/proto/json request 在业务调用前返回 ResourceExhausted；
- gzip/decompression bomb 在业务调用前返回 ResourceExhausted；
- oversize 请求的 fake service/repository 调用计数为零；
- 不改变同 mux 中其他 handler 的独立上限。

application/domain 必须拥有可复用的语义验证，不得只依赖 transport 或 PostgreSQL constraint。
至少固定并测试：

- name 保持现有 trim 后 1–120 code points 规则；
- icon、workspace refs 数量及每个 ref 的 id/uri/logical mount、HarnessBinding 的 provider/profile/
  credential/resource-policy references 都有明确、合理且文档化的最大值；
- 所有文本 valid UTF-8，标识/引用字段拒绝 C0/C1 控制字符；
- WorkspaceKind 不能是 UNSPECIFIED 或未知值；nil repeated item 不能 panic；
- workspace ref ID 不重复；logical mount 等需要唯一的字段不得产生歧义；
- Create/Get/Update/Archive 的 Project ID 与 List cursor 使用同一中立 Project UUIDv7 validator；
  大小写/canonical form 选择必须明确并测试，不能依赖 installation 专属 helper 的偶然行为；
- page_size < 0 为 InvalidArgument；0 表示默认 50；大于 100 规范化为 100；
- expected_revision 必须为正；Update 中 clear_harness_binding 与同时提供 harness_binding 是冲突输入，
  必须 InvalidArgument，不能静默选择一方；
- replace_workspace_refs=false 时，不得让客户端以非空 workspace_refs 制造被静默忽略的歧义；
- raw credential 永远不是合法 HarnessBinding 输入；credential_ref 仍只是有界 opaque reference。

不要为了实现本任务改变已有 Proto field presence 语义。若发现现有 v1 无法区分必须区分的输入，应
停下记录证据，不要擅自做 breaking change。

### 3. 分页由 application 明确裁决

transport 不得再根据原始 request page_size 和返回长度猜 next token。application 应返回明确 page
result，例如 items + next token，并拥有默认值、clamp 与 lookahead 语义。

要求：

- repository 以 effective limit + 1 查询；application 只返回 effective limit 个 item；
- 只有确实存在下一项时才返回最后一个已返回 Project ID 作为 next token；
- 默认 page_size=50 时能正常分页；
- 最后一页恰好等于 page size 时 next token 为空；
- 空页、单页、多页、超过 100、include_archived true/false 都有测试；
- owner 与 archived filter 在每一页保持一致；
- 非法、非 UUIDv7 或不存在的 cursor 的语义明确且稳定；不得跨 owner 泄漏存在性；
- 多页遍历不得重复或漏项；并发插入语义按现有 UUIDv7/id keyset 边界记录，不做 snapshot isolation
  的虚假承诺；
- integration fixture 必须精确记录自己创建的 ID 并精确清理，不得设置任意 20/1000 页搜索上限。

### 4. PostgreSQL 错误分类与安全映射

把 Project 基础 repository 的所有实际数据库 I/O 失败统一走共享 storeError 逻辑，包括：

- pool.Begin、query/exec/scan；
- Create/Update/Archive transaction 内的 project mutation；
- project event insert；
- outbox insert；
- transaction Commit；
- idempotency authority 的 lookup/claim/result write。

不要把 JSON encode/decode、UUID generation、domain invariant 或程序错误标成
ErrStoreUnavailable。应将 installation.go 中现有 storeError 提取到同 package 的中立共享位置，
由 installation 与基础 Project repository 共用，避免两套分类漂移。

transport 的固定映射为：

| 条件 | Connect code | 对外消息 |
| --- | --- | --- |
| 未认证 identity | Unauthenticated | 固定短消息 |
| malformed/越界/矛盾输入 | InvalidArgument | 固定短消息 |
| missing 或 foreign-owned Project | NotFound | 固定短消息 |
| stale revision / idempotency digest conflict | Aborted | 固定短消息 |
| PostgreSQL 短暂不可用 | Unavailable | 固定短消息 |
| 解码后请求超限 | ResourceExhausted | Connect 固定消息 |
| invariant、损坏持久数据、未知错误 | Internal | project operation failed |

不得把 raw domain error、pgx error、SQLSTATE 文本、DSN、constraint 或输入值直接放进 Connect message。
错误判断必须使用 errors.Is，保留内部 cause 供测试分类，但日志仍需净化。

真实故障测试必须至少使用 refused local PostgreSQL endpoint 产生真实 pgx/pgconn 连接错误，证明从
repository 到 Connect 的 Unavailable 路径；不能只注入 ports.ErrStoreUnavailable fake sentinel。
同时保留单元矩阵，证明 JSON/invariant/unknown error 仍是 Internal。

### 5. 事务、事件与聚合 revision

- Create 成功仍从 revision 1 开始，生成 UUIDv7、UTC timestamps；
- Update/Archive 的 optimistic concurrency 仍由 owner + project + expected revision 裁决；
- foreign owner 的 Project 不得因 revision 或 cursor 行为泄漏存在性；
- Update/Archive 的 Project mutation、event 与 outbox 继续在同一事务；
- event stream sequence 与 Project revision 一致；
- replay 不产生第二个 event/outbox，不改变 updated_at；
- same-key/different-digest、stale revision 和 DB failure 不得留下局部 Project/mapping/event/outbox；
- event/outbox transient failure、commit failure和事务 rollback 都要有可重复测试；
- 不建立跨进程 schema 查询、跨 owner FK、共享可变 entity 或对其他模块 adapter 的依赖。

## 分层要求

- domain：纯规则、value validation 与 domain errors；不得导入 pgx、Connect、HTTP、filesystem、
  Provider SDK 或其他模块 adapter。
- application：输入规范化、effective page size、lookahead page result 与用例编排；只依赖 ports。
- ports：表达 repository 所需的原子 Create/idempotency 与分页契约；不能暴露 pgx type。
- postgres adapter：migration、SQL、事务线性化、digest/result persistence、error classification。
- transport：identity extraction、Proto mapping、wire cap 和稳定 Connect code/message；不拥有业务幂等
  或分页猜测。
- Gateway：只保持现有 allowlist 与可信 header 清洗；不得新增旁路身份来源。

如果现有 ports.Repository 无法表达原子幂等，允许重塑该 port 及其 fake，但必须保持依赖方向
domain → application → ports ← adapters。

## 必须补齐的测试

### Domain / application

- 所有文本边界：空、最大、超限、无效 UTF-8、C0/C1、Unicode code point 而非 byte；
- idempotency key 边界与 canonical digest stability；
- workspace/harness validation、unknown enum、nil item、重复 ref、矛盾 update flags；
- page size -1/0/1/50/100/101，limit+1 和 exact-full-last-page；
- application error passthrough 与 repository 零调用断言。

### Transport

- 每个 RPC 的 Unauthenticated、InvalidArgument、NotFound、Aborted、Unavailable、Internal；
- 对外错误消息固定且无 raw cause；
- real HTTP Connect 请求覆盖默认分页与准确 next token；
- oversized JSON/proto 与 gzip bomb 在 decode 前 ResourceExhausted，业务零调用；
- spoofed owner/device headers 不能越权，foreign Project 仍 NotFound。

### PostgreSQL / migration

- pristine database 从 001 顺序升级到 013；
- 已含 001–012 的数据库 forward upgrade 到 013；
- migrate 第二次 no-op；
- 001–012 checksum 不变；
- 新 idempotency authority 的 owner/key unique、digest conflict、result snapshot 与 transaction atomicity；
- legacy key 兼容策略有显式 fixture，不得靠测试库恰好无历史数据；
- real PostgreSQL 多连接并发 Create，同请求与不同请求各有明确结果；
- refused endpoint 真实断连覆盖 Begin/query/event/outbox/commit 可达路径；
- replay after Update、Archive 和进程重启仍返回首次 Create 快照。

### 回归 / E2E

- foundation Project CRUD E2E 更新为正确的 idempotency 与 pagination 断言；
- Project App Installation、Harness binding、Surface 和 App Agent 链路继续通过；
- 不要求调用真实 DeepSeek；只运行仓库既有 keyless fixture；
- 没有 UI 改动时不要制造无意义截图；若产生任何可见变化，必须按 docs/ui/README.md 补
  before/after/current/notes 和确定性浏览器证据。

禁止用 fake 成功、mock-only integration、跳过真实 PostgreSQL 并发、sleep 猜竞态或宽松断言代替以上
证据。

## 明确非目标

- 不实现 App Agent approval、durable quota、token/cost budget 或计费；
- 不新增 Project UI、Desktop 分页 UI 或设计改版；
- 不实现 container/native workload；
- 不新增 Provider、真实 DeepSeek 网络测试或 credential vault；
- 不修改 installation grant、Surface session 或 App Bridge 协议；
- 不新增 ProjectService RPC，预期不改 Proto；
- 不更换全仓 pagination token 格式，只收敛基础 ProjectService；
- 不清理用户数据库、不回写不可恢复的历史请求内容；
- 不把本任务扩大为通用全仓 validation framework；
- 不手改 gen/、src/gen/ 或 README 状态区块。

## 文档与状态同步

完成时必须同步：

- docs/tasks/20260829-project-service-contract-hardening.md：状态、范围、决策、实际 migration、验证命令、
  结果、残余风险与下一步；
- docs/decisions/0004-project-create-idempotency.md；
- docs/architecture/implementation.md：ProjectService 的幂等 authority、分页与 error boundary；
- docs/status.json：只写有测试证据支持的状态/证据，不夸大为新的产品能力；
- 受影响模块文档。

README 的状态区块只能由工具生成。若没有 UI 变化，任务记录明确写“无 UI 变化，因此不需要视觉
记录”；若有 UI 变化，则必须按既有约定落 docs/ui 证据。

## 必须执行的验收命令

按仓库实际 Make target 调整，但不得降低覆盖：

~~~sh
make bootstrap
make generate
git status --short
make generate
git status --short
make check
make test-integration
make test-integration
make test-e2e
make test-deepseek-fixture
go test -race ./internal/core/project/...
git diff --check
~~~

还必须显式运行并在任务记录中列出：

- Project application/domain/transport 定向测试；
- Project PostgreSQL repository 定向测试；
- migration pristine/forward/checksum/no-op 测试；
- real PostgreSQL concurrency/idempotency 测试；
- refused endpoint 的真实 pgx error-to-Unavailable 测试；
- restart 后 Create replay 与首次 response snapshot 测试；
- 多页 ListProjects 无重复/漏项与准确 token 测试。

如果宿主机没有 buf，使用仓库既有 Docker/Make 流程完成 lint 与 breaking check，不能省略。
make generate 后必须再次执行并证明工作树无生成差异。任何失败先定位根因；不得删除 volume、
清理用户数据、放宽断言、跳过测试或声称“与本任务无关”而不留下可复核证据。

## 提交与交接

1. 只提交本任务文件，不混入其他智能体或用户改动。
2. 提交前检查：

   ~~~sh
   git status --short
   git diff --check
   git diff --stat
   git diff --name-only
   ~~~

3. 创建一个聚焦 commit；建议消息：

   ~~~text
   fix: harden project service contracts
   ~~~

4. 不要 merge，不要 push，不要改 main。最终交接必须给出：

   - branch 与 commit SHA；
   - 实际修改文件；
   - Create idempotency schema、digest、snapshot、legacy 和并发语义；
   - page result 与 wire cap 的推导；
   - 固定错误映射表；
   - 全部验证命令和明确 PASS/FAIL；
   - 是否有 UI 变化及相应视觉记录位置；
   - 未决风险和下一步。

## 完成判定

只有同时满足以下条件才能标记 done：

- public ProjectService 所有请求有解码前上限和 application 输入边界；
- CreateProject same key/same request 跨重启精确重放第一次 response；
- same key/different request 稳定 Aborted，失败不消费 key；
- 真实多进程/多连接并发下只有一个 Project/event/outbox；
- Update/Archive 后 Create replay 不返回后来可变状态；
- pagination 在默认、clamp、最后恰好满页和多页场景均给出准确 token；
- 真实 PostgreSQL 暂时不可用映射为净化后的 Unavailable；
- invariant/未知错误仍为净化后的 Internal；
- migrations 001–012 未改，013 pristine/forward/no-op/checksum 全通过；
- generate 二次无差异、make check、integration（连续两次）、E2E、race 与 keyless fixture 全通过；
- 任务记录、ADR、implementation 和 status 与证据一致；
- 单一聚焦 commit 已创建，未 merge、未 push。

若任一端到端证据缺失，相应状态最高只能写 scaffolded；不得以 TODO、固定成功响应、空 adapter 或
文档声明冒充 working。
