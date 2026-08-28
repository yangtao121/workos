# Prompt：Web Bundle Surface 最终合并就绪修复

> 将本文件完整交给执行智能体。当前功能行为复审基本通过，但分支仍不满足 `main` 的本地
> `--ff-only` 合并条件。请只处理本文列出的合并卫生、确定性门禁和 transient classifier
> 健壮性问题，准备一个可再次审核的干净候选分支；不要 merge、不要 push。

## 仓库与事实基线

- 仓库：`/home/aquatao/workos`
- 被审核分支：`feat/minimal-web-bundle-surface`
- 被审核 HEAD：`2f1cad82fda0a07ff645b332bb9d5da571725fb3`
- 本地 `main`：`1ca3262`，是被审核 HEAD 的直接祖先；被审核分支领先 3 个提交。
- 2026-08-28 复审开始与结束时工作树均 clean；不得覆盖其他分支或无关改动。
- `docs/structure.md`、migration 001–005 相对 `main` 无差异；006/007 checksum 分别为：
  - `628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27`
  - `b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986`
- 已独立通过：`make generate` 连续两次且无漂移、`make check`、`buf breaking`、Runtime
  `-race -count=2`、真实 PostgreSQL 并发/回滚/持久化定向 race、DeepSeek 本地 fixture、浏览器
  E2E（3 passed / 1 fixture-only skipped）。Web Bundle 行为与原 9 个审核阻断项的修复方向成立。
- 不需要真实 DeepSeek/API Provider 凭据。任何 Provider 验收只能使用仓库内 fixture 假凭据；不得把
  聊天中出现过的真实 key 写入文件、命令、环境、日志、任务记录或 Git。

开始前必须重新读取根 `AGENTS.md`、`docs/structure.md`、
`docs/architecture/implementation.md`、`docs/status.json`、当前任务记录与两份审核 prompt，并重新
核对分支、HEAD、工作树、容器和数据库状态。若事实已变化，以仓库现状为准并在任务记录中说明。

## 范围与禁止事项

本轮只做三件事：

1. 生成一个不会把误提交 ELF 带入 `main` 历史的干净候选分支；
2. 修复 acceptance-volume 一致性测试的非快照计数 flake；
3. 让新 `dbtransient` classifier 对畸形/短 SQLSTATE fail closed，并用单测钉死分类矩阵。

不要扩展 Web Bundle/App Bridge/Provider/认证能力，不要修改 Proto、Schema 或 migration，不要修改
006/007 内容，不要创建 008，不要清理/删除持久验收 volume 或历史数据，不要用 skip/retry/sleep
掩盖竞态，不要手改生成文件或 README 状态区块。

按 Agent Rules 更新或认领单一任务记录。现有
`docs/tasks/20260825-minimal-web-bundle-surface.md` 必须把“可合并/done”陈述暂时校正为等待本轮复审，
并追加真实命令、失败与修复证据；不要删除既有审核历史。

## 阻断项一：最终树已删二进制，但待合并历史仍含 19 MiB ELF

最终树中根目录 `restart` 已删除且 `.gitignore` 有精确 `/restart`，但这并没有清除分支历史里的
blob。对象级审计实际得到：

```text
blob 19126918 2dd6362e90c544487d391314732aa4063eabee41 restart
```

它由实现提交加入、后续修复提交删除。`git merge --ff-only` 或普通 merge 仍会让该 blob 永久进入
`main` 可达历史，因此当前分支不能合并。

### 必须完成

- 从复审时的本地 `main` 精确创建一个全新候选分支，使 `main` 是其直接祖先；只物化被审核分支的
  **最终树差异**，不要让候选分支引用原来 3 个污染提交。可采用安全的 squash/materialize 流程，
  但不要移动、merge 或 push `main`。
- 在新候选分支上实施本文另外两项小修复并提交。可以保留旧污染分支供本地审计，但最终交接必须明确
  哪个分支才是允许审核/快进的候选；不得让待合并候选通过 parent、merge parent、tag 等方式引用污染
  历史。
- 根目录 `restart` 必须不存在且不被跟踪；`tests/restart/` 源码必须继续存在；精确 `/restart`
  ignore 规则保留。
- 任务记录中不得再用“见旧分支 HEAD 的 fix 提交”作为当前候选事实。历史提交号可以作为审核历史
  保留，但合并证据必须指向新的干净候选 HEAD。

### 对象级验收

在新候选分支上同时验证：

```bash
git merge-base --is-ancestor main HEAD
git log --oneline main..HEAD
git rev-list --objects main..HEAD
git rev-list --objects main..HEAD | git cat-file --batch-check='%(objecttype) %(objectsize) %(objectname) %(rest)'
git log main..HEAD -- restart
git ls-files -- restart
```

