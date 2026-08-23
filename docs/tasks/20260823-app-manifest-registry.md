# Task: App Manifest Registry vertical slice

- 状态：done（2026-08-23 第二轮审核 5 个阻断项已修复，全部验收门禁重新通过；合并须经第三轮静态复审）
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

范围内：`ValidateManifest`/`RegisterApp`/`GetApp`/`ListApps` 完整实现；单一 canonical Schema 驱动的校验、规范化与 digest；owner-scoped 不可变 App version 存储与幂等注册；SemVer current version；Core-owned forward migration 与 sqlc 配置；identity middleware、安全错误映射；单元/传输/PostgreSQL 集成/Gateway 纵向/重启持久化测试；文档与状态同步。

不在范围内：Project install/uninstall（`installed_app_ids` 不变）、bundle 上传/构建/签名、runtime-host runner、Surface/iframe/App Bridge、capability token、权限授予、Credential Vault、system/trusted App 公共注册、Artifact/Reliability/Indexer、真实 Provider 网络。

## 设计（最终现状）

### Schema 是唯一事实源

- `schemas/workos-app-manifest-v1.schema.json` 通过同目录 `schemas/embed.go`（`//go:embed`）在 canonical 路径原地嵌入，不复制第二份。
- 校验链固定为：`untrusted YAML bytes → structural safety checks → JSON-compatible value → canonical JSON bytes → JSON Schema validator（仅嵌入资源，无网络）→ semantic security policy → small public WorkOSApp projection`。
- 违规输出：确定性排序（kind, JSON Pointer 路径, 消息）、去重、最多 32 条、单条 ≤ 256 字符，只含字段路径与规则说明，不含原始 YAML value。

### Canonicalization 与 digest（确定性规则）

1. 输入上限 256 KiB（application 字段检查与 Connect `WithReadMaxBytes` 384 KiB 双层，后者在解码前生效）。
2. 只接受单一 YAML document；根必须 mapping；key 必须字符串；拒绝 duplicate key、alias/anchor、merge key、自定义 tag、`!!timestamp`/`!!binary` 等非 JSON 标量；递归深度 ≤ 32、总节点 ≤ 20000。
3. 全部字符串必须有效 UTF-8 且不含 C0/C1 控制字符与 NUL；`name` 额外拒绝首尾空白；不做 trim 或 Unicode normalize（显式规则：不合法即拒绝，数据库与响应不各自处理）。
4. 结构树 → canonical JSON：object key 按字节序排序；int 按 int64 十进制、float 按 `strconv.FormatFloat('g',-1,64)`、bool/null 确定编码；字符串用 encoding/json 转义语义；数组保序。
5. Schema 校验通过后，`permissions`（Schema `uniqueItems`、语义为集合）排序为字典序再产出最终 canonical bytes（有专项测试：YAML whitespace/key order 等价 → 相同 digest；语义变化 → 不同 digest）。
6. digest = `sha256:<lowercase hex>`，基于最终 canonical bytes；不混入 raw YAML、路径、时间戳、owner、idempotency key。
7. idempotency request digest 即 manifest digest（请求体只有 manifest）；同 key 同 digest → 重放原结果，同 key 不同 digest → 确定冲突。

### mapping key 安全面与 semantic security policy（Schema 之后）

- mapping key 在结构阶段、任何 pointer 构造/map 插入/Schema 校验/持久化之前校验：有效 UTF-8、无 C0/C1/NUL 控制字符、1..256 rune；unsafe key 只报告父路径，原始 key 不进 violation。
- 两个独立概念：
  - secret-bearing 字段名称（accessToken、clientSecret、awsSecretAccessKey 等命名）按 tokenization（snake/kebab/camelCase 整词与相邻词组）命中，报告安全的字段路径；
  - key 本身形似 credential（prefixed token `sk-…`、JWT、AWS access-key ID、PEM/private-key header）由 key/value 共用的单一 credential-shape 规则在结构阶段拒绝，只报告父路径，绝不把原 key 拼进 JSON Pointer 或让它进入 canonical JSON 与数据库。
