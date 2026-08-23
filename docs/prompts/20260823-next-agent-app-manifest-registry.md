# 下一位智能体 Prompt：App Manifest Registry 纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、文档和提交，不是只输出计划。

## 你的角色

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。Foundation、DeepSeek Harness、
Harness Catalog 与 Project Binding UX 已完成并合并到本地 `main`；你的任务是实现下一条严格限定的
纵向切片：**App Manifest 校验与 App Registry 持久化**。

本任务只把一个版本化 App manifest 变成可安全校验、幂等注册、按 owner 查询并在重启后仍存在的
Registry 事实。不要顺手实现 Project 安装、App Runtime、Surface、iframe、App Bridge、Agent
capability token、Artifact、Credential Vault 或权限授予。那些能力依赖本任务，但属于后续独立任务。

持续推进直到完整验收通过并提交到功能分支。只有遇到必须破坏 v1 契约、改变六进程所有权、需要引入
新的信任根，或现有 `ListApps(project_id)` 语义与本文推荐方案存在无法兼容的仓库证据时，才停止并向
用户说明冲突与可选方案。

## 为什么下一步是 App Registry

当前 `docs/status.json` 的事实是：Project、Harness Catalog、Task Router、Harness Broker 和 Desktop
纵向链路已经 `working`，而 App Registry 仍是 `contract-only`，Runtime / Surface 仍是
`scaffolded`。`docs/structure.md` 的第一版顺序接下来是 Custom Web App Surface，但现有系统还没有可信的
App identity、manifest digest、版本事实或 owner-scoped registry。直接做 iframe 或容器会迫使 Runtime
自行解析 manifest、信任客户端提交的 runtime descriptor，或制造第二套 DTO。

因此依赖顺序固定为：

```text
本任务：canonical manifest → immutable App version → Registry persistence
  → 后续：Project install/uninstall + Web Bundle Surface
  → 后续：App Bridge + capability-scoped Agent API
```

本任务完成后只能把 **App Registry** 升为 `working`；Runtime / Surface、App Agent API 和 Desktop App
Library 仍必须如实保持未实现状态。

## 当前仓库事实

- 六个稳定进程边界不变：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- `AppRegistryService` 契约位于 `api/proto/workos/app/v1/app.proto`，当前在 Core 注册的是
  `UnimplementedAppRegistryServiceHandler`。
- Gateway 已将 `AppRegistryService` 列入 public service allowlist，但生产 device session 尚未实现；
  默认开发环境只能在 loopback + explicit dev bypass 下使用。
- canonical manifest 唯一 Schema 是 `schemas/workos-app-manifest-v1.schema.json`，`apiVersion` 为
  `workos.app/v1`。不得复制或手写另一套同义 Schema/DTO 作为第二事实源。
- `RegisterAppRequest` 接收 `manifest_yaml`，`WorkOSApp` 目前只公开 ID、name、version、scope、
  requested permissions 与 manifest digest。
- `Project.installed_app_ids` 已存在，但当前没有 install/uninstall command。本任务禁止直接改数组、直接
  SQL 或把“已注册”冒充“已安装”。
- `SurfaceService`、`WorkloadService` runner、`app-sdk` 和 `app-host` 仍是 contract/scaffold；本任务不实现。
- 当前只有 `001_foundation.sql`。新 App Registry 表必须由 Core App Registry 单独拥有，使用新的前向
  migration；禁止修改已执行的 `001_foundation.sql`。
- 当前本地 `main` 在开始编写本 Prompt 时指向 `c21f0f3`，并领先 `origin/main`。执行时必须重新检查，
  不得把哈希当成永远不变的事实，不得 reset 或丢弃已有提交。
- PostgreSQL volume 包含验收数据。禁止 `docker compose down -v` 或删除 volume。

## 凭据与隐私边界

- 本任务不需要任何真实 DeepSeek、OpenAI、GitHub 或其他 Key。
- 不得使用、保存、转述或尝试恢复聊天中曾出现的真实 DeepSeek Key；不得从 shell history、进程环境、
  本机文件或聊天历史搜集凭据。
