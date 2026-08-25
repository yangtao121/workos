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
- 数据由 `004_project_app_installations.sql`（owner：workos-core Project Installation）持有：
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
  uninstall 在 tombstone 后仍可重放，失败请求不消费 key。
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
