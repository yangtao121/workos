# 下一位智能体 Prompt：Project App Installation 审核修复

> 将本文件完整交给修复智能体。当前实现尚有一个数据库 owner 隔离阻断项，以及两处验收证据缺口；
> 请直接修复、验证、同步文档并提交，不要只输出计划，也不要扩展产品范围。

## 你的角色与当前审核结论

你是 WorkOS `feat/project-app-installation` 分支的审核修复智能体。仓库位于
`/home/aquatao/workos`。本轮审核时：

- 功能分支 HEAD 为 `696c554`（`feat: implement project app installation`）；
- 本地 `main` 为 `1f096de`，且是功能分支的直接祖先；
- 工作树干净，`main...HEAD` 只有一个实现提交；
- `git diff --check`、`git diff --check main...HEAD`、`make check`、
  `buf breaking --against '.git#branch=main'` 均通过；
- 001/002/003 migration 与 `main` 逐字节一致；
- 验收 volume 已应用 `004_project_app_installations.sql`。文件与数据库记录的 SHA-256 均为
  `df364efc07892164611e4587288e46ddec491b187662f6271dd2907c5527e00b`；
- 对验收 volume 的只读核对显示 269 条 installation request mapping，当前跨 owner 错配为 0。

开始时必须重新检查这些事实，不能把哈希、计数或命令结果当成永久状态。

Project Installation 的协议、事务、revision/event/outbox、version pinning、Gateway、SDK、Desktop 和
E2E 主链路总体成立，但静态审核确认 `project_app_installation_requests` 的外键没有把 mapping owner 与目标
installation owner 绑定。该表是持久幂等权威，数据库层允许形成不可重放的跨 owner 结果映射，当前分支因此
不得合并。

只修复本文列出的 Project Installation 数据完整性和证据测试，并同步相关任务/架构/status。不要实现
Web Bundle、Artifact upload/hosting、Surface、iframe、App Bridge、capability grant/token、Runtime runner、
Credential Vault、App upgrade 或其他后续功能；不要改变六进程边界或 v1 Proto。

## 凭据与安全边界

- 本任务不需要真实 DeepSeek、OpenAI 或任何其他 Provider Key。
- 不得使用、保存、转述、验证或尝试恢复聊天中出现过的真实 Key。
- 不得从聊天、shell history、环境变量、进程或本机文件搜集凭据。
- DeepSeek 门禁只使用仓库已有 fixture 假 credential，不访问真实 Provider 网络。
- 测试、日志、错误和任务记录只能使用明显虚构的合成值，不得写入 credential、manifest 全文或用户内容。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`
   - `docs/structure.md` 中 Project、App Runtime、Surface 与首版边界
   - `docs/architecture/implementation.md`
   - `docs/status.json`
   - `docs/prompts/20260825-next-agent-project-app-installation.md`
   - `docs/tasks/20260825-project-app-installation.md`
   - `api/proto/workos/app/v1/installation.proto`
   - `internal/platform/migrations/migrate.go`
   - `internal/platform/migrations/files/001_foundation.sql` 至
     `004_project_app_installations.sql`
   - `internal/core/project/domain|application|ports|adapters/postgres|transport` 的 installation 实现与测试
   - `internal/core/orchestration/app_catalog.go` 及测试
   - `tests/integration/project_app_installation_test.go`
   - `tests/integration/project_installation_migration_test.go`
   - `tests/restart/main.go`、Gateway、SDK、Desktop App Library 与 E2E 测试
2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -8
   git branch -vv
   git diff --check
   git diff --check main...HEAD
   git merge-base --is-ancestor main HEAD
   ```

3. 保留所有不属于本任务的改动。继续在当前功能分支工作，不得 reset、rebase、直接修改 `main`、merge 或
   push。
4. 本审核 prompt 必须保留并随修复提交。
5. 修复期间将 `docs/tasks/20260825-project-app-installation.md` 状态改回 active；安全不变量和完整门禁重新
   成立前不得声称全部验收通过。最终按真实结果恢复 done/working；README 状态区块只能由生成工具更新。
6. `004` 已在持久验收 volume 执行并受 checksum 保护，绝对禁止修改、重命名、squash 或重排 001–004。
   表结构修复必须使用下一编号的 forward migration，预期为
   `005_project_app_installation_request_owner.sql`（若编号已被占用则顺延）。
7. 禁止 `docker compose down -v`、删除 `workos_workos-postgres` volume、删除历史 scratch database、批量
   清理验收数据，以及手改 `gen/`、`src/gen/` 或 README 生成状态区块。

## 合并阻断项：幂等结果 mapping 没有绑定 installation owner

004 当前定义：

```sql
CREATE TABLE workos_core.project_app_installation_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    ...
    installation_id uuid NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key),
    FOREIGN KEY (installation_id)
        REFERENCES workos_core.project_app_installations (id)
        ON DELETE RESTRICT
);
```

`owner_user_id` 与 `installation_id` 分别有合法外键，但二者之间没有关系。数据库因而允许：

