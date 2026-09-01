# 当前实现架构

本文把 `docs/structure.md` 的产品愿景映射为可执行边界。愿景与实现冲突时必须先写 ADR，不能在
业务提交中顺手改变主线。

## 进程所有权

| 进程             | 当前所有权                                                                                                                                             | 不拥有                     |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------- |
| workos-gateway   | TLS、identity/device auth、capability、公开 API、静态 Shell                                                                                            | Project/Harness 状态       |
| workos-core      | Project、Task Router、Event Backbone、Harness Catalog facade、binding orchestration、App Registry、Artifact contracts、Credential Vault（sealed 事实） | Provider 进程、cgroup      |
| harness-host     | Broker、Provider Adapter、run execution                                                                                                                | Project 数据、公开 API     |
| runtime-host     | Workload、runner、Surface                                                                                                                              | Incident 决策、业务数据    |
| reliability-host | Supervisor、Incident、Repair/Deploy ports                                                                                                              | App 业务逻辑、Harness 路由 |
| indexer          | Archive/RAG/indexing                                                                                                                                   | 原始业务表写权限           |

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

## ProjectService 基础契约

public `workos.project.v1.ProjectService`（Create/Get/List/Update/Archive）的基础公开契约由
application 边界与 Core-owned PostgreSQL 事务共同裁决（ADR-0004）：

```text
Desktop → Gateway public ProjectService → Core（identity → 有界解码 → 语义验证）
Create: canonical digest 裁决 → 单事务：project row + create-request mapping（首次响应快照）
        + project.created.v1 event + outbox → 净化响应
List: application 规范化 page size → repository limit+1 探测 → 明确 page result
```

- 请求在 Connect handler 构造层（`WithReadMaxBytes` 128 KiB）于解码前受限。上限由全部合法
  字段的最大值推导（16 个 workspace ref、1 KiB URI 等约 90 KiB 合法上界 + headroom），覆盖
  base64 膨胀与 gzip 解压后内容；超限为 `ResourceExhausted`，业务代码零执行。同一 mux 中其他
  handler 的独立上限不受影响。
- 输入验证拥有明确、文档化的上限：name（trim 后 1–120 code points）、icon（≤128）、workspace
  refs（≤16 个，id ≤128 / uri ≤1024 / logical_mount ≤128 code points，id 与非空 mount 唯一）、
  harness binding 引用（provider/policy ≤128、credential_ref ≤256，仍只是有界 opaque
  reference）。全部文本要求 valid UTF-8 并拒绝 C0/C1 控制字符；WorkspaceKind 拒绝
  UNSPECIFIED 与未知数值；Project ID 与 List cursor 使用同一 canonical UUIDv7 validator；
  expected_revision 必须为正；`clear_harness_binding` 与同时提供 binding、
  `replace_workspace_refs=false` 携带非空 refs 均为 `InvalidArgument`。畸形输入在任何
  存在性读取之前被拒绝。
- Create 幂等权威是 `workos_core.project_create_requests`（migration `013`，PK
  `(owner_user_id, idempotency_key)`，owner：workos-core Project）：canonical request digest
  覆盖 command 版本标记、规范化 name、icon、提交顺序的 workspace refs（含全部公开字段）与
  optional binding presence，不含 owner/时间/服务端 ID/数据库状态；`result` 列持久化版本化
  （`result_version=1`）的首次响应 Project 快照。same key/same digest 跨请求、跨进程、跨重启
  精确重放第一次响应（Project 后续 Update/Archive 不影响重放）；same key/different digest
  稳定 `Aborted`；失败事务不消费 key。并发完全由数据库裁决：`projects` 上保留的
  `UNIQUE (owner_user_id, idempotency_key)` 是插入物理仲裁，落败事务在锁内重读 mapping 后
  replay 或冲突；winner 的 project、mapping、event、outbox 单事务原子提交。
- legacy 兼容（诚实、fail closed）：013 之前的 project row 没有请求记录与首次响应快照，对其
  key 的 Create 重放统一 `Aborted`（与 digest conflict 同一净化消息，避免双消息存在性
  oracle）；迁移不伪造 digest、不从可变 row 伪造"首次结果"。
- 分页由 application 明确裁决：page size 仅在 application 规范化一次（默认 50、上限 100、
  负数为 `InvalidArgument`），repository 以 effective limit + 1 探测下一页，transport 原样
  转发 application 的 next token；恰好装满的最后一页不产生 token，翻页无重复、无遗漏。
- 错误矩阵（固定净化消息，`errors.Is` 判定，不泄漏 SQL/constraint/输入）：未认证 →
  `Unauthenticated`；malformed/越界/矛盾输入 → `InvalidArgument`；missing/foreign Project →
  `NotFound`；stale revision 与 idempotency conflict → `Aborted`；PostgreSQL 暂时不可用 →
  `Unavailable`（真实 pgx 断连由共享 `storeError`/dbtransient 分类，installation 与基础
  repository 共用同一实现）；invariant/损坏持久数据/未知错误 → `Internal`（project operation
  failed）。持久化 JSON 解码失败与 UUID 生成失败属于程序错误，永不映射为 Unavailable。
- Update/Archive 的 optimistic concurrency（owner + project + expected revision）、event
  sequence = Project revision、event+outbox 同事务、foreign Project 不泄漏存在性等既有语义
  不变；Archive 先做存在性读取，missing 与 stale revision 分别映射 `NotFound`/`Aborted`。

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
  install/uninstall/set-grants 共用命名空间，见下文 Mutable Project App Grants）：request digest
  覆盖客户端 canonical 请求字段（command、project、app、请求 version、expected revision、
  installation id），不含时间戳或解析结果，因此空 version 安装的 replay 不会因 Registry
  current 变化而漂移；结果快照（installation id + project revision + result_uninstalled_at +
  `011` 的 result grant/revision 快照）使 replay 精确返回第一次响应——grant 可变后，历史
  install/uninstall key 的重放返回第一次响应的 grant 事实而非后来被 Set 更新的行——uninstall
  在 tombstone 后仍可重放，失败请求不消费 key。`005` 以 composite FK
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

#### Project review artifacts（ADR-0008，migrations `021` / `022`）

`internal/core/artifact` 的第二个 immutable subtype 组：canonical `document.markdown.v1` 与
`code.unified-diff.v1`，由 Project Agent 在 active task lease 下经私有
`TaskExecutionService.AppendTaskArtifact` materialize。request 只含 lease/worker、`output_key`
（`^[a-z][a-z0-9._-]{0,63}$`）、title（trim 后 1–200 code points）与 typed content；owner、
project、task、artifact ID、digest、时间、事件 sequence 全部由 Core 在单事务内派生或 mint：

```text
harness-host worker（provider 经中立 ports.ArtifactSink 输出）
  → 私有 AppendTaskArtifact → orchestration.TaskArtifactMaterializer（单一共享事务）
     1. Agent port：按 lease 锁定 task stream（lease 失效/terminal fail closed）
     2. 校验 project scope（global task 拒绝）与 requested output type
     3. Artifact port：读 (task, output_key) 映射 → replay / 稳定冲突
     4. domain 准备：CRLF→LF、NUL/C0/C1 拒绝（允许 LF/TAB）、≤512 KiB/≤20k 行/
        单行 ≤16 KiB UTF-8、不 trim 不补尾换行、versioned digest、server-mint UUIDv7/时间
     5. Artifact port：artifact 行 + 裁决映射（(task,key) PK + (task,type) 唯一索引，
        ON CONFLICT DO NOTHING 物理仲裁；0 行 → 事务内重读分类）
     6. Agent port：Core-minted artifact_created 事件（sequence=last+1，仅推进序号）
  → WatchTaskEvents 只返回 Core-minted 引用；public GetReviewArtifact/ListArtifacts
    按 owner/project 读取 typed canonical content（Web Bundle 明确 not-reviewable）
```

