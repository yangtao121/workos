# 当前实现架构

本文把 `docs/structure.md` 的产品愿景映射为可执行边界。愿景与实现冲突时必须先写 ADR，不能在
业务提交中顺手改变主线。

## 进程所有权

| 进程             | 当前所有权                                                                                                            | 不拥有                            |
| ---------------- | --------------------------------------------------------------------------------------------------------------------- | --------------------------------- |
| workos-gateway   | TLS、identity、capability、公开 API、静态 Shell                                                                       | Project/Harness 状态              |
| workos-core      | Project、Task Router、Event Backbone、Harness Catalog facade、binding orchestration、App Registry、Artifact contracts | Provider 进程、credential、cgroup |
| harness-host     | Broker、Provider Adapter、run execution                                                                               | Project 数据、公开 API            |
| runtime-host     | Workload、runner、Surface                                                                                             | Incident 决策、业务数据           |
| reliability-host | Supervisor、Incident、Repair/Deploy ports                                                                             | App 业务逻辑、Harness 路由        |
| indexer          | Archive/RAG/indexing                                                                                                  | 原始业务表写权限                  |

服务之间使用版本化 Connect API 与 durable event，不共享 internal package 或直接查询对方 schema。

## 请求与事件流

```text
Desktop → Gateway → Core: SubmitTask
Core transaction: task + outbox(agent.task.requested.v1)
Harness Host: claim event → Provider → append canonical events
Desktop → Gateway → Core: WatchTaskEvents(after_sequence)
```

断线不会取消任务；事件流可从持久化 sequence 恢复。取消命令是幂等状态转换，不依赖客户端连接。

Provider discovery 与 Project binding 使用独立边界：

```text
Desktop → Gateway → Core HarnessCatalogService
Core → harness-host private HarnessHostService.DescribeProviders

Desktop → Gateway → Core ProjectHarnessBindingService
Core: owner/revision check → Catalog health check → server preset → Project update
```

Gateway 使用 public service allowlist，不能把同时拥有 `ExecuteTask` / `CancelRun` 的 private
`HarnessHostService` 暴露给浏览器。Catalog 不缓存、不持久化瞬时 health，也不参与 Core readiness；
Task 只持久化 Submit 时解析的 stable provider ID。clear binding 不依赖 Catalog。

## App Registry

App Registry 把一个版本化 manifest 变成 Core 持有的不可变事实。链路固定为：

```text
Desktop → Gateway public AppRegistryService → Core
Core: 结构安全检查 → YAML→JSON 规范化 → canonical JSON bytes
  → canonical v1 JSON Schema 校验（仅嵌入资源，无网络）→ semantic policy
  → digest → Core-owned app_versions 事实 → Get/List 投影
```

- 唯一 Schema 事实源是 `schemas/workos-app-manifest-v1.schema.json`，经同目录 `schemas/embed.go`
  原地嵌入进程；YAML/Schema 适配在 `internal/core/appregistry/adapters/manifestvalidator`，
  domain 不接触 YAML、validator、pgx 或 Connect。
- digest 基于校验后 canonical JSON（key 排序、数字/bool/null 确定编码、permissions 集合排序），
  格式 `sha256:<hex>`；YAML whitespace/key order 等价 → 相同 digest。
- `(owner, app_id, version) → 唯一 manifest_digest` 由 `app_versions` 的 UNIQUE 约束保证；`app_versions`
  只保存 immutable manifest 事实。`(owner, idempotency_key) → 唯一注册` 由 `003` 迁移引入的
  `app_registration_requests`（主键 owner+key、复合外键绑定同 owner 的 version、backfill 自 002 数据）
  作为唯一幂等事实源保证。Register 在单事务内先按已消费 key 裁决（同请求重放、不同请求 `Aborted`），
  再由 version 唯一约束仲裁插入，最后原子消费 key：每条成功响应的 key 都已持久化，失败事务不遗留
  orphan version 或未消费 key；冲突映射为 `AlreadyExists` / `Aborted`。
- 请求消息在 Connect handler 构造层（`WithReadMaxBytes` 384 KiB）于 protobuf/JSON 解码前设置上限，
  覆盖 base64 膨胀与解压后内容（gzip bomb 拒绝为 `ResourceExhausted`）；application 层 256 KiB
  manifest 字段检查保留。idempotency key（UTF-8、无控制字符、≤128 rune）、app_id/cursor（canonical
  app-ID grammar）、project_id（UUID）在 application 边界校验，畸形输入为 `InvalidArgument`。
