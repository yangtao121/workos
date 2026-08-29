# Task: ProjectService 持久幂等、分页与公开边界加固

- 状态：done
- Owner/Agent：next implementation agent (2026-08-29)
- 进程/模块：workos-core / internal/core/project（domain、application、ports、adapters/postgres、transport）+ cmd/workos-core 组合根
- 依赖：无新 Proto 变更；无 UI 变化；复用 installation repository 的 storeError 判定原则与 dbtransient 分类

## 目标与范围

把 public `workos.project.v1.ProjectService`（Create/Get/List/Update/Archive）收敛为
production-grade 契约，闭合链路：

```text
bounded public wire request
  → canonical application validation
  → owner-scoped PostgreSQL transaction
  → exact Create idempotency adjudication
  → project + event + outbox atomic commit
  → deterministic page result
  → sanitized Connect error
```

包含：

1. **CreateProject 持久精确幂等（ADR-0004）**：canonical request digest + 版本化首次响应
   快照，持久化于新 Core-owned authority 表 `workos_core.project_create_requests`
   （migration 013）。same owner+key+digest 跨请求/跨进程/跨重启精确重放第一次响应；
   same key/different digest 稳定 Aborted；失败不消费 key；Project 后来 Update/Archive
   不影响重放内容；历史（legacy）key 无原始请求记录，重放 fail closed（Aborted），
   不伪造 digest 或快照。
2. **输入验证与 wire 上限**：专用 Connect handler constructor
   （`projecttransport.NewProjectConnectHandler`）+ `connect.WithReadMaxBytes(128 KiB)`；
   application/domain 拥有可复用语义验证（明确上限、UTF-8/C0/C1、kind enum、nil item、
   重复 ref/mount、UUIDv7 Project ID 与 cursor、page size、expected revision、矛盾 update
   flags）。
3. **分页由 application 裁决**：application 规范化 page size 并以 limit+1 探测，返回明确
   page result（items + next token），transport 原样转发；默认 50、上限 100、负数
   InvalidArgument；恰好满页无伪 token。
4. **PostgreSQL 错误分类与安全映射**：基础 Project repository 的全部实际数据库 I/O
   （begin/query/scan/event/outbox/commit/幂等 lookup）统一走共享 `storeError`
   （`adapters/postgres/store.go`，与 installation 共用）；transport 固定净化错误矩阵。
5. **事务与 revision 不变量**：Create 的 project + create-request mapping + event +
   outbox 单事务；Update/Archive 的 guarded update + event + outbox 不变；event sequence
   = Project revision；失败事务零残留；Archive 先做存在性读取区分 NotFound 与 stale。

非目标（全部未做）：App Agent approval、quota/budget、Project UI、container/native
workload、新 Provider、credential vault、installation/Surface/App Bridge 协议修改、
新 RPC、全仓 pagination token 格式、通用 validation framework。

## 协议/数据影响

- Proto：none（api/proto 未变，field presence 语义未变）。
- Migration：`013_project_create_requests.sql`（owner：workos-core Project）新增
  `workos_core.project_create_requests`（PK `(owner_user_id, idempotency_key)`、
  `request_digest` CHECK `^sha256:[0-9a-f]{64}$`、`result` jsonb CHECK
  `result_version='1'`）。001–012 逐字节不变（本次新增的逐文件 checksum pin 集成测试
  钉住）。projects 表数据与约束全部保留，其 `UNIQUE (owner_user_id, idempotency_key)`
  继续作为并发插入的物理仲裁。
- Legacy 兼容策略（诚实、fail-closed，ADR-0004 §5）：013 之前创建的 Project 只有
  `projects.idempotency_key`，无 canonical request 记录与首次响应快照；对这些 key 的
  Create 重放统一 fail closed 返回 Aborted（与 digest conflict 同一净化消息，避免双消息
  存在性 oracle），不伪造任何事实。集成测试以显式 legacy fixture 证明：无 mapping 生成、
  无第二个 project。
- Event：none（沿用 `project.created.v1` 等，sequence = Project revision）。
- Digest 版本：`project.create/v1`（command marker 内嵌 canonical JSON body）。
- UI：无 UI 变化，因此不需要视觉记录（无 before/after/current 截图义务）。

## 验收

