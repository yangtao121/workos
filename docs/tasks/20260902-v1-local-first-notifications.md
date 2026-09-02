# Task: v1 Local-first Notification——持久通知、实时补收、跨设备已读与授权 App 闭环

- 状态：done（三门禁 + 全量回归通过；APNs/FCM 后台推送、Service Worker push、通知搜索、
  用户偏好如实 unavailable；Reliability supervisor 判定不变）
- Owner/Agent：overnight implementation agent（唯一写入者，单 branch/worktree）
- 进程/模块：workos-core（Notification 模块 + tx-scoped producers + private Reliability/App ingest）、
  reliability-host（notification publication outbox + source service）、workos-gateway（public
  NotificationService allowlist + stream revalidate）、runtime-host（App Bridge `notifications.create`）、
  desktop-web（Adaptive Notification Center + bell/badge + typed actions）
- 依赖：Agent approval/task terminal（ADR-0005）、Artifact review materialization（ADR-0008）、
  tx-scoped sink/claim-complete 模式（ADR-0013）、App grants/revision（ADR-0003）、surface
  session/token（ADR-0002）、Gateway identity（ADR-0007）、Adaptive Shell
- Branch：`feat/v1-local-first-notifications`（自本地 `main` @ `4e6e0b8`）
- 实现依据：`docs/prompts/20260902-local-first-notifications-prompt.md`（提示文件位于
  `docs/prompts/20260902-next-agent-local-first-notifications.md`，提交后重命名为仓库事实路径）
- ADR：`docs/decisions/0014-local-first-notifications.md`

## 目标与范围

唯一最终目标链路：

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

明确非范围（保持 unavailable/不实现）：APNs/FCM/Web Push VAPID/后台 Service Worker delivery、
云 relay/公网、native wrapper、email/SMS/第三方 provider、任意 App 广播、通知正文搜索/
embedding、免打扰/订阅/i18n 模板、删除/编辑/标回未读、跨 owner 共享、第七个进程、rootless
Podman 状态升级。

## 契约矩阵

### Notification kind / target / source（finite）

| kind                          | origin | target   | source process                    | 原子性                                             |
| ----------------------------- | ------ | -------- | --------------------------------- | -------------------------------------------------- |
| `agent.approval.required`     | system | approval | workos-core Agent                 | source tx hard requirement                         |
| `agent.task.terminal`         | system | task     | workos-core Agent                 | source tx hard requirement（首次 terminal）        |
| `artifact.review.created`     | system | artifact | workos-core Artifact materializer | source tx hard requirement                         |
| `reliability.incident.opened` | system | incident | reliability-host publication      | at-least-once durable（outage=degraded freshness） |
| `app.instance.message`        | app    | app      | runtime-host→Core private ingest  | grant+quota+idempotency 单事务                     |

### RPC 面

- public `workos.notification.v1.NotificationService`（Gateway allowlist，device session 强制）：
  `ListNotifications` / `GetNotification` / `MarkNotificationRead` / `MarkNotificationsRead` /
  `WatchNotificationEvents`（server stream，有界 lifetime + heartbeat，RESET_REQUIRED）。
- private `workos.notification.ingest.v1.AppNotificationIngestService`（Core，runtime-host 专用，
  不进 allowlist）：`CreateAppNotification`（grant revision 重验后 quota/idempotency/notification
  单事务）。
- private `workos.reliability.notification.v1.NotificationPublicationSourceService`（Reliability，
  Core consumer 专用）：`ClaimIncidentPublications` / `CompleteIncidentPublications`。
- bridge additive：`AppBridgeService.CreateNotification`（method 名）↔ grant 名
  `notifications.create`（集中映射）。

### Migration（forward-only）

- `029_core_notifications.sql`（owner：workos-core Notification）：`notifications`、
  `notification_changes`、`notification_owner_sequences`、`notification_source_receipts`、
  `notification_read_requests`、`notification_app_requests`、`notification_app_quotas`。
- `030_reliability_notification_publications.sql`（owner：reliability-host）：
  `workos_reliability.notification_publications`。

### 事件序列与 read state