- public registration 仅接受 `scope: user|project`；`scope: system` 与 `runtime.type: trusted` fail closed。
- permissions 必须属于 Core 集中定义的 vendor-neutral capability vocabulary（domain 单一定义、全链路复用），未知 capability fail closed；permissions 只是 requested permissions，Registry 不生成 grant/token。
- key/value 的 credential/secret 检查是安全 policy，不替代 Credential Vault/DLP。

### App version、幂等与查询语义（最终现状）

- 不变式：`(owner_user_id, app_id, version) → exactly one manifest_digest`；`(owner_user_id, idempotency_key) → exactly one registration`。均由数据库约束保证（version 由 `app_versions` UNIQUE，key 由 `app_registration_requests` 主键），并发 winner 由约束决定，不靠进程内 mutex。
- 同 owner、同 app/version、同 digest 重试（无论 key）→ 返回原记录；同 app/version、不同 digest → `AlreadyExists`；同 idempotency key、不同 manifest → `Aborted`。失败请求不消费 key。
- version 按 Schema 的 SemVer 子集（`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`）验证；current version 使用完整 SemVer precedence（release > 对应 prerelease；numeric identifier 按数值比较，`1.10.0 > 1.9.0`、`rc.10 > rc.2`），在 Go domain 比较器中实现并测试。
- `GetApp(app_id)` 空 version → current；`GetAppRequest` additive `string version = 2`，显式值返回该 immutable version；缺失/他人 owner → `NotFound`。
- current/List 读取均为 summary 流式折叠：公开查询只选择 summary 列（不含 `canonical_manifest`），repository 以 app ID 有序流式读取（`VisitVersionSummaries`），application 以单候选 accumulator 折叠 SemVer current——内存受 page size 上限约束，不随历史 version 总数线性增长；canonical manifest 仅存于 `app_versions`，只在需要完整事实的内部路径读取，不进入日志或公共响应。
- `ListApps`：page size 只在 application 边界规范化一次（0 → 默认 50，>100 → clamp 100，<0 → `InvalidArgument`）；repository 以 effective limit+1 探测，仅当确有额外记录时返回 nextCursor；transport 原样转发 token。按 app ID 稳定排序；恰好装满的最后一页不产生 token。`project_id` 非空时先经中立 application port（orchestration adapter，不导入 Project adapter/SQL）验证 Project 属于 owner 且未归档，再返回该 owner Project 上下文的 Registry catalog；它不是 installation state，`Project.installed_app_ids` 不变。

### 数据所有权与 migration（最终现状）

- `002_app_registry.sql`（checksum 保护、已在验收 volume 执行，禁止修改）创建 `workos_core.app_versions`：immutable manifest 事实（id、owner、app_id、version、scope、name、permissions、manifest_digest、canonical_manifest、created_at），UNIQUE `(owner_user_id, app_id, version)`，CHECK 收紧形态。
- `003_app_registry_idempotency.sql`（前向 migration，Core App Registry owner）建立唯一幂等事实源 `workos_core.app_registration_requests`：`PRIMARY KEY (owner_user_id, idempotency_key)`、`request_digest`、复合外键 `(owner_user_id, app_version_id) → app_versions (owner_user_id, id) ON DELETE RESTRICT`（key 永不能绑定他人 version），并为该外键在 `app_versions` 增加 `UNIQUE (owner_user_id, id)`；随后从 002 行 backfill 映射并 **DROP** `app_versions` 的 `idempotency_key`/`request_digest` 列与旧 UNIQUE——映射表是唯一幂等权威，不存在两个裁决源。
- Register 单事务裁决（无进程内 mutex、无先查后写竞态、不依赖 constraint 名字符串）：先按 (owner, key) 读映射（digest 不同 → `Aborted`；相同 → 重放指向的 version）；`InsertAppVersion ON CONFLICT DO NOTHING`，rows=0 时按 (owner, app, version) 复查（digest 不同 → 复查 key 并发消费优先裁决 `Aborted`，否则 `AlreadyExists`；digest 相同 → 同事务为新 key 写映射后提交）；新 version 插入成功后原子消费 key（`ON CONFLICT DO NOTHING` rows=0 → 复读映射分类重放/`Aborted`，失败侧回滚不留 orphan version）。
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