- request digest（`workos.review-artifact-output.v1`）覆盖 project/task/output key/归一化
  title/content digest；same digest replay 首个 artifact 与首个 publication（映射行记录
  `event_id/sequence/occurred_at` 三个 Core-minted 引用，事件本身仍归 Agent stream 所有）；
  different digest 稳定冲突 → run fail closed；失败校验不消费 key。并发由 task 行
  FOR UPDATE 串行化 + 唯一索引兜底：无重复 artifact、无重复事件、无 orphan row。
- 已执行的 `021` 保持逐字节不变；forward-only `022` 在 Artifact 自有 artifact/mapping 表之间增加
  `(artifact, owner, project, task, output key, type)` composite FK、content byte-count 相等与 finite
  timestamp CHECK，不建立到 Project/Agent/event 表的跨模块 FK。Artifact 与 publication time 统一为
  UTC 微秒精度，使首次响应与 PostgreSQL restart replay 精确一致。
- generic `AppendTaskEvent` 对 `ArtifactCreated` 一律 `InvalidArgument`：timeline 引用只能由
  materializer 从已验证 artifact projection 构造，Provider 不能伪造 foreign ID/type。
- replay 还通过 Agent port 逐字段验证 mapping 指向的 durable Agent-owned event；缺失或 payload/
  identity/time 漂移均 fail closed 为 stored corruption，mapping 本身不替代 event authority。
- `HarnessCapabilities.supported_artifact_types`（additive，exact list）与
  `structured_artifacts` bool 必须一致：catalog 视漂移为 capability corruption（read 返回
  unavailable）；Task Router 在入队前按 resolved provider 的 exact list 校验请求
  （≤2、无重复、global scope 拒绝），unsupported → FailedPrecondition、零副作用、不 fallback。
  Fake adapter 声明两类并产出 deterministic bounded 输出（每请求 type 恰一个、terminal 前）；
  DeepSeek/Generic CLI 保持 false/empty 并拒绝非空请求。worker 在完成事件前校验全部请求
  type 已 materialize，缺失 → run 确定性失败。
- 公开错误矩阵固定：unknown/foreign/wrong-project → 统一 NotFound；stored corruption（每次
  读重验 grammar 并重算 digest）→ sanitized Internal；transient → Unavailable；Web Bundle
  的 review 读 → Unimplemented（bundle bytes 仍永不公开）。project list 经中立
  `ArtifactProjectScope` port 校验同 owner（归档 Project 保持可读）；owner-wide list 跨两个
  subtype 表 ordered union；分页 limit+1、满页无 phantom token。
- metadata Get/List 与 typed read 都在各自 authoritative SQL snapshot 中读取并重验 review content、
  digest/count/canonical bytes；公开 metadata 仍不泄露 content。任何已命中的 review row 都经中立
  Project port 重验 owner/project binding，持久事实漂移为 sanitized Internal。
- transport 在构造层分别限制 Create upload（4 MiB）、public get/list/review read（32 KiB）与 private
  AppendTaskArtifact（768 KiB），对 protobuf/JSON/gzip 在业务调用前 enforce，合法 512 KiB 内容有
  wire headroom。
- Desktop：dock ☰ "Open Artifact Center" 普通窗口（分页、loading/empty/unavailable/retry、
  key/remount 于 active Project、generation guard 隔离迟到响应）；timeline 的 artifact 事件
  为可点击按钮，点击仍走 ArtifactService；"Artifact Review" 只读窗口用受限 allowlist 渲染
  Markdown（heading/paragraph/emphasis/list/blockquote/inline+fenced code）与 unified diff
  （file/hunk header、增删行、context/meta 只做转义文本与样式分类），无 HTML parser、无
  `dangerouslySetInnerHTML`、无 image/active link/网络/存储路径，无 apply/edit/download。
  Viewer fatal-decode UTF-8，并重验 expected Project、artifact ID、type/oneof、canonical media type、
  byte count 和 512 KiB display bound；Project 切换会 abort 并 generation-invalidate 旧 task stream，
  迟到 timeline event 不能进入新 Project。
  真实链路门禁 `make test-artifact-review`（PostgreSQL + Core + harness-host + Gateway +
  Chromium）；`021_project_review_artifacts.sql` 与 `022_project_review_artifact_integrity.sql`
  checksum 已钉住，001–021 历史 migration 零修改。

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

## Adaptive Desktop / Mobile Shell（2026-08-31）

`@workos/adaptive-shell` 是 Desktop 与 `apps/mobile-shell` 共享的唯一设备布局契约：直接复用
Proto `DeviceClass`，纯 `resolveDeviceLayout` 从 viewport/orientation/DPR 与可选 window
segments 推导 Compact / Medium / Expanded / Fold-separated；DOM/Window Segments API 只存在于
React adapter。边界 599/600、1023/1024、异常 DPR、segment 反序/重叠/变化、横向或纵向 hinge
均由纯测试覆盖；没有 segment 时按宽度安全退化，不做 UA/品牌猜测。Fold-separated 按两个真实
segment 的宽高比例排布行或列，零宽 gap 也保持为零，hinge 无点击目标。

Desktop 继续持有唯一 Project/window/App/Agent/Artifact 状态：Compact 是单主内容 + 底部导航 +
Project sheet，Medium 是单主内容 + 用户打开的 Agent slide-over + 显式 Dock，Expanded 仍用原
window-manager 自由窗口，Fold-separated 只投影两个 pane。Project 切换仍使旧 Surface、Artifact、
context 与迟到响应 inert；uninstall、grant revision 或 pinned version 改变时统一关闭该
installation 的 Surface 并清理所有 device-class 引用。Project sweep 只在完整分页读取成功后执行，
初始空列表/服务不可达不被当作权威删除；确认 logout/current-device revoke 后清空该 browser
profile 的 shell 引用。

布局状态 owner 是 origin-scoped、versioned IndexedDB，key = `(device_class, project_id)`，只含
canonical UUIDv7 UI 引用、有界 recent/dock 列表、system-window allowlist、single/dual preference、
canonical UTC `updated_at` 与 revision。所有 mutation 在同一 readwrite transaction 上读取最新
record 后重放并在 commit 后才返回；读取遇到损坏只删除当前 key，sweep/prune 后重新 sanitize。
IndexedDB 打不开时使用同一个 session memory store（不是每次操作返回空状态），memory adapter
同样串行写入并 clone 数组，调用方无法绕过 revision 修改持久值。PWA 只声明 manifest/icon/
viewport-fit；HTML/manifest no-store、内容哈希 assets immutable，不缓存 API 响应。

门禁 `make test-adaptive-shell` 在真实 Gateway/Core/harness/runtime/reliability + Chromium 上覆盖
390×844、820×1180、1440×900 与注入双 segment；`@workos/adaptive-shell` 40 个测试和
desktop-web 110 个测试覆盖 store/layout/hook 与共享 UI 回归。真实 foldable hardware、Capacitor
iPad/Android wrapper、push/native secure storage 仍不在当前证据内。

## Gateway 设备配对与会话（ADR-0007）