- [x] 行为测试（domain/application/transport 单元矩阵 + 真实 PostgreSQL 集成矩阵）
- [x] `make check`（含 proto lint/breaking、sqlc vet、go vet+test、TS 架构/eslint/prettier/web build、README 状态一致）
- [x] `make generate` 二次执行无生成差异
- [x] `make test-integration` 连续两次通过（含 restart persistence 链路与真实并发）
- [x] `make test-e2e` 通过；`make test-deepseek-fixture` 通过（keyless fixture）
- [x] `go test -race ./internal/core/project/...`
- [x] 文档与 `docs/status.json`（ADR-0004、implementation.md、README 状态区块由工具重生成）

## 交接

### Branch / Commit

- branch：`fix/project-service-contract-hardening`（自本地 main 3e2e428 创建）
- commit：`2249477`（原始契约实现）+ 修正轮提交（见 `git log`；未 merge、未 push）。
  修正轮的代码、测试、migration 与文档修复全部已提交，工作树无未提交残留——直接
  merge 本 branch 得到的是修复后版本，不会合入 2249477 的旧实现。

### 实际修改文件

- 实现：`internal/core/project/domain/project.go`、`internal/core/project/ports/repository.go`、
  `internal/core/project/application/service.go`、
  `internal/core/project/adapters/postgres/{repository.go,store.go,installation.go,queries.sql,projectdb/*}`、
  `internal/core/project/transport/connect.go`、`cmd/workos-core/main.go`
- migration：`internal/platform/migrations/files/013_project_create_requests.sql`（新增）
- 测试：`domain/project_test.go`、`application/service_test.go`（新增）、
  `transport/connect_test.go`（新增）、
  `adapters/postgres/repository_transient_test.go`（新增）、
  `tests/integration/project_service_contract_test.go`（新增）、
  `tests/integration/foundation_test.go`（幂等断言改写）、
  `tests/integration/{app_bridge,mutable_grant_revocation}_test.go`（Router 组装改走 application service）、
  `internal/core/orchestration/project_directory_test.go`（fixture 改为 UUIDv7）
- 文档：`docs/decisions/0004-project-create-idempotency.md`（新增）、本任务记录、
  `docs/architecture/implementation.md`（新增 ProjectService 基础契约章节）、`docs/status.json`、
  `README.md`（工具重生成）、`docs/prompts/20260829-…-hardening.md`（prettier 格式化，
  修复 main 上既有的 `make check` 失败，见基线记录）

### 关键语义（详见 ADR-0004）

- digest：canonical JSON（`project.create/v1`）覆盖规范化 name、icon、提交顺序 refs
  （id/kind/uri/logical_mount/read_only）、binding presence 与全部引用字段；不含 owner、
  key、ID、时间、revision、数据库状态。
- 快照：`result_version=1` 的完整首次响应 Project 投影（含 created/updated UTC
  RFC3339Nano）；重放只读 mapping，不读可变 row。
- 并发：数据库唯一索引 + mapping PK 裁决；双 pool 同 key/same request → 恰一个 winner、
  一个 project/event/outbox，loser 精确 replay；different request → 一个 winner 一个
  Aborted。失败（约束、注入 transient、注入 commit 失败）全部回滚且不消费 key。
- wire cap 推导：16 refs ×（4 KiB uri + 0.5 KiB id + 0.5 KiB mount + 开销）≈ 84 KiB +
  name/icon/key/binding ≈ 数 KiB → 合法上界 ~90 KiB → 128 KiB（131,072 bytes）。
- 错误映射固定消息：`project request is invalid` / `project is not available` /
  `idempotency key was already used for a different request` / `project revision conflict` /
  `project service is temporarily unavailable` / `project operation failed` /
  `authentication is required`。

### 验证命令与结果

基线（改动前记录）：

| 命令                    | 结果                        | 归属                                                                                                                                                    |
| ----------------------- | --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `make bootstrap`        | PASS                        | —                                                                                                                                                       |
| `make check`            | FAIL（web-check prettier）  | 既有问题：`docs/prompts/20260829-next-agent-project-service-contract-hardening.md`（3e2e428 引入）格式不符合 prettier；与本任务无关，本任务已格式化修复 |
| `make test-integration` | PASS（exit 0）              | —                                                                                                                                                       |
| `make test-e2e`         | PASS（5 passed, 1 skipped） | deepseek-fixture spec 在无 fixture profile 时按设计 skip                                                                                                |

改动后（全部在 branch 上执行）：

