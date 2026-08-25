# 下一位智能体 Prompt：Project App Installation 纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、文档和提交，不是只输出计划。

## 你的角色

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。App Manifest Registry 已审核并
快进合并到本地 `main`；你的任务是实现下一条严格限定的纵向切片：**Project App Installation**。

本任务把 owner 注册的一个不可变 App version 安装到一个 Project，产生稳定的安装实例身份，支持查询、
卸载、Project revision/event 和 Desktop App Library 最小交互。不要在本轮启动 App、托管 bundle、创建
Surface、iframe 或 capability token。

持续推进直到实现、测试、文档、状态和提交全部完成。只有遇到必须破坏 v1 契约、改变六进程所有权、
修改已执行 migration，或需要新增信任根时，才停止并向用户报告证据与选项。

## 为什么下一步是 Project App Installation

当前事实：

- App Registry 已经能安全注册、按 owner 查询并固定 immutable version/digest；
- `Project.installed_app_ids` 已在公开模型中存在，但没有 install/uninstall command，始终只是空投影；
- `SurfaceService.CreateSurface` 接收 `app_instance_id`，但仓库尚不存在可信的安装实例事实；
- `runtime-host` 仍为 scaffold，不能让客户端随意提交 app ID/version/manifest 并冒充可运行实例。

依赖顺序固定为：

```text
已完成：canonical manifest → immutable Registry version
  → 本任务：Project installation → pinned version/digest → stable app instance ID
  → 下一任务：minimal Web Bundle Surface（只接受已安装实例）
  → 后续：App Bridge + capability grant/token + Runtime runner
```

直接实现 Surface 会迫使 runtime-host 信任任意 `app_instance_id`，或自行查询/解析 Registry 数据，制造跨进程
直接耦合。因此本任务完成后，Project/Desktop installation 可以升为 working；Runtime / Surface 仍必须
保持 scaffolded。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时本地 `main` 为 `f033c8f`，领先 `origin/main` 16 个提交；执行时必须重新检查，不能把
  哈希或 ahead 数量当作永久事实。
- App Registry 位于 `internal/core/appregistry`，数据由 `002_app_registry.sql` 和
  `003_app_registry_idempotency.sql` 持有；这些 migration 已执行且 checksum 保护，禁止修改。
- Project 位于 `internal/core/project`；`projects.installed_app_ids text[]` 由已执行的
  `001_foundation.sql` 创建，当前没有任何公开 mutation 会写入非空值。
- Project mutation 使用 optimistic revision，并把 Project event/outbox 与数据库 mutation 放在同一事务；
  新安装命令必须共享这条 revision/event 序列，不能建立第二套 Project revision。
- App Registry 的 `Get(owner, app_id, version)` 已支持空 version 取当前 SemVer、显式 version 取 immutable
  version；Project 模块不得直接查询 `app_versions`。
- `internal/core/orchestration/project_directory.go` 已展示中立 application bridge 模式；反向解析 App 时也
  必须通过 port/orchestration，不能跨模块导入 adapter 或 SQL。
- Gateway 只公开 allowlist 中的 Core 服务；`SurfaceService` 当前仍应保持 public 404。
- `runtime-host` 的 `SurfaceService` 是 Unimplemented，`surface-broker` capability 明确 unavailable。
- PostgreSQL acceptance volume 含已有验收数据、6 个历史 migration scratch database 和历史分页 fixture；
  禁止删除 volume 或顺手清理历史数据。

## 凭据与安全边界

- 本任务不需要真实 DeepSeek、OpenAI、GitHub 或其他 Provider Key。
- 不得使用、保存、转述、验证或尝试恢复聊天中曾出现的真实 Key；不得从 shell history、环境变量、本机
  文件或聊天历史搜集凭据。
- 所有 DeepSeek 验收继续使用仓库已有的本地 fixture 假凭据，不访问真实 Provider 网络。
- 安装事实只保存 Registry identity、version、digest 和必要投影，不保存 raw/canonical manifest、真实
  credential 或用户内容全文。