- per-owner `notification_owner_sequences.last_sequence` 在插入事务内原子分配；
  `notification_changes (owner, sequence)` 严格递增，`created`/`read` 两类；
  `swept_through` 水位是 stream gap 权威（cursor < swept_through → `RESET_REQUIRED`）。
- read 单调：unread→read 幂等（idempotency key + first-response snapshot），no-op 不重复产生
  change；`MarkNotificationsRead` ≤100 条、单事务全有或全无。

### App 配额

- `notification_app_quotas (owner, installation)`：`burst_window_start/burst_count`（≤10/分钟）
  与 `utc_date/daily_count`（≤200/日）guarded UPDATE 原子预留；系统通知不占 App 配额；
  exhausted → `ResourceExhausted`，失败不消费 key。

## 失败矩阵（实现与测试对照）

| 场景                                     | 结果                                                               |
| ---------------------------------------- | ------------------------------------------------------------------ |
| 未认证/被 revoke device                  | `Unauthenticated`；长流有界终止（Gateway revalidate + stream TTL） |
| malformed UUID/enum/filter/cursor        | 存在性读取前 `InvalidArgument`                                     |
| foreign/missing notification/target      | 净化 `NotFound`，无存在性 oracle                                   |
| same idempotency key / different request | `Aborted`，first response 与配额不变                               |
| DB 临时不可用                            | `Unavailable`，无部分 source/read/quota commit                     |
| stored invariant/digest 损坏             | 净化 `Internal`，不静默修复                                        |
| old cursor 已被 sweep                    | `RESET_REQUIRED` + authoritative resync                            |
| duplicate/out-of-order stream change     | 客户端按 sequence/revision 幂等                                    |
| Reliability source 不可达                | incident freshness degraded；其他 source 可用                      |
| publication complete response 丢失       | source 重放，Core receipt no-op，仅一条通知                        |
| App missing/revoked grant/old epoch      | fail closed；Core 零副作用                                         |
| App quota exhausted                      | `ResourceExhausted`；不消费 key                                    |
| notification target 随后失效             | 固定 stale UI，可标 read                                           |
| browser Notification denied/unavailable  | durable center 正常                                                |

## 阶段计划与提交序列

0. 基线 + ADR + 任务记录（`docs: define local-first notification boundary`）
   A. Proto + Core notification domain/storage/public service（`feat: add durable notification service`）
   B. Core atomic producers（`feat: publish agent and artifact notifications atomically`）
   C. Reliability publication（`feat: bridge reliability incident notifications durably`）
   D. Gateway stream + client projection（`feat: add resumable paired-device notification stream`）
   E. Adaptive Notification Center（`feat: add adaptive notification center`）
   F. App notifications.create（`feat: allow quota-bound app notifications`）
   G. restart/fault/E2E/文档（`test: prove notification replay and restart convergence`、
   `docs: record local-first notification evidence`）

## 基线记录

- 基线 HEAD：`4e6e0b8`（本地 main，ahead origin/main 1）。branch：仅 main + 本 feature branch。
- `git status --short --branch`：干净。`git diff --check`：干净。
- `make bootstrap`：docker/compose 就绪。`make generate`：exit 0、工作树无生成差异。
- `make check`：PASS（exit 0）。
- `make test-integration`：PASS（exit 0）。`make test-e2e`：PASS（19 passed / 12 skipped）。

## 验收（完成记录）

- [x] ADR-0014 覆盖全部裁决；Proto additive（`workos.notification.v1` 公共 + 私有 ingest +
      私有 incident source）；migration `029`（Core Notification）/`030`（reliability-host
      publication）owner 注释；`make generate` 幂等（最终验证复跑）
- [x] Core notification 模块分层（domain/ports/application/adapters/transport）+ 真实 PostgreSQL
      集成测试：exactly-once projection + 并发收敛、digest drift fail closed、read 幂等/
      conflict/单调、分页满页无 phantom、sweep gap、并发序列唯一递增
      （`tests/integration/notification_test.go`）
