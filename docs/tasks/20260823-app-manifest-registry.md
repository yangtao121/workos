# Task: App Manifest Registry vertical slice

- 状态：done（2026-08-23 审核修复完成，全部验收门禁重新通过）
- Owner/Agent：app registry builder
- 进程/模块：workos-core `internal/core/appregistry`；workos-gateway allowlist 复用；`schemas/` embed
- 依赖：canonical `workos-app-manifest-v1.schema.json`、`workos.app.v1` 契约、`001_foundation.sql`、Project application（ListApps project 上下文校验）

## 目标与范围

把一个版本化 App manifest 变成可安全校验、幂等注册、按 owner 查询并在 Core 重启后仍存在的 Registry 事实：

```text
owner-authenticated client → Gateway public AppRegistryService
  → Core manifest byte/structure guard → YAML-to-JSON normalization
  → canonical v1 JSON Schema validation → semantic security policy
  → App Registry domain/application → Core-owned PostgreSQL app version
  → deterministic WorkOSApp projection → Get/List after Core restart
```

范围内：`ValidateManifest`/`RegisterApp`/`GetApp`/`ListApps` 完整实现；单一 canonical Schema 驱动的校验、规范化与 digest；owner-scoped 不可变 App version 存储与幂等注册；SemVer current version；新 Core-owned forward migration 与 sqlc 配置；identity middleware、安全错误映射；单元/传输/PostgreSQL 集成/Gateway 纵向/重启持久化测试；文档与状态同步。

不在范围内：Project install/uninstall（`installed_app_ids` 不变）、bundle 上传/构建/签名、runtime-host runner、Surface/iframe/App Bridge、capability token、权限授予、Credential Vault、system/trusted App 公共注册、Artifact/Reliability/Indexer、真实 Provider 网络。

## 设计

### Schema 是唯一事实源

- `schemas/workos-app-manifest-v1.schema.json` 通过同目录 `schemas/embed.go`（`//go:embed`）在 canonical 路径原地嵌入，不复制第二份。
- 校验链固定为：`untrusted YAML bytes → structural safety checks → JSON-compatible value → canonical JSON bytes → JSON Schema validator（仅嵌入资源，无网络）→ semantic security policy → small public WorkOSApp projection`。
- 违规输出：确定性排序（kind, JSON Pointer 路径, 消息）、去重、最多 32 条、单条 ≤ 256 字符，只含字段路径与规则说明，不含原始 YAML value。

### Canonicalization 与 digest（确定性规则）

1. 输入上限 256 KiB（transport 与 application 双层）。
2. 只接受单一 YAML document；根必须 mapping；key 必须字符串；拒绝 duplicate key、alias/anchor、merge key、自定义 tag、`!!timestamp`/`!!binary` 等非 JSON 标量；递归深度 ≤ 32、总节点 ≤ 20000。
3. 全部字符串必须有效 UTF-8 且不含 C0/C1 控制字符与 NUL；`name` 额外拒绝首尾空白；不做 trim 或 Unicode normalize（显式规则：不合法即拒绝，数据库与响应不各自处理）。
4. 结构树 → canonical JSON：object key 按字节序排序；int 按 int64 十进制、float 按 `strconv.FormatFloat('g',-1,64)`、bool/null 确定编码；字符串用 encoding/json 转义语义；数组保序。
5. Schema 校验通过后，`permissions`（Schema `uniqueItems`、语义为集合）排序为字典序再产出最终 canonical bytes（有专项测试：YAML whitespace/key order 等价 → 相同 digest；语义变化 → 不同 digest）。
6. digest = `sha256:<lowercase hex>`，基于最终 canonical bytes；不混入 raw YAML、路径、时间戳、owner、idempotency key。
7. idempotency request digest 即 manifest digest（请求体只有 manifest）；同 key 同 digest → 重放原结果，同 key 不同 digest → 确定冲突。

### semantic security policy（Schema 之后）

