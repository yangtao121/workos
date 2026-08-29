# Task: Mutable Project App Grants 与立即撤销纵向切片

- 状态：done（实现、测试门禁、UI 视觉记录与文档同步均完成；分支待后续审核者审查并决定合并）
- Owner/Agent：orchestrator（ZCode 主智能体）+ 多个并行实现子智能体
- 进程/模块：workos-core（Project Installation、orchestration AppAgent/Surface resolver）、
  runtime-host（Surface session/App Bridge）、workos-gateway（既有 allowlist 前缀自动公开）、
  Desktop Shell（App Library 权限管理）
- 依赖：`feat/minimal-project-agent-app-bridge`（已合入 main 的 2486ca3 及后续修复）

## 目标与范围

让用户在不卸载 App 的前提下，显式替换某个 Project App installation 的 grant 集合；
每次真实变更产生 installation grant revision 并使旧 Surface 的全部 App Bridge 方法失效；
用户重新打开 Surface 后才按新 grant 建立能力。

包含：

- additive public RPC `SetAppGrants`（full replacement 语义）；
- `AppInstallation.grant_revision`、私有 surface resolver / App Agent 链路的 revision 传递；
- migration `011_mutable_project_app_grants.sql`（owner: workos-core Project Installation）与
  `012_surface_grant_revision.sql`（owner: runtime-host Surface）；
- Core 侧事务线性化（grant + Project revision + event + outbox + idempotency 同事务）；
- runtime Surface session 持久化 grant revision，私有调用携带，Core 每次授权比对；
- Desktop `Manage permissions` dialog（exact pinned version requested set 基准）；
- 集成/撤销/E2E/restart 测试与 UI before/after 视觉证据。

不包含：App Agent approval/quota/budget、自动取消既有 durable task、manifest requested
permissions 变更、App 升降级、新 Bridge executor、container/Web Service Workload、
Reliability、跨 schema SQL/FK 实现撤销、`docs/structure.md` 主线修改。

## 四种事实（语义基准）

| 概念                           | owner                               | 语义                                                     |
| ------------------------------ | ----------------------------------- | -------------------------------------------------------- |
| requested permissions          | Core App Registry immutable version | App 只提出请求，永不自动授权                             |
| current installation grant     | Core Project Installation           | 用户最后一次成功确认的 canonical 完整集合                |
| installation grant revision    | Core Project Installation           | 从 1 开始、仅在 grant 集合真实改变时 +1                  |
| effective Surface capabilities | runtime Surface session             | 创建时 `current grant ∩ implemented bridge methods` 快照 |

## 协议/数据影响

- `api/proto/workos/app/v1/installation.proto`：additive `SetAppGrants` RPC、
  `AppInstallation.grant_revision`；
- `api/proto/workos/surface/v1/surface_resolver.proto`：`ResolveWebBundleResponse` additive
  grant revision；
- `api/proto/workos/agent/v1/app_agent.proto`：私有 Run/Watch 请求 additive session 派生
  revision 字段；
- event `project.app.grants.updated.v1`（sequence = new Project revision）；
- migrations `011`/`012`（单一 process owner；001–010 逐字节不变）；
- idempotency 命名空间沿用 `project_app_installation_requests (owner_user_id, idempotency_key)`，
  digest 扩展 set-grants command，结果快照增加 grant/revision 列。

决策细节见 `docs/decisions/0003-mutable-app-grants.md`。

## 执行计划（子智能体拆分）

| 波次   | 子任务           | 范围                                                                            |
| ------ | ---------------- | ------------------------------------------------------------------------------- |
| Wave 0 | orchestrator     | 分支、基线门禁、任务记录、ADR、before/ 基线                                     |
| Wave 1 | S1 契约与存储    | Proto additive、migration 011/012、sqlc queries、`make generate`、checksum 测试 |
| Wave 2 | A Core 纵向      | `internal/core/project` SetAppGrants 全链路 + orchestration revision 二次授权   |
| Wave 2 | B Runtime 纵向   | `internal/runtime/surface` revision 持久化、私有传递、public bridge 失效        |
| Wave 2 | C Desktop        | AppLibrary `Manage permissions` dialog + unit 测试                              |
| Wave 3 | D 测试门禁       | 集成/撤销/E2E/restart/race 全量验证与修复                                       |
| Wave 4 | E 文档与视觉证据 | after 截图、current/ 更新、文档同步、交接记录                                   |

## 验收

