# Task: App Manifest Registry vertical slice

- 状态：done
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

- 未实现 Project install/uninstall（`Project.installed_app_ids` 保持只读默认）、App bundle 分发、runtime-host runner、Surface/iframe、App Bridge、capability token、权限授予与 Credential Vault；`docs/status.json` 只把 App Registry 升为 `working`，Runtime/Surface 仍 `scaffolded`。
- secret 形态扫描是注册门禁的安全 policy，不替代 Credential Vault/DLP；生产设备认证仍缺位，public 边界仍依赖 loopback + dev bypass。
- 下一任务建议：Project install/uninstall + minimal Web Bundle Surface（依赖本任务的 immutable version 与 digest）。