生产设备身份属于 Gateway（migration `020`，`workos_gateway` schema，owner: workos-gateway）：
`device_credentials`（server-minted UUIDv7、canonical P-256 SPKI + SHA-256 digest、revision、
revocation）、`pairing_tickets`（domain-separated secret hash、public origin + TLS leaf
fingerprint snapshot、bounded attempts、每 owner 至多一个 pending）、`device_auth_challenges`
（32-byte nonce、显式 proof version/purpose、单次消费、bounded attempts）、`device_sessions`
（token hash、absolute expiry、
每 device 至多一个 active session）、`device_revocation_requests`（owner-scoped idempotency +
完整公开 Device 投影的不可变首次结果快照）。Gateway 不查询 Core/Runtime/Reliability 表；
`workos_core.devices`（migration
`001`）保持 foundation 开发脚手架原状，永不被 backfill 为生产凭据。

配对与会话链路固定为：

```text
operator workosctl device pair → Gateway admin Unix socket（0600，仅此一服务，TCP 确定性 404）
  → RotatePairingTicket：owner 锁内 revoke 旧 outstanding（pending/claimed）+ 插入新 ticket
  → QR URL：https://<origin>/pair#v=1&t=<43-char base64url>&fp=sha256:<leaf DER digest>
  → 浏览器：WebCrypto 生成 non-extractable P-256 private key（IndexedDB only），回读后做 key-pair 自检
  → BeginPairing（claim pending ticket，绑定 key digest/name/class，返回 version 1 + pairing purpose）
  → CompletePairing：版本化 proof transcript（domain separator + purpose byte +
    uint32-BE-length 字段：origin/purpose/challenge/nonce/device/key digest/ticket/fingerprint）
    上 64-byte raw r||s ECDSA-P-256/SHA-256 签名，单事务创建 credential + session
  → __Host-workos_session（HttpOnly/Secure/SameSite=Strict/Path=/，库内仅存 sha256）
  → 每个受保护请求：Cookie hash → active session + active credential + owner（无进程内缓存）
  → Director 剥除客户端 identity/Cookie/bridge token 后注入 session 派生的 owner/device
```

安全边界（ transport 用 `errors.Is` 映射固定错误矩阵，不读数据库文本）：Host 精确匹配
configured public origin；unsafe 方法要求 exact `Origin` 且拒绝 cross-site Fetch-Metadata
（SameSite=Strict 只是纵深防御）；auth 端点解码前 16 KiB、admin socket 4 KiB；auth 端点同时叠加
不信任 `X-Forwarded-For` 的 bounded RemoteAddr limiter（map 有容量与淘汰上限）和 process-global
limiter（地址轮换不能绕过），对象级 attempt 预算持久化于 DB；store 故障是净化 503（绝不伪装
401，也不回退 configured identity），而 stored UUID/state/result/timestamp/canonical SPKI/hash 损坏在
adapter 出口 fail closed 为净化 500；
unknown/revoked/wrong-proof 响应外形一致，不产生 device 存在性 oracle（unknown 设备的 session
challenge 是持久化 decoy）；撤销用 owner-scoped idempotency + expected revision，同 key 不同
请求稳定 `Aborted`；revoke + 全部 session + 快照同事务，重放严格保留 id/name/class/revision/
created/last-authenticated/revoked 时间并复核 request binding；撤销的 `revoked_at` 用数据库事务时间。
ticket snapshot 在证书/origin 变更后立即失效；已配对设备的 session proof 不钉死旧 fingerprint。
`last_seen_at` 由有界门槛 guarded UPDATE 维护，不参与授权、不延长 absolute expiry。测试向量
Go/TS 共用同一 SHA-256（`internal/gateway/auth/domain/proof.go` 与
`clients/device-auth/src/transcript.ts`）；wire challenge 明确携带 version 1 + purpose，浏览器在签名前
核对，并在每次 IndexedDB 载入后用 digest + 实际 sign/verify 证明 non-extractable private key 与 SPKI
属于同一 key pair。admin socket 只位于当前用户所有、group/other 不可写的 runtime directory；仅
`ECONNREFUSED` 且 inode 未变可回收，运行期失败使整个 Gateway 非零退出。验收门禁
`make test-lan-pairing`：临时 TLS leaf +
admin socket ticket + 真实 Chromium（pair → HttpOnly Cookie → Core 动态身份业务写 →
`/surfaces/` 同一 gate 匿名 401 → gateway restart 会话存活 → 清 Cookie 后 IndexedDB proof
重认证 → 撤销后 fail closed），临时证书/profile 目录 exit 清理。App/Surface/Bridge 的隔离
（opaque origin iframe、CSP `connect-src 'none'`、bridge token 边界）不变；App 无权访问
DeviceService、Cookie 或 IndexedDB 凭据。

## Project-scoped App Agent Bridge

App Bridge 让已安装的不可信 Web Bundle App 在用户显式批准后调用 Project-scoped Agent 任务。
信任边界由 ADR-0002 固定（其中 §3 的“installation grant 在安装生命周期内不可变”自 ADR-0003
起被局部取代，其余边界不变）；链路固定为：

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

- Grant 唯一事实源是安装级事实（owner：workos-core Project Installation，
  `project_app_installations.granted_permissions`，008 加列、默认空）：canonical 排序、无重复、
  严格 ⊆ pinned version requested set；duplicate/malformed → `InvalidArgument`，不在 requested
  set → 净化 `PermissionDenied`。grant 集合自 ADR-0003 起可经 `SetAppGrants` 全量替换
  （见下节），不再只能 uninstall + reinstall；同 version 同 grant 的重装仍是确定 no-op。
  安装幂等 digest 版本化：空 grant 沿用旧 digest（历史 replay 兼容），非空 grant 使用
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
  grant；已安装行显示 `Granted:` 摘要与 grant revision（空 grant 显示 none）及
  `Manage permissions` 入口（见下节）；App window 显示
  bridge pending/ready/failed/unavailable 状态，failed 可重试握手；bridge token 只存于
  Desktop 的 ref（不进可序列化 window state/DOM）；Project 切换/关窗/卸载/iframe reload
  关闭旧 port 并使迟到 response inert，Agent task 本身 durable。

## App Version Transition 与 Owner 触发的 Rollback（ADR-0012，2026-08-31）

App Registry 的 immutable SemVer 版本之上，Project Installation 增加 owner 明确触发的
版本切换与"上一 pinned 版本"回滚。additive RPC 挂在现有 public
`AppInstallationService`（Gateway allowlist 自动覆盖，identity 注入）：

```text
Desktop Versions 对话框 / System Monitor eligible Incident
  → Gateway public AppInstallationService.TransitionAppVersion / RollbackAppVersion
Core: identity → 幂等 key 裁决（共用 installation request 命名空间）
  → 中立 AppCatalog 解析 exact 目标 version → pinned identity/scope 重验
  → 当前 grants ⊆ 目标 requested（否则 FailedPrecondition "permissions need review"）
  → 单事务：installation version/digest + history 追加（含裁剪）
      + Project revision(+1) + project.app.version.updated.v1 + outbox + 首响应快照
```

- `TransitionAppVersion` 请求只有 key/project/installation/expected revision/目标
  version；digest 由 Core 从 Registry 重解析，客户端不能提交 digest/image/container。
  同 version 同 digest 是确定性 no-op（消费 key、不动 revision/历史）。
- `RollbackAppVersion` 无目标字段：Core 在锁内从 durable history 重新推导"最近一个与
  当前 (version, digest) 不同的快照"并经 Catalog 重验逐字一致；无 previous snapshot →
  FailedPrecondition 零副作用。Application 层先推导一次用于验证 + 锁内重推导比对，
  漂移为稳定 `Aborted`。
- 幂等沿用 `project_app_installation_requests`；migration `025` 增加
  `result_version`/`result_manifest_digest` 快照列（从 owner-bound installation
  fail-closed 回填 + NOT NULL），same key/same request 精确重放第一次响应（含版本
  事实），different request 稳定 `Aborted`，失败不消费 key。