- current version 按 SemVer precedence 在 Go domain 比较（release 高于对应 prerelease、numeric
  identifier 按数值）；公开查询只选择 summary 列（不含 canonical_manifest），按 app ID 流式读取并用
  固定大小 accumulator 折叠 current，内存受 page size 上限约束、不随历史 version 数量线性增长。
  `GetApp` 空 version 返回 current，显式 version 返回该 immutable version；manifest 只在需要完整事实的
  内部路径读取，不进入日志或公共响应。
- `ListApps` page size 仅在 application 边界规范化一次（默认 50、上限 100、负数为 `InvalidArgument`），
  repository 以 effective limit + 1 探测下一页并返回明确 page result，transport 原样转发 application
  的 next token；恰好装满的最后一页不产生 token，翻页无重复、无遗漏。
- mapping key（UTF-8、C0/C1/NUL 控制字符、长度 1..256）在任何 pointer 构造、map 插入、Schema 校验或
  持久化之前校验，unsafe key 只报告父路径；key 本身形似 credential（prefixed token、JWT、AWS key ID、
  PEM header）由与 value 共用的单一 credential-shape 规则在结构阶段拒绝，同样只报告父路径；secret key
  policy 以 tokenization（snake/kebab/camelCase）匹配整词与复合词（accessToken、clientSecret、
  credentialValue、awsSecretAccessKey 等），不因字母片段误杀邻近字段。
- public 注册 fail closed：`scope=system` 与 `runtime.type=trusted` 拒绝；permissions 必须属于
  集中定义的 capability vocabulary；manifest 中 secret 形态的 key/value 按路径拒绝，且此检查
  不是 Credential Vault/DLP 替代品。permissions 只是 requested permissions。
- `ListApps(project_id)` 先经中立 orchestration port 验证 Project 属于 owner 且未归档，返回该
  owner 的 Registry catalog；它不是 installation state，`Project.installed_app_ids` 由未来的
  install 命令负责。App Registry 不查询 Project/Agent 表。
- 违规输出只含字段路径与规则说明（排序、去重、数量/长度上限），不含原始 YAML value；错误映射
  不回传 SQL、constraint、路径或 validator 内部信息。

## Project App Installation

Project App Installation 把 Registry 的一个 immutable version 变成 Project 持有的安装实例事实。
链路固定为：

```text
Desktop App Library → Gateway public AppInstallationService → Core
Core: identity → 幂等 key 裁决 → 中立 AppCatalog port 解析 current 或显式 version
  → 一个 Project-owned 事务：installation/tombstone + idempotency result
      + installed_app_ids 投影 + revision(+1) + project event(sequence=revision) + outbox
  → List/Get Project / ListInstalledApps（Core restart 后仍成立）
```

- 契约是 additive 的 `workos.app.v1.AppInstallationService`（`InstallApp`/`UninstallApp`/
  `ListInstalledApps`，`api/proto/workos/app/v1/installation.proto`）。installation ID 是持久
  app instance identity（未来 Surface 的 `app_instance_id`），不代表 workload 已运行；响应不含
  manifest、credential，也不声称 permissions 已授权。
- 数据由 `004_project_app_installations.sql` 与
  `005_project_app_installation_request_owner.sql`（owner 均为 workos-core Project Installation）持有：
  `project_app_installations` 是安装事实的唯一权威（UUIDv7 id、pinned version/digest、
  `uninstalled_at` NULL=active），partial unique `(project_id, app_id) WHERE uninstalled_at IS NULL`
  保证一个 Project/app 至多一个 active row，tombstone 保留历史；复合 FK
  `(project_id, owner_user_id) → projects` 把 owner 绑定下沉为数据库约束；不建立指向
  `app_versions` 的跨模块 FK，Project SQL 不 join Registry 表。
- `projects.installed_app_ids` 保持兼容投影（方案 1）：由 install/uninstall 事务在持有 project
  行锁时从 active installation 聚合（`array_agg(app_id ORDER BY app_id)`）并写入同一条
  revision UPDATE；普通 `UpdateProject` 不能接收或覆盖该列。