- public registration 仅接受 `scope: user|project`；`scope: system` fail closed。
- public registration 拒绝 `runtime.type: trusted`（后续受信安装路径独立实现）。
- permissions 必须属于 Core 集中定义的 vendor-neutral capability vocabulary（domain 单一定义、全链路复用），未知 capability fail closed；permissions 只是 requested permissions，Registry 不生成 grant/token。
- key/value 扫描拒绝明显 secret-bearing 内容（api key/secret/password/token/credential/private key 等命名，与 `sk-…`、`Bearer …`、PEM 私钥头等值形态）；错误只报告字段路径。此检查是安全 policy，不替代 Credential Vault/DLP。

### App version、幂等与查询语义

- 不变式：`(owner_user_id, app_id, version) → exactly one manifest_digest`；`(owner_user_id, idempotency_key) → exactly one registration`。均由数据库 UNIQUE 约束保证，并发 winner 由约束决定，不靠进程内 mutex。
- 同 owner、同 app/version、同 digest 重试（无论 key）→ 返回原记录；同 app/version、不同 digest → `AlreadyExists`；同 idempotency key、不同 manifest → `Aborted`。
- version 按 Schema 的 SemVer 子集（`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`）验证；current version 使用完整 SemVer precedence（release > 对应 prerelease；numeric identifier 按数值比较，`1.10.0 > 1.9.0`、`rc.10 > rc.2`），在 Go domain 比较器中实现并测试。
- `GetApp(app_id)` 空 version → current；`GetAppRequest` 新增 additive `string version = 2`，显式值返回该 immutable version；缺失/他人 owner → `NotFound`。
- `ListApps`：每个 app ID 只返回 current version，按 app ID 稳定排序，page size 默认 50、上限 100，cursor 为 last app ID。`project_id` 非空时先经中立 application port（orchestration adapter，不导入 Project adapter/SQL）验证 Project 属于 owner 且未归档，再返回该 owner Project 上下文的 Registry catalog；它不是 installation state，`Project.installed_app_ids` 不变。

### 数据所有权与 migration

- 新增 `internal/platform/migrations/files/002_app_registry.sql`（Core App Registry 独占 owner；`001_foundation.sql` 不变；可在空库与现有 volume 前向执行）。
- 表 `workos_core.app_versions`：`id uuid`（UUIDv7 row id）、`owner_user_id`、`idempotency_key`、`request_digest`、`app_id`、`version`、`scope`、`name`、`permissions text[]`、`manifest_digest`、`canonical_manifest jsonb`、`created_at timestamptz`（UTC）；UNIQUE `(owner_user_id, app_id, version)`、UNIQUE `(owner_user_id, idempotency_key)`，CHECK 约束收紧 id/version/scope/digest 形态。
- 独立 sqlc 配置 `internal/core/appregistry/adapters/postgres`（package `appdb`）；Project/Agent repository 不查询 App 表，App 也不查询 Project/Agent 表。

### 分层与错误映射

```text
internal/core/appregistry: domain → application → ports ← adapters/postgres
                                                      ↑ transport/connect
```

- domain 不导入 YAML、Schema validator、pgx/sqlc、Connect、Proto、文件系统或其他模块 adapter；SemVer、scope、permission vocabulary、canonical JSON 纯逻辑放 domain；YAML/Schema 适配放 `adapters/manifestvalidator`。
- owner 只从 identity context 获取；Core composition root 组装 validator、repository、project directory adapter 与 service；公开 handler 经 identity middleware。
- 错误映射：malformed → `InvalidArgument`；missing/other owner → `NotFound`；immutable version 冲突 → `AlreadyExists`；idempotency 冲突 → `Aborted`；内部错误 → 净化 `Internal`；identity 缺失 → `Unauthenticated`。`ValidateManifest` 对普通无效输入返回 `valid=false + violations`，transport/内部故障才用 Connect error。
- 日志不包含 raw YAML、canonical manifest 全文或数据库 JSON。

## 协议/数据影响