- 版本历史：`workos_core.project_app_installation_versions`（append-only，owner：
  workos-core Project Installation），`(installation_id, sequence)` 主键、复合 FK
  CASCADE 绑定同 owner installation，source ∈ install/transition/rollback；每个
  installation 有界保留最近 20 条（条目不可更新，同事务删除最旧条目）。每次读取重验
  version/digest/source、canonical UUIDv7、UTC 微秒时间、严格递增序列，并要求最新保留
  snapshot 与 installation 当前 pinned identity 一致；损坏为净化 Internal。安装事务写入
  install origin 快照。
- 所有 installation 数据库投影与幂等首响应行在 adapter 出口重验 canonical UUIDv7、
  version/digest、canonical sorted grants、正 grant/revision、UTC 微秒时间及 tombstone
  顺序；首响应 snapshot 覆盖到 installation 后在 application 边界再次整体重验，跨行
  组合损坏也不能作为重放结果泄漏。application 写入时间先规范为 PostgreSQL 微秒精度，
  首次响应与跨重启重放的时间事实逐字一致。
- 权限绝不扩大：目标 requested 集合不完全覆盖当前 grants（锁内重验）→
  `FailedPrecondition`，要求显式 `SetAppGrants` 重新确认；rollback 同样不恢复更宽的
  历史 grant。
- 事件 `project.app.version.updated.v1` payload 只含稳定 ID/fromVersion/toVersion/
  manifestDigest/source；Surface 失效依赖既有的每请求 Core revalidation（pinned
  digest 漂移立即 404），Desktop 对确认的版本变更 best-effort 关闭该 installation 窗口。
- Reliability 不读取/复制该历史；System Monitor 的 rollback 入口由 Desktop 组合
  public Incident 读 + public `ListAppVersionHistory`（eligibility 预览，服务端命令
  仍全量裁决）；Reliability 不可达只降级 incident 列表。
- Desktop：App Library 已安装行新增 `Versions` 对话框（历史、Switch version 显式
  consent、Roll back to <prev> 预览）；System Monitor 对绑定同 Project/app instance、
  非 resolved 且历史存在 previous snapshot 的 incident 显示可执行 rollback，固定文案
  区分 completed / no previous / permissions need review / conflict / Core 不可达，
  且明确"Core 切换成功 ≠ App 已健康"。命令成功后先采用 Core 首响应中的 installation +
  Project revision 并立即关闭/清理旧 Surface 引用，再 best-effort 刷新列表，因此刷新失败或
  慢响应不会让 UI 留在旧 revision；无 previous history 是一次有缓存的权威 verdict，不会
  render-loop 重复读取。门禁 `make test-app-version-rollback`
  （PostgreSQL + Core + Gateway + Chromium：两个 immutable web-bundle 版本注册、
  App Library consent 安装、UI transition、旧 Surface 失效、新 Surface 可开、UI
  rollback（System Monitor 的 Incident read 使用确定性 browser fixture，history/命令/
  revision/Surface 链路仍真实）、API exact replay、stale revision Aborted、unknown version NotFound、
  重开 Surface 呈现回滚后内容），集成套件覆盖 grant 扩张 fail closed、有界历史、
  分页与 restart battery（transition/rollback 事实与两次 exact replay 跨进程重启
  成立）。container 链路的同命令语义待 rootless acceptance host 复验；自动
  canary/repair/deployment controller 仍 unavailable。

## Mutable Project App Grants

ADR-0003 让用户在不卸载 App 的前提下显式替换一个 installation 的 grant 集合（局部替代
ADR-0002 §3 的“安装生命周期内不可变”；ADR-0002 的 iframe 边界、bridge token、provenance、
每次调用二次授权与 Gateway 信任边界全部不变）。链路固定为：

```text
Desktop Manage permissions → Gateway public AppInstallationService.SetAppGrants → Core
Core: identity → 幂等 key 裁决（install/uninstall/set-grants 共用命名空间）
  → 中立 AppCatalog 解析 exact pinned version requested set（服务端重解析，不信客户端）
  → 一个 Project-owned 事务：installation grant + grant_revision(+1) + idempotency result
      + Project revision(+1) + project.app.grants.updated.v1(sequence=revision) + outbox
CreateSurface：Core resolver 返回 grant snapshot + grant_revision
  → runtime 把 revision 持久化进 Surface session（012，backfill 1）
  → 私有 AppAgentService Run/Watch 携带 session 派生 revision
Core 每次授权（每次 run、每个 watch polling round）重新解析 active installation
  → current grant_revision 必须与 session revision 完全相等，再校验整个 current grant
```

- `SetAppGrants` 是 full replacement：`granted_permissions` 表达用户想要的完整最终集合，
  空数组/省略明确表示撤销全部，绝不回退为 manifest requested permissions；输入 canonical
  排序、去重、校验 grammar，目标集合必须是 exact pinned version requested permissions 的
  子集。客户端不能提交 app ID/version/manifest digest/requested set/grant revision/新 Project
  revision——全部由 Core 在事务内重新解析与裁决。
- 幂等沿用 `project_app_installation_requests (owner_user_id, idempotency_key)` 共用命名空间；
  Set 请求的 canonical digest 覆盖 command 版本标记、project、installation、expected
  revision 与 canonical 排序目标集合，不含时间/随机 ID/服务端解析结果。same key/same digest
  精确 replay 第一次响应；same key/different digest 稳定 `Aborted`（含跨命令、跨 project
  复用）；失败请求不消费 key。结果快照持久化 grant/revision
  （`result_granted_permissions`、`result_grant_revision`），使 grant 可变后历史 key 的重放
  返回第一次响应的事实。
- 真实变更（集合改变）：grant revision +1 且 Project revision +1，installation 更新、
  Project revision、project event、outbox、idempotency result 在同一事务提交；事件
  `project.app.grants.updated.v1` payload 只含稳定非敏感事实（projectId/revision/
  installationId/appId/version/manifestDigest/grantRevision/完整 canonical grantedPermissions）。
  same-set no-op：两个 revision、event、outbox、updated timestamp 均不变，但成功请求的
  idempotency key 仍被持久消费并可精确重放。
- Project revision 与 installation grant revision 是两个独立事实：前者是 Project 聚合的
  optimistic concurrency 基准，后者是单个 installation 的授权 epoch（从 1 起，仅在集合真实
  改变时恰好 +1）。grant mutation 与其他 Project mutation 由同一 Project row lock/guard 按
  `expected_project_revision` 串行化，数据库裁决：同 revision 恰一个 winner、loser `Aborted`；
  Set 与 Uninstall 竞争同一 revision 时同样只有一个能落库。
- 错误映射：malformed/duplicate/越界输入 `InvalidArgument`；非 pinned requested 子集 → 净化
  `PermissionDenied`；未知/他人/归档 Project、未知/foreign/uninstalled installation → 净化
  `NotFound`；stale expected revision → `Aborted`；stored grant/revision/digest invariant
  漂移视为 corruption → 净化 `Internal`，不静默修复。错误文本为固定短消息，不泄露 SQL、
  constraint、current revision 或 current grants。
- 任何真实 grant 变更使旧 Surface 的全部 bridge 方法失效：effective capability 是 Surface
  session 创建时的快照；grant revision 变化（无论 capability 是否在新旧集合共有）后，所有旧
  session 的 App Bridge 方法一律失败。失效的是 bridge 方法而非静态资产——installation 仍
  active 时 Web Bundle 资产照常服务，iframe 可继续渲染，但每次 bridge 调用在 Core 的 revision
  比对处失败（public bridge 层净化 `PermissionDenied`）。旧 CreateSurface key 的重放在
  revision 不一致时 fail closed（净化 `FailedPrecondition`），不铸造绑定旧 epoch 的新 token。