- 幂等权威是 `project_app_installation_requests`（PK `(owner_user_id, idempotency_key)`，
  install/uninstall 共用命名空间）：request digest 覆盖客户端 canonical 请求字段（command、
  project、app、请求 version、expected revision、installation id），不含时间戳或解析结果，
  因此空 version 安装的 replay 不会因 Registry current 变化而漂移；结果快照
  （installation id + project revision + result_uninstalled_at）使 replay 精确返回第一次响应，
  uninstall 在 tombstone 后仍可重放，失败请求不消费 key。`005` 以 composite FK
  `(owner_user_id, installation_id) → project_app_installations (owner_user_id, id)` 把每条结果
  映射绑定到同 owner 的 installation（引用 005 新增的 `UNIQUE (owner_user_id, id)`），数据库层
  拒绝跨 owner 结果映射；005 在改 schema 前以 fail-closed 检查拒绝携带既有错配的升级。
- 并发完全由数据库裁决：mutation 以 `SELECT … FOR UPDATE` 锁定 owner-scoped project 行后比较
  revision，与 `UpdateProject`/`ArchiveProject`/binding 的 guarded UPDATE 互斥；同 key 跨 project
  由 mapping PK 仲裁；同 project 同 expected revision 恰有一个 winner，loser `Aborted`。
  同 app 同 version 的确定 no-op 在锁内验证 revision 后原样返回，不新建 row、不增 revision、
  不发事件；不同 version 是统一 `AlreadyExists`，不隐式升级。
- 事件：`project.app.installed.v1` / `project.app.uninstalled.v1`，sequence = Project revision，
  payload 只含稳定 ID 与 pinned version/digest。未知/他人/归档 Project、未知 App/installation
  统一净化 `NotFound`；scope `system`/trusted fail closed。
- 模块边界：installation 属于 `internal/core/project`（domain/application/ports/postgres），
  application 只依赖中立 `AppCatalog` port，由 `internal/core/orchestration/app_catalog.go`
  包装 App Registry application service；Registry 不反向查询 installation 表；域内 app ID
  grammar / SemVer 语法 / digest / idempotency key 校验为本模块 domain 纯函数，不 import
  appregistry internal package。
- Desktop App Library（`apps/desktop-web/src/AppLibrary.tsx`）：为 active Project 列出 owner 的
  Registry catalog（标识 active installation 与 pinned version），Install/Remove 使用
  `crypto.randomUUID()` key 与当前 Project revision，成功后以服务端 revision + 重新读取的
  project/installation list 为准；revision conflict 时重新加载并提示，不自动重放；Project
  切换/卸载通过 key remount + generation guard 隔离。Desktop 现以 sessionStorage 记忆 reload
  前的 active Project（不在首页时通过 `GetProject` 取回）。

## Minimal Web Bundle Surface

Web Bundle Surface 把已安装实例变成真实可打开的 App 窗口。链路固定为：

```text
测试/开发者客户端 → Gateway public ArtifactService.CreateArtifact → Core
Core Artifact: identity → 有界 WebBundleContent 校验/规范化
  → canonical bundle digest（与提交顺序无关）→ immutable metadata/files + 持久幂等

RegisterApp（runtime.type=web-bundle + artifactId/artifactDigest）
  → 中立 ArtifactDirectory 验证 same-owner artifact + exact digest
  → immutable Registry version（digest 覆盖 descriptor）

Desktop App Library: Open → Gateway public SurfaceService → runtime-host
runtime-host: identity → 幂等裁决 → 私有 Core SurfaceLaunchResolverService
Core resolver: active installation → exact pinned version → manifest digest 一致
  → same-owner artifact + exact digest → 单个有界 asset
runtime-host: owner/device-bound session（TTL/Close/重启持久）
Gateway /surfaces/<session>/… → runtime-host → Core 每次 revalidate
Desktop: sandboxed iframe（仅 allow-scripts）内渲染
```

- Artifact（owner：workos-core Artifact，`internal/core/artifact`）只实现 `app.web-bundle.v1`
  subtype：显式 `repeated WebBundleFile` 上传（≤128 files、总 ≤2 MiB、单文件 ≤512 KiB、path
  ≤240 ASCII bytes，拒绝空/绝对/反斜杠/dot-segment/重复 slash/percent-encoding/重复与 case-fold
  collision），entrypoint 必须是 bundle 内 `.html`；media type 仅由服务端按受控扩展名表派生，
  未知/可执行类型拒绝；canonical digest 以长度前缀编码覆盖版本标识、entrypoint 与按 path 排序
  的 path+bytes，文件顺序无关、任何内容变化敏感。client 只能提交 title，server-owned 字段
  （id/project/type/media/content_ref/digest/时间/计数）非空即拒。`(owner, idempotency_key)`
  持久幂等（同 canonical request replay、不同 `Aborted`、失败不消费 key），metadata/files/
  idempotency 单事务；public Get/List 永不返回文件 bytes。数据由 `006_web_bundle_artifacts.sql`
  持有，无跨模块 FK。