- Additive v1：`GetAppRequest.string version = 2`；其余 `workos.app.v1` 不变（`ListApps(project_id)` 语义为 owner Registry catalog，非 installed list，Proto 注释、任务记录与测试一致）。
- 新 migration `002_app_registry.sql`（owner: workos-core App Registry）。
- 新 Go 依赖：`github.com/santhosh-tekuri/jsonschema/v6`（draft 2020-12 校验，loader 仅嵌入资源）；`schemas/embed.go` 原地嵌入 canonical Schema；Dockerfile build stage 增加 `COPY schemas`。
- Gateway allowlist 不变（AppRegistryService 已在 public allowlist；private 服务仍 404）。

## 验收

- [x] Manifest validator：结构安全、Schema 违规、policy fail closed、digest 等价性、violation 上限/排序/去重
- [x] Domain/application/transport：幂等/冲突/owner 隔离/SemVer current/显式 version/分页/Project 上下文/错误净化
- [x] PostgreSQL：migration 空库+现有 volume 前向执行；并发注册由真实约束裁决；Gateway 注册→Get/List→Core 重启持久化
- [x] `make generate` 二次执行无差异
- [x] `make check`
- [x] `make test-integration`
- [x] `make test-deepseek-fixture`
- [x] `make test-e2e`
- [x] `buf breaking --against '.git#branch=main'`
- [x] README（生成）、`docs/architecture/implementation.md`、`docs/status.json` 同步；`docs/structure.md` 不变

## 实现 supplement

- 新增 Go 依赖 `github.com/santhosh-tekuri/jsonschema/v6 v6.0.2`（draft 2020-12）；schema loader 只注册嵌入资源，其余 URL 一律拒绝，remote `$ref` 无网络路径。Dockerfile build stage 增加 `COPY schemas` 与 `ENV GODEBUG=netdns=cgo`（该开发网络 DNS 对 Go 纯解析器返回不可达 AAAA；cgo/glibc 解析返回 IPv4），Makefile `GO_RUN`/`GO_HOST_RUN` 与 compose fixture 同步注入该 GODEBUG。
- 违规消息不使用库的默认文本（`Pattern`/`Format` 会回显实例值），由 kind+JSON Pointer 重新生成；`AdditionalProperties` 不回显未知字段名。
- 版本有效性 = Schema pattern + 非空 prerelease 标识符（domain `ParseVersion`，validator 与 application 双层执行），`1.0.0-`、`1.0.0-.rc1`、`rc.01` 拒绝。
- capability vocabulary（集中定义于 domain）：`agent.event.watch`、`agent.task.run`、`artifact.read`、`artifact.write`、`knowledge.read`、`project.read`。
- `app_versions` 列：`id uuid PK`（UUIDv7）、`owner_user_id → users(id)`、`idempotency_key`（1..128）、`request_digest`（sha256 hex64）、`app_id`、`version`、`scope IN ('user','project')`、`name`（1..80）、`permissions text[]`、`manifest_digest`、`canonical_manifest jsonb`、`created_at timestamptz`；UNIQUE `(owner_user_id, app_id, version)`、UNIQUE `(owner_user_id, idempotency_key)`；CHECK 收紧全部形态。owner 由 gateway dev 身份注入的既有 users 行提供。
- Register 事务内裁决：先按 idempotency key 查（digest 不同 → `Aborted`），insert `ON CONFLICT DO NOTHING`，rows=0 时按 (owner, app, version) 再分类（同 digest 重放 / 不同 digest `AlreadyExists`），最后按 key 分类并发同 key 同请求。
- `ListApps` = DISTINCT app_id 分页 + 按 app 批量取版本 + Go domain SemVer current；current 选择不依赖 SQL 文本序。

## 交接

实现前基线（2026-08-23 UTC）在 clean `main`（`484c057`）上全部通过：`make bootstrap`、`make check`、
`make test-integration`、`make test-e2e`（integration 内含 Project 纵向 + task restart persistence）。

## 2026-08-23 审核结论（修复前，原“最终证据”中以下声明不成立）

静态审核确认原实现（`8e48d92`）存在 6 个阻断项，本文档此前记录的下列证据不真实或不完整，已作废：

1. “每个 owner/idempotency key 恰好一个 registration”不成立：新 key 在“version 已存在且 digest 相同”
   的成功路径上不持久化（事务回滚丢弃映射），key 复用可注册不同 manifest；并发裁决顺序也会先按
   version 返回成功而忽略已提交的 key 冲突。