验收要求：`main..HEAD` 中没有 `2dd6362e...`、没有路径 `restart`、没有异常大 blob；最终树也没有根
`restart`。只检查最终 `git status` 不算完成。

## 阻断项二：完整集成门禁存在可复现原理明确的非快照 flake

复审第一次执行 `make test-integration` 时，所有新增 Web Bundle、outage、并发和回滚用例均通过，
但既有测试 `TestProjectInstallationMigrationAppliedToAcceptanceVolume` 失败：

```text
acceptance volume has owner-inconsistent mappings: 434 total, 435 owner-consistent
```

“可解析行数大于总行数”不是数据损坏，而是测试在默认 Read Committed 下用两条独立 `SELECT count(*)`
读取共享 acceptance volume；其他 `t.Parallel()` 纵向测试恰好在两条语句之间提交新 mapping。该文件在
当前分支相对 `main` 未改，但本任务新增了更多并行 acceptance writer，完整门禁已实际暴露此 flake。
失败后该测试单独 `-count=10` 全通过，第二次完整 `make test-integration`（含 restart）也通过，进一步
证明是观测竞态而不是外键不一致。

### 必须完成

- 在 `tests/integration/project_installation_migration_test.go` 中把该断言改为**单条 SQL、单个 statement
  snapshot** 的一致性判断。优先直接统计 owner 不匹配/缺失的 mapping（`LEFT JOIN ... WHERE
matched installation IS NULL`）并断言为 0；也可以在一条语句中同时返回 total/resolvable。
- 保持对 005 owner-bound composite FK 的真实验证强度；不得删除 `t.Parallel()`、不得 skip、不得加
  retry/sleep、不得仅放宽为大小比较。
- 不得清理共享 volume 来“修复”计数；历史数据必须原样保留。

## 阻断项三：`dbtransient.IsTransient` 可被短 SQLSTATE 切片崩溃，分类矩阵无单测

`internal/platform/dbtransient/transient.go` 当前直接执行 `pgErr.Code[:2]`。真实 PostgreSQL 通常返回
5 字符 SQLSTATE，但畸形代理、测试 double 或异常协议响应可构造空/1 字符 code；availability 分类器
不应因依赖错误让进程 panic。该新 package 当前也没有测试，SQLSTATE class 08/53/57/58 与普通
constraint/programming error 的边界未被钉死。

### 必须完成

- 在切片前检查长度；空 code、1 字符 code 和未知 class 必须安全返回 false，不得 panic，也不得按
  message 文本猜测。
- 新增 `internal/platform/dbtransient/transient_test.go`，至少覆盖：nil、普通错误、wrapped
  `context.DeadlineExceeded`、一个真实/自定义 `net.Error`、wrapped `*pgconn.PgError` 的
  08/53/57/58 class、明确非 transient 的 23/42 class，以及空/1 字符 code。
- 保持 adapter 只在 port 边界附加各模块 `ErrStoreUnavailable`；不要借本轮扩展错误语义。

## 必跑验收

在干净候选分支执行并记录实际 exit code：

```bash
make generate
make generate
git diff --check
git diff --check main...HEAD
make check
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$PWD:/workspace" -w /workspace bufbuild/buf:1.55.1 \
  breaking --against '.git#branch=main'
```

然后执行：

- `go test -race -count=2 ./internal/platform/dbtransient ./internal/runtime/...`（使用仓库固定 Go 容器）；
- 真实 PostgreSQL 定向 race：Surface concurrency、create/close race、Artifact rollback、Artifact
  concurrency、Surface durability；
- `TestProjectInstallationMigrationAppliedToAcceptanceVolume` 定向多次复跑；
- `make test-integration` 连续完整通过至少 2 轮，均含 restart seed/verify；不得把失败后重试当成两轮通过；
- `make test-deepseek-fixture`（只用 Makefile 内置 fixture 假凭据）；
- `make test-e2e`。

生成后必须无漂移。再次核对 001–005、006/007 checksum、无 008、`docs/structure.md` 不变；扫描
tracked/root 文件与 `main..HEAD` 可达对象，确认没有 ELF、secret、测试输出或大 blob。不要搜索、输出
或引用聊天中的真实 key；secret 扫描只报告是否命中，不回显疑似 secret 内容。

## 交接条件

交接必须包含：

1. 新的干净候选分支名、HEAD、与 `main` 的祖先/领先关系；
2. `main..HEAD` 最大 blob 列表，以及污染 blob/path 不可达的证据；
3. 单 statement consistency query 的说明与连续两轮完整集成结果；
4. `dbtransient` 分类矩阵和短 SQLSTATE 不 panic 的测试结果；
5. 所有必跑命令、exit code、migration checksum、最终 clean worktree；
6. 明确“未 merge、未 push、未删除 volume、未使用真实 Provider key”。

只有上述条件全部满足，才可交回审核者决定是否本地 `--ff-only` 合并到 `main`。