- Manifest v1 additive launch descriptor：`runtime.type` 增加 `web-bundle`，`runtime.artifactId`
  （UUIDv7）/`runtime.artifactDigest`（sha256）由 Schema pattern + Go cross-field policy 双重校验
  （缺一拒绝、image/command/port 禁止、仅允许单一 web-bundle surface）；canonical digest 覆盖
  descriptor；legacy manifest 不受影响。Register 经 registry application 的中立 `ArtifactDirectory`
  port（orchestration 包装 Artifact service）验证 same-owner + exact digest；foreign/unknown/digest
  mismatch 统一净化 `NotFound`。缺 launch descriptor 的既有 App 安装后 `CreateSurface` 为
  `FailedPrecondition`，不回退固定页面。
- Core 私有 resolver（`workos.surface.v1.SurfaceLaunchResolverService`，不进 Gateway allowlist）
  每次从权威事实解析：`ResolveActiveInstallation`（active + 同 owner + 同 project + project 未
  归档，一条 SQL join 本模块表）→ exact pinned version 的 canonical manifest（`GetVersionManifest`，
  内部读取）→ manifest digest 与 installation snapshot 完全一致（漂移=净化 Internal）→
  `VerifyWebBundle`/`ReadVerifiedWebBundleAsset`（same-owner + exact digest + 单文件）。runtime-host
  不导入任何 `internal/core/*` package，不查询/FK Core 表。
- Surface Broker（owner：runtime-host，`internal/runtime/surface`，`007_surface_sessions.sql`）：
  session 为 owner/device-bound 事实（UUIDv7 id、canonical request digest、Core 返回的 immutable
  descriptor snapshot、`/surfaces/<id>/` 相对路径、UTC created/expires/closed）。canonical create
  digest 明确覆盖 Gateway 注入的可信 `device_id`（仅来自 identity context，绝不在 public request
  body），因此 `(owner, idempotency_key)` mapping 的裁决与 device 无关地权威：同 key 不同可信
  device 或任何 canonical 字段不同 → 稳定 `Aborted`；同 key/device/相同 request 即使已关闭/过期
  也精确 replay 第一次 snapshot。`CreateSurface` 幂等裁决先于 Core 解析（失败不消费 key；replay
  不自动复活）；preferred renderer 只接受 UNSPECIFIED（默认 web-bundle）与 WEB_BUNDLE，其余
  已声明值与未知 enum 数值在 transport 边界 `InvalidArgument`，不触 resolver、不消费 key；
  project/installation/session/cursor 标识按 canonical UUIDv7 校验（hyphen/lowercase/version 7/
  RFC variant），viewport 拒绝 NaN/±Inf 并把 0/-0 归一为唯一 unspecified 语义；TTL 经
  `WORKOS_SURFACE_SESSION_TTL`（默认 15m，启动校验 1m..24h）；`CloseSurface` owner/device-scoped
  幂等；expired/closed/foreign asset 统一 404。并发由 `(owner, idempotency_key)` mapping PK 在
  单事务内裁决（真实 PostgreSQL 双 pool 并发测试覆盖 same-key race、different-request 零
  orphan、Create/Close/asset barrier race 与事务中途回滚）。
- 错误分类（本切片各 port）：`internal/platform/dbtransient` 在 adapter 边界按 Go 错误类型与
  SQLSTATE class（08 连接/53 资源/57 运维介入/58 系统 I/O）区分暂时不可达与不变量破坏，不读
  constraint 名或错误文本。四个 postgres adapter（Artifact/Project/Registry/Runtime Surface）把
  transient 失败包装为各自 `ports.ErrStoreUnavailable`：Runtime Create/Close 返回净化
  `CodeUnavailable`、asset 返回净化 503（不再伪装"资源不存在"）；private Core resolver 的
  Project/Registry/Artifact store outage 返回净化 `CodeUnavailable`，runtime 转换为 public
  `Unavailable`/503；digest drift、descriptor corruption、未知错误继续净化 Internal；
  Gateway→Core/Runtime upstream failure 继续固定 503。