## 协议/数据影响（最终现状）

- Additive v1：`GetAppRequest.string version = 2`；其余 `workos.app.v1` 不变（`ListApps(project_id)` 语义为 owner Registry catalog，非 installed list）。
- migrations：`002_app_registry.sql`（app_versions）与 `003_app_registry_idempotency.sql`（app_registration_requests + backfill + 旧列删除），owner 均为 workos-core App Registry；002 checksum 固定。
- 新 Go 依赖：`github.com/santhosh-tekuri/jsonschema/v6`（draft 2020-12 校验，loader 仅嵌入资源）；`schemas/embed.go` 原地嵌入 canonical Schema；Dockerfile build stage 增加 `COPY schemas`（embed 必需），无 `GODEBUG=netdns=cgo`。
- Gateway allowlist 不变（AppRegistryService 已在 public allowlist；private 服务仍 404）。

## 验收

- [x] Manifest validator：结构安全、Schema 违规、policy fail closed、credential-shaped key（4 形态）、digest 等价性、violation 上限/排序/去重
- [x] Domain/application/transport：幂等/冲突/owner 隔离/SemVer current/显式 version/分页/Project 上下文/错误净化
- [x] PostgreSQL：migration 空库+现有 volume 前向执行；并发注册由真实约束裁决（含 loser `AlreadyExists` 与 key 未消费证明）；Gateway 注册→Get/List→Core 重启持久化
- [x] 测试资源零残留：migration scratch database 连续两次零新增；高基数分页 fixture 精确清理并验证归零
- [x] `make generate` 二次执行无差异
- [x] `make check`
- [x] `make test-integration`
- [x] `make test-deepseek-fixture`
- [x] `make test-e2e`
- [x] `buf breaking --against '.git#branch=main'`
- [x] README（生成）、`docs/architecture/implementation.md`、`docs/status.json` 同步；`docs/structure.md` 不变

## 实现 supplement（最终现状）

- 违规消息不使用库的默认文本（`Pattern`/`Format` 会回显实例值），由 kind+JSON Pointer 重新生成；`AdditionalProperties` 不回显未知字段名。
- 版本有效性 = Schema pattern + 非空 prerelease 标识符（domain `ParseVersion`，validator 与 application 双层执行），`1.0.0-`、`1.0.0-.rc1`、`rc.01` 拒绝。
- capability vocabulary（集中定义于 domain）：`agent.event.watch`、`agent.task.run`、`artifact.read`、`artifact.write`、`knowledge.read`、`project.read`。
- Connect read max：`transport.NewConnectHandler` 以 `connect.WithReadMaxBytes(384 KiB = 393216)` 在解码前限制每条解压后请求消息。选择依据：合法 256 KiB manifest 的 protojson base64 膨胀约 349.6 KiB + 字段名/标点与 ≤128 rune 的 idempotency key（最坏 512 UTF-8 字节）；384 KiB 覆盖两种编码并留余量，仍为明确小常量（库默认无限制）。压缩请求按解压后大小裁决（gzip bomb → `ResourceExhausted`）。application/transport 的 256 KiB manifest 字段检查保留。
- 请求边界：idempotency key（UTF-8、无控制字符、1..128 rune、不 trim）、app_id 与 cursor（canonical `^[a-z][a-z0-9-]{2,62}$`）、project_id（UUID）在 application 边界校验，畸形值为 `InvalidArgument`，未知/他人/归档 project 仍为净化 `NotFound`。
- 工具链：Makefile `USER_FLAGS` 唯一 HOME=/tmp；`GO_RUN`/`GO_HOST_RUN` 均保留 `GOPATH=/tmp/workos-go`；无任何硬编码 `GODEBUG=netdns=cgo`（若未来环境复现 AAAA 问题，以显式选择的最小范围 override 加证据重新引入）。

