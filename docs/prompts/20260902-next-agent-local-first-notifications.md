# 下一位智能体 Prompt：Local-first Notification——持久通知、实时补收、跨设备已读与授权 App 闭环

> 将本文件完整交给下一位实现智能体。用户将离线休息，希望你持续自主工作至少七小时（预计十至十四
> 小时）并直接完成实现，不是只输出计划、审查报告或下一份 Prompt。整个批次只有一个最终目标、一个
> branch、一个 worktree、一个任务记录和一个写入智能体；所有阶段严格串行，禁止为并行、审核或修复再
> 创建分支、worktree，禁止让其他 Agent 修改仓库。

## 你的角色与唯一最终目标

你是 WorkOS 第一条本地优先通知链路的实现与收口智能体。仓库位于 `/home/aquatao/workos`；
`docs/structure.md` 是产品架构主线，`docs/architecture/implementation.md` 是当前代码边界，
`docs/status.json` 是唯一进度事实源。

当前 Project、Agent Task、运行前审批、Artifact Review、Knowledge Search、LAN pairing、Adaptive Shell、
App Bridge 和 Reliability Incident read model 已有真实链路，但系统没有 Notification Proto、持久通知事实、
未读同步或通知中心。用户只有打开各窗口并主动刷新，才知道任务完成、需要审批、Artifact 已生成或
Incident 已发生。`docs/structure.md` 13.4 已明确第一版现实边界：前台实时接收，离线/断线后同步未读；
后台 APNs/FCM 即时投递不作虚假保证。

本批次的唯一最终目标是：

```text
Agent approval / terminal task / review Artifact / Reliability Incident
  或获得 notifications.create grant 的当前 Project Web Bundle App
  → source mutation 与 durable publication/notification 原子提交
  → Core-owned、owner-scoped Notification + monotonic change stream
  → Gateway 只在有效 device session 下公开 list/get/read/watch
  → Desktop/PWA 前台实时收到，断线、Gateway/Core 重启后从 cursor 精确补收
  → 两个已配对设备看到一致的未读计数和单调 read state
  → Notification Center 在 compact / medium / expanded 布局可达
  → typed action 只能打开其权威 Project/Approval/Task/Artifact/Incident/App 目标
  → App grant revoke、uninstall、version/grant epoch 变化、session 关闭、foreign scope、
    replay、配额耗尽、publication response loss、stream gap 与依赖中断全部 fail closed
```

这是一个 local-first working slice，不是 Push 营销壳。成功结束时必须有真实 PostgreSQL、Core、Gateway、
harness-host、runtime-host、reliability-host、真实 Chromium、两个独立浏览器 context、opaque-origin Web
Bundle App、断线/重启与确定性视觉证据。只有这些证据齐全，才能在 `docs/status.json` 新增或升级
Notification working slice，并更新相关 Desktop/Runtime/Gateway evidence。

本批次明确不宣称 APNs、FCM、后台 Service Worker push、远程 relay、公网访问或 native wrapper 已完成；
不得把轮询叫 WebSocket，不得让浏览器、App 或 Reliability 直接写 Notification 表，不得让通知正文进入
日志、URL、metrics label、trace attribute 或未经定义的持久缓存；客户端只保留可丢弃、可从 Core 重建的
内存 projection。

## 单分支纪律（不可偏离）

执行时从真实本地 `main` 创建且只创建：

```text
feat/v1-local-first-notifications
```

并且只建立一个实现任务记录：

```text
docs/tasks/20260902-v1-local-first-notifications.md
```

强制规则：

1. 只允许上述一个 feature branch、当前一个 worktree、当前一个写入 Agent。不得创建 review、fix、
   candidate、backup 等辅助分支，不得添加 worktree，不得让 sub-agent 或第二个 Agent 写文件。
2. 不 stash 后切分支，不 reset/rebase/squash 已有历史，不修改或删除本地 `main`，不覆盖用户未提交改动。
3. 若目标 branch 已存在，先只读核对 merge-base、任务记录与工作树；确认就是本任务后继续，不得删除
   重建。无法安全确认时留下证据并停止破坏性操作。
4. 所有阶段在同一 branch 严格串行；每个可验证阶段完成后做聚焦提交，再继续下一阶段。
5. 禁止把整夜工作压成一个巨型提交。建议提交序列：

   ```text
   docs: define local-first notification boundary
   feat: add durable notification service
   feat: publish agent and artifact notifications atomically
   feat: bridge reliability incident notifications durably
   feat: add resumable paired-device notification stream
   feat: add adaptive notification center
   feat: allow quota-bound app notifications
   test: prove notification replay and restart convergence
   docs: record local-first notification evidence
   ```

6. 每次提交前运行 `git diff --check` 并审查 staged diff。不得提交 secret、私钥、真实用户内容、数据库、
   容器归档、构建二进制、trace/video、Playwright 临时目录、浏览器 profile、宿主绝对路径快照或测试
   报告 dump。
7. 未经用户新授权，不 merge 到 `main`、不 push、不删除其他分支。最终停在唯一 feature branch 的干净
   HEAD，供用户醒来后审查。

## 无人值守与工作量安排

用户离线期间不要等待普通澄清。优先从架构、Proto、现有实现和测试推导最保守的正确方案，然后持续
推进。下列时间按有经验的实现智能体估算为十至十四小时，只用于确保任务量和依赖顺序充足，不是到点
停工条件：