- manifest 是配置声明，不是 secret 容器。不得把 API Key、password、bearer token、private key、
  credential value 或其片段写入 manifest fixture、数据库、日志、错误、task record 或测试快照。
- 不记录或回显原始 `manifest_yaml`。错误只返回有界的 JSON Pointer/字段路径和安全规则说明。
- `permissions` 在本阶段只是 capability request，绝不代表已授权；响应、README 和状态证据必须明确。
- public `RegisterApp` 不得允许普通 App 自称 system/trusted App。`scope=system` 或
  `runtime.type=trusted` 的注册需要未来独立的受信安装路径，本任务应 fail closed。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`
   - `README.md`
   - `CONTRIBUTING.md`
   - `docs/structure.md`
   - `docs/architecture/implementation.md`
   - `docs/decisions/0001-foundation-boundaries.md`
   - `docs/status.json`
   - `docs/tasks/20260823-foundation.md`
   - `docs/tasks/20260823-harness-catalog-binding-ux.md`
   - `schemas/workos-app-manifest-v1.schema.json`
   - `api/proto/workos/app/v1/app.proto`
   - `api/proto/workos/project/v1/project.proto`
   - `internal/core/project` 的 domain/application/ports/Postgres/transport 与测试
   - `internal/platform/migrations`、`sqlc.yaml`、`cmd/workos-core/main.go`
   - `internal/gateway` 及测试
   - `sdk/protocol`、`sdk/app-sdk`、`clients/app-host`
   - 现有 integration/E2E 测试与 Make targets
2. 运行 `git status --short --branch`、`git log --oneline --decorate -10`、`git branch -vv`，保留所有既有
   改动。若工作树不干净，先区分用户改动，不得覆盖。
3. 从当前本地 `main` 创建独立分支，建议 `feat/app-manifest-registry`。不要在 `main` 直接实现。
4. 从 `docs/tasks/TEMPLATE.md` 新建 `docs/tasks/20260823-app-manifest-registry.md`，状态先设为
   `active`，写明范围、Schema/Proto/migration 影响、版本语义、表所有权与验收。
5. 运行基线：

   ```bash
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败时记录证据、判断是否与本任务有关；不要靠删除数据库 volume 或放宽测试解决。

## 目标链路

完成并证明以下真实链路：

```text
owner-authenticated client
  → Gateway public AppRegistryService
  → Core manifest byte/structure guard
  → YAML-to-JSON normalization
  → canonical v1 JSON Schema validation
  → App Registry domain/application
  → Core-owned PostgreSQL app version
  → deterministic WorkOSApp projection
  → Get/List after Core restart
```

至少使用两个语义不同但安全的测试 manifest，证明注册的不是固定成功响应或空 adapter。

## 范围内

- 完整实现 `ValidateManifest`、`RegisterApp`、`GetApp`、`ListApps`。
- 单一 canonical JSON Schema 驱动的 YAML 校验、规范化和 digest。
- owner-scoped、不可变 App version 存储与幂等注册。
- 确定的 current version、显式 version 查询和稳定分页语义。
- App Registry 的 domain/application/ports/Postgres adapter/Connect transport 分层。
- 新的 Core-owned forward migration 与 sqlc 配置/生成代码。
- Core identity middleware、Gateway public boundary 与安全错误映射。
- 单元、传输、PostgreSQL 集成、Gateway 纵向与重启持久化测试。
- README、实现架构、任务记录和 `docs/status.json` 同步。

## 明确不在范围内

- Project install/uninstall，禁止修改 `Project.installed_app_ids`。
- App bundle/archive 上传、Git clone、image pull、build 或签名分发。
- `runtime-host`、Podman/container/native runner、Workload start/stop。
- Surface session、iframe、Web Bundle/Web Service 代理、App Library、Dock 或 Desktop UI。
- App Bridge、MessageChannel、bridge token、Agent API capability token。
- 实际权限授权、预算、审计后端或 Credential Vault。
- system/trusted App 的公共注册。
- Artifact、Reliability、Indexer、Mobile、LAN pairing 或生产认证。
- 修改 Harness、DeepSeek、Project binding 或 Agent Task 语义。
- 真实 Provider 网络测试或真实 Key。

