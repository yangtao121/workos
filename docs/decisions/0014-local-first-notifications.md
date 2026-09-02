# ADR-0014: Local-first Notification——持久通知、owner change stream 与授权 App 闭环

日期：2026-09-02。状态：Accepted（实现分支 `feat/v1-local-first-notifications`）。

## 背景

系统已有真实的 Project、Agent Task/审批、Artifact Review、Reliability Incident read model、LAN
pairing、Adaptive Shell 与 App Bridge，但没有 Notification 事实、未读同步或通知中心：用户只有
打开各窗口并主动刷新，才知道任务完成、需要审批、Artifact 已生成或 Incident 已发生。
`docs/structure.md` 13.4 已固定第一版现实边界——前台实时接收，离线/断线后同步未读，后台
APNs/FCM 即时投递不作保证。

本 ADR 固定第一条 local-first 通知链路的边界：**Core-owned、owner-scoped 的持久通知事实 +
monotonic owner change stream + Gateway 公开 list/get/read/watch + 双设备 read 收敛 +
Adaptive Notification Center + typed action + 受 `notifications.create` grant 约束的 App 通知 +
Reliability Incident 的跨进程 durable publication**。

明确不在范围：APNs/FCM/Web Push VAPID/后台 Service Worker delivery/云 relay、native wrapper、
email/SMS/第三方 provider、任意 App 广播、通知正文搜索/semantic embedding、免打扰时段/频道
订阅/i18n 模板管理、删除/编辑/标回未读、跨 owner 共享。

## 决策

### 1. 所有权：Core 是唯一 durable authority

- `workos-core` 新增 Notification 模块（`internal/core/notification`），物理上仍属于 Core 进程，
  不增加第七个 daemon。它是 notification fact、owner change stream、read state、App
  notification quota/idempotency receipt 的唯一权威。
- Agent/Artifact/Project 等 Core 模块不直接 SQL 写 Notification 表；同进程原子写通过
  neutral、tx-scoped 的 `TxSink` 端口注入（复制 ADR-0013 indexfeed→Artifact/Project 的既有
  模式）。Notification domain 不导入 Agent/Artifact/Project entity，也不反查它们的表。
- `reliability-host` 继续唯一拥有 Incident/action 与它的 publication outbox。Reliability 禁止写
  `workos_core` schema，Core 禁止查询 `workos_reliability` schema；跨进程只走 versioned
  private RPC + durable claim/complete/receipt，按 at-least-once 设计。
- `runtime-host` 只验证 App surface/session/grant 并调用 Core private notification command；
  它不持久化 notification、不裁决配额、不直接访问数据库。
- Gateway 只 allowlist public `workos.notification.v1.NotificationService`；private App ingest、
  Reliability publication source、admin/sweep RPC 一律确定性 404。

### 2. 通知事实模型

migration `029`（owner：workos-core Notification）持久化：

- `workos_core.notifications`：immutable identity（UUIDv7）、owner binding、可选 project scope、
  finite kind/severity/origin、server-derived 有界 inert title/body、typed target
  （`target_kind` + `target_id`）、source binding（`source_process`、`source_id`、`source_digest`）、
  App origin binding（`app_installation_id`，仅 app origin）、`created_at`、`read_at`、
  `read_change_sequence`。CHECK 约束落实 kind/target/app-origin 形状、正文上限、UTC 时间
  coherence；stored row 每次读取与幂等 replay 都重新验证，损坏 fail closed 为净化 Internal。
- `workos_core.notification_changes`：owner-wide 严格递增 change log（PK
  `(owner_user_id, change_sequence)`），`change_type ∈ {created, read}`；read mutation 也产生
  change，第二台设备才能实时收敛。
- `workos_core.notification_owner_sequences`：per-owner monotonic sequence 计数器
  （PK owner）+ `swept_through` 水位。序列在插入事务内以
  `INSERT .. ON CONFLICT DO UPDATE last_sequence = last_sequence + 1 RETURNING` 分配；
  事务回滚自动回收，因此正常序列无空洞。
- `workos_core.notification_source_receipts`：`(source_process, source_id, source_digest)`
  物理唯一——同一 source fact 恰好投影一次；same source/different digest 是 stored
  corruption/contract violation，绝不覆盖原通知。
- `workos_core.notification_read_requests`：public read-command 幂等 mapping（PK
  `(owner_user_id, idempotency_key)`）+ canonical request digest + 版本化 first-response
  snapshot（ADR-0004 模式）。
- `workos_core.notification_app_requests`：App create 的
  `(owner, installation, idempotency_key)` 幂等 mapping + canonical request digest + 首响应
  snapshot；失败不消费 key。
- `workos_core.notification_app_quotas`：UTC daily hard cap + 短窗口 burst 计数，由 Core
  PostgreSQL 在首次成功事务中原子预留（guarded UPDATE 防超卖，ADR-0005 模式）。

