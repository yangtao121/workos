# 下一位智能体 Prompt：App Manifest Registry 第三轮审核修复

> 将本文件完整交给修复智能体。当前分支只有一个集中在 migration 测试资源生命周期上的合并阻断项；
> 请完成修复、验证、文档同步和提交，不要只输出计划，也不要扩展产品范围。

## 你的角色与当前状态

你是 WorkOS `feat/app-manifest-registry` 分支的第三轮修复智能体。仓库位于
`/home/aquatao/workos`。第三轮审核时 HEAD 为 `98eafaa`（`fix: close app registry review gaps`），
本地 `main` 为 `484c057`，且 `main` 是功能分支的直接祖先；开始时必须重新检查，不能盲信这些哈希。

前两轮提出的产品与数据正确性问题已经基本落实：

- credential-shaped mapping key 在 pointer/canonicalization/持久化前按安全父路径拒绝；
- 高基数分页 fixture 精确追踪并清理本轮 app IDs 与 idempotency keys；
- 并发 immutable-version 测试明确证明 loser 为 `AlreadyExists` 且没有消费 key；
- 任务记录顶部已经收敛为 002 + 003 migration、summary streaming、Connect read max 等最终现状；
- migration cleanup 的正常路径已经改为在 cleanup 中新建 admin connection，连续测试不再新增残留。

第三轮独立运行 `make check` 已通过；`git diff --check`、`git diff --check main...HEAD`、
002 migration 与 `8e48d92` 的逐字节比较均通过。migration 定向测试连续运行两次也通过，运行前、
第一次后、第二次后的 scratch database 集合完全相同，均为以下 6 个历史残留：

- `workos_migration_test_1787498316725324135`
- `workos_migration_test_1787498316725495588`
- `workos_migration_test_1787498439446423137`
- `workos_migration_test_1787498439446610484`
- `workos_migration_test_1787498480690229539`
- `workos_migration_test_1787498480690854487`

审核者未删除这些数据库、未删除 PostgreSQL volume、未运行真实 Provider 网络测试，也未合并或 push。

## 凭据边界

- 本任务不需要真实 DeepSeek 或任何其他 Provider 凭据。
- 不得使用、保存、转述、验证或尝试恢复聊天中出现过的真实 Key。
- 不得从聊天、shell history、环境变量或本机文件搜集凭据。
- 所有 Provider 验收继续使用仓库已有 fixture 假 credential，不访问真实 Provider 网络。
- 测试、日志、错误和任务记录只能使用明显虚构的合成值，不能回显真实凭据。

## 开始前

1. 完整阅读：
   - `AGENTS.md`
   - `docs/structure.md`
   - `docs/architecture/implementation.md`
   - `docs/status.json`
   - `docs/tasks/20260823-app-manifest-registry.md`
   - `docs/prompts/20260823-review-2-app-manifest-registry.md`
   - `tests/integration/app_registry_migration_test.go`
   - `internal/platform/migrations/migrate.go`
   - `internal/platform/migrations/files/002_app_registry.sql`
   - `internal/platform/migrations/files/003_app_registry_idempotency.sql`