- [x] 行为测试（domain/application/postgres/transport/runtime/desktop/E2E）
- [x] `make bootstrap` / `make generate`（二次无差异）/ `make check`
- [x] `buf breaking --against '.git#branch=main'`
- [x] `make test-integration` ×2（资源计数与 scratch DB 卫生）
- [x] `make test-deepseek-fixture`（仅仓库假凭据）
- [x] `make test-e2e`
- [x] `go test -race ./internal/core/project/... ./internal/core/orchestration/... ./internal/runtime/...`
- [x] migrations 001–010 逐字节不变；011/012 checksum 钉住
- [x] UI before/after/current/notes；`git diff --check` 干净
- [x] 文档与 `docs/status.json` 同步

## 交接

- Branch：`feat/mutable-project-app-grants`；base `afa05d2`（main）；实现 HEAD `2af3736`
  （五个提交：契约/存储 → surface 绑定 → Desktop → Core SetAppGrants/二次授权 →
  测试门禁与修复）。文档与视觉记录由协调方审核后随本分支追加提交（本记录与
  `docs/ui/**`、`docs/architecture/implementation.md`、`docs/decisions/0002`、
  `docs/status.json`、README 状态区块渲染产物即该提交内容）。

### Proto 与 migration

- `api/proto/workos/app/v1/installation.proto`：additive——`AppInstallation.grant_revision`
  （字段 9）、`SetAppGrantsRequest`（1–5）/`SetAppGrantsResponse`（1–2）与 RPC
  `SetAppGrants`；无字段号复用、无删除。
- `api/proto/workos/surface/v1/surface_resolver.proto`：`ResolveWebBundleResponse.grant_revision`
  （字段 3）。
- `api/proto/workos/agent/v1/app_agent.proto`：私有
  `RunAgentTaskRequest.installation_grant_revision`（6）、
  `WatchAgentTaskEventsRequest.installation_grant_revision`（5）——只能由 runtime 的
  validated session 派生，public bridge body 不暴露。
- migration `011_mutable_project_app_grants.sql`（owner：workos-core Project Installation，
  sha256 `1b85383b53f23829151cacca44c5f400f1fb9ca1e06f4836767a3c40f354775f`）：installation
  `grant_revision`（backfill 1）、request `command` 约束扩展 `set-grants`、
  `result_granted_permissions`/`result_grant_revision` 结果快照列 + owner-bound fail-closed
  回填。
- migration `012_surface_grant_revision.sql`（owner：runtime-host Surface，
  sha256 `9b8335b1a7936ef96b5b5aaeeeac8b351768bb5c98152bfed6d80bbd904bcc89`）：session
  `installation_grant_revision` 快照列（backfill 1，无跨 schema FK）。
- 001–010 逐字节不变：`TestAllMigrationChecksumsArePinned`
  （`tests/integration/mutable_project_app_grants_migration_test.go`）把全部 checksum 钉住；
  008 内"本切片无 mutable 更新路径"的注释是当时切片事实的历史记录，按 checksum 钉住
  约定保持原样。

### 验证命令与真实结果（Wave 3 实测，全部 exit 0）

| 命令                                                                                                 | 结果                                                      |
| ---------------------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| `make generate`（连续两次）                                                                          | 两次后工作树无生成差异                                    |
| `make check`                                                                                         | 通过（proto/go/web/prettier/渲染 check）                  |
| `buf breaking --against '.git#branch=main'`                                                          | 无 breaking                                               |
| `make test-integration` ×2                                                                           | 两轮全绿（另做 2 轮净增量测量，见下表）                   |
| `make test-deepseek-fixture`                                                                         | 通过（仅仓库假凭据 `workos-fixture-only-not-a-real-key`） |
| `make test-e2e`                                                                                      | 5 passed、1 skipped-by-design（deepseek fixture 门控）    |
| `go test -race ./internal/core/project/... ./internal/core/orchestration/... ./internal/runtime/...` | 通过                                                      |
| `git diff --check` 与 `git diff --check main...HEAD`                                                 | 干净                                                      |

### 资源计数（持久验收 volume 净增量，两轮）

| 表/事实                                          | 第一轮  | 第二轮  |
| ------------------------------------------------ | ------- | ------- |
| `project_app_installations`                      | +21     | +20     |
| `project_app_installation_requests`              | +43     | +42     |
| `workos_runtime.surface_sessions`（含 requests） | +16     | +16     |
| project events / outbox                          | +62/+62 | +62/+62 |
| `project.app.grants.updated.v1` 事件             | +10     | +10     |
| `agent_tasks`                                    | +9      | +9      |

- 一轮 = 完整集成套件（本任务 4 个测试 + 既有全部）+ 6 组 restart seed
  （task/app/install/surface/bridge/grants）。