> 更正（2026-08-29 修正轮）：本表最初记录的 `make check` PASS 不实——提交 2249477 后
> `make check` 实际失败（web-check prettier 报本任务记录自身第 1 行格式不合格），复审
> 还发现 migration 013 CHECK NULL 漏洞、NormalizeName 缺控制字符拒绝、快照重放缺
> 语义 fail-closed 校验、并发测试未证明跨进程独立 ID 五个缺陷。全部修复与复验见下节
> 「修正轮」，本表保留为原始（部分不实）记录，不得再作为验收依据。

| 命令                                                                                                                                    | 结果                                           |
| --------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------- |
| `make bootstrap`                                                                                                                        | PASS                                           |
| `make generate`（连续两次）                                                                                                             | PASS，两次 `git status` 无差异（生成无漂移）   |
| `make check`                                                                                                                            | PASS（该声明当时不实，见上方更正与修正轮复验） |
| `make test-integration`（第 1 次）                                                                                                      | PASS                                           |
| `make test-integration`（第 2 次）                                                                                                      | PASS                                           |
| `make test-e2e`                                                                                                                         | PASS                                           |
| `make test-deepseek-fixture`                                                                                                            | PASS                                           |
| `go test -race ./internal/core/project/...`                                                                                             | PASS                                           |
| `go test ./...`（make check 内）                                                                                                        | PASS                                           |
| 定向：`go test -tags=integration`（ProjectService 契约测试集：CreateRequests / CreateIdempotency / ListPagination / ServiceCreatePath） | PASS                                           |
| 定向：`go test -tags=integration -run 'TestProjectToHarnessVerticalSlice'`                                                              | PASS                                           |
| `git diff --check`                                                                                                                      | 干净                                           |

必须项对照：

- migration pristine（001→013 空库）/forward（手工 001–012+legacy 数据升级）/no-op（二次
  Run）/checksum（001–012 逐文件 pin）：`TestProjectCreateRequestsMigrationChain`、
  `TestProjectCreateRequestsMigrationForwardsLegacyVolume` PASS；
- 真实 PostgreSQL 并发/幂等/重启 replay：`TestProjectCreateIdempotencyAgainstRealPostgres`
  全部子测试 PASS（含双 pool、事务中 transient 注入、deferred trigger 注入 commit 失败、
  legacy fail-closed、Update/Archive 后 replay）；
- refused endpoint 真实 pgx 断连 → `ErrStoreUnavailable`：
  `TestBaseRepositoryTransientFailuresCarryStoreUnavailable`（覆盖 Lookup/Create/Get/List/
  Update/Archive 全路径）+ 既有 installation 同类测试；
- 多页 ListProjects 无重复/漏项、准确 token、archived filter、owner 隔离：
  `TestProjectListPaginationWalksExactly` PASS；
- wire cap：oversize JSON/proto、gzip bomb decode 前 `ResourceExhausted`、合法近上限请求
  进入业务层、业务零调用、同 mux installation 上限独立：
  `transport/connect_test.go` 全部 PASS；
- E2E/回归：foundation vertical slice（新幂等语义）、installation、surface、bridge、
  grants 链路、deepseek fixture 全部 PASS；restart persistence（test-integration 内
  seed→restart→verify）PASS。

### 残余风险与下一步

- `projects.idempotency_key` 列与其 UNIQUE 约束保留为物理仲裁（数据/约束兼容性要求）；
  与 mapping 表存在可推导冗余，未来若移除需新 ADR 与 additive migration。
- legacy key 一律 Aborted 是有意收紧：旧客户端若复用旧 key 换内容重试，会得到显式冲突
  而非静默旧 Project（与 installation 命令语义一致）。
- List 的 keyset 分页按 UUIDv7/id 边界，不承诺 snapshot isolation（与既有语义一致，已在
  测试注释中固定）。
- 下一步产品主线（App Agent approval / quota-budget policy）可直接依赖本契约。

## 修正轮（2026-08-29，契约复审）

提交 2249477 的复审发现五个缺陷（1–5），第二轮复审再发现三个缺陷（6–8），第三轮复审
发现一个 P1 回归（9），全部在本 branch 上修复；013 未进入 main，原位修正（非新
migration）符合"未发布 migration 由本任务拥有"的边界。全部修复已以独立提交落盘
（见「Branch / Commit」），不残留未提交状态。

### 缺陷与修复

1. **任务记录 make check 声明不实**：2249477 后 `make check` 实际失败（prettier 报本
   任务记录格式不合格）。修复：本记录按 prettier 重排，声明改为如实历史（见上方更正
   块），并以修正轮复验表取代验收依据。