2. “同 version/digest 不同 key 的持久化重放”只断言了首次响应，未复用新 key 验证映射已写入；8 并发
   同 digest 测试未验证数据库中的 request mapping 数量。
3. “secret 4 子项”证据不准确：password/private-key/token 三个 case 通过在合法 manifest 后追加第二个
   `resources` 根字段构造，首先因 duplicate mapping key 被拒绝，secret policy 实际未被触发。
4. 默认/上限分页声明不成立：transport 用原始 page_size 生成 next token，默认 50 上限 100 时第 51+ 条
   不可达，page_size>100 同样不可达，恰好装满最后一页时产生指向空页的假 token。
5. Connect/body 层 256 KiB 边界缺失：限制发生在 Connect 解码之后，超大 protobuf/JSON/压缩请求可在
   字段检查前占用无界内存。
6. GetApp/ListApps 物化全部版本及完整 canonical_manifest，内存与传输不受 page size 约束；
   mapping key 控制字符未校验可进入 JSON Pointer/数据库；idempotency key/app_id/cursor/project_id
   边界校验缺失或落到 pgx Internal。
7. Makefile `GO_RUN` 重复设置 HOME、`GO_HOST_RUN` 将 GOPATH 误改为 HOME；`GODEBUG=netdns=cgo`
   作为机器相关 workaround 被硬编码进全部 Go runner、Docker build stage 与 DeepSeek fixture compose。

修复要求见 `docs/prompts/20260823-review-app-manifest-registry.md`；修复完成并重新验收前，
`docs/status.json` 中 App Registry 保持 `scaffolded`。

## 2026-08-23 修复设计（针对上述阻断项）

### 幂等：独立权威 mapping（阻断项一）

- 新前向 migration `003_app_registry_idempotency.sql`（Core App Registry owner）：
  - 新表 `workos_core.app_registration_requests`：`PRIMARY KEY (owner_user_id, idempotency_key)`、
    `request_digest`（sha256 hex CHECK）、`app_version_id`；复合外键
    `(owner_user_id, app_version_id) → app_versions (owner_user_id, id) ON DELETE RESTRICT`
    fail closed（key 永远不能绑定他人 version）；`app_versions` 增加
    `UNIQUE (owner_user_id, id)` 支撑该复合外键。
  - backfill：从 002 的 `app_versions` 逐行复制 `(owner, idempotency_key, request_digest, id,
created_at)` 到 mapping，随后 `DROP` 旧列与旧 `(owner, idempotency_key)` UNIQUE 约束——旧列不保留，
    mapping 是唯一幂等事实源，不可能出现两个裁决源。
- Register 单事务裁决顺序（无进程内 mutex、无先查后写竞态、不依赖 constraint 名字符串）：
  1. 按 `(owner, key)` 读 mapping：digest 不同 → `Aborted`；相同 → 返回 mapping 指向的 immutable
     version（重放）。
  2. `InsertAppVersion ON CONFLICT DO NOTHING`：rows=0 时按 `(owner, app, version)` 再查——digest 不同
     → 复查 key（并发消费优先裁决为 `Aborted`），否则 `AlreadyExists`；digest 相同 → 在同一事务内为新
     key 写入 mapping 后提交（成功即持久化）。
  3. 新 version 插入成功后写 mapping：`ON CONFLICT DO NOTHING` rows=0 说明并发同 key 已提交，复读
     mapping 分类为重放或 `Aborted`，失败侧回滚不留 orphan version。
- PostgreSQL 证据（`tests/integration/app_registry_test.go` + `app_registry_migration_test.go`）：
  K1→M、K2→M 同 version 成功后 K2→N 必须 Aborted；8 个不同 key 并发注册同一 manifest 全部成功且
  DB 恰有 8 条 mapping、1 个 version，随后每个 key 换请求均 Aborted；同 key 并发两个不同 version 的
  manifest 一胜一 Aborted 且失败侧无 version 行；同 version 不同 digest 一胜一 AlreadyExists；
  003 在空库与 002 数据前向执行均通过、backfill 后 mapping 指向原 version、旧列被删除、
  002 文件 checksum 固定守卫。