## Schema 是唯一事实源

必须直接加载/嵌入 `schemas/workos-app-manifest-v1.schema.json`；禁止把该文件复制到另一路径，禁止在 Go
struct tags、Proto 或 TypeScript 中再维护一套完整 manifest 规则。

推荐把流程明确分成：

```text
untrusted YAML bytes
  → structural safety checks
  → JSON-compatible value
  → canonical JSON Schema validator
  → semantic security policy
  → small public WorkOSApp projection
```

允许 App Registry domain 保存执行后续任务真正需要的 normalized manifest value，但字段合法性仍由
canonical Schema 决定。`WorkOSApp` 是公开摘要，不是第二套 manifest。

### 输入与 YAML 安全

至少满足：

- manifest 输入设置明确上限，建议不超过 256 KiB；Connect/body 层与 application 层都不能无界处理。
- 只接受一个 YAML document；拒绝空文档、多文档、非 map 根节点、非字符串 key、duplicate key、alias、
  anchor、merge key、自定义 tag、循环结构与非 JSON 标量。
- 不执行模板、环境变量展开、shell substitution、文件 include 或网络引用。
- JSON Schema 的 remote `$ref` 不得触发网络访问；当前 Schema 必须完全从仓库本地解析。
- violation 顺序确定、去重且有数量/单条长度上限；不得包含原始 YAML value。
- `ValidateManifest` 对普通无效输入返回 `valid=false + violations`；transport/内部故障使用 Connect error，
  两者不能混淆。

不要为了方便放松 canonical v1 Schema。若发现 Schema 与 `docs/structure.md` 示例不一致，只实现当前
Schema 能明确表达的注册能力，并在任务记录列为后续 contract task；收紧或破坏 v1 Schema 必须新版本或
ADR，不能顺手改变已发布语义。

### semantic security policy

Schema 通过后仍需执行与信任边界有关、但不属于 JSON 形状的策略：

- public registration 只接受 custom `user` / `project` scope；拒绝 `system`。
- public registration 拒绝 `runtime.type=trusted`。
- manifest 及其自由结构块中不得出现明显的 secret-bearing key 或 credential material；错误只报告字段
  路径，不报告值。此检查是安全 policy，不得宣称能替代 Credential Vault/DLP。
- permissions 必须是已知、vendor-neutral capability ID；未知 capability fail closed。当前仓库公开的
  capability vocabulary 应集中定义并测试，不能在 transport/UI 各写一份列表。
- permissions 只是 requested permissions；Registry 不生成 grant/token。
- 字符串必须是有效 UTF-8，控制字符、NUL、异常超长集合和可造成日志/UI 注入的值要拒绝或按明确规则
  规范化。

## Canonicalization 与 digest

在任务记录中先写出 deterministic canonicalization 规则，再实现。最低要求：

- digest 基于 Schema 校验后的 canonical JSON，不基于原始 YAML 格式。
- object key 排序确定；数字、bool、null 和字符串编码确定；数组顺序除非 Schema 明确表示集合，否则保留。
- 可以对 `permissions` 这类 Schema 已声明 `uniqueItems` 且语义为集合的字段排序，但必须测试并记录。
- 对 name 等用户可见字符串是否 trim/Unicode normalize 必须有一个明确规则；不能数据库与响应各自处理。
- digest 格式固定为 `sha256:<lowercase hex>`，使用常量时间不属于必要条件，但比较语义必须明确。
- 同一语义、不同 YAML whitespace/key order 的 manifest 得到相同 digest；任何有意义字段变化得到不同
  digest。

不得把 raw YAML、绝对文件路径、时间戳、owner ID 或 idempotency key 混入 manifest digest。

## App version 与幂等语义

App version 是不可变事实，不是可原地覆盖的 mutable row：

```text
(owner_user_id, app_id, version) → exactly one manifest_digest
```

必须满足：