- 线性化点是 Core Project transaction commit：commit 之后进入 Core authorization read 的新
  run/watch 必须失败；已在 commit 前通过授权的并发请求可能完成。已打开的 watch stream 在
  Core 下一次 polling reauthorization（既有 200ms 轮询）发现 epoch mismatch 时终止，不再向
  旧 epoch 流送事件；撤权不是 CancelTask，既有 durable Agent task 不被隐式取消。
- runtime 与 core 之间只传 session 派生的 revision：Core `ResolveWebBundle` 返回 authoritative
  `grant_revision`，runtime `CreateSurface` 持久化进 Surface session，私有 AppAgent Run/Watch
  请求携带该字段；它只能由 runtime 的 validated session 派生，public App Bridge body、
  MessageChannel envelope 与 iframe SDK 不增加该字段。runtime 不查询 Core schema，Core 不查询
  runtime schema，无跨 schema FK——撤销完全由私有 RPC 上的 revision 相等比对实现。
- runtime/desktop 职责是 best-effort：Desktop 在本地保存成功后 best-effort 关闭该 installation
  的 open window/MessagePort 并 `CloseSurface`；服务端安全不依赖该客户端动作（旧 token 在
  Core 每轮比对处失败）。Manage permissions 对话框以 exact pinned version 的 requested set
  为上限渲染 checkbox、以 current grant 为初值（绝非默认全选），Save 提交排序后的完整替换
  集合；revision conflict 时重新加载 fresh facts 并要求用户重新确认，不重放旧选择；对话框
  校验 registry 返回的 app id/version/manifest digest 与 installation pinned 事实完全一致，
  任何漂移 fail closed 不可编辑。已安装行显示 `Granted:` 摘要与 grant revision；App 不在
  catalog 中无法解析 pinned requested set 时明确显示 Manage permissions unavailable。
- 数据：migration `011_mutable_project_app_grants.sql`（owner：workos-core Project Installation）
  增加 `project_app_installations.grant_revision`（backfill 1）、扩展 request `command` 约束
  至 `set-grants`、增加 `result_granted_permissions`/`result_grant_revision` 快照列并从
  owner-bound installation fail-closed 回填历史 mapping；migration
  `012_surface_grant_revision.sql`（owner：runtime-host Surface）为 session 增加
  `installation_grant_revision` 快照列（backfill 1）。001–010 逐字节不变，由 checksum 集成
  测试钉住。

## App Agent 预算策略、运行前审批与配额

ADR-0005 让 Agent 域为每个 active installation 持有执行策略：grant 回答
"能否调用 bridge method"，policy 回答"新任务怎么跑、最多花多少"。链路固定为：

```text
Desktop App Library → Gateway public AgentAppPolicyService → Core Agent
Core: identity → 有界 spec 验证 → 中立 InstallationSource 重验 installation 活性
  → Agent 事务：consumed-key replay/conflict 裁决 → expected_policy_revision 乐观并发
      → policy upsert（真实变化 revision+1）+ 原子失效全部 pending approval（其
        waiting task 终止为 cancelled + approval_expired 事件）+ 首响应快照

App bridge run（fresh key）固定裁决序：
replay（client digest 仍只覆盖 role/goal）→ grant/epoch 授权（既有）
  → effective policy（explicit row 或版本化 system default）→ provider snapshot
  → provider capability（hard_token_budget + hard_runtime_deadline + usage_reporting
    全部显式支持，缺失为净化 FailedPrecondition）
  → execution_mode：
      allow            → 单事务：task(policy/budget 快照) + provenance + guarded
                          daily reservation + claimable outbox
      require_approval → 单事务：waiting task + provenance + pending approval
                          （完整 policy spec 快照）+ approval_required 事件；
                          不建 outbox、不占额度
      block            → 净化 PermissionDenied，零副作用，不消费 run key
      quota 不足       → 净化 ResourceExhausted，事务回滚，key 未消费

Owner decide（public AgentApprovalService，Gateway identity）：
重验 installation/grant/policy revision/provider capability（任何漂移 fail closed
FailedPrecondition 且保持 pending）→ 单事务：decision idempotency replay/conflict
→ guarded reservation（approve）→ waiting→queued + approval_decided 事件 + outbox
（approve）或 task cancelled + approval_decided + run_cancelled（reject）

worker 续租观察 cancellation_requested → 确定性取消；usage_recorded 事件与
task event 同事务累计 agent_task_usage 与 bucket usage projection；reported
output 超过任务 reservation → bucket breach + 确定性 cancellation_requested，
breached bucket 的后续 fresh run fail closed（ResourceExhausted）
```

- 事实 owner 全部在 `workos-core Agent`（migration `014`）：`agent_app_policies`
  （explicit 策略行 + revision epoch）、`agent_app_policy_requests`（owner-scoped
  幂等 + 版本化首响应快照，ADR-0004 模式）、`agent_app_approvals`（含完整 policy
  spec 快照与 decision 幂等键/digest）、`agent_app_daily_reservations`（UTC 日
  bucket，guarded UPDATE 防超卖）、`agent_app_daily_usage`（观测投影，breach
  标记）、`agent_task_usage`（任务级累计）；`agent_tasks` 增加 additive、NULLable
  的 policy/budget 快照列（014 之前的任务保持 NULL，不伪造历史）。跨模块只存
  ID 快照，无跨模块 FK/SQL；installation 活性每次经中立 port 重验。
- 无 explicit row 时使用版本化有限 system default v1（allow、4096 token/任务、
  120s、50 任务/日、204800 token/日）；零/负数/超界/未知 enum 一律
  `InvalidArgument`，不存在 unlimited 语义。policy 变化不隐式取消已
  queued/running/terminal 任务，不改 Project revision/grant revision/Surface。
- 三个 public service（`AgentAppPolicyService` / `AgentApprovalService` /
  `AgentAppUsageService`）进入 Gateway allowlist，全部 identity 保护的
  owner-scoped 读/决策；private `AppAgentService` 仍不公开。App Bridge request、
  runtime→Core request 与 MessageChannel envelope 不新增 budget/policy 字段；
  `AgentTaskInput.budget` 只由 Core 从 effective policy 注入。
- Harness 侧 `HarnessCapabilities` 增加 additive `hard_token_budget` 与
  `hard_runtime_deadline`：Fake/DeepSeek 经测试证明后声明 true（Fake 的确定性
  token cap 截断 + ctx deadline；DeepSeek 的 provider-side max_tokens cap 与
  进程级 runtime deadline）；Generic CLI 如实 false，其上的 App run 入队前被
  拒绝（FailedPrecondition），不 fallback。worker 用 server-derived
  `max_runtime_seconds` 建立独立 deadline：即使 adapter 忽略 context，worker
  也取消 run、合成恰好一个 terminal 事件并正确结束 lease。
- 明确未实现并如实报告：provider/tool-call 中途审批（未来 Harness `approve()`
  协议）、vendor-neutral 金额硬上限（`max_cost_decimal` 只作可选已验证观测，
  无版本化定价源前不做 enforcement）、per-stream 中途 token 切断（当前 adapter
  usage 为 run 末聚合，circuit break 依赖 usage 事件 + worker 续租检查点）。

## 受监督的 Rootless Web Service Workload（ADR-0006，2026-08-29）

container manifest profile 让一个 Project 中已安装、digest-pinned 的 Personal Web App 在
runtime-host 以 rootless Podman + cgroup v2 hard limits 启动，通过只读 Web Service Surface 在
Desktop 窗口内渲染；reliability-host 观测真实 Workload 并执行有限 restart/stop。事实 owner：

