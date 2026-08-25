# Task: Project App Installation vertical slice

- 状态：done（2026-08-25；全部验收门禁通过。合并须经审核者静态复审后本地 `--ff-only`）
- Owner/Agent：project installation builder
- 进程/模块：workos-core `internal/core/project`（installation 子域）；`internal/core/orchestration`（App catalog bridge）；workos-gateway allowlist；desktop-web App Library
- 依赖：App Registry（immutable version/digest，`002`/`003` 已执行）、`001_foundation.sql` 的 `projects.installed_app_ids`、Project revision/event/outbox 事务模式

## 目标与范围

把 owner 已注册的一个 immutable App version 安装到 owner 的 active Project，产生稳定的安装实例身份，支持查询、卸载、Project revision/event 与 Desktop App Library 最小交互：

```text
Desktop App Library
  → Gateway public AppInstallationService
  → Core identity
  → owner Project + expected revision
  → neutral App Catalog application port（orchestration包装 App Registry）
  → resolve current 或 explicit immutable version
  → 一个 Project-owned PostgreSQL 事务：
       installation/tombstone + idempotency result
       + installed_app_ids 投影 + Project revision（+1）
       + project event（sequence = revision）+ outbox
  → List/Get Project（Core restart 后仍成立）
  → uninstall 经同一事务边界
```

在范围内：`InstallApp`/`UninstallApp`/`ListInstalledApps`；version pinning（空 version = 本次命令内解析 current 并固定 exact version+digest）；同一 Project 同一 app 至多一个 active installation；tombstone 卸载与重装新实例；`(owner, idempotency_key)` 持久幂等（install/uninstall 共用命名空间，action 不同即 `Aborted`）；Project revision 竞争与 event/outbox；`installed_app_ids` 事务内派生投影；Gateway allowlist、SDK clients、Desktop App Library（loading/empty/error/retry/saving、revision conflict 刷新、Project 切换隔离）；单元/传输/PostgreSQL 集成/迁移/并发/重启/Gateway/浏览器 E2E 测试；文档与状态同步。

不在范围内：App upgrade/downgrade 或自动跟随 current、bundle 上传/托管、runtime-host runner、Surface/iframe/App Bridge、capability grant/token、Credential Vault、system/trusted App 安装路径、真实 Provider 网络或真实 Key。Runtime/Surface 保持 `scaffolded`（runtime-host 未改动，`surface-broker` 仍如实报告 unavailable）。

## 契约（最终现状）

Additive `api/proto/workos/app/v1/installation.proto`（package `workos.app.v1`），独立 `AppInstallationService`：

- `AppInstallation`：`id`（UUIDv7，即后续 Surface 的 `app_instance_id`；本任务不代表 workload 已运行）、`project_id`、`app_id`、`version`（安装时固定的 immutable version）、`manifest_digest`（安装时固定）、`installed_at`（UTC）、optional `uninstalled_at`。
- `InstallApp(idempotency_key, project_id, app_id, version 空=命令内解析 current 并固定, expected_project_revision)` → `(installation, project_revision)`。
- `UninstallApp(idempotency_key, project_id, installation_id, expected_project_revision)` → `(tombstoned installation, project_revision)`。
- `ListInstalledApps(project_id, page)`：只列 active，按 app ID 稳定排序；page size 默认 50、上限 100、负值 `InvalidArgument`，repository effective limit+1 探测，恰好满最后一页不产生 token（以 100-installation fixture 证明两整页且第二页无 token）。
- 响应不返回 manifest、credential，不声称 permissions 已授权；未新增 `GetInstallation`（无本任务调用方）。

## 数据模型（owner：workos-core Project Installation）

`004_project_app_installations.sql`（forward-only；001/002/003 逐字节未变）：

- `workos_core.project_app_installations`：安装实例 authoritative fact。UUIDv7 `id` PK、`owner_user_id`、`project_id`、`app_id`、`version`、`manifest_digest`、`installed_at`、`uninstalled_at`（NULL=active）。partial unique `(project_id, app_id) WHERE uninstalled_at IS NULL`；复合 FK `(project_id, owner_user_id) → projects (id, owner_user_id)`（004 为 projects 增补 `UNIQUE (id, owner_user_id)`）；CHECK 收紧 app ID/version/digest 形态与 `uninstalled_at >= installed_at`。无跨模块 FK、不 join Registry 表。
- `workos_core.project_app_installation_requests`：幂等权威。PK `(owner_user_id, idempotency_key)`、`command ('install'|'uninstall')`、`request_digest`（客户端 canonical 请求字段的 sha256 JSON：action/app_id/expected_project_revision/installation_id/project_id/version，不含时间戳或解析结果）、`installation_id`（FK RESTRICT）、`project_revision`（第一次响应 revision）、`result_uninstalled_at`（响应快照）、`created_at`。
- `projects.installed_app_ids`：方案 1（事务内派生投影）。install/uninstall 事务持有 project 行锁时 `array_agg(app_id ORDER BY app_id)` 聚合并写入同一条 revision UPDATE；普通 `UpdateProject` 的 SQL 不接收/不覆盖该列（原有行为保持）。