```text
owner A 拥有 installation IA
owner B 是另一合法 user
插入 request(owner_user_id=B, installation_id=IA) → 当前 schema 接受
```

这违反了持久幂等结果的基本不变量：

```text
request.owner_user_id == referenced installation.owner_user_id
```

公开 API 的正常 repository 路径目前会写入一致数据，因此验收 volume 现有 269 条 mapping 没有错配；这不
能替代数据库约束。若其他 Core writer、迁移或故障路径写入上述映射，`LookupInstallationRequest(B, key)`
会认为 key 已消费，随后 owner-scoped `GetInstallation(B, IA)` 又返回 NotFound，使一个成功结果 mapping
永久不可重放。它也是与 App Registry 003 已采用的 composite owner FK 不一致的数据隔离退化。

### 必须达到的数据模型

- 使用新的 005 forward migration 为 `project_app_installations` 提供可被引用的 owner+ID 唯一键，并把
  request mapping 的单列 installation FK 替换为 composite owner FK。推荐形态：

  ```sql
  UNIQUE (owner_user_id, id)

  FOREIGN KEY (owner_user_id, installation_id)
      REFERENCES workos_core.project_app_installations (owner_user_id, id)
      ON DELETE RESTRICT
  ```

- 保留 `(owner_user_id, idempotency_key)` 的 owner-wide install/uninstall 共享命名空间，以及现有 tombstone
  replay、request digest、结果 revision/timestamp 语义。
- 005 必须验证并保留已有合法 mapping；不得删除、重写或悄悄忽略现有行。若发现历史错配，必须停止并报告
  精确只读证据，不能猜测 owner 或删除数据。
- 评估新增 UNIQUE 后原 `(owner_user_id, id)` 非唯一索引是否冗余；只按实际 query/index 证据保留或移除，
  不要顺手重构其他索引。
- 不建立指向 App Registry 表的跨模块 FK，不在 Project SQL 中 join `app_versions`。
- 本轮不要求重做 request digest schema。若认为还必须新增 `project_id` 绑定，先用可复现的不变量说明必要性；
  不要在没有证据时扩大 migration。
- repository 的正常写入、同 key replay、跨 project key 仲裁及错误净化不得回归。

### 必须新增的 PostgreSQL/migration 证据

1. 在真实 PostgreSQL scratch database 中创建两个 owner、各自 Project/installation；同 owner mapping 成功，
   owner B 指向 owner A installation 的 mapping 必须由 composite FK 拒绝。
2. 同一个 idempotency key 可由两个不同 owner 各自使用并分别指向自己的 installation；owner 隔离不能被
   错误收紧成全局 key。
3. tombstoned installation 仍可被原 owner 的结果 mapping 引用和重放；`ON DELETE RESTRICT` 继续成立。
4. 从 pristine database 执行完整 001→005 链，明确断言 005 已应用、约束定义正确。
5. 建立真实 004-era 合法 installation/request 数据后前向执行 005，证明数据保留且 replay 关联不变；不能
   只测空库。
6. 在当前持久 acceptance volume 前向执行 005，执行前只读断言跨 owner mapping 为 0，执行后断言 005
   checksum 记录、composite FK 和全部现有 mapping 数量/关联保持一致。
7. 为 004 增加逐字节 checksum 回归断言，固定本审核记录的
   `df364efc07892164611e4587288e46ddec491b187662f6271dd2907c5527e00b`；同时继续证明 001/002/003 未改。
8. migration scratch helper 必须继续在成功、`t.Fatal`、panic/提前返回时精确清理本轮数据库。定向 migration
   测试连续运行两次，前/第一次后/第二次后的 scratch database 精确集合必须相同；历史残留只报告不删除。

不能只增加 fake repository 测试，也不能依赖 application 正常写入来替代约束测试。

## 同轮补强两处验收证据

这两项不是新增产品功能，但当前测试没有证明任务记录所声称的行为，应在同一聚焦修复中补齐。

### 1. UUIDv7 断言目前只检查字符串外形

`TestProjectAppInstallationVerticalSlice/InstallPinsCurrentVersionIntoProjection` 的注释声称证明 UUIDv7，实际
只检查长度为 36 且包含连字符，UUIDv1/v4 等同样会通过。

- 使用可靠 UUID parser 解析真实 Gateway/Core 返回的 installation ID；
- 明确断言 UUID version 为 7（并按库能力断言标准 variant）；
- 保留“重新安装产生新的 UUIDv7 instance ID”的断言；不要用固定测试字符串替代真实 composition root
  generator。

### 2. explicit-version catalog 单测没有观察传入参数

`internal/core/orchestration/app_catalog_test.go` 的
`TestAppCatalogExplicitVersionUsesImmutableRead` 声明了 `requestedVersion`，但 stub 从未为它赋值，当前断言
永远不能证明 `1.9.0` 被传给 App Registry。