- project events 与 outbox 恒等成对是事务不变量的直接观测；grants_updated 事件数与
  真实变更次数一致。
- installations/requests 的 ±1 属既有 `InstallCompetesWithProjectUpdate` 用例的固有
  非确定性（并发 winner 可能是 install 或 update），非泄漏。
- scratch DB 卫生：`TestGrantEpochWatchStreamTerminates` 等自建 scratch database 均在
  测试内精确清理；既有的 6 个约 5 天前残留 scratch 库未触碰。

### 授权 / 重放 / 并发 / 撤销矩阵（用例清单）

`tests/integration/mutable_project_app_grants_test.go`：

- `TestMutableProjectAppGrantsVerticalSlice`
  - `RealChangeBumpsBothRevisionsExactlyOnce`：真实变更 grant revision 恰 +1（→2）、
    Project revision 恰 +1；durable row 同事实；恰一 `project.app.grants.updated.v1`
    （sequence=新 revision，8 字段 payload 齐全）；恰一 outbox 行且 `occurred_at` 与事件
    绑定（同事务证明）；project `updated_at` 移动。
  - `SameSetNoOpKeepsFactsButConsumesKey`：no-op 不动两个 revision、row、events、outbox、
    `updated_at`（精确时间戳比较），但 key 持久消费且映射存 no-op 结果；same key 精确
    replay；same key 不同目标集合 → `Aborted`。
  - `ReplayReturnsFirstSnapshotAfterLaterMutations`：后续撤销/恢复后，第一个 key 的重放返回
    第一次响应的 grant/epoch/project revision（非当前行）；第二个 key 同理；历史 install
    key 重放安装时事实（双权限 epoch 1、原 revision）。
  - `FailedRequestsDoNotConsumeKey`：非子集 `PermissionDenied`、malformed
    `InvalidArgument` 均不消费 key；denied key 随后成功驱动真实变更（→epoch 5），
    malformed key 随后作为 no-op 成功。
  - `ErrorMatrix`：malformed project/installation/key(>128 rune)/zero revision/控制字符
    grant/duplicate grant → `InvalidArgument`；非子集 → 净化 `PermissionDenied` 且错误文本
    无 SQL/constraint/输入泄露；unknown project/installation、foreign installation、
    uninstalled installation → 统一 `NotFound`；stale expected revision → `Aborted`。
  - `SameKeyAcrossCommandsAndProjectsAborts`：same key 复用为不同 Set、为 Install、跨
    project 的 Set → 一律 `Aborted`。
- `TestMutableProjectAppGrantsConcurrency`
  - `SameExpectedRevisionYieldsExactlyOneWinner`：4 个独立 HTTP client 并发 Set 同 expected
    revision → 恰一 winner、其余 `Aborted`；恰一 epoch bump、恰一事件、revision 恰前移 1。
  - `SetCompetesWithUninstallOnSameRevision`：Set 与 Uninstall 竞争同一 revision → 恰一
    成功；库内恰一份一致事实（active epoch 2 grant [run] 或 tombstone epoch 1）。

`tests/integration/mutable_grant_revocation_test.go`：

- `TestMutableGrantRevocationChain`（真实 gateway/core/runtime/PostgreSQL/Fake Harness）
  - 旧 epoch 上 run+watch 至 terminal 正常（前置）。
  - 真实变更（缩为仍含 watch 的子集，epoch 2）后：旧 token 的 run 与 watch 均
    `PermissionDenied`——包括新 grant 仍含的 watch，证明拒绝来自 Core epoch 比对而非
    UI 隐藏或 per-capability diff。
  - 旧 CreateSurface key 重放 → `FailedPrecondition`（fail closed，不为旧 epoch 铸新
    token、不落第二 session 行、错误无内部泄露）。
  - fresh key 的 session：capabilities = 新 grant ∩ implemented；session 持久 revision 2、
    旧 session 持久 revision 1；fresh session 可 watch 既有 durable task、未授予的 run
    被本地 capability gate 拒绝。
  - 再扩回双权限（epoch 3）：epoch-1 与 epoch-2 session 的 run/watch 均拒绝；新 surface
    双 capabilities、持久 revision 3、run+watch 至 terminal。
  - 撤销不隐式取消：撤销前的 durable task 保持 completed 且 owner 可见。
  - grant 变更不是 uninstall：静态 Web Bundle 资产对仍 active installation 照常服务，
    installation 未 tombstone。