### 分页（阻断项二）

- page size 只在 application 规范化一次：`0` → 默认 50，`>100` → clamp 100，`<0` → `InvalidArgument`
  （不再静默默认）；transport 不再按原始 page_size 猜测 token。
- `ports.Repository.ListAppIDPage` 以 `LIMIT effective+1` 探测，仅当确实存在额外记录时返回
  nextCursor（最后一条已返回 app ID）；application 返回 `PageResult{Items, NextToken}`，transport
  原样转发 token。
- 证据：>100 个 app 下 nil page 首屏恰 50 条且可达、page_size=101 clamp 100 且可达、总页数补齐后
  恰好装满的最后一页无 token、全量翻页无重复/无遗漏/严格递增、畸形 cursor/negative page size 为
  `InvalidArgument`。

### Pre-decode 请求上限（阻断项三）

- `transport.NewConnectHandler`（composition root 与测试共用）以
  `connect.WithReadMaxBytes(384 KiB = 393216)` 在 Connect 解码前限制每条解压后请求消息：
  - 选择依据：合法 256 KiB manifest 的 protojson base64 膨胀约 349.6 KiB + 字段名/标点与 ≤128 字节
    idempotency key；384 KiB 覆盖两种编码并留余量，仍为明确小常量（库默认无限制）。
  - 压缩请求按解压后大小裁决（gzip bomb → `ResourceExhausted`），非仅 Content-Length。
  - application/transport 的 256 KiB manifest 字段检查保留；超限错误为稳定
    `ResourceExhausted`/`InvalidArgument` 净化文本，不回显 body。
  - 证据：真实 Connect HTTP handler 上 512 KiB proto 请求被拒且业务 stub 未执行；256 KiB manifest
    在 proto 与 protojson（base64 膨胀）编码下均正常处理；压缩 bomb 被解压上限拒绝。

### Key 安全面与 secret policy（阻断项四）

- mapping key 在结构阶段（任何 pointer 构造、map 插入、Schema 校验、持久化之前）校验：有效 UTF-8、
  无 C0/C1/NUL 控制字符、1..256 rune；unsafe key 只报告父路径，原始 key 不进 violation。
- secret key policy 改为 tokenization：按非字母数字边界与 camelCase 驼峰切分后整词匹配
  （secret/password/passwd/pwd/passphrase/token/credential(s)/auth/authorization/bearer/apikey 等）
  与相邻词组（api+key、api+secret、private+key）；`accessToken`、`clientSecret`、`credentialValue`、
  `awsSecretAccessKey` 命中，`keyboard`、`monetization`、`sort_order`、`displayHint` 不误杀。
- secret 测试重写：注入合法 manifest 既有 resources/health/maintainer 自由块内（不再追加重复根字段），
  逐 case 断言具体路径与 policy 消息，值全部为明显合成串且不被回显。

### Bounded 读取与请求字段边界（阻断项五）

- 公开 Get current / List current 只选择 summary 列（不含 canonical_manifest）；repository 以
  app ID 有序流式读取（`VisitVersionSummaries` visit 回调），application 以单候选 accumulator 折叠
  SemVer current——内存受 page size 上限约束，不随历史 version 总数线性增长；current 仍由 domain
  SemVer 比较器决定（不依赖 SQL 文本序、created_at）。
- 显式 version 查询同样只读 summary；canonical manifest 仅存于 `app_versions`，公开响应与日志不包含。
- 请求边界：idempotency key（UTF-8、无控制字符、1..128 rune、不 trim）、app_id 与 cursor
  （canonical `^[a-z][a-z0-9-]{2,62}$`）、project_id（UUID）在 application 边界校验，畸形值为
  `InvalidArgument`，未知/他人/归档 project 仍为净化 `NotFound`。

### 工具链回归修复（阻断项六）

