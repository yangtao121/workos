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
- commit：见 `git log -1`（单一聚焦提交；未 merge、未 push）

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

| 命令 | 结果 | 归属 |
| --- | --- | --- |
| `make bootstrap` | PASS | — |
| `make check` | FAIL（web-check prettier） | 既有问题：`docs/prompts/20260829-next-agent-project-service-contract-hardening.md`（3e2e428 引入）格式不符合 prettier；与本任务无关，本任务已格式化修复 |
| `make test-integration` | PASS（exit 0） | — |
| `make test-e2e` | PASS（5 passed, 1 skipped） | deepseek-fixture spec 在无 fixture profile 时按设计 skip |

改动后（全部在 branch 上执行）：

| 命令 | 结果 |
| --- | --- |
| `make bootstrap` | PASS |
| `make generate`（连续两次） | PASS，两次 `git status` 无差异（生成无漂移） |
| `make check` | PASS |
| `make test-integration`（第 1 次） | PASS |
| `make test-integration`（第 2 次） | PASS |
| `make test-e2e` | PASS |
| `make test-deepseek-fixture` | PASS |
| `go test -race ./internal/core/project/...` | PASS |
| `go test ./...`（make check 内） | PASS |
| 定向：`go test -tags=integration -run 'TestProjectCreateRequests|TestProjectCreateIdempotency|TestProjectListPagination|TestProjectServiceCreatePath'` | PASS |
| 定向：`go test -tags=integration -run 'TestProjectToHarnessVerticalSlice'` | PASS |
| `git diff --check` | 干净 |

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