数据库约束（不是只靠 Go validation）：owner binding FK、canonical finite kind/target CHECK、
`(owner, change_sequence)` 唯一、source receipt 唯一、read 单调
（`read_at IS NULL OR read_change_sequence > 0` 且只能 NULL→值）、正文 byte/code-point 上限、
app origin 必须携带 installation、system origin 必须无 installation。

### 3. finite kind 与 typed target

v1 只允许五个 kind，target 只能是有限 typed oneof：

| kind                         | origin | target_kind | 生产者与原子性                                 |
| ---------------------------- | ------ | ----------- | ---------------------------------------------- |
| `agent.approval.required`    | system | approval    | Agent `CreateForAppApproval` 同事务（hard requirement） |
| `agent.task.terminal`        | system | task        | Agent 首次 terminal 转换同事务（hard requirement）；迟到 provider event、terminal replay、fallback repair 不重复 |
| `artifact.review.created`    | system | artifact    | Artifact materializer 同事务（hard requirement）；Web Bundle Artifact 不产生 |
| `reliability.incident.opened`| system | incident    | Reliability publication 经 at-least-once 投影（best-effort durable：source outage 只降级 freshness） |
| `app.instance.message`       | app    | app         | App `notifications.create`（grant/quota/idempotency 约束） |

- title/body 由服务端按有限 kind/template 派生（任务 goal、Agent raw output、Artifact 正文、
  Incident raw telemetry、workspace URI、credential 一律不进入持久化正文或日志）。
- typed action 只能打开其权威目标：approval→Agent Center Approvals、task→Agent Center、
  artifact→inert Artifact Viewer、incident→System Monitor、app→仍 active 的 Surface/App
  Library。禁止存任意 URL/route/JS/HTML/Connect method/host:port。

### 4. System producers：与 source 事务原子

- Core 内 source mutation 与 notification/receipt/CREATED change 同事务全有或全无；不允许
  commit 后 best-effort goroutine 补通知。对 approval.required、task.terminal、
  artifact.review.created 三类，"事实产生必有通知"是 source transaction 的 hard requirement：
  notification 失败则 source 事务整体回滚。
- source key/digest 由 canonical、版本化字段构造（`workos.notification.source.v1` digest 域）；
  重放、并发双写、response loss、Core restart 都只能得到一个 notification 和一个 CREATED
  change——由 source receipt 唯一约束在事务内物理仲裁，冲突时事务内重读既有投影并幂等返回。
- 不为每个 token delta/usage event 产生通知。

### 5. Reliability → Core durable publication

```text
Reliability Incident 事务
  → workos_reliability.notification_publications（migration 030，owner：reliability-host）
  → Core optional consumer 经 versioned private claim/complete RPC
  → Core 事务：source receipt + notification + CREATED change
  → complete Reliability publication
```

- publication 只含 source ID、owner、project、incident ID、finite kind/severity/action outcome
  category、occurred_at 与 versioned digest，绝不含 raw observation/telemetry/正文。
- claim 使用 lease（worker identity + deadline + bounded batch + `FOR UPDATE SKIP LOCKED`）；
  Core commit 后才 complete。complete response 丢失、lease expiry、双 consumer、任一端重启都
  安全 replay（Core receipt no-op）。
- Reliability 不可达时 Core/Gateway/其他 source 仍 ready；只把 incident source freshness 报为
  degraded/unavailable。恢复后 backlog 追平，不丢、不重复。
- 私有 source/complete RPC 不进 Gateway allowlist。fixture Runtime observation 驱动的
  `make test-incident-notifications` 只证明软件侧 cross-process 链路，不证明 rootless
  supervisor working；`docs/status.json` 的 Reliability 现状保持诚实。

### 6. Public 契约与 resumable stream

`workos.notification.v1.NotificationService`：`ListNotifications`（owner-scoped newest-first，
project/unread/kind 有界过滤，application 规范化 page size + limit+1 探测，恰好满的末页无
phantom token）、`GetNotification`、`MarkNotificationRead`、`MarkNotificationsRead`（bounded
ids，单事务全有或全无）、`WatchNotificationEvents`（owner-wide resumable server stream）。

- `Notification` 公开 canonical ID、owner-safe scope、finite kind/severity/origin、
  server-derived inert title/body、typed target、created_at/read_at/revision；绝不公开内部
  receipt、quota bucket、publication lease 或 storage error。
- `NotificationEvent` 携带严格递增的 owner change sequence，区分 `CREATED`/`READ`。Watch
  cursor 是"最后已应用的 change sequence"；reconnect 从 `after_sequence` 精确续传；客户端按
  sequence/notification/revision 幂等应用，重复/乱序/旧 revision inert。
- List 返回与 snapshot 对应的 high watermark；客户端固定顺序：snapshot/watermark → 建
  after-watermark stream，避免 list/watch 窗口丢事件。
- cursor 落在被 sweep 区间（`after_sequence < owner.swept_through`）→ 服务端发
  `RESET_REQUIRED`（携带新 snapshot watermark），客户端清空本地 projection 后走 authoritative
  List。绝不从当前最小 sequence 静默继续。