- Makefile 恢复 main 语义：`USER_FLAGS` 唯一 HOME=/tmp；`GO_RUN`/`GO_HOST_RUN` 均保留
  `GOPATH=/tmp/workos-go`，不再重复 HOME。
- 移除全部硬编码 `GODEBUG=netdns=cgo`（Makefile、Dockerfile build stage、compose fixture）。修复时
  在同网络环境下探测纯 Go 解析器（`GODEBUG=netdns=go go mod download`）成功，无跨环境复现的必要性
  证据，故按审核要求完全移除而非引入默认关闭的 override；若未来环境复现 AAAA 问题，应以显式选择
  的最小范围 override 加证据重新引入，不得恢复全局硬编码。
- Dockerfile 保留 `COPY schemas`（schema embed 必需）；fixture 假 credential、网络隔离与测试目标
  语义不变。

## 2026-08-23 修复后最终证据

分支 `feat/app-manifest-registry`（基于 `8e48d92`，修复提交见 git log），全部命令实际执行：

- `make generate` 连续两次：通过，第二次无差异（gen/go、sdk/protocol/src/gen、README 一致；本任务无
  Proto 变更，`buf breaking --against '.git#branch=main'` 通过）。
- `make check`：通过（buf format/lint/vet、sqlc vet、gofmt、go vet/test 全部 internal 包、架构守卫、
  eslint、prettier、web build、status render --check）。
- Go 单元测试：appregistry domain 8 + manifestvalidator 10（结构安全、schema 违规、**secret 11 子项
  （6 个 secret-bearing key + 5 类合成 value 形态，均注入合法自由块并断言具体路径与 policy 消息）+
  tokenization 25 断言 + unsafe mapping key 5 子项**、digest 等价、violation 上限/排序）+ application 10
  （幂等映射持久化语义 fake 镜像、并发 key/版本裁决、分页归一化/clamp/负数/精确末页/畸形 cursor/
  project UUID 边界、owner 隔离、内部错误不冒充域错误）+ transport 9（HTTP 级 pre-decode 上限：
  512 KiB proto 拒绝且业务 stub 未执行、256 KiB manifest 在 proto 与 protojson 编码下通过、gzip bomb
  按解压后大小拒绝、token 转发/恰好装满无假 token/畸形 cursor）全部通过。
- `make test-integration`：通过。`TestAppRegistryVerticalSlice` 9 个子测试全部 PASS：
  - `IdempotencyMappingIsDurableAndAuthoritative`：K1→M、K2→M 成功后 **K2→N 为 Aborted**；DB 直查
    确认 2 条 mapping、1 个 version。
  - `ConcurrentRegistrationsAgreeOnOneFact`：**8 key 并发同 manifest 全成功、DB 恰 8 mapping、
    1 version、随后每个 key 换请求均 Aborted**；**同 key 并发不同 version 一胜一 Aborted、失败侧无
    version 行**；同 version 不同 digest 一胜一 AlreadyExists。
  - `ListAppsPagingDefaultsClampAndExactFinalPage`：105+ app 下 nil page 首屏恰 50 且可达、
    page_size=101 clamp 为 100 且可达、全量翻页无重复/无遗漏/严格递增、**补齐至整页倍数后恰好装满的
    末页无 token**、畸形 cursor/project_id/app_id/负 page size 均 `InvalidArgument`。
  - `RequestSizeBoundariesHoldOnTheWire`：256 KiB+1 → `InvalidArgument`；512 KiB wire 请求 →
    `ResourceExhausted` 且错误不回显 body。
  - `ListAppsProjectContext`（分页 walk）、`TrustBoundaryFailsClosed`（secret key 注入 resources 块）、
    Validate digest 等价、SemVer current 均通过。
  - `TestAppRegistryMigrationsFromEmptyDatabase`：001+002+003 空库全录、`app_registration_requests`
    存在、**app_versions 旧幂等列已删除**、system scope 数据库拒绝、**002 文件 checksum 固定守卫**。
  - `TestAppRegistryMigration003BackfillsExistingVolumeData`：**模拟现有 volume（手铺 001+002 + 002
    era 数据）前向执行 003**：legacy key backfill 后 mapping digest/version 指向正确；双 owner 同 key
    隔离成立；复合外键拒绝跨 owner 绑定；PK 拒绝同 owner 重复 key；畸形 digest 拒绝。
  - 既有 Project 纵向通过；task restart 与 app registry restart（digest 重推导、current/list 恢复）
    verified（真实 volume 上 003 已由 bootstrap 前向执行并 backfill 31 行 002 数据）。