| 阶段                                            |    建议投入 | 结果                                                  |
| ----------------------------------------------- | ----------: | ----------------------------------------------------- |
| 基线、ADR、通知语义与失败矩阵                   |  40–60 分钟 | 任务记录、ADR、契约与 owner 裁决                      |
| A. Proto + Core notification domain/storage     | 90–120 分钟 | durable facts、change log、read/idempotency/paging    |
| B. Agent/Artifact 原子 producers                |  60–90 分钟 | source transaction 内唯一通知                         |
| C. Reliability durable publication              | 75–105 分钟 | private claim/complete、receipt、response-loss replay |
| D. Gateway stream + paired-device client        | 75–105 分钟 | session gate、resume/gap、双设备 read sync            |
| E. Adaptive Notification Center + typed actions |  75–90 分钟 | compact/medium/expanded UI 与视觉证据                 |
| F. App Bridge `notifications.create`            | 90–120 分钟 | grant/epoch/配额/SDK/opaque App/revoke                |
| G. restart/fault/E2E/文档收口                   | 90–150 分钟 | 三专项门禁、全量回归、状态与交接                      |

执行规则：

- 不要只写计划；完成基线和 ADR 裁决后立即实现。
- 一个测试慢、镜像构建慢、偶发下载失败或某个独立门禁需修复都不是停止理由。网络瞬时失败可有界重试；
  Buf 中途失败导致生成目录变化时，先恢复到可再生状态，禁止手改 `gen/` / `src/gen/`。
- 每 60 秒以内给用户一条简短进度更新，但持续推进，不因更新打断测试。
- 不访问真实 DeepSeek/OpenAI/Codex、APNs/FCM 或收费服务；Agent 链路使用现有 Fake/local fixture。
- 不搜索 shell history、用户 home、环境变量或 credential store 获取 key。
- 不安装宿主软件、不用 `sudo`、不改内核/systemd/防火墙。本任务不依赖 Podman；既有
  `make test-podman-fixture` blocker 不得拖住本批次，也不得被伪造为 PASS。
- 独立门禁遇到环境波动时记录并继续其他阶段，收尾时复试。只有本文“停止条件”允许等待用户。

## 本 Prompt 编写时的仓库事实（执行时必须重新核对）

- 编写前本地 `main` 为 `a877fac`，与 `origin/main` 一致。执行时以真实本地 `main` 为准，不 reset 到
  本文哈希或更旧远端。
- 六个进程固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。Notification Service 物理归入 `workos-core`，不得增加第七个 daemon。
- migrations `001`–`028` 已存在且可能已在持久卷执行，禁止修改。执行时重新确认下一个空闲编号；Core
  Notification、Reliability publication 如需不同表，必须使用各自 forward-only migration 并写清 owner。
- 仓库目前没有 `workos.notification.v1` Proto 或 `internal/core/notification` 模块，也没有 Notification
  Center。不要把新 DTO 塞进 Agent、Incident 或 Common Proto 规避模块边界。
- Core 已拥有 Project、Agent task/event/outbox、Artifact 与 Credential；Artifact materialization 已有
  tx-scoped orchestration 端口，Project/Agent 写入也已有 PostgreSQL transaction 与 durable idempotency
  模式可复用。
- Reliability 已拥有 Incident、action ledger、public IncidentService 与私有 Runtime observation/control
  链路。真实 supervisor capability 仍因 rootless Podman acceptance evidence 缺失而 unavailable；通知
  测试不得改变这个事实。
- Gateway 已有 device pairing/session proof、精确 public service allowlist、动态 owner/device 注入和
  server-streaming Agent event 路由。生产 auth stream 的 revocation/最大生命周期语义需按实际代码审计，
  不能假设 unary middleware 自动覆盖长流。
- Desktop 已有 Adaptive Shell、Agent Center/Approvals、Artifact Center/Viewer、System Monitor、Knowledge
  Center、App Library 与 Device Center；compact/medium/expanded/fold-separated 都有既有行为和截图基线。
- App Registry 的 capability vocabulary 当前包含 `agent.task.run`、`agent.event.watch`、`artifact.read`、
  `artifact.write`、`knowledge.read`、`project.read`，尚无 `notifications.create`。
- Runtime App Bridge 已实现 Agent run/watch 与 `knowledge.search`；每次 knowledge 调用都重验 surface
  session、installation、Project 与 exact current grant revision。新增 App notification 必须复用同一
  信任模式，不能只在前端 capability 列表加字符串。
- Indexer 的 lexical knowledge working slice 已完成；本任务不得修改 ranking、rebuild generation 或
  semantic RAG 状态，也不得把 Indexer 当通知队列。
- README 状态区块由 `docs/status.json` 生成，禁止手改。UI 变更必须按 `docs/ui/README.md` 提供
  before/after/current。

## 开始前必须完成

完整阅读，不要只靠关键词片段：