## 交接

实现前基线（2026-08-23 UTC）在 clean `main`（`484c057`）上全部通过：`make bootstrap`、`make check`、
`make test-integration`、`make test-e2e`（integration 内含 Project 纵向 + task restart persistence）。

## 审核历史（压缩）

- 第一轮审核（`docs/prompts/20260823-review-app-manifest-registry.md`，针对 `8e48d92`）：确认 6 个阻断项——幂等映射未持久化、分页 token 用原始 page_size、256 KiB 限制晚于 Connect 解码、mapping key 未校验且 secret 测试在 duplicate-key 路径假通过、Get/List 物化全部版本、Makefile/GODEBUG 回归。修复提交 `42755bd`（003 migration、summary streaming、Connect read max、key guard、tokenization、Makefile 恢复）。
- 第二轮审核（`docs/prompts/20260823-review-2-app-manifest-registry.md`，针对 `42755bd`）：确认 5 个阻断项并全部在本轮修复——credential-shaped 字符串作为 map key 可入库、migration scratch database cleanup 在已关闭连接上执行、高基数分页 fixture 每次污染 acceptance volume、并发 immutable-version 测试未断言 loser 错误码与 key 消费、本任务记录顶部仍描述已删除的 002-only 结构。早期证据中不成立的部分已在第一轮结论中作废，不再与当前设计并列。

## 2026-08-23 第二轮修复后最终证据

分支 `feat/app-manifest-registry`（基于 `42755bd`，修复提交见 git log），全部命令实际执行：

- credential-shaped map key：
  - 单元（manifestvalidator）：`TestCredentialShapeRuleIsSharedByKeysAndValues` 固化 key/value 共用的单一 credential-shape 实现；`TestValidatorRejectsCredentialShapedMappingKeys` 在结构/Schema 合法的 manifest 中分别以 prefixed-token/JWT/AWS/PEM 形态 key 注入 resources/health/maintainer，全部命中 credential-material policy（非 duplicate key/Schema 错误）、只报告父路径（`/resources` 等 + 静态消息）、violation 不含 token 前缀/payload/完整或 escaped key、normalized manifest 与 digest 为空；邻近非 credential key（short_code、ski_rating、slack_channel）通过。
  - 集成（`TrustBoundaryFailsClosed`）：合成 `sk-…` key 经真实 Gateway→Core 链路 Validate 拒绝且 violation 不回显 token，Register 为净化 `InvalidArgument`，数据库 `app_versions`/`app_registration_requests` 对全部 deny 用例零写入。