- 同 owner、同 app ID/version、同 digest 的重试返回原记录。
- 同 app ID/version、不同 digest 返回确定的 `AlreadyExists` 或 `Aborted`，不得覆盖旧 manifest。
- 同 owner、同 idempotency key、同 normalized request 返回第一次结果。
- 同 idempotency key、不同 request 返回确定冲突。
- 不同 owner 可使用相同 app ID/version，互不可见。
- 并发相同 key、并发相同 app version 必须有数据库约束与确定 winner，不靠进程内 mutex。
- version 必须按 Schema 的 SemVer 子集验证；current version 使用明确、测试充分的 SemVer precedence，
  不得按普通字符串比较。release 高于对应 prerelease。
- `GetApp(app_id)` 空 version 返回 current；为了后续安装固定版本，优先给 `GetAppRequest` 增加 additive
  `string version = 2`，显式值返回该 immutable version。若静态审核发现更合适的既有兼容方案，可调整，
  但必须保留“显式版本可查”和“空值确定返回 current”两个行为。
- `ListApps` 每个 app ID 只返回 current version，按 app ID 稳定排序，page size 有上限，cursor 可恢复且
  不泄漏其他 owner 信息。

### `ListApps(project_id)` 语义

当前字段存在但仓库没有安装模型。不要把它解释为“已安装列表”。本任务推荐并必须在 Proto 注释、任务
记录和测试中统一：

- 空 `project_id`：列出当前 owner 注册的 current app versions。
- 非空 `project_id`：先通过 Project application port 验证该 Project 属于 owner 且未归档，再返回可用于
  该 owner Project 上下文的 Registry catalog；它仍不是 installation state。
- App Registry 不直接 SQL 查询 Project 表，不导入 Project adapter；需要校验时使用中立 orchestration
  或 application port。
- `Project.installed_app_ids` 保持不变，安装语义留给下一任务。

若已有契约测试或文档明确证明 `ListApps(project_id)` 必须表示已安装列表，停止实现该语义，在任务记录
给出“保留旧 RPC + 新增 ListRegisteredApps”与“先实现 Project installation”两种 additive 方案，征求
用户确认。禁止静默猜测。

## 数据所有权与 migration

- 新增 `002_app_registry.sql`（若执行时编号已占用则使用下一个编号），禁止修改 `001_foundation.sql`。
- 表和 migration 由 `workos-core` App Registry 独占；其他进程未来只能通过 port/RPC 获取 manifest。
- 建议持久化 owner、app ID、version、scope、normalized public projection、canonical manifest、digest、
  idempotency request digest、current 标记和 UTC timestamps；最终列设计写入任务记录。
- 数据库约束必须落实 immutable version、owner idempotency 与单一 current version；不能只靠 Go 检查。
- migration 可从空库执行，也必须能在当前 foundation 数据 volume 上前向执行。
- 为 App Registry 增加独立 sqlc query/package 配置；禁止让 Project/Agent repository 直接查询 App 表。
- 时间统一 UTC，若新增资源 ID 使用 UUIDv7。不要用递增 ID 或客户端提供的 owner ID。
- 日志不包含 canonical manifest 全文、raw YAML 或数据库 JSON。

## 分层与错误映射

建议模块为 `internal/core/appregistry`，保持：

```text
domain → application → ports ← adapters/postgres
                         ↑
                    transport/connect
```

- domain 不导入 YAML、JSON Schema validator、pgx/sqlc、Connect、HTTP、Proto、文件系统或其他模块 adapter。
- YAML/Schema 解析可作为 application port 的 adapter，或放在不污染 domain 的明确边界；在任务记录说明。
- Core composition root 组装 validator、repository 和 service；公开 handler 必须经过 identity middleware。
- owner 不从 request body/manifest 获取，只从可信 identity context 获取。
- malformed input → `InvalidArgument`；missing/other owner → `NotFound`；immutable version/idempotency conflict →
  `AlreadyExists`/`Aborted`（选择后全链路一致）；数据库内部错误 → 净化的 `Internal`。
- 不回传 SQL、constraint name、文件路径、raw YAML、secret-like value 或内部 validator stack。
- Gateway 只公开 App Registry，不新增 Runtime/Surface/private service 转发。

## 必须覆盖的测试

### Manifest validator