1. `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`docs/ui/README.md`；
2. `docs/structure.md` 的 0–3、4、7、9、10.5、11、12、13.4、14–18 节；
3. `docs/architecture/implementation.md` 全文与 `docs/status.json`；
4. ADR `0002`、`0003`、`0005`、`0006`、`0007`、`0008`、`0010`、`0012`、`0013`；
5. `docs/tasks/20260829-app-agent-approval-budget-policy.md`、
   `docs/tasks/20260829-supervised-web-service-workload.md`、
   `docs/tasks/20260830-project-artifact-review.md`、
   `docs/tasks/20260830-lan-device-pairing.md`、
   `docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md` 与对应视觉 notes；
6. Agent/App policy/Artifact/Incident/Bridge/Device Auth/Common Proto 与生成代码；
7. Core Agent event append、approval creation/decision、Artifact materializer、Project tx/outbox、indexfeed
   tx-scoped sink、PostgreSQL adapter 与 integration/restart tests；
8. Reliability domain/application/PostgreSQL/transport、pending action replay、Runtime private client 与
   `cmd/reliability-host` wiring；
9. Gateway allowlist/proxy、device session middleware、rate limit、server-streaming tests、Core/Runtime/
   Reliability optional upstream 配置；
10. Runtime App Bridge authorizer、surface token/grant revision、`sdk/app-sdk`、`sdk/surface-sdk`、
    `clients/app-host` 与 opaque Web Bundle browser E2E；
11. Desktop system-window/window-manager、AdaptiveShell、Approvals/Artifact/SystemMonitor actions、agent-sdk、
    Project generation guard、Playwright fixture 与截图工具；
12. migrations/sqlc、Compose、systemd env、Makefile、CI targets、status renderer 和 migration checksum tests。

随后创建唯一 branch 与唯一实现任务记录，写明 baseline SHA、范围/非范围、阶段依赖、预期 Proto、
migration owner、失败矩阵、验收、提交计划和风险。开始改代码前实际运行并记录：

```sh
git status --short --branch
git log --oneline --decorate -20
git branch -a -vv
git diff --check
make bootstrap
make generate
make check
make test-integration
make test-e2e
```

不得为了基线清理 PostgreSQL volume。若基线失败，先分类为代码、环境或已有 blocker；记录证据并继续
所有不依赖阶段，禁止把基线失败归咎于尚未产生的改动。

## 全批次不可违反的边界

### 架构与数据所有权

- 依赖方向固定为 `domain → application → ports ← adapters`。
- Domain 不得导入 PostgreSQL/pgx/sqlc、Connect/Proto、HTTP、文件系统、浏览器 API、厂商 SDK或其他
  模块 adapter。
- `workos-core` Notification 模块是 notification fact、owner change stream、read state、App notification
  quota/idempotency receipt 的唯一 durable authority。
- Agent、Artifact、Project 等 Core 模块不能直接 SQL 写 Notification 表；同进程原子写通过 neutral、
  tx-scoped port/orchestration 注入。Notification 也不能反查对方 adapter/schema 拼装权威事实。
- `reliability-host` 继续唯一拥有 Incident/action 与它的 publication outbox；Core 禁止查询
  `workos_reliability` schema，Reliability 禁止写 `workos_core`。跨进程只能走 versioned private RPC +
  durable claim/complete/receipt，按 at-least-once 设计。
- `runtime-host` 只验证 App surface/session/grant 并调用 Core private notification command；它不持久化
  notification、不代替 Core 配额裁决、不直接访问数据库。
- Gateway 只负责 TLS/session/rate-limit/公开路由与可信 identity 注入；不得缓存 unread authority、
  伪造通知或把 private source/App-ingest RPC 加入 public allowlist。
- 跨进程 DTO 必须先 additive 修改 `api/proto`，再运行 `make generate`。v1 字段号不得复用；删除字段/
  枚举值必须 reserved；无法 additive 表达才写新版本与 ADR。
- 已执行 migration 逐字节不变。每张新表、index、constraint、sequence 和 migration 写明唯一 owner；
  sqlc package 与进程边界一致。
- 所有资源 ID 使用 canonical UUIDv7，时间 UTC microsecond；外部写命令用 durable idempotency key 或
  monotonic etag/revision，绝不以进程内 mutex/map 冒充裁决。

### 身份、安全、内容与隐私

- 浏览器提交的 owner/user/device/project/app header 全部不可信。Gateway 必须先剥除，再从有效 device
  session 注入；Core public Notification handler 只从 trusted context 取 owner/device。
- public request 不得包含 owner。Project filter/typed target 的任何 ID 都要 owner scoped 重验；foreign/
  missing 使用同一净化语义，不形成存在性 oracle。
- Notification target 必须是有限 typed oneof/enum，例如 approval、task、review artifact、incident、
  app instance；禁止存任意 URL、route、JavaScript、HTML、Connect method、host/port 或 provider payload。
- System notification 的 title/body 优先由服务端根据有限 kind/template 生成；不得持久化 task goal、Agent
  raw output、Artifact 正文/diff、Incident raw telemetry/log、Project workspace URI、credential 或 secret。
- App notification 允许的用户可见文本必须有严格 UTF-8/code-point/byte/line/control-char 上限，只作为
  inert plain text 渲染；禁止 markup interpretation、`dangerouslySetInnerHTML`、自动链接、任意 action
  URL。错误、日志和 metrics 只记录安全类别/ID，不记录正文。
- 浏览器 Notification API 默认不展示正文预览；只有 owner 明确开启且浏览器权限已授予才能展示有界
  preview。不能自动弹权限框。OS notification 的 `data`/tag 不含 token、cookie、owner、正文或路由，只
  使用 canonical notification ID；点击后仍回到 Gateway 重新读取/重验。
- App 永远拿不到 device cookie、Core URL、Reliability source lease、owner/project override、unread stream
  或其他 App 的通知。iframe 只能经已经转移的 MessagePort 调用一个受限 create command。
- 截图与 fixture 只用明显假数据；不得出现真实用户内容、真实 Provider、真实设备 ticket/cookie、
  bridge token、notification raw database payload 或 credential。

### 真实能力与诚实状态

- local-first 指“页面/PWA 活跃时实时接收；断线或再次打开时 durable 补收”。不实现 APNs/FCM 时，必须
  明确后台即时通知 unavailable。
- 若 transport 实际是 Connect server stream、SSE 或 bounded long-poll，就按真实名称记录；只有确实
  实现 WebSocket 才能称 WebSocket。
- fake Reliability observation 可证明 publication/notification 软件链路，但不能把
  `supervisor`/`incident-manager` 或 rootless container capability 升为 working。`docs/status.json` 的
  Reliability 现状保持诚实。
- 固定成功响应、内存-only unread count、DOM-only badge、setInterval 假事件、测试直插最终
  Notification 表、浏览器 route mock 都不构成 working evidence。

## 契约与 Notification 事实模型

先写 ADR（预期 `docs/decisions/0014-local-first-notifications.md`，若编号已占用取下一个），明确下面语义
再写 migration/代码。允许基于仓库证据调整字段形状，但不允许丢掉行为。

### Public NotificationService

建议新增 `api/proto/workos/notification/v1/notification.proto`，至少覆盖：

```text
ListNotifications       owner-scoped newest-first，project/unread/kind 有界过滤
GetNotification         owner-scoped exact fact
MarkNotificationRead    单条 monotonic unread→read，幂等
MarkNotificationsRead   bounded ids 或 read-through watermark，幂等
WatchNotificationEvents owner-wide resumable server stream
```

契约要求：

- `Notification` 公开 canonical ID、owner-safe scope（可选 project_id）、finite kind/severity/origin、
  server-derived inert title/body、typed target、created_at/read_at/revision；绝不公开内部 source receipt、
  quota bucket、publication lease 或 storage error。
- `NotificationEvent` 使用严格递增、持久化的 owner change sequence，至少区分 CREATED、READ/UPDATED 和
  RESET_REQUIRED。read mutation 也进入 change stream，第二台设备才能实时收敛。
- Watch cursor 是“最后已应用 change sequence”，不是 notification ID、数组 offset 或进程内 counter。
  reconnect 从 `after_sequence` 精确续传；客户端可重复收到但按 sequence/id/revision 幂等应用。
- List response 返回与 snapshot 对应的 high watermark；客户端固定顺序为：拿 snapshot/watermark → 建立
  after-watermark stream，避免 list/watch 窗口丢事件。
- 分页只在 application 边界规范化一次（明确 default/max），repository 用 limit+1，恰好满的末页不产生
  phantom token。token 必须绑定 owner/filter/sort snapshot 或经 owner-bound boundary lookup；不得裸 offset。
- 所有 enum 拒绝 UNSPECIFIED/未知数值；UUID、UTF-8、page size/token、idempotency key、filter 集合在任何
  existence read 前验证。Connect handler 设置从合法最大消息推导的 pre-decode wire budget，覆盖 gzip。
- Get foreign/missing 使用同一 `NotFound`；stale/conflicting idempotency 使用 `Aborted`；坏输入
  `InvalidArgument`；暂时数据库错误 `Unavailable`；存储不变量损坏 `Internal`，错误文本固定净化。

### Durable tables 与 invariants

Core migration（编写时预期 `029`，执行时取下一个空闲号）至少需要持久化：

- immutable notification identity/scope/origin/kind/template/typed target/source binding/created time；
- monotonic read projection/revision；
- owner change log/sequence，可在重启后 resume；
- source receipt/dedup key，保证同一 source fact 恰好投影一次；
- public read-command idempotency mapping 与 first-response snapshot；
- App create request digest/first response + UTC quota reservation；
- retention/sweep watermark 或等价的 stream-gap 权威事实。

数据库约束必须落实 owner binding、canonical finite kind、target shape、source uniqueness、read monotonicity、
revision、UTC timestamp coherence、正文上限和 App origin binding；不能只靠 Go validation。stored row 每次读和
idempotent replay 都重新验证，损坏 fail closed，不能把多个坏行拼成表面合法响应。

### Read、retention 与 stream gap

- read 是 owner 级单调事实，不提供“标回未读”；同 key/same request 精确 replay，different request
  `Aborted`，no-op 也要有确定 first response 且不能生成重复 change event。
- 多通知 read 命令要有硬上限并在单事务内全有或全无；foreign/missing 不能造成部分 mutation。
- 不能让 change log 无界增长后假装没有维护问题。ADR 必须定义并实现一个保守的 bounded sweep：只清理
  满足明确 age/state 的 read notification 与旧 change，绝不删除仍需展示的近期 unread fact。
- 若客户端 cursor 落在已清理区间，服务端必须发 `RESET_REQUIRED`（含新的 snapshot watermark 或明确的
  重新 List 指令），客户端清空本地 projection 后走 authoritative full list；绝不能从当前最小 sequence
  静默继续造成漏数。sweep/crash/restart/gap 均有真实 PostgreSQL 测试。
- 若为 unread 设置硬上限，系统 critical/approval/incident 不能被 App spam 挤掉；优先用 App 原子配额、
  source dedup 与安全 overflow/coalescing 策略。任何抑制都必须成为可观测的有限事实，不能静默 drop。

## System notification producers：与 source 事务原子

至少实现并证明下面四类 finite system notification：

1. `agent.approval.required`：waiting-approval task 与 pending approval 创建的同一事务内产生；typed action
   指向 approval/task，owner 打开后进入 Agent Center Approvals。
2. `agent.task.terminal`：completed/failed/cancelled 首次成为 terminal 的同一事务内产生；迟到 provider
   event、terminal replay 或 fallback repair 不得重复。
3. `artifact.review.created`：review Artifact + output mapping + Core timeline event + index publication 的
   同一事务内产生；Web Bundle Artifact 不产生该 kind，typed action 只指向 exact review Artifact。
4. `reliability.incident.opened` 或 terminal mitigation failure：由 Reliability source publication 经跨进程
   at-least-once 投影；typed action 指向 Incident/System Monitor，正文不携带 raw observation。

原子性要求：

- Core 内 source mutation 与 notification/source receipt/change event 要么同事务全部提交，要么全部回滚。
  不允许 transaction commit 后用 best-effort goroutine “补通知”。
- 通过 neutral tx-scoped port/orchestration 注入，参考 Artifact→IndexFeed 的既有模式；Agent/Artifact domain
  不导入 Notification adapter，Notification domain 不导入 Agent/Artifact entity。
- source key/digest 由 canonical、版本化字段构造；重放、并发双写、response-loss、Core restart 都只能
  得到一个 notification 和一个 CREATED change。
- source 失败若会破坏“事实产生必有通知”的 contract，则整个 source transaction 失败；任务记录必须
  写清哪些 source 是原子 hard requirement，不能在不同 producer 各自猜测。
- 不为每个 token delta/usage event 产生通知；只允许有限 kind，防止通知风暴。

## Reliability → Core durable publication

Incident 归 Reliability、Notification 归 Core。推荐链路如下；若现有代码证明另一种 private direction 更
符合依赖，可调整，但必须保留所有 durability/invariant：

```text
Reliability Incident transaction
  → workos_reliability.notification_publications（不含正文）
  → versioned private claim/complete RPC（lease + bounded batch）
  → Core optional consumer
  → Core transaction: source receipt + notification + CREATED change
  → complete Reliability publication