- `TestGrantEpochWatchStreamTerminates`（scratch DB + 真实 Core 私有 transport）
  - commit 前已打开的 watch stream 在下一轮 polling reauthorization 终止（channel
    deadline 界定 200ms ticker，无任意 sleep）；终止错误为净化 stale-epoch
    `PermissionDenied`（无数字/内部泄露）；commit 后不再向旧 epoch 转发任何事件。
  - stale（=1）与 absent（=0）epoch 的 run 同为 `PermissionDenied`（不可区分）；
    fresh epoch run 成功；epoch 匹配但 grant 不含 watch 的 watch 亦 `PermissionDenied`。
  - 被撤销 epoch 创建的 durable task 保持 queued（未取消）。

`tests/restart/grants.go`（`make test-integration` 内 `grants-seed` → restart →
`grants-verify`）：

- seed：双权限安装（epoch 1）→ 开 surface → 真实 Fake Harness 任务 → SetAppGrants 撤销
  全部（epoch 2），记录 token/project/installation/surfaceKey/setKey/revision。
- verify（restart 后）：旧 token run `PermissionDenied`（持久 epoch 检查）；旧 create key
  `FailedPrecondition`；set key 精确 replay 第一次响应（空 grant epoch 2 + 原 revision）；
  revoked installation 的新 surface 无 capabilities；fresh re-grant（epoch 3）+ reopen 双
  capabilities 且 run/watch 至 terminal（restart 后全链路恢复）。

### UI 视觉记录

- 路径：[before/](../ui/desktop-web/changes/20260829-mutable-project-app-grants/before/)（4 张）、
  [after/](../ui/desktop-web/changes/20260829-mutable-project-app-grants/after/)（8 张）、
  [notes.md](../ui/desktop-web/changes/20260829-mutable-project-app-grants/notes.md)；
  `current/` 已用 after 同名替换 + 新增 manage-permissions 系列（共 8 张）。
- 覆盖：installed 行 grant revision + Manage permissions、更新后 consent 文案、对话框
  initial current selection、Adding/Removing diff、revoke-all 确认、保存成功"重开生效"
  提示（行显示 `Granted: none · grant revision 2`）、撤销→重授→重开后新 epoch 的真实
  bridge terminal 结果。
- 采集：Chromium、1440×900、DSF 1、确定性合成 fixture（与 e2e 相同 bridge bundle seed
  模式 + `workos.activeProjectId`）；驱动脚本为 git 忽略的 `tmp/capture-grants.mjs`，
  命令与步骤详见 notes.md。

### secret / 生成物审计

`git status` 仅含本任务文件（docs/\*\*、README 渲染区块、截图）；无 ELF/构建产物、无
playwright report/trace/video、无临时数据库文件、无 root-owned 文件、无凭据或 token
（截图仅合成 fixture 数据；e2e 输出目录在容器内 `/tmp`）。`.pnpm-store` 为仓库既有
git-ignored 本地工具缓存。

### 修复记录（Wave 3 发现并修复的缺陷）

1. App Library `Manage permissions` 从列表缓存行直接打开：跨标签页/设备变更 grant 后
   checkbox 初值与 Save 的 expected revision 可能描述过期事实——改为打开前先重读
   server facts（installation 消失时提示且不开编辑器、双击防抖、读取失败回退缓存行
   由服务端裁决 Save）。
2. 既有静态 fixture 未填新的 authoritative `GrantRevision`/私有请求 epoch 字段（零值
   通不过 ≥1 的 epoch 比对），app-bridge/web-bundle/runtime 既有测试会被误伤——统一
   钉住 epoch 1 并在私有调用中携带匹配值。
3. E2E 第二个标签页按侧栏项目卡点击选择 project，但侧栏只列第一页项目卡（持久 volume
   下本 run 项目可能不在首页）——改为经 `workos.activeProjectId` 种子直达（GetProject
   支持的 reload 路径），消除选择不确定性。
4. E2E 重开 surface 前未关闭已保存对话框，对话框遮挡/拦截后续 Open 点击——保存断言后
   显式 Close 并等待隐藏再重开。

### 未决风险与下一步

- 撤销语义边界（有意）：已在 commit 前通过授权的并发请求可完成；durable task 不隐式
  取消（撤销不是 CancelTask）——自动取消/审批策略是后续显式工作。
- 下一步（建议方向一）：App Agent approval + durable quota/budget policy——grant 现在
  只裁决"哪些方法可调"，尚无"花多少、超限怎么办"的持久策略层；结合本任务的 grant
  epoch 机制做预算绑定与超额 fail closed。
- 备选方向二：rootless container Workload lifecycle（Runtime / Surface 仍只有 Web
  Bundle；container/native runners 维持 unavailable）。