- 资产 HTTP 边界（runtime-host `/surfaces/`）：仅 GET/HEAD（405 + Allow），响应固定
  CSP（default-src 'none'、script-src 'self'、connect-src 'none'、frame-ancestors 'self'、
  worker-src 'none' 及服务端强制的 `sandbox allow-scripts`——绝不含 `allow-same-origin`/forms/
  popups/top-navigation/downloads/storage，使 HTML/SVG 等一切文档即使在 Desktop iframe 之外
  顶层打开也保持 opaque origin）、nosniff、no-referrer、no-store；MIME/ETag 来自服务端存储事实；
  路径仅接受未编码的安全相对 POSIX path（traversal/双重编码/反斜杠/dot-segment/重复 slash 均
  404）；对外只有 404/405/503 与固定短消息。每次 asset 请求都经 Core revalidate active
  installation，uninstall/archive 后立即 fail closed。
- Gateway 增加受控 Runtime upstream（`WORKOS_RUNTIME_URL`，启动 fail-fast 校验 absolute
  http(s)+非空 host，`gateway.New` proxy 构造器对 target 形态二次校验）：仅
  `workos.surface.v1.SurfaceService` 与 `/surfaces/` 前缀路由到 runtime-host，两条路径同一
  device-session gate，Director 删除客户端伪造的 identity headers 后写入 trusted owner/device；
  `WorkloadService`/private resolver/host RPC 继续 404；upstream 失败净化 503，不落入 Desktop SPA
  fallback。`SurfaceSession.url` 是同 origin 相对路径（仅 session UUID + asset path），session ID
  不是授权凭据。
- Desktop（`apps/desktop-web`）：window state 引入 discriminated `kind`（agent-center /
  app-surface）；App Library 对 installed app 显示 `Open`（`crypto.randomUUID()` key、当前
  project/installation、desktop device class、实际 viewport、`WEB_BUNDLE` renderer），成功后在
  Window Manager 内创建 App window，iframe `sandbox="allow-scripts"`（无 allow-same-origin/
  forms/popups/storage）+ `referrerPolicy="no-referrer"`，只使用返回的相对 URL；失败映射为
  净化提示（NotFound/FailedPrecondition/Unavailable），重复点击防抖，Project 切换/卸载/卸载组件
  使旧响应 inert 并 best-effort `CloseSurface`；整个 Desktop 组件卸载时对仍打开的 app surface
  session 逐个 best-effort `CloseSurface`（ref 维护 live 集合、后端幂等；页面 crash/断网由 TTL
  与逐请求 Core revalidation 兜底，unload RPC 不保证必达）；窗口关闭调用 `CloseSurface`。
  `bridge_token` 最初保持空；bridge 注入由后续的 Project-scoped App Agent Bridge 切片引入
  （见下节），iframe 隔离属性不变。App window 与 Agent Center window 并存渲染。

## Project-scoped App Agent Bridge

App Bridge 让已安装的不可信 Web Bundle App 在用户显式批准后调用 Project-scoped Agent 任务。
信任边界由 ADR-0002 固定；链路固定为：

```text
requested permission（manifest，永远只是请求）
  → Desktop 安装确认（逐项 checkbox、默认全不选、显式 version）
  → InstallApp 事务：installation + granted_permissions 排序快照（008 列）
  → CreateSurface：Core resolver 返回 grant snapshot
      → runtime 计算 effective capabilities = requested ∩ granted ∩ implemented
      → session 持久 token hash（010）+ capabilities；响应携带一次性 token（轮换语义）
  → 可信 Desktop 经 MessageChannel handshake 把受控 port 交给 iframe（token 不进 iframe）
  → iframe agent.run/stream → port 有界请求 → AppHost 加 token metadata
  → Gateway public AppBridgeService → runtime-host 验 token/session/device/capability
  → Core private AppAgentService：再次验证 active installation + grant + Project 未归档
      → 强制 target_scope = installation Project → Task Router provider snapshot
  → Fake/Harness Broker 执行 → 持久事件流 → provenance-bound watch 流回 iframe
```

- Grant 唯一事实源是安装级快照（owner：workos-core Project Installation，
  `project_app_installations.granted_permissions`，008 加列、默认空）：canonical 排序、无重复、
  严格 ⊆ pinned version requested set；duplicate/malformed → `InvalidArgument`，不在 requested
  set → 净化 `PermissionDenied`；同 version 同 grant 才 no-op，grant 变更只能 uninstall +
  reinstall。安装幂等 digest 版本化：空 grant 沿用旧 digest（历史 replay 兼容），非空 grant 使用
  `v2` digest（同 key 不同 grant 稳定 `Aborted`）。