```

要求：

- Reliability migration 与表由 `reliability-host` 独占，Core 零 SQL；publication 只含 source ID、owner、
  project、incident ID、finite kind/severity/action outcome category、occurred_at 和 versioned digest。
- claim 使用 lease、worker identity、deadline 与 bounded batch；Core commit 后才 complete。complete response
  丢失、lease expiry、两个 worker 竞争、Core/reliability 任一重启都安全 replay。
- Core receipt 对 `(source_process, source_id, version/digest)` 物理唯一；same ID/different digest 是 stored
  corruption/contract violation，不能覆盖原通知。
- Reliability 不可达时 Core/Gateway/其他 notification sources 仍 ready；只把 incident source freshness
  报为 degraded/unavailable。恢复后 backlog 追平，不丢、不重复。
- private source/complete RPC 不进 Gateway allowlist，不向 App/浏览器开放；复用仓库既有 private transport
  安全模式并验证精确 URL/config，不能接受浏览器伪造 service identity。
- `make test-incident-notifications` 可用真实 reliability-host + 一个版本化 fixture Runtime service 驱动
  中立 observation 和 Incident application，不得直接 INSERT 最终 notification。fixture 证明软件侧
  cross-process 链路，但任务记录必须明确它不等于真实 rootless supervisor acceptance。

## Gateway 与 resumable paired-device stream

- Gateway 只 allowlist public NotificationService；private App create、Reliability publication/source、admin/
  sweep RPC 必须确定性 404。
- 对所有 notification RPC 剥除外来 identity/bridge headers，再从 production device session 注入 owner+
  device；dev bypass 只保留既有明确边界。
- 审计 server-streaming auth：session 在握手后被 revoke 时，不能无限保持已授权流。采用现有架构可证明的
  方案，例如 Gateway 周期性 revalidate + cancel，或有界 stream lifetime/heartbeat 后强制客户端带新
  session 重连；写入 ADR、配置上限和测试，不能只靠页面关闭。
- 每 device/session 限制并发 notification streams 与 reconnect rate；超限 `ResourceExhausted`，不能拖垮
  Gateway/Core。取消浏览器 stream 只释放本地资源，不删除 notification。
- heartbeat/control message 不推进 durable cursor。只有实际 change sequence 被应用后客户端才更新
  in-memory cursor；不把 cursor/token/正文写入 URL、DOM attribute 或不受控 localStorage。
- Core 或 Gateway 短暂不可达时 UI 显示 bounded degraded state，指数/有界 backoff 重连并从最后 sequence
  补收；不得每次错误清空 unread，也不得用固定 “connected” 指示冒充。
- 两个独立、真实配对或 production-auth-equivalent Chromium context 必须证明：A 收到 CREATED，B 断线后
  补收，B 标 read，A 收到 READ change，两个 badge 收敛；Gateway/Core restart 后仍成立。

## Adaptive Notification Center 与 typed actions

新增普通 system window，不做永久侧栏：

- expanded：顶部/系统入口有 bell + bounded unread badge，打开普通 Notification Center window；
- compact：从现有 bottom nav/project sheet 中可达，不遮住 Agent composer/safe area；
- medium：从明确 dock/system action 可达；fold-separated 复用 pure window projection，不跨 hinge；
- Project Mission Control/Project selector 可显示按 Project 推导的有限 unread count；global notification 不得
  被错误归入当前 Project。

Notification Center 最低状态：loading、empty、unread/list、read、unavailable/retry、stream reconnecting、
reset/resync；支持 All/Current Project/Unread 有界过滤与显式 Mark read/Mark visible page read。不要为此引入
无限列表或一次性加载全部历史。

行为边界：

- 列表按权威顺序，React key 用 notification ID；同 sequence/revision event 幂等，乱序/旧 revision inert。
- Project 切换不会销毁 owner-global stream，但 filter/action generation 必须隔离；Project A 的迟到 List/
  Get/action response 不得污染 B。
- read 只在用户显式动作或成功打开 typed target 后提交；“进入窗口”不能自动把所有项读掉。
- typed action 打开前通过现有 public service重新读取目标：approval→Approvals、task→Agent Center、
  artifact→inert Viewer、incident→System Monitor、app→仍 active 的 Surface/App Library。missing/archived/
  uninstalled 显示固定 stale 文案并允许标 read，不尝试任意 URL fallback。
- 通知正文、App text、Artifact title 都以 inert text 渲染；不把 ID/digest/token 放入可见文案、URL 或
  data attribute。可访问性覆盖键盘、visible focus、aria label、触控尺寸、reduced motion。
- 浏览器 Notification API 是可选增强：明确 toggle、permission 状态、preview 默认关闭；仅页面仍活跃且
  hidden 时触发。denied/unavailable 不影响 durable Notification Center。Service Worker 后台 push 不在
  本批次，不得写固定成功 worker。
- 所有用户可见变化按 `docs/ui/README.md` 保存
  `docs/ui/desktop-web/changes/20260902-local-first-notifications/{before,after,notes.md}` 并更新 `current/`。
  至少固定采集 1440×900、820×1180、390×844 的 unread/list/action 状态，以及 granted App notification
  来源状态；使用确定 fixture，隐藏随机时间/ID。

## Capability-scoped App `notifications.create`

这是同一通知闭环的受限 producer，不是 App 任意广播 API。

### Manifest、grant 与 Bridge

- 在 App Registry 唯一 capability vocabulary 中 additive 增加 `notifications.create`；Schema 仍是唯一
  manifest shape 事实源。它只是 requested permission，安装/Manage permissions 必须明确 consent。
- additive 扩展 Bridge Proto、`sdk/surface-sdk`、`sdk/app-sdk`、`clients/app-host` 和 Runtime handler。
  capability grant 名与 negotiated method 名是否相同要明确并集中映射，禁止 SDK/host/runtime 各写一套
  漂移词表。
- 每次调用服务端重新验证 bridge token grammar/hash/expiry、device、surface session、owner、Project、
  app instance/version、active installation 与 exact current grant revision，然后才调用 Core private
  command。grant revoke、uninstall、version/grant epoch 变化、surface close 后旧 MessagePort 立即 fail
  closed，并且零 Core create/quota side effect。
- App body 不能携带 owner/project/device/source/origin/severity override/target URL。Runtime 从可信 session
  派生 scope，Core 从受信 App principal 派生 origin/typed app target。

### 输入、幂等、配额与内容

- 请求至少有 App-scoped idempotency key、bounded title、optional bounded body。明确 rune/UTF-8 byte/line
  上限，拒 invalid UTF-8、NUL、C0/C1、异常换行；plain text 中的 `<script>` 只能作为文字且不得被解释。
- same `(owner, installation, idempotency key)` + same canonical request 跨请求/进程/重启精确 replay first
  response；same key/different request `Aborted`；失败不消费 key。
- replay-first：先裁决已消费 key，再检查当前 quota；首次成功在单事务中完成 request mapping、UTC quota
  reservation、notification 和 CREATED change。response loss 不重复收费/通知。
- 配额必须由 Core PostgreSQL 原子裁决，至少有短窗口 burst 与 UTC daily hard cap；数值有限、写入 ADR/
  config/status evidence。不同 App 隔离，系统通知不占 App quota，不能用 runtime memory rate limiter作为
  唯一权威。
- App 只能创建 INFO/normal、Project-scoped、origin-labeled notification；action 最多“打开当前 app
  instance”，不能伪造 approval/incident/system kind、high severity、其他 App 或 Project。
- quota exhausted 用稳定 `ResourceExhausted`/业务码，冲突 `Aborted`，revoked/epoch drift
  `PermissionDenied` 或既有 Bridge 约定，dependency outage `Unavailable`；错误不回显 title/body。

### Opaque App 真实验收

`make test-app-notifications` 必须用真实 Registry→install consent→Web Bundle Artifact→Surface token→
opaque iframe MessageChannel→Runtime→Core→Gateway owner stream/Notification Center 链路证明：

- 未请求 capability 时 method 不协商；请求但未 grant 时不协商；grant 后可调用并显示 origin label；
- same key replay 只有一条，different payload 冲突，burst/day quota 原子且重启后仍有效；
- foreign project/owner override 在 SDK/host/server 各边界不可表达或被拒；
- revoke 后已有 port 的下一次调用失败且 Core 调用计数/notification/quota 零变化；重新 grant 的新 epoch
  只对新 Surface 生效；uninstall/close 同理；
- malicious markup/oversize/invalid envelope/inflight storm/timeout/cancel 不执行或不泄漏；
- notification typed action 只能重开仍 active 的同一 app，卸载后固定 stale。

## 分阶段实施要求

### 阶段 0：基线、ADR 和任务记录

- 完成必读与基线，查看现有 `docs/ui/desktop-web/current/`，采集或复制准确 before。
- 在任务记录写出 Notification kind/target/source matrix、public/private RPC、表 owner、event sequence、read
  state、retention/gap、App quota、stream auth、错误矩阵和 capability status 规则。
- 写 ADR 后再改 Proto；若 ADR 暴露必须破坏 v1 或新增信任根，才按停止条件请求用户。

### 阶段 A：Proto、domain、storage 与 public service

- additive Notification Proto + `make generate`；在 `internal/core/notification/` 下建立
  `domain`、`application`、`ports`、`adapters`、`transport`，保持分层。
- forward-only Core migration/sqlc，真实 PostgreSQL 约束、transaction、idempotency、pagination、stored-row
  validation、sweep/gap。
- public transport wire budget、identity、fixed error matrix；Core wiring/SystemService capability；Gateway 精确
  allowlist但此阶段先不做 UI。
- 单元/transport/real PostgreSQL integration 覆盖并发、replay、foreign、corruption、transient、last-page、
  read monotonicity、retention reset。

### 阶段 B：Core atomic producers

- 用 neutral tx-scoped sink接入 approval required、task terminal、review Artifact created。
- 每类做 commit rollback、并发、lost response、terminal replay、wrong artifact subtype 测试；验证 source
  rollback时没有 orphan notification，notification failure时 source也不半提交。
- 不扩大到每个 project/app/index event；先完成本文 finite matrix。

### 阶段 C：Reliability publication

- additive private Proto、Reliability forward migration、publication domain/application/adapter/transport与 Core
  optional consumer/receipt。
- 覆盖 lease expiry、complete response loss、consumer restart、source restart、same-ID drift、outage/backlog、
  bounded batch/fairness。
- 建立 `make test-incident-notifications` 的真实跨进程 fixture；保持 supervisor capability false。

### 阶段 D：Gateway stream 与 client projection

- 实现/加固 stream session lifetime、concurrency/reconnect budget、identity stripping；private 路由 404。
- agent-sdk/protocol client 封装 authoritative snapshot + resumable stream；处理 duplicate、out-of-order、
  RESET_REQUIRED、abort、Project switch 与 exponential backoff。
- 用两个 Chromium context 证明跨设备 read convergence、断线补收和 Core/Gateway restart。

### 阶段 E：Adaptive Notification Center

- Window Manager/Adaptive Shell/desktop window、bell/badge、filters、typed actions、read commands、degraded/reset
  states与可选 foreground browser notification preference。
- 先补 Vitest/React tests，再写真实 Playwright；无 route mock 冒充最终 full-stack gate。
- 固定 viewport采集 before/after/current 和 notes，并人工检查 PNG 尺寸/内容。

### 阶段 F：App notification producer

- capability vocabulary + grant consent + additive Bridge/SDK/app-host/runtime/Core private command。
- PostgreSQL idempotency/quota/notification single transaction；每次 exact grant revision reauth。
- opaque App真实 gate覆盖 grant/deny/revoke/uninstall/regrant/restart/quota/hostile payload。

### 阶段 G：故障、重启、全量门禁与文档

- restart battery 增加 notification seed/verify：system/App notification、read state、idempotency replay、stream
  watermark/quota跨 Core/Gateway/Runtime/ Reliability 重启保持。
- 注入 Core DB outage、Reliability outage/recovery、Gateway stream cut、App revoke race、retention gap、stored
  row corruption；每条都验证固定错误、零越权、零重复、恢复收敛。
- 连续执行 `make generate` 直到第二次无 diff；全量验证、status/implementation/ADR/task/README 生成收口；
  staged diff secret/binary/trace扫描，聚焦提交，干净工作树。

## 必须新增的专项门禁

### `make test-notification-center`

真实 PostgreSQL + Core + harness-host Fake provider + Gateway + Chromium（两个 context），至少证明：

- approval required、approve后同一 task terminal、review Artifact created 都产生唯一通知；
- typed action进入正确窗口并重验目标；mark read在两设备收敛；
- 断流期间产生事件，重连精确补收；duplicate event inert；
- Core/Gateway restart后 unread、cursor、idempotency、read state不漂移；
- foreign owner/project、invalid cursor、oversize/gzip、DB outage、retention reset fail closed；
- 390×844、820×1180、1440×900关键行为可达且 expanded 既有桌面不回退。

### `make test-incident-notifications`

真实 PostgreSQL + reliability-host + Core + Gateway + versioned fake Runtime observation fixture，至少证明：

- Incident 与 Reliability publication同事务；Core receipt/notification/change同事务；
- claim/complete lost response、lease expiry、双 consumer、两端 restart不重复；
- Reliability outage只降级 incident notification freshness，Core其他通知继续；恢复追平；
- owner typed action经 Gateway读到同一 Incident；publication不含 raw telemetry/content；
- 测试结果与文档明确“不证明 rootless supervisor working”。

### `make test-app-notifications`

真实 Registry/Core/Runtime/Gateway/PostgreSQL/Chromium + opaque Web Bundle App，覆盖上一节完整 grant、epoch、
idempotency、quota、restart、hostile envelope、revoke/uninstall matrix；不能 route mock Notification service。

三个门禁都必须：

- 在全新唯一 fixture namespace内运行，可重复执行；
- 不删除共享 PostgreSQL volume，不依赖执行顺序；
- 使用 timeout/condition等待，不用盲目 sleep消除竞态；
- 失败时清理临时进程/profile/file，不删除用户数据；
- 输出不含正文、token、cookie、secret、raw SQL/constraint、宿主路径。

## 失败矩阵（至少覆盖）

| 场景                                     | 必须结果                                              |
| ---------------------------------------- | ----------------------------------------------------- |
| 未认证/被 revoke device                  | `Unauthenticated`；新请求失败，长流在有界时间内终止   |
| malformed UUID/enum/filter/cursor        | 存在性读取前 `InvalidArgument`                        |
| foreign/missing notification/target      | 净化 `NotFound`，无存在性 oracle                      |
| same idempotency key / different request | `Aborted`，原 first response与配额不变                |
| stale read revision（若采用 revision）   | `Aborted` 或确定 monotonic replay，不覆盖新事实       |
| DB临时不可用                             | `Unavailable`，无部分 source/read/quota commit        |
| stored invariant/digest损坏              | 净化 `Internal`，不静默修复/覆盖                      |
| old stream cursor已被sweep               | `RESET_REQUIRED` + authoritative resync，不静默跳过   |
| duplicate/out-of-order stream change     | client按 sequence/revision幂等，badge不漂移           |
| Reliability source不可达                 | incident freshness degraded；其他 sources/API保持可用 |
| publication complete response丢失        | source重放，Core receipt no-op，仅一条通知            |
| App missing/revoked grant/old epoch      | fail closed；Core create/quota调用零副作用            |
| App quota exhausted                      | `ResourceExhausted`；不消费失败key，不挤占system通知  |
| notification target随后失效              | 固定 stale UI，可标read；不fallback任意URL            |
| browser Notification denied/unavailable  | durable center正常；不循环提示权限                    |

## 明确不在范围内

- APNs、FCM、Web Push VAPID、后台 Service Worker delivery、云 relay、公共互联网、VPN/overlay；
- native iOS/Android wrapper、Keychain/Keystore、真实手机后台/系统通知验收；
- email、SMS、Slack、Webhook 或第三方通知 provider；
- 任意 App 广播、跨 Project/owner通知、App读取通知列表/未读流、App自定义URL/action/severity；
- notification正文搜索、semantic embedding、Indexer notification indexing；
- 完整用户偏好/免打扰时段/频道订阅/国际化模板管理/复杂聚合推荐；
- 删除/编辑通知正文、标回未读、多人协作与跨 owner共享；
- rootless Podman安装/验收、Reliability supervisor状态升级、Repair Orchestrator/Deployment Controller；
- 新 Provider、真实模型调用、Credential Vault变更；
- 第七个常驻进程、跨schema SQL、shared mutable entity或第二套manifest DTO；
- 手改 `gen/`、`src/gen/`、README生成状态区块。

## 停止并请求用户的条件

仅在以下情况停止整批并请求用户决定：

1. 必须复用/删除/改变已有 v1 字段号，无法 additive 表达；
2. 必须修改已执行 migration `001`–执行时最新编号，而非新增 forward migration；
3. 必须增加第七个常驻进程或新的外部信任根/付费服务；
4. 必须读取真实 secret、安装宿主软件、提权或删除用户数据/volume；
5. 工作树有无法归属且与任务文件直接冲突的用户改动，无法安全保留；
6. 仓库证据证明 Notification authority不能归 Core，且两种方案会造成实质不同的产品/安全结果。

普通实现选择、命名、测试失败、依赖暂时不可达、工作量大、无 Podman、无 APNs/FCM、没有真机都不是
停止理由。采用最保守方案，记录依据，继续所有可执行工作。

## 必须执行的最终验证

按风险和依赖顺序执行并把真实结果写入任务记录；失败必须修复或如实保持未完成，不能只列命令。先把
所有预期源码和生成结果提交到当前 feature branch 的聚焦 commit，再从干净 HEAD 执行下列幂等检查：

```sh
git diff --check
make generate
git diff --exit-code -- gen sdk/protocol/src/gen
make generate
git diff --exit-code -- gen sdk/protocol/src/gen
make check
make test-integration
make test-e2e
make test-notification-center
make test-incident-notifications
make test-app-notifications
make test-adaptive-shell
make test-lan-pairing
docker compose config --quiet
```

另外执行：

- `buf lint` 与相对执行时 `main` 的 `buf breaking`；
- `go test -race` 覆盖新 Core Notification、Reliability publication、Runtime Bridge package；
- 前端 Notification Center/agent-sdk/app-sdk/app-host Vitest；
- migration checksum pin 与真实 PostgreSQL migration forward/restart；
- private RPC Gateway 404、identity header stripping、pre-decode gzip bomb；
- staged diff secret/credential/private-key/大二进制/trace/video/绝对路径扫描；
- 视觉 PNG 尺寸、before/after/current和notes链接核验。

`make test-podman-fixture` 不是本批次通过条件；若顺手探测，只记录既有真实 blocker，不安装、不伪造、不
因此改变 Runtime/Reliability状态。

## 文档、状态与完成定义

完成时同步：

- ADR `0014`（或执行时下一个编号）；
- `docs/architecture/implementation.md`：Notification authority、producer transaction、Reliability source、
  App create、stream/read/retention/UI边界；
- `docs/status.json`：只有真实三门禁都通过才增加/升级 Notification working slice；Gateway/Desktop/Runtime
  evidence只写实际证明内容；Reliability supervisor仍按Podman证据保持原状态；
- `docs/tasks/20260902-v1-local-first-notifications.md`：scope、依赖、Proto/migration owner、commit、命令、
  截图、失败矩阵、未决风险与下一步；
- `docs/ui/desktop-web/changes/20260902-local-first-notifications/` 的 before/after/notes和
  `docs/ui/desktop-web/current/`；
- Makefile专项门禁、Compose/systemd/env示例、README生成结果（只经工具）。

可以标 done/working 的必要条件：

- 不是固定响应/内存状态，真实 PostgreSQL重启后一致；
- Core source原子性与Reliability at-least-once receipt都有证据；
- 两个device context实时/补收/read收敛，session revoke有界终止；
- App grant/revoke/epoch/quota/opaque iframe链路完整；
- UI三viewport视觉证据齐全；
- `make generate`幂等、`make check`、三专项门禁、integration/E2E通过；
- 工作树干净、提交聚焦、未merge、未push。

若 system通知working但App或Reliability子链路没有完整证据，任务不得整体标done；可以提交已完成阶段，
但 `docs/status.json` 只能写实际子能力，任务记录保持active并给出可复现阻塞。不得用“代码已写”替代端到端
证据。

## 最终交接格式

最终回复和任务记录都必须包含：

1. 唯一最终目标是否达成；哪些 notification source/consumer真实 working；
2. branch名、base SHA、按阶段 commit hash，确认只有一个 branch/worktree/writer；
3. Proto/ADR/migration与每张表的owner；
4. system producer原子性、Reliability publication、App quota/grant、stream/read/retention的关键语义；
5. 三个专项门禁、全量命令与真实 PASS/FAIL/BLOCKED；
6. before/after/current视觉证据路径与viewport；
7. 故障/重启/跨设备/revoke/response-loss测试结论；
8. `docs/status.json`最终裁决，尤其明确后台Push和Reliability supervisor仍未声明的能力；
9. 未决风险和下一步，不用聊天记录替代仓库事实；
10. 最终 `git status --short --branch`、HEAD，并明确未merge、未push、未删除volume、未使用真实secret或
    外部Push/Provider。

从现在开始：先核对仓库与基线，创建唯一 branch/任务记录，写 ADR和契约矩阵，然后持续实现到完整验收。