2. **NormalizeName 只做 trim + 长度**：`A\nB` 这类含内部 C0/C1 控制字符或非法 UTF-8 的
   name 会被接受，违反 implementation.md 的统一文本语法。修复：trim 后走与其它字段
   相同的 `requiredText` 语法（valid UTF-8、无 C0/C1、1–120 code points），新增
   domain/集成用例（`A\nB`、C1、NUL、非法 UTF-8、trim 后残留 DEL）。
3. **migration 013 result CHECK 的 NULL 漏洞**：`result ->> 'result_version' = '1'` 在
   键缺失时比较结果为 NULL，PostgreSQL CHECK 放行未版本化快照；`->>` 文本比较还会把
   数值 `1` 误判合法。修复：改为 `result -> 'result_version' IS NOT DISTINCT FROM
'"1"'::jsonb`（jsonb 类型敏感等值，缺失键判 FALSE）；ADR-0004 的 SQL 与理由同步
   更新；shape 集成测试新增"缺失 result_version / 数值 result_version 必须拒绝"断言。
   验收卷 80 行存量 mapping 全部满足新谓词（已验证），未发布 migration 原位修复并
   同步卷上 checksum（操作记录见下）。
4. **快照重放缺语义 fail-closed**：`decodeCreateResult` 只验 JSON 结构与时间格式，
   owner 不匹配、非 UUIDv7 ID、revision ≠ 1、Create 携带 archived、字段语法损坏的
   快照都会被当作合法首次响应返回。修复：新增 `validateCreateSnapshot`——owner 必须等
   于请求 owner 且为 canonical UUIDv7，project/collection ID 为 canonical UUIDv7，
   revision 必须 = 1（ports.CreateCommand 契约），ArchivedAt 必须为空，name 必须是
   NormalizeName 的不动点，icon/refs/binding 走 domain 语法；任何违反都是 opaque
   Internal，绝不返回语义损坏数据。新增 `repository_create_snapshot_test.go` 单元矩阵。
5. **并发测试复用同一 command**：两个连接共用同一 `ports.CreateCommand`（同一服务端
   ID、同一时间戳、同一 collection ID），不能证明"两个真实进程各自生成事实后，loser
   精确采用 winner 快照"。修复：左右连接各自持独立 Project ID（999/9fe）、独立
   knowledge/artifact collection ID、独立时钟（`now` / `now+1s`），仅共享 canonical
   request（name/icon/refs/binding）与 key；断言恰一方返回自己的 ID（物理 winner），
   另一方（loser）经 `sameProject` 逐字段比较器全字段等于 winner 响应——winner 的
   ID、collection ID 与时间戳，而非 loser 自己提交的任何事实——loser 自己的 ID 不
   产生 project/event/outbox，事件与 outbox 挂在 winner ID 下；count 断言对数据库
   裁决顺序对称（任一方都可能赢）。父测试连跑 6 次覆盖两种裁决顺序。
6. **（第二轮）快照未与请求 digest 交叉验证**：`LookupCreateRequest` 分别返回 digest
   与快照，application 只比较 digest——若 result 列的 name 等请求承载字段被篡改成另一
   组仍合法的值，系统会把错误快照当作合法重放返回。修复：`decodeCreateResult` 接收
   stored digest，在语义校验后用 `domain.CreateRequestDigest` 对解码出的 name/icon/
   refs/binding 重算 digest 并与 stored digest 精确比对，不一致即 opaque Internal
   （LookupCreateRequest 与 adjudicateLostCreate 两条路径都过这道闸）；单元矩阵新增
   "改名为另一合法值 / 换 icon / 翻转 ref 只读位 / 用外来 digest 裁决" 四个 fail-closed
   用例。
7. **（第二轮）首响应不变量未完全固定**：create 命令恒产生空 `InstalledAppIDs`、空
   `DefaultAgentRole`、`created == updated` 单一时刻（application/service.go Create），
   但快照校验仍允许非空 App/role、允许 updated 晚于 created。修复：`validateCreateSnapshot`
   拒绝非空 InstalledAppIDs 与非空 DefaultAgentRole，`decodeCreateResult` 要求
   `created.Equal(updated)`（非零），单元矩阵新增 apps/role/updated-before/updated-after
   用例。至此快照全部字段要么被 digest 钉住（请求承载字段），要么被首响应不变量钉住
   （ID/owner/revision/archived/apps/role/时刻）。