- 错误矩阵：foreign/missing → 同一净化 `NotFound`（无存在性 oracle）；same key/different
  request → `Aborted`；坏输入在任何存在性读取前 → `InvalidArgument`；暂时数据库错误 →
  `Unavailable`；stored invariant 损坏 → 净化 `Internal`。
- transport pre-decode wire budget 覆盖 protobuf/JSON/gzip；enum 拒绝 UNSPECIFIED/未知数值；
  UUID/UTF-8/page size/token/idempotency key/filter 先验证。

### 7. Read、retention 与配额

- read 是 owner 级单调事实（unread→read 一旦提交不可逆）；同 key/same request 精确 replay
  first response，different request `Aborted`，no-op（已读）有确定 first response 且不产生重复
  change event。多通知 read 有硬上限（≤100）且单事务全有或全无；foreign/missing 不造成部分
  mutation。
- bounded sweep 只清理满足明确 age/state 的 read notification 与旧 change，绝不删除近期
  unread fact；sweep 推进 `swept_through` 水位，是 stream-gap 的权威事实。
- App 配额由 Core PostgreSQL 原子裁决：短窗口 burst（默认 10/分钟）与 UTC daily hard cap
  （默认 200/日，数值写入 config/status evidence）；不同 App 隔离，系统通知不占 App quota。
  quota exhausted → 稳定 `ResourceExhausted`；失败不消费 key、不产生通知；任何抑制都是
  可观测的有限事实。

### 8. App `notifications.create`

- App Registry capability vocabulary additive 增加 `notifications.create`（仍是 requested
  permission，安装/Manage permissions 必须显式 consent；grant 名与 negotiated bridge method 名
  集中映射，SDK/host/runtime 不各写一套词表）。
- 每次调用 Runtime 重新验证 bridge token grammar/hash/expiry、device、surface session、owner、
  Project、app instance/version、active installation 与 exact current grant revision，再调用
  Core private `AppNotificationIngest`。grant revoke、uninstall、version/grant epoch 变化、
  surface close 后旧 MessagePort 立即 fail closed，零 Core create/quota 副作用。
- App body 不能携带 owner/project/device/origin/severity override/target URL。请求只有
  App-scoped idempotency key、bounded title、optional bounded body（UTF-8/code-point/byte/
  line/control-char 上限；invalid UTF-8、NUL、C0/C1、异常换行拒绝；`<script>` 只作为文字）。
- App 只能创建 INFO/normal、Project-scoped、origin-labeled `app.instance.message`；action 最多
  "打开当前 app instance"。

### 9. Gateway 流安全与身份

- 所有 notification RPC 剥除浏览器提交的 identity/bridge headers，从有效 device session 注入
  owner+device；public request 不含 owner。project filter/typed target ID 都要 owner-scoped
  重验；foreign/missing 同一净化语义。
- WatchNotificationEvents 采用**有界 stream lifetime（默认 120s）+ 周期 heartbeat control
  消息**：stream 到期正常结束，客户端带新 session 重连；Gateway 另对 notification watch 路由
  做周期性 session revalidate + context cancel，session 握手后被 revoke 时流在有界时间内终止。
  heartbeat/control 不推进 durable cursor。
- 每 owner 限制并发 notification streams（默认 4）与 reconnect rate；超限
  `ResourceExhausted`。取消浏览器 stream 只释放本地资源，不删除 notification。

### 10. 客户端与 UI

- Desktop 以内存 projection 消费（可丢弃、可从 Core 重建）；cursor 不进 URL/DOM attribute/
  不受控 localStorage。Core/Gateway 短暂不可达时 UI 显示 bounded degraded state，指数/有界
  backoff 重连并从最后 sequence 补收。
- 新增普通 Notification Center system window：expanded 顶部 bell + bounded unread badge、
  compact 从 bottom nav/project sheet 可达、medium 从 dock 可达、fold-separated 复用 pure
  window projection；loading/empty/unread/read/unavailable/reconnect/reset 全状态；
  All/Current Project/Unread 有界过滤；显式 Mark read / Mark visible page read；"进入窗口"
  不自动读掉。
- 浏览器 Notification API 是可选增强：明确 toggle、permission 状态、preview 默认关闭、仅页面
  活跃且 hidden 时触发；denied/unavailable 不影响 durable Center；不写固定成功 Service Worker。
- UI 证据按 `docs/ui/README.md` 保存 before/after/current + notes。

## 后果

- 三个新专项门禁：`make test-notification-center`、`make test-incident-notifications`、
  `make test-app-notifications`。
- Notification working slice 只有在三门禁全通过后才进入 `docs/status.json`；后台 Push 与
  Reliability supervisor 能力继续保持如实未声明。
- 已知代价：per-owner sequence 计数器把同一 owner 的通知插入串行化（低频场景可接受）；
  watch 有界 lifetime 带来重连开销；App 通知配额是保守硬上限。