- 让 stub/fake 明确记录 owner、app ID、version；
- 断言 explicit `1.9.0` 原样进入 Registry application 的 immutable `GetVersion` 路径；
- 同时保留空 version 解析 current、NotFound/Invalid/Internal 分类测试；
- 不为了测试而给生产接口增加无调用方字段。

## 回归边界

修复后必须继续证明：

- `InstallApp`/`UninstallApp`/`ListInstalledApps` 的 Proto 无破坏性变化；Gateway 只新增既有
  `AppInstallationService` allowlist，Surface/private API 仍为 404。
- current/explicit version 固定 exact version+digest；新 Registry version 不静默升级旧 installation。
- install/uninstall 共用 owner-wide idempotency namespace；same key same request 精确重放，different request/
  action/Project 为 `Aborted`，失败不消费 key。
- 一个 Project/app 至多一个 active installation；uninstall tombstone、重装新 instance、旧 key 不复活。
- 真正 mutation 在一个事务内同步 installation、`installed_app_ids`、revision、event、outbox、request result；
  no-op/replay 不重复 revision/event。
- 并发 install/uninstall/Project update 仍由数据库 revision/row lock 裁决，不引入进程内 mutex。
- unknown/foreign/archived Project/App/installation 继续净化；响应/日志不含 SQL、constraint、manifest、
  credential 或 owner details。
- Desktop App Library 的 loading/empty/error/retry/saving、revision conflict 刷新、Project 切换隔离和真实
  E2E 不回归。
- Runtime/Surface 状态保持 scaffolded，`surface-broker` 继续如实 unavailable。

## 文档与状态

- 更新 `docs/tasks/20260825-project-app-installation.md` 的数据模型：004 创建初始表，005 补上 authoritative
  request mapping 的 owner composite FK；记录审核原因、upgrade 行为、实际命令、资源计数和未决风险。
- 更新 `docs/architecture/implementation.md`，明确 idempotency mapping 通过 composite FK 绑定同一 owner；
  不扩写未来 Surface 设计。
- `docs/status.json` 只能在新的 migration、约束测试和完整纵向门禁真实通过后保持/恢复 Project App
  Installation 与 Desktop evidence 为 working。
- README 状态区块只通过 `make generate`/status render 更新，禁止手改。
- 保留本 prompt，并把修复结果写入仓库任务记录；聊天说明不能代替仓库事实。

## 验收顺序

### 定向数据验证

先记录验收 volume 中 migration、mapping 数量和 owner 错配数，以及 scratch database 精确集合。然后使用仓库
Docker runner/host network 连续两次运行包含 004 checksum、004→005 upgrade、pristine 001→005、双 owner
composite FK 的 migration 测试。每次后重新核对 scratch 集合零新增。

再运行 Project Installation 的 application/orchestration/transport 单测，以及真实 PostgreSQL installation
纵向、并发、分页和 restart 定向测试。命令必须使用仓库既有 Makefile 等价环境，不能通过修改 HOME/GOPATH、
跳过数据库或直接改 acceptance 数据让测试变绿。

### 完整门禁

全部修复完成后执行并记录：

```sh
make generate
make generate
git diff --check
make check
make test-integration
make test-integration
make test-deepseek-fixture
make test-e2e
buf breaking --against '.git#branch=main'
git diff --check
git diff --check main...HEAD
git diff --exit-code 696c554 -- internal/platform/migrations/files/004_project_app_installations.sql
git diff --exit-code main -- internal/platform/migrations/files/001_foundation.sql
git diff --exit-code main -- internal/platform/migrations/files/002_app_registry.sql
git diff --exit-code main -- internal/platform/migrations/files/003_app_registry_idempotency.sql
git status --short --branch
```

另外确认：

- 第二次 `make generate` 后无新增差异，Proto/Go/TypeScript/sqlc/README/status 生成物一致；
- 只有预期的新 005 migration；001–004 均未改变；
- 两次 integration 前后记录 installation/request/event/outbox 与 Registry/Project fixture 的真实增量；高基数
  fixture 继续精确清理，低基数固定增长如实报告；
- scratch database 集合连续两次零新增，历史残留未删除；
- `docs/structure.md`、六进程边界、runtime-host 和 Surface capability 未发生意外变化；
- 无 root-owned 文件、临时产物、真实 credential 或 Provider 网络访问；
- `workos_workos-postgres` volume 未删除；最终工作树干净。

## 完成与交接

- 完成上述数据约束、测试、文档、状态和全部门禁后，在当前功能分支创建聚焦 Conventional Commit，建议：

  ```text
  fix: bind installation request results to owners
  ```

- 最终交接必须写明提交哈希、005 的约束与 upgrade 结果、004 checksum、双 owner 拒绝证据、UUIDv7 与
  explicit-version 测试、两次 integration 资源计数、scratch 集合、完整门禁及未决风险。
- 明确说明未使用真实 Key、未访问真实 Provider、未删除 volume/历史数据库、未修改 001–004。
- 不要 merge 到 `main`，不要 push、rebase、force 或删除分支；留给审核者复审并执行本地
  `git merge --ff-only`。