```text
canonical container manifest + immutable version   → workos-core App Registry
active installation + pinned digest + grants       → workos-core Project Installation
resolved neutral launch descriptor（oneof）        → Core orchestration 只读投影
effective resource/health policy（clamp）          → runtime-host Workload Manager
Workload identity/generation/container/cgroup      → runtime-host Workload Manager
Surface session/token/grant epoch                  → runtime-host Surface Broker
neutral observation（bounded numeric facts）       → runtime-host 只读输出
supervision 决策/Incident/action ledger            → reliability-host
```

- **Container manifest profile**：`runtime.type=container` 要求严格
  `name@sha256:<64 lowercase hex>`（单 `@`、无 tag、无 credential、可选 registry 端口）、bounded
  argv（1..16 项、每项 1..4096 且无控制字符）、container port；`resources`
  （cpuHard 0.1..4、memoryHighMb 16..1024 ≤ memoryMaxMb 32..2048、pidsMax 8..512，整数 grammar）
  与 `health`（httpPath、startupSeconds 1..120、restartLimit 0..8）对 container 是 strict shape
  （未知字段 fail closed）；恰好一个 `web-service` surface、route 固定 `/`；web-bundle artifact
  字段混入即拒。Schema（allOf/if-then）+ Go `containerPolicy` 双重校验；canonical digest 覆盖全部
  container/resource/health 字段（键序无关、任何 policy/argv/image 变化敏感）。Registry 只做
  syntax/security policy，不访问 engine、不检查本机 image。
- **Core 私有 `ResolveSurfaceLaunch`**（additive RPC，oneof
  `web_bundle | web_service_container`，仍不进 Gateway allowlist）：active installation → exact
  pinned version → installation digest 一致（漂移=净化 Internal）→ web-bundle 侧维持 artifact
  verify；container 侧返回中立 immutable facts（image/argv/port/requested policy/route），永不
  返回 engine flags、host endpoint、container ID 或 effective policy。stored manifest 不满足自身
  profile = `FailedPrecondition`（不静默补默认）。
- **Runtime Workload Manager**（owner：runtime-host，`internal/runtime/workload`，
  migrations `015`/`018`）：durable `workos_runtime.workloads`（owner/project/instance/app/digest、
  requested vs effective policy、generation、state 机 pending/starting/running/stopping/stopped/
  failed、restart_count、engine container identity、loopback endpoint、engine-inspected cgroup
  path、health/exit 分类、lease）+ `workload_operations`（ensure/restart/terminate 的 durable
  idempotency：副作用前持久化目标 generation，同 key+同 canonical digest 精确 replay、不同命令
  稳定 Aborted；transient 失败可重驱，deterministic permanent 失败精确重放而不重复烧 generation；
  terminal verdict 不可被迟到写降级）。starting→running/failed 与 operation terminal verdict、
  stopping→stopped 与 terminate verdict 各自在同一数据库事务提交；restart 只接受相邻的
  `generation+1/restart_count+1`，模糊提交重放不会越代或把 failed generation 伪造成成功。
  一个 owner+instance 至多一个 active Workload（partial unique index）。crash-window 协议：DB
  reserve → engine create/start（deterministic name `workos-wl-<id>` + 完整 WorkOS labels）→
  inspect/cgroup/health 验证 → persist；create/start 后还会再次 inspect 完整 identity 与 immutable/
  security profile，engine 接受 argv 却放宽安全配置时精确删除本次 create 返回的 ID，绝不先落
  running。启动 reconcile + 周期 reconcile（lease 线性化）重驱中断
  原 operation key、失败 exited workload 并清理 exact orphan；stop/restart 只有在 exact ID +
  完整 WorkOS labels 验真且删除后复查确实不存在，才落 stopped/推进 generation；restart/adoption
  额外要求 image/argv/security profile 全匹配，停止路径则允许精确删除已证实归属但 profile 漂移的
  对象，避免安全放宽把 live container 永久卡在 stopping；identity/旧 generation 不匹配的对象仍
  永不接管或删除。engine unavailable 时保持
  stopping/原 generation，由 reconcile 继续收敛，不伪造成功。
  Core 重验：definitive NotFound → 立即 stop（uninstalled）；transient Unavailable 超出
  bounded grace → fail-safe stop；未知/非法 verifier verdict 同样按不确定性计入 grace，不会无限
  绕过 fail-safe；idle TTL 由 migration `018` 的 durable `idle_since` 锚定当前
  no-surface interval（与 lifecycle `updated_at` 解耦）→ 确定性 stop。effective policy
  由 server-owned maxima（PolicyVersion v1）clamp 请求；启动时回读真实 cgroup 值核对，未生效即
  fail closed。
- **Rootless Podman adapter**（`adapters/podman`）：全部经 argv 直呼（绝对可执行、deadline、
  bounded output，无 shell、无 raw stderr 外泄）；启动 probe（有界 `podman info --format json`）
  要求 rootless=true + cgroup v2 + delegated subtree + `--internal` WorkOS 网络，任一缺失如实
  unavailable，绝不 fallback Docker/rootful/裸进程；`--pull=never` + exact digest ref（本地缺
  image=FailedPrecondition，不访问 registry）；manifest 的完整 argv 以 JSON exec-form
  `--entrypoint` 覆盖镜像 ENTRYPOINT/CMD，inspect 时由实际 Entrypoint+Cmd 重建并逐项核对；
  `--restart=no`（重启权只属 Reliability）、
  read-only rootfs、有界 noexec tmpfs、`--cap-drop=all`、no-new-privileges、无 host
  mount/device/env、`--network workos-app-internal`（无外部 egress）、仅 `127.0.0.1::port` 随机
  发布、`--pids-limit/--memory/--memory-reservation/--cpu-quota`。inspect 要求 declared container
  port 恰好一个 loopback binding，并回读 image、argv、read-only/privileged/cap-add、实际
  `EffectiveCaps`/`BoundingCaps` 均为空、no-new-privileges/network/restart/mount/device/tmpfs facts；
  capability 字段缺失/畸形也拒绝，绝不把相对默认值计算的 `CapDrop` 当作实际零权限证据。Podman
  stdout/stderr 超出 1MiB 时整次调用失败而非静默截断；exit 125 不等同 not-found/name
  collision，只有独立 `container exists` 的 0/1 结果可证明存在/缺失，storage 125 保持失败；managed
  container list 对每个精确 ID 再 inspect，因此 orphan sweep 取得真实完整 labels。endpoint 从 engine inspect
  取得并验证 loopback 后才持久化；cgroup path 从 container PID 解析并验证位于本进程 delegated
  subtree（拒绝 traversal/空段/host cgroup）。compose 里的 runtime-host 诚实报告
  container-runner unavailable；systemd 部署说明给出仅针对 runtime-host 的最小 drop-in。