- App task provenance（owner：workos-core Agent，009 新表
  `workos_core.agent_app_task_requests`）：PK `(owner, app_instance_id, client_key)` 命名空间化
  client key；request digest 覆盖有界输入（role ≤64 rune、goal 1..16 KiB）；task + mapping +
  outbox 单事务，same key/same digest 精确 replay 首次 provider snapshot，same key/different
  digest `Aborted`，两个 App 同 client key 互不冲突；composite FK 绑定同 owner 的 task；
  watch 需 provenance（owner + app_instance + project 一致），知道 task ID 不构成读取能力。
  task 行自身 key 列存无关唯一值（App 幂等由 mapping 承担）。
- bridge token（owner：runtime-host Surface，010 加列）：`crypto/rand` 256-bit、base64url；
  at rest 只有 sha256 hash（NULL = 无有效凭证），常量时间比较；绑定 session 行的
  owner/device/project/app_instance；有效期 = session expiry；每次成功 Create（fresh 或
  open replay）轮换并立即失效旧 token，closed/expired replay 不铸造，Close 原子清除；
  restart 后 token 继续有效（PostgreSQL 持久）。token 只出现在 CreateSurface response 与
  `X-WorkOS-Bridge-Token` metadata；Gateway 只向 runtime Connect 路由转发，Core 路由与
  `/surfaces/` asset 一律剥除；绝不进入 URL/cookie/storage/DOM/MessageChannel/日志。
- public `workos.bridge.v1.AppBridgeService`（runtime-host）body 只有有界输入
  （`agent.run`: key/role/goal；`agent.watch`: task id/after_sequence ≥ 0），owner/device/
  project/app_instance 全部从 Gateway identity、validated token、stored session 派生；
  Connect 读上限 32 KiB。private `workos.agent.v1.AppAgentService`（Core，不进 Gateway
  allowlist）每次调用重新 ResolveActiveInstallation + grant + 词汇表校验（漂移=净化
  Internal），强制 Project scope，canonical `AgentTaskInput` 不接受 iframe 的
  capabilities/output types/budget/parent/incident/global scope。
- effective capability 只包含 `agent.task.run`/`agent.event.watch`（当前唯一实现的两个方法）；
  其他 granted permission（artifact/project/knowledge）只是存储事实，绝不因已 grant 而 working。
- MessageChannel 协议（`@workos/surface-sdk` 单一定义）：版本 `workos.app-bridge/v1`；
  parent 每次 iframe load 关旧 port、生成一次性 nonce、向 exact `iframe.contentWindow` 发送
  versioned hello（`targetOrigin="*"`，安全来自 exact window 引用）并 transfer port；iframe
  SDK 只接受 `event.source === window.parent` + 正确版本 + 恰好一个 port，在 port 上回 ack
  nonce；parent 只接受一次正确 ack。单消息 ≤64 KiB、inflight ≤32、超时 15s；未 offer 的方法
  双侧 fail closed；`agent.stream` 提前结束发送 cancel，只取消本地/server stream，durable
  task 继续。业务 payload 复用 `@workos/protocol` 生成类型。
- Desktop：安装确认对话框显示 exact version 的 requested permissions（默认全不选），提交排序
  grant；已安装行显示 `Granted:` 摘要（空 grant 显示 none）；App window 显示
  bridge pending/ready/failed/unavailable 状态，failed 可重试握手；bridge token 只存于
  Desktop 的 ref（不进可序列化 window state/DOM）；Project 切换/关窗/卸载/iframe reload
  关闭旧 port 并使迟到 response inert，Agent task 本身 durable。

## 状态与失败

- liveness 表示进程事件循环存活，readiness 表示必需依赖可用。
- capability discovery 表示可选功能是否真实可用。
- 未实现能力返回明确的 Unimplemented；依赖暂时失效返回 Unavailable。
- 所有跨进程 consumer 按 at-least-once 处理，不假设 exactly-once。

## 工程守卫

- Go 架构测试拒绝 Domain 导入数据库、HTTP、生成协议或 adapter，并拒绝进程 internal 互相导入。
- TypeScript 架构检查验证 workspace 分层、依赖声明、边界逃逸和依赖环。
- Buf 负责 Proto lint/生成与 CI breaking check；README 状态表只从 `docs/status.json` 生成。
- 所有入站和内部 HTTP/Connect 调用支持 W3C trace propagation；只有配置 OTLP endpoint 时才启动
  exporter，因此本地运行没有隐含可观测性依赖。