- Registry `permissions` 在安装阶段仍只是 requested permissions，不得生成 grant、bridge token、Agent
  token 或暗示权限已经批准。
- App 不得读取模型或外部服务凭据；本任务不得新增任何 credential 字段。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`
   - `README.md`
   - `CONTRIBUTING.md`
   - `docs/structure.md` 中 Project、App Runtime、Surface、第一版产品边界章节
   - `docs/architecture/implementation.md`
   - `docs/decisions/0001-foundation-boundaries.md`
   - `docs/status.json`
   - `docs/tasks/20260823-app-manifest-registry.md`
   - `docs/prompts/20260823-next-agent-app-manifest-registry.md` 及三轮审核 Prompt
   - `api/proto/workos/app/v1/app.proto`
   - `api/proto/workos/project/v1/project.proto`
   - `api/proto/workos/surface/v1/surface.proto`
   - `schemas/workos-app-manifest-v1.schema.json`
   - `internal/core/appregistry` 与 `internal/core/project` 的 domain/application/ports/adapters/transport/tests
   - `internal/core/orchestration`、`cmd/workos-core/main.go`、`internal/gateway` 与测试
   - `internal/platform/migrations`、001/002/003、`sqlc.yaml`
   - `sdk/protocol`、`sdk/agent-sdk`、`apps/desktop-web` 和现有 integration/restart/E2E 测试
2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -10
   git branch -vv
   git diff --check
   ```

   保留所有既有改动；不得 reset、rebase 或覆盖用户文件。

3. 从当前本地 `main` 创建独立分支，建议 `feat/project-app-installation`。禁止直接在 `main` 实现，不要
   merge 或 push。
4. 从 `docs/tasks/TEMPLATE.md` 创建 `docs/tasks/20260825-project-app-installation.md`，状态先设为 active，
   写清契约、安装身份、version pinning、revision/idempotency、表所有权和验收。
5. 记录基线并运行：

   ```sh
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败必须保留证据并判断归属；禁止通过删除 PostgreSQL volume、放宽断言或固定成功响应绕过。

## 目标链路

完成并证明以下真实用户链路：

```text
Desktop App Library
  → Gateway public AppInstallationService
  → Core identity
  → owner Project + expected revision
  → neutral App Registry application port
  → resolve current or explicit immutable App version
  → one Project-owned PostgreSQL transaction
       installation/tombstone + idempotency result
       + installed_app_ids projection + Project revision
       + project event + outbox
  → List/Get Project after Core restart
  → uninstall through the same boundary
```

至少注册两个安全的合成 App manifest，安装其中一个，证明另一个不会出现在该 Project 的安装状态；再注册
目标 App 的更高版本，证明既有安装仍固定在原 version/digest，不会静默升级。

## 协议优先

跨 Go/TypeScript/Gateway 的契约必须先修改 `api/proto`，然后立即运行 `make generate`，再实现 producer 和
consumer。不得先手写同义 DTO。

推荐新增 additive 文件 `api/proto/workos/app/v1/installation.proto`，继续使用
`workos.app.v1` package，并定义独立的 `AppInstallationService`，不要把“已注册”和“已安装”重新混为同一
RPC。建议最小契约如下；字段名可以按仓库证据微调，但语义不能丢失：

```text
AppInstallation
  id                  // UUIDv7；也是后续 Surface 的 app_instance_id
  project_id
  app_id
  version             // 安装时固定的 immutable version
  manifest_digest     // 安装时固定的 digest
  installed_at        // UTC
  optional uninstalled_at

InstallAppRequest
  idempotency_key
  project_id
  app_id
  version             // 空值表示“在本次命令中解析 current 并固定”
  expected_project_revision

InstallAppResponse
  installation
  project_revision

UninstallAppRequest
  idempotency_key
  project_id
  installation_id
  expected_project_revision