## 安装语义与不变式（最终现状）

- 空版本只在第一次成功命令解析 current 并持久化 exact version+digest；Registry 后续更高版本不改变既有 installation（集成测试显式证明）。显式 version 精确解析 immutable version；未知 app/version、他人 app → 净化 `NotFound`。
- scope 仅 `user|project`；catalog 返回非可安装 scope（system/trusted）时 fail closed（净化 Internal，不入库）。
- 同 app 同 version/digest 已 active：新 key 在 expected revision 正确时为确定 no-op（锁内验证 revision、消费 key、返回既有实例；不新建 row、不增 revision、不发事件——以事件计数与 revision 断言证明）。不同 version：统一 `AlreadyExists`。
- 卸载 tombstone；重装新 key → 新 UUIDv7 实例；旧 install key replay 精确返回第一次结果（含 revision 与 active 投影），不复活 tombstone（DB 断言 active=0）；uninstall 成功结果 tombstone 后仍可重放；新 key 卸载已 tombstone 的 installation → `NotFound`。
- archived Project 的 install/uninstall/list 统一 `NotFound`；unknown/foreign Project/installation 统一 `NotFound`。

## Project revision 与并发（最终现状）

- mutation 事务流程：`SELECT … FOR UPDATE` 锁定 owner-scoped project 行 →（锁内）复查幂等 key → 比较 revision → 业务分类 → 写 installation/tombstone → `UPDATE revision=revision+1, updated_at, installed_app_ids`（rows-affected 防御）→ event(sequence=new revision)+outbox → 消费 key（mapping PK `ON CONFLICT DO NOTHING` 仲裁，rows=0 时复读分类 replay/Aborted，loser 全回滚）→ commit。
- 与 `UpdateProject`/`ArchiveProject`/binding 的 guarded UPDATE 通过同一行锁互斥：同 expected revision 并发恰好一个 winner，loser `Aborted`（HTTP 层 4 并发、install×update 竞争、双 repository instance 直连 scratch DB 三种形态证明）。no-op install 在锁内验证 revision 但不 +1；并发 update+no-op-install 可按串行化语义同时成功（no-op 非mutation，已按此文档化并调整测试为真实 mutation 竞争）。
- 事件 `project.app.installed.v1` / `project.app.uninstalled.v1`：sequence 与 Project revision 连续一致（整流断言），payload 只含 projectId/revision/installationId/appId/version/manifestDigest。

## 模块边界（最终现状）

```text
internal/core/project: domain/installation.go → application/installation.go
                        → ports/installation.go ← adapters/postgres/installation.go（同 package projectdb）
internal/core/orchestration/app_catalog.go：App Registry application → project AppCatalog port
transport：internal/core/project/transport/installation.go（仅生成协议类型）
```

- Project 不 import appregistry internal package（domain 内自带 app ID grammar/SemVer 语法/digest/idempotency key 纯函数）；Registry 不查询 installation 表。架构守卫新增：project SQL 禁止引用 `app_versions`，appregistry SQL 禁止引用 `project_app_installations`。
- owner 只来自 identity context；错误映射：malformed→`InvalidArgument`；missing/foreign/archived Project、unknown App/installation、catalog denial→`NotFound`；stale revision、同 key 不同请求→`Aborted`；不同 version→`AlreadyExists`；identity 缺失→`Unauthenticated`；内部→净化 `Internal`。

## Desktop App Library（最终现状）

- `sdk/agent-sdk` 统一 clients 新增 `appRegistry` 与 `appInstallations`；`sdk/protocol` 导出 `installation_pb`。
- `apps/desktop-web/src/AppLibrary.tsx`：active Project 入口列出 owner Registry catalog（页遍历），标识 active installation 与 pinned version/digest 摘要；Install（空 version 固定 current）与 Remove 使用 `crypto.randomUUID()` key + 当前 Project revision；成功后以服务端返回为准重读 project（`getProject`）与 installation list；`Aborted` 时重载并提示，不自动重放；错误净化为可理解文案（AlreadyExists 提示升级不在本版本）；Project 切换经 `key` remount + generation guard 隔离（延迟 Promise 测试证明旧 Project 响应不污染新 Project、卸载后 inert）。
- Desktop 以 sessionStorage 记忆 active Project，reload 后若不在 `ListProjects` 首页则经 `GetProject` 取回保持激活（jsdom 测试 afterEach 清理 storage）。无 manifest 编辑/上传、无 launch/Dock/窗口/iframe/Surface URL/bridge。

## 验收（全部通过，2026-08-25 UTC）