- `make test-deepseek-fixture`：通过（Go fixture 测试 + restart + Playwright fixture spec 1 passed）。
- `make test-e2e`：通过（foundation spec 1 passed，fixture-only spec 按设计 skipped）。
- `git diff --check`、`git diff --check main...HEAD`：通过；`docs/structure.md` 无变化；
  `002_app_registry.sql` 与 `8e48d92` 中内容逐字节一致（仅新增 003 前向 migration）；无 root-owned
  文件；`workos_workos-postgres` volume 未删除；未 merge、未 push、未使用真实 Key（全部 fixture 假
  credential，未访问真实 Provider 网络）。

### 原实现证据（历史保留，部分已被上文审核结论推翻）

最终证据（2026-08-23 UTC，分支 `feat/app-manifest-registry`）：

- `make generate` 连续两次：通过，第二次无差异（gen/go、sdk/protocol/src/gen、README 一致）。
- `make check`：通过（buf format/lint/vet、sqlc vet、gofmt、go vet/test 20 个 internal 包、架构守卫、eslint、prettier、web build、status render --check）。
- Go 单元/适配器测试：appregistry domain 7 + manifestvalidator 9（含结构安全 13 子项、schema 12 子项、secret 4 子项）+ application 6 + transport 5 + orchestration project directory 1，全部通过。
- `make test-integration`：通过。`TestAppRegistryVerticalSlice` 7 个子测试（Validate 摘要/YAML 等价 digest、幂等重放与 Aborted/AlreadyExists、SemVer current `1.10.0` 与显式版本、排序分页、Project 上下文含 archived→NotFound、system/trusted/未知 capability/secret key fail closed、并发一胜一负 + 8 并发同 digest 重放）+ `TestAppRegistryMigrationsFromEmptyDatabase`（空库 001+002、`app_versions` 存在、system scope 被数据库约束拒绝）+ 既有 Project 纵向；task restart 与 app registry restart（`app-seed`/`app-verify`，digest 重推导、current/list 恢复）均 verified。
- `make test-deepseek-fixture`：通过（Go fixture 测试 + restart + Playwright fixture spec）。
- `make test-e2e`：通过（foundation spec 1 passed，fixture-only spec 按设计 skipped）。
- `buf breaking --against '.git#branch=main'`：通过（`GetAppRequest.version` additive）。
- `git diff --check`、`git diff --check main...HEAD`：通过；`docs/structure.md` 未变化；无 root-owned 文件；未使用真实 Key（仅仓库既有 fixture 假 credential）；PostgreSQL volume `workos_workos-postgres` 未删除。
- 提交：`feat: implement app manifest registry`；提交后 `git status --short` 为空；未 merge、未 push。

限制与后续：

- 未实现 Project install/uninstall（`Project.installed_app_ids` 保持只读默认）、App bundle 分发、runtime-host runner、Surface/iframe、App Bridge、capability token、权限授予与 Credential Vault；`docs/status.json` 只把 App Registry 恢复 `working`，Runtime/Surface 仍 `scaffolded`。
- secret 形态扫描是注册门禁的安全 policy，不替代 Credential Vault/DLP；生产设备认证仍缺位，public 边界仍依赖 loopback + dev bypass。
- `app_versions.canonical_manifest` 目前仅存储、尚无内部消费方；未来 install/runtime 任务按需读取。
- 若未来开发环境复现 Go 纯解析器 AAAA 不可达问题，以调用方显式选择的最小范围 override 加证据处理，不得恢复全局硬编码 `netdns=cgo`。
- 下一任务建议：Project install/uninstall + minimal Web Bundle Surface（依赖本任务的 immutable version 与 digest）。