UninstallAppResponse
  installation        // tombstoned result，便于确定重放
  project_revision

ListInstalledAppsRequest
  project_id
  page

ListInstalledAppsResponse
  active installations only
  page
```

RPC 至少包含 `InstallApp`、`UninstallApp`、`ListInstalledApps`。若增加 `GetInstallation`，必须有本任务的
明确调用方或测试价值，不能为未来猜测扩张契约。

### 契约要求

- v1 字段号不复用；只做 additive 变更；删除字段/枚举必须 reserved。
- Proto 注释明确：installation ID 是持久 Project App instance identity，但本任务不代表 workload 已运行。
- `ListInstalledApps` 只列 active installation，按 app ID 稳定排序；page size 默认 50、上限 100、负值
  `InvalidArgument`，repository 使用 effective limit+1，恰好满最后一页不得伪造 token。
- `Project.installed_app_ids` 保持兼容公开投影：只含 active app IDs，唯一、稳定排序。
- response 不返回 raw/canonical manifest，不返回 credential，不声称 permissions 已授权。
- Gateway 仅新增 `AppInstallationService` public allowlist；`SurfaceService`、Workload private commands 和其他
  private Core/host API 仍必须返回 404。

## 安装语义与不变式

### 安装身份与版本固定

- 每次成功的新安装生成 UUIDv7 installation ID；该 ID 将在下一任务作为 `app_instance_id` 使用。
- 一个 Project 同一时间最多有一个 active installation 对应同一 app ID：

  ```text
  (owner_user_id, project_id, app_id, active) → at most one
  ```

- 请求 version 为空时，只在第一次成功命令中解析 Registry current；持久化 exact version + digest。之后
  Registry 出现更高版本，既有 installation 不变。
- 显式 version 必须按 App Registry 的 SemVer 规则验证并准确解析；未知 version、其他 owner 的 app、未知
  app 均返回净化的 `NotFound`，不能泄漏存在性。
- `scope=user|project` 的 owner-scoped custom App 可以安装到该 owner 的 active Project；任何意外出现的
  `scope=system`/trusted 事实必须 fail closed，不能借安装路径绕过 Registry public policy。
- 同一 app 已安装相同 version/digest：新 key 在 revision 正确时可以作为确定 no-op 返回已有实例；不得
  创建第二个 active row、事件或 revision。
- 同一 app 已安装不同 version：返回稳定的 `AlreadyExists` 或 `FailedPrecondition`（选择一个并统一文档、
  transport 和测试），不得隐式升级。升级属于后续独立命令。
- 卸载只 tombstone/deactivate installation，不删除 App Registry version。重新安装必须使用新 key 并产生
  新 UUIDv7 instance；旧 idempotency replay 不得把已卸载实例重新激活。
- archived Project 不允许安装或卸载；unknown/foreign Project/installation 对外统一 `NotFound`。

### Project revision 与并发

- `expected_project_revision > 0` 为必填 optimistic concurrency token。
- 真正改变 active installation 集合的 install/uninstall 必须将 Project revision 精确加一，并在同一事务
  更新 `updated_at`、安装事实、Project 投影、event、outbox 和 idempotency result。
- 普通 `UpdateProject`、Harness binding、archive、install 和 uninstall 必须竞争同一 Project revision；
  不能让两个并发 mutation 使用同一 expected revision 都成功。
- stale revision 映射 `Aborted`；数据库 constraint/UPDATE rows affected 必须裁决并发，禁止进程内 mutex 或
  read-then-write 假原子性。
- no-op replay 不得重复增加 revision 或重复 event/outbox。
- Project event stream sequence 继续等于 Project revision。事件建议使用
  `project.app.installed.v1` / `project.app.uninstalled.v1`，payload 只含稳定 ID、app/version/digest、revision，
  不含 manifest 全文。

### 幂等语义

- `(owner_user_id, idempotency_key)` 在 install/uninstall command 范围内必须唯一持久化；同 key 同 canonical
  request 返回第一次结果，同 key 不同 action/Project/app/version/revision 返回 `Aborted`。
- 幂等 key 的裁决优先于重新解析空 version 的 current：第一次空 version 安装固定到 v1 后，即使 Registry
  current 已变为 v2，同 key 重试仍返回第一次 v1 结果，不能漂移或冲突。
- uninstall 的成功结果在 tombstone 后仍可重放；不能因 active row 已不存在而把相同 key 变成 NotFound。
- 失败请求不得消费 key，除非设计明确记录了可重放的确定业务结果；选择必须写入任务记录并由数据库测试
  证明，不能只依赖进程内缓存。
- idempotency key 使用与 Registry/Project 一致的 UTF-8、控制字符和长度边界；请求 digest 不混入时间戳或
  随机 ID。

## 模块边界与组合

- installation state 属于 `workos-core` 的 Project 边界；推荐放在
  `internal/core/project/domain|application|ports|adapters/postgres` 中，或建立同等清晰且不会让 Project
  repository 跨模块 SQL 的子模块。
- 推荐建立独立 `project/application.InstallationService`，避免把 App Registry 依赖塞进现有 Project CRUD
  service 并形成构造环。
- Project application 只依赖中立 `AppCatalog`/`InstallableAppDirectory` port。由
  `internal/core/orchestration` adapter 包装 App Registry application service；Project 禁止导入
  appregistry adapter、sqlc package、Connect/Proto 或直接查询 `app_versions`。
- App Registry 也不得反向查询 installation 表；Registry immutable facts 与 Project install facts各自有唯一
  owner。
- Domain 继续满足 `domain → application → ports ← adapters`，禁止数据库、HTTP、Connect、Proto、文件
  系统或其他模块 adapter 导入 Domain。
- composition root 可以依次构造 Project CRUD、App Registry、App directory bridge、InstallationService；
  不得用 package global、service locator 或 import cycle 绕过依赖。

## 数据模型与 migration

新增下一编号的 forward migration，预期为 `004_project_app_installations.sql`。禁止修改 001/002/003，
禁止 squash 已执行 migration。

### 唯一事实源

- 新的 Project-owned installation table 是安装实例的 authoritative fact，至少保存：UUIDv7 ID、owner、
  Project、app ID、pinned version、manifest digest、installed UTC、optional uninstalled UTC。
- 使用数据库约束保证 Project owner 绑定、ID/version/digest 形态、一个 Project/app 只有一个 active row；
  历史 tombstone 可保留以支持重装和审计。
- 不要给 Project-owned table 建立指向 App Registry table 的跨模块外键，也不要在 Project SQL 中 join
  `app_versions`。安装时通过 application port 校验后复制 immutable reference（app ID/version/digest）。
- idempotency/result mapping 必须持久化，且能在 installation 已 tombstone 后重放 uninstall。可使用独立
  request table；具体列写入任务记录并以数据库约束保证 owner/key 唯一。

### `installed_app_ids` 兼容投影

`projects.installed_app_ids` 已由 001 创建，但它不能与 installation table 成为两个独立裁决源。必须明确
选择并证明一种方案：

1. 保留该列作为 transactionally maintained derived projection：只有 Project repository 的同一事务从
   active installation facts 计算并写入，普通 `UpdateProject` 不能接收或覆盖它；或
2. 通过新的 forward migration 移除物理数组，Project query 从 Project-owned installation table 派生公开
   字段，并对已有非空数据给出安全、可测试的迁移策略。

推荐优先方案 1，以兼容现有 001 和 Project queries；但必须用事务、稳定排序、唯一约束和集成测试证明
不会出现“安装成功但数组未更新”或相反情况。禁止 transport 同时发两次独立写请求，禁止用后台 eventually
consistent job 修补当前用户可见状态。

### migration 验证

- 新 migration 必须能在 pristine database 与当前持久 acceptance volume 前向执行。
- 使用现有已加固的 scratch database helper；测试前后记录精确 scratch database 集合，连续运行 migration
  tests 两次必须零新增。
- 不得修改 001/002/003 checksum，不得删除现有 volume、6 个历史 scratch database 或历史 Registry rows。
- 每张新表和 migration owner 必须在任务记录与实现架构中明确为 Core Project Installation。

## Connect 与错误映射

- owner 只从 identity context 取得，绝不接受 request 中的 owner ID。
- malformed UUID/app ID/SemVer/idempotency/page token/revision → `InvalidArgument`。
- unknown、foreign 或不可见 Project/App/installation → `NotFound`，避免 existence oracle。
- stale Project revision、同 key 不同 request → `Aborted`。
- 已安装不同 version 的业务冲突 → 统一的 `AlreadyExists` 或 `FailedPrecondition`。
- 数据库/内部错误 → 固定净化 `Internal`；错误不能回传 SQL、constraint 名、DSN、manifest 或 owner details。
- identity 缺失 → `Unauthenticated`。

## Desktop App Library 最小 UX

本任务包含最小用户入口，但不实现 App window 或 Surface：

- 在 `sdk/agent-sdk` 的统一 clients 中加入 App Registry 与 App Installation clients；只使用生成协议类型。
- Desktop 为 active Project 提供明确的 “App Library” 入口，列出该 owner 在 Project context 可用的 Registry
  current Apps，并标识 active installation 与 pinned version。
- 支持 Install 和 Remove；操作使用 `crypto.randomUUID()` idempotency key 和当前 Project revision。
- 成功后以服务端返回 revision/重新读取的 Project 与 installation list 为准，不能本地猜测数组。
- revision conflict 时重新读取最新 Project 和 installation list，并显示可理解反馈；不得自动用新 revision
  重放用户 mutation。
- Project 切换、组件卸载或请求乱序后，旧 Project 的 list/install/uninstall response 不能污染当前 Project；
  使用 project identity + generation/token 或等价机制，并增加延迟 Promise 测试。
- 有 loading、empty、error/retry、saving 状态；失败后按钮可恢复，不泄漏后端错误。
- 不实现 manifest 编辑/上传、App launch、Dock pin、窗口、iframe、Surface URL 或 bridge。

## 必须测试的行为

### Domain/application/transport

- current 与 explicit version 安装均固定 exact version/digest；Registry 新版本不改变旧 installation。
- owner、Project、App、installation 隔离；archived Project fail closed。
- invalid input、错误码净化和无 identity。
- same key replay、same key different request/action、失败 key 未消费。
- same app same target no-op 与 different target conflict。
- uninstall replay、重装生成新 instance ID、旧 install key replay不复活 tombstone。
- page default/clamp/negative/exact-final-page、稳定排序和非空数据库。
- requested permissions 不变成 grant/token。

### PostgreSQL/concurrency/events

- pristine + current-volume migration。
- active unique constraint、pinned digest、UTC、UUIDv7、tombstone 和 installed_app_ids 投影一致。
- install/uninstall/project update 并发使用同 expected revision 时恰好一个 winner，loser `Aborted`。
- 多进程/多 repository instance 并发，不靠 mutex；数据库最终只有一个 active fact、一个 revision event、一个
  outbox result。
- transaction 任一步失败时 installation、Project projection/revision、event/outbox、idempotency mapping 全部
  回滚。
- event sequence 与 Project revision 连续；no-op/replay 不重复事件。
- Core restart 后 installation/list/Project projection 与幂等 replay 仍成立。

### Gateway/Desktop/E2E

- Gateway 公开 AppInstallationService，伪造 identity headers 被覆盖；Surface/private service 继续 404。
- App Library component tests 覆盖 loading/empty/error、安装/卸载、revision conflict、Project switch 和 stale
  async response。
- 浏览器 E2E：通过真实 Gateway 注册合成 App → 在 App Library 安装 → Project 显示 active app → reload
  后仍存在 → 卸载 → reload 后消失。
- E2E 不能通过直接改数据库冒充用户链路；数据库只读断言可作为补充。

## 明确不在范围内

- App upgrade/downgrade、自动跟随 Registry current 或版本解析策略 UI。
- bundle/archive 上传、Git clone、image pull/build、签名、Artifact storage。
- `runtime-host` runner、Podman/container、Workload start/stop、cgroup enforcement。
- Surface session、Web Bundle hosting、reverse proxy、iframe、WebRTC、App window 或 Dock launch。
- App Bridge、MessageChannel、bridge token、capability grant/token、approval、预算或 Credential Vault。
- system/trusted App 安装路径。
- Artifact、Reliability、Indexer、Mobile、LAN pairing 或生产 device authentication。
- 修改 Harness、DeepSeek、Task Router 或 Provider binding 语义。
- 真实 Provider 网络或真实 Key。

## 文档与状态

完成时同步：

- `docs/tasks/20260825-project-app-installation.md`：最终契约、数据库 authority/projection、幂等/revision、测试
  命令、未决风险与下一步。
- `docs/architecture/implementation.md`：增加 Project Installation 链路、表 owner、App Registry port 和事件
  边界。
- `docs/status.json`：只按真实 E2E 证据更新 Project/Desktop evidence；App Registry 保持 working，
  Runtime / Surface 必须仍为 scaffolded。
- README 状态区块只用生成工具更新，禁止手改。
- `docs/structure.md` 原则上不改；若实现必须偏离产品主线，先写 ADR，而不是顺手改愿景。

任务记录必须明确下一任务是 **minimal Web Bundle Surface backed by installed app instance**，并列出仍缺的
bundle artifact/hosting 契约；不得把本任务描述成 App 已经运行或可打开。

## 验收顺序

### 基础与生成

```sh
make generate
make generate
git diff --check
make check
buf breaking --against '.git#branch=main'
```

第二次 generation 后必须无新增差异；Proto、Go、TypeScript 和 README/status 生成物一致。

### 数据与纵向

```sh
make test-integration
make test-integration
make test-deepseek-fixture
make test-e2e
```

- 两次 integration 前后分别记录 scratch database 精确集合、新 installation/request/event/outbox 行数和本轮
  唯一 fixture IDs；不能只看退出码。
- 测试资源需要清理时，只能精确清理本轮 run-unique IDs，并在单一事务中按 FK 顺序执行；不得 broad
  `LIKE` DELETE、TRUNCATE、wildcard DROP 或删除 volume。
- DeepSeek 门禁只使用 fixture target 自带假 credential。

### 最终一致性

```sh
git diff --check
git diff --check main...HEAD
git diff --exit-code main -- internal/platform/migrations/files/001_foundation.sql
git diff --exit-code main -- internal/platform/migrations/files/002_app_registry.sql
git diff --exit-code main -- internal/platform/migrations/files/003_app_registry_idempotency.sql
git status --short --branch
```

另外确认：

- 只有预期的新 migration，001/002/003 未改变；
- `docs/structure.md` 无意外变化；
- 无 root-owned 文件、临时产物、raw manifest 或 credential；
- `runtime-host` capability 仍如实报告 Surface/runner unavailable；
- worktree 最终干净，功能分支提交聚焦；未 merge、未 push。

## 完成与交接

- 完成所有范围内实现，不以 TODO、空 adapter、固定响应或只测 fake repository 冒充 working。
- 将任务状态改为 done，仅在真实跨 Gateway/Core/PostgreSQL/Desktop/restart 证据成立后更新 working evidence。
- 在 `feat/project-app-installation` 创建聚焦提交；提交信息建议：
  `feat: implement project app installation`。
- 最终交接必须写明提交哈希、实际运行命令、migration 前后证据、两次 integration 资源计数、未决风险、
  下一任务依赖和 worktree 状态。
- 不要 merge 到 `main`，不要 push；留给审核者静态复审和本地 `--ff-only` 合并。