- **Web Service Surface**（`internal/runtime/surface` 扩展）：`UNSPECIFIED` = server 依 pinned
  descriptor 选择 renderer；显式 WEB_BUNDLE/WEB_SERVICE 必须精确匹配，否则 `FailedPrecondition`。
  create digest：auto 请求携带解析后的 kind（`auto:<kind>`），显式请求沿用原公式；历史 v1 行
  （`"" → web-bundle`）按精确映射重放，升级不 Aborted、auto 与显式不同键。web-service session
  持久引用 `workload_id + workload_generation`（renderer-specific 互斥 CHECK，migration `015`），
  Create 仅在 exact container running 且 startup health 通过后返回；启动失败不消费 create key，
  已建 workload 由 reconcile 收敛。`/surfaces/<session>/...` 对 web-service 只允许 GET/HEAD
  （405+Allow）、拒绝 query/upgrade/写方法；不转发 Cookie/Authorization/bridge token/identity/
  Forwarded/Host/hop-by-hop，剥除 Set-Cookie/认证 challenge/Server/hop-by-hop；backend 只能是
  server-owned、已验证 loopback endpoint（client/manifest URL 永不参与，杜绝 SSRF）；响应 media
  type 白名单外降级 octet-stream、64 个/64KiB header 预算（含 Connection-nominated header
  剥除）/8MiB body cap/超时有限；压缩传输关闭自动解码且拒绝非 identity `Content-Encoding`，并
  剥除 backend 的 CORS/reporting/navigation 等浏览器控制头；response 在 header 与完整 bounded body 验证前不提交，超限固定
  安全头 `502`，不返回截断的 backend 成功；redirect 仅在通过 surface
  path grammar 后重写回 session 前缀；每响应覆盖固定 WorkOS CSP + nosniff/no-referrer/no-store，
  backend 无法放宽。拨号前再次验证 proxy target 是 canonical session UUID + 精确
  `127.0.0.1:<canonical port>` + canonical path。每请求重验：identity → active session → Core
  active installation + exact container kind/app/version/pinned digest → workload running 且 generation
  精确匹配；session store outage 保持 503，uninstall/stop/generation/descriptor drift 立即 404。
  grant 变化沿 ADR-0003：bridge 方法在 Core epoch 比较处失败，旧内容不新增授权。
- **Reliability**（owner：reliability-host，`internal/reliability`，migrations `016`/`017`/`019`）：经私有
  版本化 `workos.workload.v1.SupervisedWorkloadService`（ListObservations/RestartWorkload/
  TerminateWorkload，仍不进 Gateway allowlist）取得中立 observation（稳定 ID、generation、状态、
  health verdict、exit 分类、有界 cgroup 计数；无 endpoint/cgroup path/container ID/内容）并执行
  幂等控制。决策全部确定性：unexpected exit（含 OOM/pids 特化分类，抑制通用重复上报）、健康失败
  连续episode、OOM/pids 事件 → 每个 `(workload, generation, violation, occurrence)` 恰好一个
  Incident（occurrence digest unique，at-least-once 重放不重复）；restart 经 per-incident durable
  action key（同 key replay 同一结果，Runtime 侧同 key replay 保证 crash window 不二次重启）；
  private pending-action queue 不伪造 owner wildcard，并独立于最新 observation state/generation 重驱，
  覆盖 runtime 已 restart/stop、Reliability 尚未提交 action outcome 的窗口；`unsupported`
  (FailedPrecondition) 与 `limit_exhausted` (ResourceExhausted) 在协议上分离，前者绝不误报预算耗尽；
  仅 `unavailable` action 可重驱，`failed` 是 owner 可见的 terminal verdict，action ledger 的 terminal
  outcome 不可被迟到 unavailable 写回；
  runtime 回 `limit_exhausted` → 一次性上报 restart 预算 Incident 并确定性 stop（无无限 crash
  loop）；新 generation 连续稳定观测达阈值会解析同 workload 的旧 generation mitigated Incident，
  且数据库只允许 mitigated→resolved（open 保持 open，不触发 mitigation CHECK）；acknowledge 是 owner 的独立事实，幂等 key +
  revision）。Reliability 不查 Runtime schema、不碰 Podman；Harness/Core/模型全停时 cgroup hard
  limits 与既定 policy 独立生效。IncidentService 经 Gateway 可选 Reliability upstream 公开
  （`/workos.incident.v1.IncidentService/`，identity 注入 + owner scope（分页 token 的 boundary lookup
  也受同一 owner/project 约束）+ 有界分页 limit+1 +
  固定错误矩阵）；Gateway core readiness 不因 Reliability 不可达而失败，bridge header 在该路由
  仍被剥除。
- **Desktop**：App Library Open 提交 `UNSPECIFIED`（server-selected renderer），Web Bundle 链路
  不变；container App 的 Open 显示 bounded in-flight 文案、迟到响应 inert。新增普通（非永久）
  System Monitor 窗口：列出当前 Project 的 sanitized Incident（severity/state/violation/restart
  outcome/acknowledged）与一次性 acknowledge；Reliability 不可达时仅该窗口降级，不影响
  Desktop/Agent/App Library；不显示 cgroup path、host port、container ID、raw 日志。
- 明确 unavailable（如实报告）：`supervisor`/`incident-manager` capability 在真实 observation→
  Incident→action E2E 前固定 false。真实 rootless 证据链（fixture image→digest pin→E2E→浏览器截图）
  需要 `make test-podman-fixture` 通过的主机；无该证据时 `container-runner`/Reliability 相关
  capability 与 docs/status.json 不升级，代码路径以 fake engine/单元/集成测试与 opt-in fixture
  门禁交付；Podman fixture 使用唯一 image tag、在 after snapshot 前精确清理，且只证明 adapter/
  cgroup boundary，不冒充跨进程 E2E。write transport（POST/表单/WebSocket）、network capability、repair/deployment/
  rollback、background-service/native runner 仍 unavailable。

## Central Credential Vault 与私有 harness 执行通道（ADR-0009，2026-08-30）

长期 provider credential 的唯一 durable authority 是 workos-core Credential Vault
（`internal/core/credential`，migrations `023`/`024`）。本文早先"workos-core 不拥有
credential"的表述由此显式取代。事实边界：

```text
operator 0600 文件/stdin → workosctl 进程内存
  → Core credential admin Unix socket（0600，仅 CredentialAdminService，16 KiB pre-decode）
  → canonical 校验 → master-key 派生的 keyed request digest（HMAC-SHA256）
  → AES-256-GCM seal（CSPRNG nonce，AAD 绑定 version/owner/ID/consumer/purpose/revision）
  → workos_core.provider_credentials（只有 nonce+ciphertext，绝无 plaintext）

active harness worker → Core 私有 mTLS listener（TLS 1.3，URI SAN 精确身份）
  → AcquireTaskCredential(task_lease_id, worker_id)
  → 单事务：Agent tx-scoped authority 锁定 active lease 并读取 durable snapshot
     + Credential tx-scoped store 验证 exact revision 并物理仲裁插入 short lease
  → 一次 bounded in-memory secret 交付 → 该 task 的 allowlisted child env
```

- 模块内规则：credential material 1–8192 bytes（不 trim，拒 NUL/CR/LF）；consumer ID 为
  1–128 ASCII `[-a-z0-9._]`；purpose 第一版仅 `provider-api-key.v1`；每个
  `(owner, consumer, purpose)` 至多一个 active credential；rotate/revoke 保持逻辑 ID 且
  revision 严格 +1；admin 幂等使用 keyed digest + versioned 首响应快照（失败不消费 key）。
  master key 来自绝对路径、非 symlink、owner-only、恰好 32 raw bytes 的文件；认证失败=
  stored corruption→净化 Internal，不 fallback 明文。Go 无形式化 zeroization，实现只做
  best-effort 覆写并承认该限制。
- Task credential snapshot（agent-owned `agent_task_credentials`，无跨模块 FK）：fresh user
  task、App allow 与 waiting-approval task 都在任何 queue/outbox/reservation 前解析 exact
  `(credential_id, revision)` 并与 task 同事务持久化；idempotency replay 返回首次 snapshot
  不漂移；approval decide 时经中立 port 重验，漂移保持 pending（FailedPrecondition）。