- migration scratch database cleanup：`scratchDatabase` 的 DROP 在 cleanup 内部新建的 admin connection 上执行（helper 返回前显式关闭创建连接并检查错误），独立有界 context、`pgx.Identifier` 精确 quote 生成的库名、`DROP DATABASE … WITH (FORCE)`、DROP/close 失败 `t.Errorf` 暴露。新增 `TestScratchDatabaseCleanupDropsCreatedDatabase` 守卫“helper 返回即关闭 cleanup connection”回归（subtest 结束后库必须不存在）。连续两次运行 `TestScratchDatabaseCleanup|TestAppRegistryMigration*`：运行前 scratch 集合为 6 个历史残留（`workos_migration_test_1787498316725324135`、`…5495588`、`…439446423137`、`…439446610484`、`…480690229539`、`…480690854487`），每次运行后集合不变（零新增）；历史残留仅在此登记，未经授权未删除。
- 高基数分页 fixture cleanup：`ListAppsPagingDefaultsClampAndExactFinalPage` 以 run-unique stamp 追踪全部 bulk/pad 的精确 app ID 与 idempotency key 集合，subtest cleanup 在单一事务中先删对应 request mappings 再删对应 versions 并验证归零；清理集只含本次 stamp 派生 ID/key，无 LIKE/通配/清空表。连续两次运行纵向测试：bulk/pad 行在每次结束后均为 0（总量中 573 条 bulk-/pad- 全部为历史残留，仅报告未删除）；两次运行的总量增长仅来自非高基数子测试的固定增量（每轮 +9 versions/+17 requests），不因分页 fixture 增长。
- 并发 immutable-version：`ConcurrentRegistrationsAgreeOnOneFact` 的 digest-race 段结果 channel 携带 key/身份，明确断言 loser 为 `CodeAlreadyExists`；DB 验证仅保留 winner 的 version/digest、winner key mapping 指向 winner version、loser key 零 mapping；随后 loser key 注册不冲突新请求（5.0.1）成功、同 key 不同请求（5.0.2）`Aborted`——失败事务未错误消费 key。其余并发证据（8 key 同 manifest 全成功且恰 8 mapping/1 version、同 key 不同 version 一胜一 Aborted 且失败侧无 version 行）保持。
- Go 单元测试：appregistry domain、manifestvalidator（结构安全、schema 违规、secret 11 子项、credential-shaped key 4+邻居、tokenization、共享 shape 规则、unsafe mapping key、digest 等价、violation 上限/排序）、application、transport 全部通过。
- `make generate` 连续两次：通过，第二次无差异（gen/go、sdk/protocol/src/gen、README 一致）。
- `make check`：通过（buf format/lint/vet、sqlc vet、gofmt、go vet/test 全部 internal 包、架构守卫、eslint、prettier、web build、status render --check）。注：f1a5ff8 提交的 `docs/prompts/20260823-review-2-app-manifest-registry.md` 本身未过 prettier（基线失败），本轮仅按 prettier --write 重排格式，未改内容。
- `make test-integration`：通过（`TestAppRegistryVerticalSlice` 9 子测试、migration 空库/003 backfill、既有 Project 纵向、task restart 与 app registry restart 持久化 verified）。
- `make test-deepseek-fixture`：通过（Go fixture 测试 + restart + Playwright fixture spec 1 passed）。
- `make test-e2e`：通过（foundation spec 1 passed，fixture-only spec 按设计 skipped）。
- `buf breaking --against '.git#branch=main'`：通过。
- `git diff --check`、`git diff --check main...HEAD`：通过；`docs/structure.md` 无变化；`002_app_registry.sql` 与 `8e48d92` 逐字节一致、`003` 未修改、无新增 migration；无 root-owned 文件或 untracked 测试产物；`workos_workos-postgres` volume 未删除；未 merge、未 push、未使用真实 Key（全部 fixture 假 credential，未访问真实 Provider 网络）。

## 限制与后续

- 未实现 Project install/uninstall（`Project.installed_app_ids` 保持只读默认）、App bundle 分发、runtime-host runner、Surface/iframe、App Bridge、capability token、权限授予与 Credential Vault；Runtime/Surface 仍 `scaffolded`。
- secret/credential 形态扫描是注册门禁的安全 policy，不替代 Credential Vault/DLP；生产设备认证仍缺位，public 边界仍依赖 loopback + dev bypass。
- `app_versions.canonical_manifest` 目前仅存储、尚无内部消费方；未来 install/runtime 任务按需读取。
- 历史 573 条 bulk-/pad- 分页 fixture 行与 6 个历史 scratch database 仍在验收 volume/实例中，删除需用户明确授权。
- acceptance volume 中非高基数子测试每轮固定 +9 versions/+17 requests 的累积是既有测试形态，如需收敛应作为独立测试任务处理。
- 若未来开发环境复现 Go 纯解析器 AAAA 不可达问题，以调用方显式选择的最小范围 override 加证据处理，不得恢复全局硬编码 `netdns=cgo`。
- 下一任务建议：Project install/uninstall + minimal Web Bundle Surface（依赖本任务的 immutable version 与 digest）。