- [x] Domain/application/transport/orchestration 单元测试（`go test ./internal/...`）
- [x] PostgreSQL：004 pristine（`TestProjectInstallationMigrationFromEmptyDatabase`，含约束/owner 绑定/形态拒绝/tombstone 释放 active 槽）+ 验收 volume 前向执行（bootstrap 已应用，`TestProjectInstallationMigrationAppliedToAcceptanceVolume`）+ 双 repository 并发与整事务回滚（`TestProjectInstallationRepositoryConcurrency`）
- [x] 集成纵向（8 子测试）、并发（3 子测试）、分页（100-installation 高基数 + 精确清理归零）、重启（`make test-integration` 内 `install-seed`/`install-verify`）
- [x] Gateway 公开 `AppInstallationService`（伪造 identity 覆盖测试扩展至三个 RPC path）；Surface/private service 继续 404
- [x] Desktop 组件测试 29 passed（AppLibrary 9 项 + Desktop 10 项）；浏览器 E2E `app-installation.spec.ts`（注册→安装→reload→卸载→reload）2 passed + fixture spec skipped-by-design
- [x] `make generate` 连续两次无差异；`make check`；`make test-integration` ×2；`make test-deepseek-fixture`；`make test-e2e`；`buf breaking --against '.git#branch=main'`
- [x] `docs/architecture/implementation.md`、`docs/status.json`（新增 Project App Installation=working，Desktop evidence 更新）、README（生成）同步；`docs/structure.md` 不变

## 交接（证据）

实现前基线（2026-08-25，clean `main` @ `1f096de`）：`make bootstrap`、`make check`、`make test-integration`、`make test-e2e` 全部通过。

### 资源计数（两次连续 `make test-integration`，验收 volume `workos_workos-postgres`）

| 表                                | 运行前 | 第 1 次后   | 第 2 次后   |
| --------------------------------- | ------ | ----------- | ----------- |
| app_versions                      | 1110   | 1131（+21） | 1152（+21） |
| app_registration_requests         | 1302   | 1331（+29） | 1360（+29） |
| projects                          | 347    | 354（+7）   | 361（+7）   |
| project_app_installations         | 185    | 191（+6）   | 196（+5）   |
| project_app_installation_requests | 248    | 258（+10）  | 267（+9）   |
| workos_events.events              | 2012   | 2041（+29） | 2070（+29） |
| workos_events.outbox              | 1132   | 1153（+21） | 1174（+21） |

- 增量为既有固定形态（Registry 纵向/restart 每轮固定增量 + 本任务低基数子测试每轮约 +5 installations/+9~10 requests）；高基数分页 fixture（100 installations/100 versions/200 keys + 对应 events/outbox/project 行）每轮以 run-unique stamp 精确清理并验证归零（单事务按 FK 顺序删除，无 LIKE/TRUNCATE/wildcard，volume 未删除）。
- migration scratch database：运行前、两次运行后集合完全相同，均为 6 个历史残留（`workos_migration_test_1787498316725324135`、`…5495588`、`…439446423137`、`…439446610484`、`…480690229539`、`…480690854487`），零新增；历史残留仅登记，未经授权未删除。
- 本轮唯一 fixture 前缀：`inst-board-/inst-notes-/inst-race-/inst-bulk-/inst-race-two-`（时间戳后缀）；restart helper 每轮 `restart-install-<stamp>@1.4.2`。
- 004 已于 08:28 由 compose bootstrap 作为前向迁移应用到含 706 app_versions/204 projects 的验收 volume；`schema_migrations` 记录确认。

### 实际运行命令

`make bootstrap`、`make generate`×2、`make check`、`make test-integration`×2（均含 restart install-seed/verify 输出 `installation persistence verified for …`）、`make test-deepseek-fixture`（Go fixture + restart + Playwright 1 passed）、`make test-e2e`（foundation + app-installation 2 passed）、`buf breaking --against '.git#branch=main'`、`git diff --check`、`git diff --check main...HEAD`、`git diff --exit-code main -- …/001|002|003`（逐字节一致）、status render --check（含于 make check）。

### 未决风险

- no-op install 与并发 update 可同时成功（no-op 非 revision mutation，语义已在任务记录文档化）；若产品要求 no-op 也占 revision 需独立变更。
- Desktop `ListProjects` 仍单页 100 且无分页 UI；reload 恢复依赖 sessionStorage + `GetProject` 取回。项目数量增长后的列表分页是独立 UX 任务。
- 安装不验证 app 的运行时可用性（无 bundle/artifact 事实），这是下一任务的输入而非本任务缺口。
- acceptance volume 的低基数固定增量（上表）为既有测试形态，收敛需独立测试任务。

## 下一任务

**minimal Web Bundle Surface backed by installed app instance**：`SurfaceService.CreateSurface` 只接受真实存在的 installation ID 作为 `app_instance_id`。仍缺：bundle artifact 上传/托管契约（`workos.artifact.v1` 目前 contract-only）、Surface 会话生命周期与 runtime-host 托管边界、Surface URL 签名与 Gateway 路由策略。不得把本任务描述成 App 已可运行或可打开。