- [x] 三类 system producer 与 source 同事务：terminal/approval/artifact 通知失败回滚 source
      （`TestTaskTerminalNotificationAtomicity`、
      `TestArtifactNotificationProjectsExactlyOnceAndFailsAtomically`）；orchestration 单测
      断言 replay 不重复投影
- [x] Reliability publication claim/complete at-least-once：lease 互斥、complete 丢失 replay
      receipt no-op、digest drift fail closed 且可收敛
      （`TestIncidentPublicationClaimApplyComplete`、`TestIncidentPublicationDigestDriftFailsClosed`）
- [x] Gateway：精确 allowlist（私有 ingest/source 404）+ identity 剥除注入 + watch 流 30s
      周期 session 重验 + Core 侧有界 lifetime/heartbeat/per-owner 并发预算 + RESET_REQUIRED
- [x] 双 Chromium context 实时到达、跨设备 read 收敛、断线补收、Gateway/Core 重启持久
      （`notification-center.spec.ts` 两阶段）
- [x] Notification Center 三布局（expanded bell+badge / medium Dock / compact 底部导航）+
      typed action + before/after/current + notes（`docs/ui/desktop-web/changes/
20260902-local-first-notifications/`）
- [x] App notifications.create：grant 协商/deny、same-key replay 精确、different-key 新事实、
      revoke 后 stale port `permission_denied` 零副作用、burst/day 配额原子、restart 后配额与
      幂等保持（`TestAppNotificationIngest*` + `app-notifications.spec.ts`）
- [x] 三专项门禁 PASS：`make test-notification-center`、`make test-incident-notifications`、
      `make test-app-notifications`
- [x] `make check`、`make test-integration`（含 notifications seed/verify restart battery）、
      `make test-e2e`、`make test-adaptive-shell`、`make test-lan-pairing` 通过（见命令结果汇总）
- [x] `docs/status.json`（新增 Notification working slice + Gateway/Desktop/Runtime/Reliability
      evidence 增量）、`docs/architecture/implementation.md`、README 生成区块同步

## 命令结果汇总（最终验证，feature branch HEAD 执行）

- `git diff --check`：干净。
- `make generate` 两次：第二次无 diff（`git diff --exit-code -- gen sdk/protocol/src/gen` 通过）。
- `make check`：PASS。
- `make test-integration`：PASS（含 restart battery notifications seed/verify）。
- `make test-e2e`：PASS（全部既有 spec + 新增三个通知 spec）。
- `make test-notification-center`：PASS（Go 集成 + 跨进程 incident gate + 双 context E2E +
  重启验证阶段）。
- `make test-incident-notifications`：PASS（明确注释:只证明软件链路,不证明 rootless supervisor）。
- `make test-app-notifications`：PASS（Go ingest 门禁 + opaque Web Bundle E2E）。
- `make test-adaptive-shell` / `make test-lan-pairing`：PASS。
- `buf lint` + 相对 main 的 `buf breaking`：PASS（纯 additive）。
- 视觉 PNG：7 张 after/current 全部尺寸核对（1440×900 / 820×1180 / 390×844）+ 人工检查。

## 交接

- Branch:`feat/v1-local-first-notifications`,基线 `4e6e0b8`,唯一 branch/worktree/写入者。
  提交序列:docs boundary → durable service → atomic producers → reliability bridge →
  resumable stream → adaptive center → quota-bound app notifications → (本收口提交)。
  未 merge、未 push。
- Proto/migration owner:`workos.notification.v1` 三文件(Core Notification);
  `029` owner workos-core Notification;`030` owner reliability-host。
- 未决风险与下一步:
  - 后台推送(APNs/FCM/Web Push/Service Worker)未实现,状态如实 unavailable;未来需要
    独立 ADR 与 relay 信任根。
  - 通知标题/正文模板为英文常量;i18n/用户偏好/免打扰未做(范围外)。
  - `make test-podman-fixture` blocker 未触碰,Reliability supervisor 判定不变。
  - watch 流 2 分钟有界 lifetime + 30s Gateway 重验是保守上限,可在真实设备规模下调优。
  - app 通知配额(10/分钟、200/日)为固定保守值,后续可配置化。