2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -8
   git branch -vv
   git diff --check
   git diff --check main...HEAD
   git diff --exit-code 8e48d92 -- internal/platform/migrations/files/002_app_registry.sql
   ```

3. 保留不属于本任务的改动。继续在功能分支工作，不得 reset、rebase、修改 `main`、merge 或 push。
4. 本 prompt 是第三轮审核产物，必须保留并随修复提交。
5. 修复期间把任务状态改为 active；未重新取得完整证据前，不得继续声称所有门禁已经通过。完成后同步
   `docs/tasks/20260823-app-manifest-registry.md`；若 `docs/status.json` 的事实状态发生变化，必须使用生成工具
   同步 README，禁止手改生成区块。
6. `002` 与 `003` 都是 checksum 保护且已执行的 migration，本修复不需要数据契约变化，禁止修改它们或
   新增 migration。
7. 禁止 `docker compose down -v`、删除 volume、删除历史 scratch database 或批量清理 acceptance 数据。

## 唯一合并阻断项：scratch cleanup 仍未覆盖 CREATE 后的失败路径

当前 `scratchDatabase` 的关键顺序是：

```go
CREATE DATABASE
admin.Close(ctx) // 可能返回错误并 t.Fatalf
t.Cleanup(dropScratchDatabase)
```

也就是数据库已经创建成功后，代码先执行一个可失败的 `admin.Close(ctx)`，只有成功后才注册 cleanup。
如果 Close 超时、网络中断或返回其他错误，`t.Fatalf` 会立即终止当前测试，而 `t.Cleanup` 尚不存在，刚创建
的数据库会永久残留。这仍然违反第二轮明确要求的“cleanup 在测试成功、`t.Fatal`、panic/提前返回时都执行”。
现有 `TestScratchDatabaseCleanupDropsCreatedDatabase` 只覆盖正常 Close 成功的路径，不能防止这个回归。

此外，`dropScratchDatabase` 虽然为 connect/DROP 创建了 15 秒 context，但 defer 中最终执行的是：

```go
admin.Close(context.Background())
```

这个 Close 没有时间上限，也不属于前述 bounded lifecycle；极端情况下 cleanup 本身可以无限阻塞。这同样
不满足第二轮要求的“cleanup 使用独立、有界 context，最后关闭 admin connection并暴露失败”。

### 必须达到的行为

- 一旦 `CREATE DATABASE` 可能已经产生数据库，在任何后续可失败操作之前就建立可靠的 exact-name cleanup。
- 推荐在执行 CREATE 前就注册使用精确内部生成名称的幂等 cleanup（例如安全 quote 后的
  `DROP DATABASE IF EXISTS ... WITH (FORCE)`），从而也覆盖“服务端已创建、客户端 Exec 却收到不确定错误”
  的情况；如果采用其他设计，必须给出同等的全路径证明。
- 如果选择在 CREATE 成功后注册，注册必须至少发生在 creation connection 的 Close 之前，并明确说明如何
  处理 CREATE 返回不确定错误的路径。
- test helper 的成功、`t.Fatal`、panic、提前返回以及 CREATE 后的 Close 失败都不能留下本轮数据库。
- DROP 仍须在 cleanup 自己新建的 admin connection 上执行，使用独立且有界的 connect/exec/close 生命周期。
- admin connection 的最终 Close 也必须使用有界 context；不能使用无界 `context.Background()`。
- CREATE、DROP 和 Close 的错误都必须成为明确 test error；不得静默忽略会造成资源泄漏的错误。
- database identifier 只允许内部生成，并继续通过 `pgx.Identifier.Sanitize()` 等安全方式 quote。
- 只能 DROP 本轮生成的精确名称；不得 wildcard DROP，不得触碰 `postgres`、`workos`、历史 6 个残留或
  其他测试数据库。
- 不要借此重构 migration runner、修改产品代码或引入新的基础设施抽象。

### 必须新增或加强的回归证明

现有正常路径测试必须保留，并增加一个可重复、非 timing-flaky 的生命周期测试，证明：

1. 数据库已经创建后，模拟 post-CREATE/pre-return 失败（至少覆盖 creation connection Close 返回错误）；
2. cleanup 在该错误被上报前已经注册，或由可检查 helper 返回给调用方并必定执行；
3. 执行 cleanup 后用 `pg_database` 精确查询，刚创建的名称不存在；
4. cleanup admin Close 使用有界 context，错误不会被吞掉。

优先通过小型、可注入的未导出 helper/fake lifecycle 来稳定制造 Close 错误；不要依赖随机 sleep、真实网络
抖动或故意污染共享数据库。若测试设计采用 subprocess，必须保证子进程失败是预期且父测试仍能精确验证
零残留。不能只靠源码注释或再次运行正常路径测试代替异常路径证明。

## 验收顺序

### 定向验证

1. 在测试前只读记录以下精确集合：

   ```sql
   SELECT datname
   FROM pg_database
   WHERE datname LIKE 'workos_migration_test_%'
   ORDER BY datname;
   ```

2. 运行新增的异常生命周期测试，以及现有 migration 测试：

   ```sh
   go test equivalent: -tags=integration -count=1 \
     -run 'TestScratchDatabaseCleanup|TestAppRegistryMigration' ./tests/integration
   ```

   `go test equivalent` 表示必须使用仓库 Makefile 同等的 Docker runner、host network、HOME/GOPATH 和 module
   cache 配置，不得绕过依赖边界。

3. 连续运行上述测试两次。每次后重新查询精确名称集合，必须与测试前完全相同；不仅要看命令退出码。
4. 报告历史 6 个残留但不要删除。新增测试产生的名称必须为零残留。

### 完整门禁

修复完成后重新执行并记录：

```sh
make generate
make generate
make check
make test-integration
make test-deepseek-fixture
make test-e2e
buf breaking --against '.git#branch=main'
git diff --check
git diff --check main...HEAD
git diff --exit-code 8e48d92 -- internal/platform/migrations/files/002_app_registry.sql
```

还要确认：

- 第二次 `make generate` 后生成文件无差异；
- `docs/structure.md` 无变化；
- `002`、`003` 无变化且没有新增 migration；
- worktree 没有 root-owned 文件、临时产物或意外生成差异；
- 高基数分页 cleanup、credential-shaped key 和并发错误码测试仍通过；
- 没有使用真实 Key，没有访问真实 Provider 网络，没有删除 volume；
- 最终任务记录只写实际运行过的命令与可复核结果，不把计划写成证据。

## 完成与交接

- 只修复上述测试资源生命周期缺口，不实现 Project install/uninstall、Runtime、Surface、App Bridge、
  Credential Vault 或其他后续功能。
- 完成后把任务恢复为 done，并准确补充第三轮异常路径与连续零残留证据；产品状态只有在完整门禁通过后
  才能保持 working。
- 在 `feat/app-manifest-registry` 上创建一个聚焦提交，提交信息建议：
  `fix: guarantee scratch database cleanup on failures`。
- 最终交接写明提交哈希、实际验证命令、三次 scratch 集合（前/第一次后/第二次后）、未决风险和 worktree
  状态。
- 不要 merge 到 `main`，不要 push；留给审核者复审并执行本地 `--ff-only` 合并。