8. **（第二轮）记录表格被管道符破坏 + 并发证据描述不符**：改动后复验表中
   `-run` 过滤器内的未转义管道符把一行命令拆成五列伪表格；且并发测试（上一条修复后）
   仅独立 Project ID，与"独立 identifiers 和 timestamps"的描述不符。修复：表格改为
   不含管道符的等价描述；并发测试按上一条升级为 ID/collection/时钟全独立。
9. **（第三轮，P1）非空 harness_binding 的合法重放被摘要校验误杀**：digest 交叉验证
   在 binding 重新挂载到 Project 之前执行——binding 是 digest 覆盖字段，重算时按 nil
   参与，任何携带 binding 的合法同 key 重放都会摘要失配返回 Internal；新增单元矩阵的
   合法基准恰好无 binding，未暴露此缺陷。修复：binding 恢复提前到摘要重算之前；
   新增 `TestDecodeCreateResultRoundTripsHarnessBinding`（binding 全程往返 + 剥离
   binding 必须摘要失配 fail closed），集成 `SameRequestReplaysExactFirstResponse`
   升级为携带 binding 的请求（digest 覆盖 binding，重放经 `sameProject` 全字段断言，
   含 binding）。变异验证：把重算参数临时改回 nil 时新单测即失败，证明测试对该缺陷
   敏感。

### 修正轮实际修改文件

- `internal/core/project/domain/project.go`（NormalizeName 语法）
- `internal/core/project/adapters/postgres/repository.go`（decodeCreateResult +
  validateCreateSnapshot，两个调用点传 owner）
- `internal/platform/migrations/files/013_project_create_requests.sql`（CHECK 谓词）
- `docs/decisions/0004-project-create-idempotency.md`（SQL 与谓词理由同步）
- 测试：`domain/project_test.go`、`adapters/postgres/repository_create_snapshot_test.go`
  （新增）、`tests/integration/project_service_contract_test.go`（shape 负例 + 并发
  重写 + sameProject 比较器）
- 本任务记录

### 修正轮验证命令与结果

| 命令                                                                                                                                               | 结果                                         |
| -------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------- |
| `gofmt -l cmd internal tests`                                                                                                                      | 空                                           |
| `go vet ./internal/core/project/...`、`go vet -tags=integration ./tests/integration`                                                               | PASS                                         |
| `go test -race ./internal/core/project/...`                                                                                                        | PASS                                         |
| 定向：`go test -tags=integration -count=1`（ProjectService 契约测试集）                                                                            | PASS                                         |
| `go test -tags=integration -count=6 -run 'TestProjectCreateIdempotencyAgainstRealPostgres'`                                                        | PASS（两种并发裁决顺序均覆盖，第二轮后复跑） |
| 第二轮单元矩阵：digest 交叉验证（改名/换 icon/翻转 ref/外来 digest）与 apps/role/时刻不变量全部 fail closed                                        | PASS                                         |
| 验收卷 013 原位修复：DROP/ADD CONSTRAINT + schema_migrations checksum 同步；80 行存量全部满足新谓词；缺失 result_version 探测被 CHECK 拒绝且零残留 | 完成                                         |
| `make generate`（连续两次）                                                                                                                        | PASS，两次 `git status` 无差异               |
| `make check`（proto lint、sqlc vet、go vet+test、TS architecture/eslint/prettier/web build、README 状态一致）                                      | PASS（exit 0）                               |
| `make test-integration`（全量 + seed→restart→verify 持久化链路）                                                                                   | PASS（exit 0，50 个测试通过）                |

以上为第一轮修复的复验；第二、三轮（digest 交叉验证、不变量补全、表格/并发测试
修正）的复验已重跑并通过：单元与集成行如上（count=6 与契约测试集均为修正后代码），
`make check`、`make test-integration`、`make generate` 二次无差异在提交前整树重跑。

### 修正轮残余风险

- 验收卷曾以旧 013 checksum 记录 schema_migrations；因 013 未发布，本次以原位
  ALTER + checksum 同步修复（80 行数据保留）。若未来出现"volume checksum 与文件
  不一致"的报错，应核对是否为本次修复前的旧卷。
- `validateCreateSnapshot` 将 create 首响应不变量（revision=1、未归档、canonical
  UUIDv7、空 apps/role、单一创建时刻、digest 交叉一致）固化为重放前置条件；未来若
  create 语义变化（例如允许携带 app 安装创建），必须同步扩展该校验与新 ADR，否则
  重放会 fail closed——这是有意的守门行为。