- short lease（`task_credential_leases`，`task_lease_id` 唯一）：Acquire 只接受
  task lease + worker；response-loss replay 返回同一 lease 行与同一 revision；Renew 只延长到
  新的 active task lease expiry 且永不再返回 secret，并重验 credential revision；rotate/revoke
  后下一次 heartbeat（≤10s）收到 invalid verdict，worker cancel 并 kill child；Release 幂等；
  过期由有界 sweep + 读路径双重收敛。
- 双私有 listener：Core 进程内新增 credential admin Unix socket 与 mutual TLS 1.3 harness
  execution listener（仅 `TaskExecutionService` + `CredentialLeaseService`，URI SAN
  `urn:workos:core` / `urn:workos:harness-host`，private CA 双端验证，CA 私钥不进常驻进程）。
  普通 Core HTTP mux 删除 TaskExecution 注册；TaskExecution 与全部 Credential RPC 对 Gateway
  保持确定性 404。mTLS 只证明 Core↔harness execution 身份，不是全系统 service mesh。
- Catalog owner-aware projection：provider 声明 `requires_task_credential_lease` 时，owner 缺
  active credential 则该 provider 投影 unavailable（固定安全文案，无 foreign 存在性 oracle）；
  ProjectHarnessBinding 的 `credential_ref` 由服务端注入（active credential 的 UUIDv7 opaque
  reference），客户端不可提交。缺 master key/admin socket 时 vault 如实 unavailable，
  credential-required provider 对新 binding/run fail closed，其余 Core 功能正常启动。
- DeepSeek：`Config.APIKey` 与 `DEEPSEEK_API_KEY` 读取路径删除；legacy env 只产生 sanitized
  迁移指引 configuration issue；provider 声明 `requires_task_credential_lease=true`，仅在
  consumer/purpose 匹配且未过期的 neutral lease 下启动 child，child env 只含该 task 的
  lease secret，不跨 task 缓存。dev/CI 的执行通道身份与 dev master key 由一次性
  workos-dev-fixture 分别写入 Core execution、Harness execution、Core vault 三个隔离 volume；
  任何 resident process 都只挂最小材料。生产由独立 Core/Harness service account + systemd
  per-unit credential directory 提供，workos-dev-fixture 永不是生产工具。
- 门禁：`make test-credential-vault`（真实 PostgreSQL + Core mTLS listener + harness-host +
  workosctl admin socket + 本地 DeepSeek fixture；missing/granted/revoked 三阶段）。
  `023`/`024` 为新 forward-only migration，001–022 逐字节不变。

## Review Artifact 作为 Agent Context（ADR-0010，2026-08-30）

`AgentTaskInput.context_refs` 的第一种 canonical 语义：`artifact.review.v1` + canonical
UUIDv7 + `sha256:` 64-hex exact digest。每 task ≤4、保持请求顺序、拒绝 duplicate 三元组与同
ID 不同 digest；resolved 聚合 ≤ 1 MiB（encode 前强制）。global task 与 App Bridge（canonical
payload 无 refs）不接受 context。

```text
Desktop（可信主界面）"Use as Agent context" → chip（title+类型，无 digest/ID）
  → SubmitTask：transport 校验 grammar/集合（InvalidArgument，零副作用）
  → Task Router：provider supported_context_ref_types exact 覆盖（否则 FailedPrecondition）
     + 中立 ArtifactContextVerifier：同 owner/project + review subtype + stored/recomputed
       digest == ref.revision（unknown/foreign/mismatch 统一 NotFound，无存在性 oracle）
  → task payload 只存三元 ref（顺序进入既有 payload digest/replay 事实）
harness worker：provider 启动前一次 ResolveTaskContext(lease_id, worker_id)
  → Core 单事务：Agent authority 锁 active lease 读 input → 重新校验 refs
     → Artifact tx-scoped read → owner/project/type/exact digest 重验 → 聚合 ≤1MiB → commit
  → canonical bounded documents 按请求顺序交给该次 provider execution
```

- 幂等 replay 返回第一次 task 的 refs，不按新 capability/artifact 重新裁决；digest pin 使
  submission↔execution 无内容漂移，execution 重验只捕获 stored 事实漂移（fail closed）。
- `HarnessCapabilities.supported_context_ref_types`（additive exact list）：词表外值/重复 =
  capability corruption → provider 投影 unavailable；Fake 与 DeepSeek 声明
  `artifact.review.v1`（经 materialized-context 测试证据），Generic CLI 保持空并对非空
  resolved context fail closed。
- DeepSeek 把 goal/context 编码为 versioned canonical JSON task envelope
  （`workos.deepseek.task-envelope.v1`）作为唯一 user content block，artifact bytes 位于
  `untrusted_contexts` 数组；Fake 以 deterministic receipt 证明 exact count/order/digest
  （不回显 content bytes 进 event）；resolved content 不进 task row/outbox/event/日志。
- Desktop：Artifact Center/Viewer 行内 "Use as Agent context" → composer 可移除 chip（title
  - 类型），duplicate 幂等、≥4 固定提示、成功提交后消费；Project 切换 abort + 清空 chips；
    digest/ID 不进 DOM data attribute/URL/storage。门禁 `make test-artifact-context`（Go 进程内
    PostgreSQL 协议测试 + 真实跨进程 Chromium 链路）与确定性 before/after/current 视觉证据。

## DeepSeek Structured Markdown / Diff Review（ADR-0011，2026-08-30）

仅当 task 的 `output_artifact_types` 非空时启用 versioned structured mode：DeepSeek adapter 把
goal/context/output contract 编码为 versioned canonical JSON task envelope（context 在
`untrusted_contexts` 数组；contract 在 `output_contract`），模型回答必须是恰好一个 JSON
document（`workos.deepseek.review-output.v1` + bounded summary + artifacts key set 恰好等于
请求集合）。

```text
structured run：RunStarted → deltas 只聚合不发布 → shutdown
  → strict parser（单 JSON 值、拒绝 unknown/duplicate key、trailing/prose/fence、
     summary ≤64KiB、每 output ≤512KiB/20k 行/16KiB 行、UTF-8/C0-C1 规则）
  → adapter 固定 output key/title（document/patch）→ 原子 batch sink
  → validated bounded summary 成为唯一 AssistantMessage → Usage → RunCompleted
```

- 原子 batch：私有协议 additive `AppendTaskArtifactBatch`（≤2 outputs；保留单项 RPC）。Core
  在单事务内锁定 stream、逐项 replay/prepare/insert/publish（连续 event sequence、request
  order 定序）；任何 conflict/corruption/校验失败整批回滚——零 artifact、零 mapping、零 event。
  all-absent → 原子插入；all-present exact → 精确 replay（同一 artifact/event）；worker 仅在
  batch 成功后标记 requested types emitted。
- malformed/partial/oversize/unsupported/revoked 输出全部 fail closed：RunFailed（非重试
  protocol failure、不回显 response）、无 artifact_created、无 RunCompleted。
- capability 翻转以证据为前提：DeepSeek 现声明 `structured_artifacts=true` +
  exact `document.markdown.v1`/`code.unified-diff.v1`（strict goldens、batch 原子性、本地
  fixture 全链路测试先行）。同一 run 同时受 ADR-0009 credential lease、ADR-0010 context、
  token/runtime budget 约束；任一失效 cancel/kill child 并停止 publication。
- 门禁 `make test-deepseek-structured-review`（PostgreSQL + Core mTLS + harness-host +
  workosctl admin socket + 本地 fixture + Gateway + Chromium；structured 成功链路 +
  malformed fail-closed 链路）；`TestArtifactBatchAtomicity` 对真实 PostgreSQL 证明
  all-or-none/exact replay/conflict。

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