- canonical valid user/project manifests；公开摘要映射完整。
- wrong/missing `apiVersion`、unknown root field、invalid ID/SemVer/scope/runtime/surface/permission。
- duplicate key、multi-document、alias/anchor/merge/custom tag、non-string key、non-map root、oversize。
- remote `$ref`/网络访问不可发生。
- violation 顺序、去重、数量和长度上限。
- secret-bearing key/value 不进入响应或日志，system/trusted registration fail closed。
- YAML whitespace/key order 等价 digest；语义变化不同 digest。
- canonical Schema 文件是实际加载对象；测试防止代码内第二套规则与 Schema 漂移。

### Domain/application/transport

- Validate 与 Register 对 invalid manifest 的行为不同且确定。
- owner scope、Get latest、Get explicit version、NotFound。
- SemVer current：prerelease/release、多位数字，不能出现 `1.10.0 < 1.9.0` 错误。
- same-version same/different digest；same idempotency same/different request。
- concurrent registration race 只有一个确定事实，不覆盖 immutable version。
- list stable order、page limits/cursor、owner isolation。
- 非空 Project context 的 owner/archived 检查，不直接访问 Project adapter/SQL。
- Connect code 与错误文本净化；identity missing 为 Unauthenticated。

### PostgreSQL / 跨进程

- migration 从空库成功，并在现有 volume 上成功。
- 通过 Gateway 注册两个 App/version，再 Get/List；Core 重启后 digest、current version、顺序与 owner scope
  保持。
- 并发/幂等由真实 PostgreSQL constraint 证明，不只用 mock repository。
- Gateway 仍拒绝 private Harness/TaskExecution/Runtime service；App Registry 之外 allowlist 不扩大。
- 现有 Project → Harness → Event、DeepSeek fixture 与 Desktop E2E 不回归。

## 文档与状态

- `docs/tasks/20260823-app-manifest-registry.md` 必须记录实际设计、migration owner、版本/幂等/List 语义、
  验证命令、风险与后续。
- 更新 `docs/architecture/implementation.md`，只描述已经实现的 App Registry 边界。
- 只有真实 Gateway→Core→PostgreSQL→restart 证据通过后，才把 `docs/status.json` 的 App Registry 改为
  `working`；evidence 应具体写 schema-backed immutable registration + persistence。
- Runtime / Surface、App SDK、App Host、Desktop 和 Access Gateway 的状态不得因本任务被抬高。
- README 状态区块只能由 `make docs`/`make generate` 生成，禁止手改生成区块。
- 明确记录下一任务建议为 Project install/uninstall + minimal Web Bundle Surface；不要在本提交实现它。

## 完整验收

实际运行并在任务记录中写结果：

```bash
make generate
make generate
make check
make test-integration
make test-deepseek-fixture
make test-e2e
buf breaking --against '.git#branch=main'
git diff --check
git diff --check main...HEAD
```

另外确认：

- 第二次生成无差异，README/status 生成一致。
- `docs/structure.md` 未变化；若真的需要改变产品主线，先写 ADR 并征求用户确认。
- 只有一个新 migration owner，没有修改旧 migration，没有跨模块直接 SQL。
- 无 root-owned 文件、无 untracked 测试产物、无真实 secret/Key/credential fixture。
- `workos_workos-postgres` volume 未删除。
- 默认与 DeepSeek fixture 全部使用仓库既有假 credential，不访问真实 Provider 网络。

## 提交与交接

- 全部验收通过后把任务状态改为 `done`。
- 使用 Conventional Commit，建议：

  ```text
  feat: implement app manifest registry
  ```

- 提交后 `git status --short` 必须为空。
- 不要 merge、push、rebase、force、删除分支或执行 `docker compose down -v`。
- 最终报告包含：分支与提交 ID、Schema/digest/version/List 语义、migration owner、测试数量、完整门禁结果、
  未决风险；明确未实现 install/Surface/App Bridge/权限授予，未使用真实 Key，未 merge、未 push、未删除
  volume。
- 由审核者进行静态复审、必要的修复 Prompt 和最终 `git merge --ff-only`。
