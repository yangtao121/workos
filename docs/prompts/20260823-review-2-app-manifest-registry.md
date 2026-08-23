# 下一位智能体 Prompt：App Manifest Registry 第二轮审核修复

> 将本文件完整交给修复智能体。当前分支仍未达到合并条件；只修复本文列出的第二轮问题，完成测试、
> 文档和提交，不要只输出计划。

## 你的角色与当前状态

你是 WorkOS feat/app-manifest-registry 分支的第二轮修复智能体。仓库位于
/home/aquatao/workos。审核时 HEAD 为 42755bd（fix: harden app registry invariants），本地 main 为
484c057；开始时必须重新检查，不得盲信上述哈希。

第一轮六组核心修复方向已经落实：

- 003 前向 migration 建立独立 app_registration_requests 幂等映射；
- Register 成功路径原子消费每个 key；
- ListApps 使用 application page result 与 limit+1；
- AppRegistry Connect handler 设置 384 KiB 解码前/解压后消息上限；
- current/List 改为 summary 流式折叠，不再物化全部 canonical manifest；
- Makefile 的 HOME/GOPATH 与全局 DNS workaround 已恢复。

第二轮审核实际运行 make check，Go/Proto/sqlc/架构、TypeScript、Desktop tests/build 和 status check
全部通过；git diff --check、git diff --check main...HEAD 通过，002 migration 与 8e48d92 中内容及
checksum 一致。

但静态审核和数据库只读核查仍发现安全漏检、测试资源泄漏与不实文档证据，因此当前分支不得合并。
不要重做第一轮架构，不要实现 Project install/uninstall、Runtime、Surface、App Bridge、Credential
Vault 或其他后续模块。

## 凭据边界

- 本任务不需要真实 DeepSeek 或其他 Provider 凭据。
- 不得使用、保存、转述、验证或尝试恢复聊天中出现过的真实 Key。
- 不得从聊天、shell history、环境变量或本机文件搜集凭据。
- 所有 Provider 验收继续使用仓库已有 fixture 假 credential，不访问真实 Provider 网络。
- 新的 secret 测试只能使用明显虚构的合成串，错误、日志、快照和任务记录不得回显其内容。

## 开始前

1. 完整阅读：
   - AGENTS.md
   - docs/prompts/20260823-next-agent-app-manifest-registry.md
   - docs/prompts/20260823-review-app-manifest-registry.md
   - docs/tasks/20260823-app-manifest-registry.md
   - docs/architecture/implementation.md
   - docs/status.json
   - schemas/workos-app-manifest-v1.schema.json
   - internal/core/appregistry/adapters/manifestvalidator 下全部代码和测试
   - internal/core/appregistry/adapters/postgres 下全部代码和 sqlc source
   - internal/platform/migrations/migrate.go、002、003
   - tests/integration/app_registry_test.go
   - tests/integration/app_registry_migration_test.go
2. 运行：

   git status --short --branch
   git log --oneline --decorate -8
   git branch -vv
   git diff --check
   git diff --check main...HEAD
   git diff --exit-code 8e48d92 -- internal/platform/migrations/files/002_app_registry.sql

3. 保留不属于本任务的改动。继续在当前功能分支工作，不得 reset、rebase 或直接修改 main。
4. 将任务状态从 done 改回 active。修复和重新验收前将 App Registry 状态降为 scaffolded，并通过生成
   工具同步 README；全部证据成立后才能恢复 working/done。
5. 002 和已经执行的 003 都是 checksum 保护的前向 migration，不得修改。本文修复不需要新表结构；
   若发现确实需要数据契约变化，停止并先说明，而不是修改旧 migration。
6. 禁止 docker compose down -v、删除 PostgreSQL volume、手改生成文件或 README 状态区块。

## 阻断项一：credential-shaped 字符串作为 map key 时仍会入库

当前 scanSecrets 对 map key 只调用 secretBearingKey；secretValuePatterns 只在 switch 的 string value
分支运行。于是以下类型的字符串如果作为 resources、health 或 maintainer 的 key：

- 合成的 sk-/ghp-/xoxb- 等 prefixed token 形态；
- JWT 形态；
- AWS access-key ID 形态；
- PEM/private-key header 形态；

不会被 value pattern 检查。它们能通过 validMappingKey，secretBearingKey 也不一定命中，随后进入
canonical JSON 和 app_versions.canonical_manifest。canonical Schema 的三个自由结构块都允许
additional properties，因此 Schema 不会替此策略兜底。

这违反了原任务“manifest 的 key/value 中不得携带明显 credential material”的边界。更危险的是，若
事后直接用 pointerChild 报错，credential 本身会进入 violation path。

### 必须达到的行为

- secret-bearing 字段名称策略与 credential-shaped 字符串策略保持两个概念：
  - accessToken、clientSecret 等字段名称可以继续报告安全字段路径；
  - key 本身若形似 credential，只能报告父路径/安全位置，绝不能把原 key 拼进 JSON Pointer。
- 对每个 map key 在任何 credential-shaped key 的 pointer 构造、canonicalization 或持久化前执行
  value-pattern 等价检查，或采用有同等安全证明的集中 helper。
- key/value 共用单一 credential-shape 规则实现，避免两份 regex 漂移。
- ValidateManifest 返回 bounded、deterministic、value-free violation；Register 返回净化的
  InvalidArgument，不能持久化该 manifest。
- 保留第一轮 control-character/UTF-8/长度 key guard、secret name tokenization 与邻近非 secret
  名称行为。

### 必须新增的测试

在结构和 Schema 都合法的单一 manifest 中，把明显合成的 credential-shaped 字符串分别作为自由块 key：

1. prefixed-token-shaped key；
2. JWT-shaped key；
3. AWS-shaped key；
4. PEM/private-key-shaped key。

每项都必须：

- 明确命中 credential-material policy，而不是 duplicate key、unknown root field 或普通 Schema 错误；
- 只报告 resources/health/maintainer 的安全父路径；
- violation 中不包含合成 token 的前缀、payload 片段、完整 key 或 escaped key；
- normalized manifest/digest 为空；
- Register 不写 app_versions 或 app_registration_requests。

再保留非 credential 邻近 key 的通过用例，防止过宽匹配。绝对不要复制聊天里的真实 Key。

## 阻断项二：migration scratch database cleanup 永远在已关闭连接上执行

tests/integration/app_registry_migration_test.go 的 scratchDatabase 当前逻辑是：

    admin := pgx.Connect(...)
    defer admin.Close(...)
    t.Cleanup(func() { admin.Exec(DROP DATABASE ...) })
    return dsn

helper 返回时 defer 立即关闭 admin；测试结束时 cleanup 使用的是已关闭连接，而且忽略 DROP 错误。因此
每次 migration integration run 都永久留下两个 workos*migration_test*\* 数据库。

第二轮审核只读查询已经确认当前 PostgreSQL 实例残留 6 个此类 scratch database。审核者没有删除它们，
也没有删除 volume。

### 必须达到的行为

- cleanup 必须在仍可用的 admin connection 上执行，或在 cleanup 时建立新的 admin connection。
- cleanup 使用独立、有界 context；先关闭被测 scratch connections，再按精确 database name 执行
  DROP DATABASE ... WITH (FORCE)，最后关闭 admin connection。
- DROP/close 失败必须让测试失败或至少产生明确 test error，不能继续忽略。
- cleanup 在测试成功、t.Fatal、panic/提前返回时都执行。
- database identifier 必须由内部生成并安全 quote；不能拼接用户输入。
- 不得 wildcard DROP，不得删除 postgres/workos 或任何非本次测试创建的数据库。

### 验证要求

- 在运行 migration tests 前记录 workos*migration_test*\* 的精确名称集合。
- 连续运行 migration integration tests 两次；每次结束后的集合必须与运行前完全相同，即不新增残留。
- 增加能够防止“helper 返回即关闭 cleanup connection”回归的测试或可检查的 helper 设计。
- 审核时已存在的 6 个历史残留只做清单和风险报告；没有用户明确授权时不要顺手删除。

## 阻断项三：高基数分页测试持续污染持久 acceptance volume

ListAppsPagingDefaultsClampAndExactFinalPage 每次向共享 workos 数据库注册至少 105 个 bulk App，再补最多
99 个 pad App，但从不清理。第二轮审核只读查询确认当前 volume 已累计 573 条 app_id 以 bulk-/pad-
开头的 app_versions。

这使 make test-integration 每次运行都永久增加上百条记录，分页扫描与后续门禁会越来越慢，也让任务
记录中的 volume/backfill 数量证据迅速失真。禁止通过删除整个 volume 解决。

### 必须达到的行为

选择一种不污染共享 acceptance 数据的方案：

- 优先把高基数分页场景放到能完整清理的隔离数据库/测试服务；或
- 精确追踪本次测试创建的 app IDs、idempotency keys 和 version IDs，在 subtest cleanup 的单一事务中
  先删除对应 request mappings，再删除对应 versions，并验证归零。

约束：

- 只能清理本次测试以唯一 stamp 创建的精确 ID/key 集合；不得用 broad LIKE、通配符或清空表。
- 不能删除 board/notes、restart seed、其他测试或用户已有 Registry 数据。
- cleanup 在 subtest 失败时也必须运行，数据库错误必须暴露。
- 高基数测试仍须经过真实 Gateway→Core→PostgreSQL 链路，不能退化成只测 fake repository。
- 测试开始时数据库非空是正常情况；分页断言不得依赖空库。

### 验证要求

- 在第一次 make test-integration 前后记录 app_versions 与 app_registration_requests 数量，以及本次
  stamp 对应的精确行数；本次 bulk/pad 行在测试结束后必须为零。
- 连续执行 make test-integration 两次，第二次结束后总量不能因高基数分页 fixture 再增长。
- 历史 573 条 bulk/pad 数据只报告，不得在没有明确授权时批量删除。

## 阻断项四：并发 immutable-version 测试没有断言 AlreadyExists

ConcurrentRegistrationsAgreeOnOneFact 的“两个不同 key、同 app/version、不同 digest”段落声称 loser
得到 AlreadyExists，但当前测试只判断“一成功一失败”，没有检查失败 Connect code，也没有验证 loser
key 未被消费。任务记录却把“一胜一 AlreadyExists”写成已证明事实。

### 必须补齐

- 结果 channel 携带请求身份/key，不要丢失 winner/loser 对应关系。
- 明确断言 loser 为 CodeAlreadyExists，而不是 Aborted/Internal/Unavailable。
- 查询数据库验证：
  - 只保留 winner immutable version 和 digest；
  - winner key mapping 指向 winner version；
  - 失败的 loser key 没有 mapping，因为当前设计只消费成功请求。
- 用 loser key 注册一个不冲突的新请求应成功，进一步证明失败事务没有错误消费 key；随后该 key 的不同
  请求重放必须 Aborted。
- 测试继续使用 channel/barrier，不使用随机 sleep。

## 阻断项五：任务记录的“当前设计”仍描述已经删除的结构

docs/tasks/20260823-app-manifest-registry.md 顶部设计、协议/数据影响和 implementation supplement 仍写：

- 只有 002 migration；
- app_versions 持有 idempotency_key/request_digest 和对应 UNIQUE；
- Register 按旧 app_versions key 分类；
- ListApps 批量物化所有版本；
- Dockerfile/Makefile/compose 强制 GODEBUG=netdns=cgo。

文件后半部又写 003 已删除这些列、summary 流式读取并移除 GODEBUG。虽然附有“修复前历史”说明，顶部
没有标成历史，任务事实源因此自相矛盾，不符合文档/任务记录同步的完成定义。

### 必须达到的文档状态

- 顶部“设计”“数据所有权”“协议/数据影响”“实现 supplement”全部改成最终现状：
  - 002 创建 immutable app_versions；
  - 003 backfill 后由 app_registration_requests 唯一持有幂等映射；
  - app_versions 已无 idempotency_key/request_digest；
  - current/List 使用 summary streaming；
  - Connect read max 与 application manifest limit 分层；
  - 无全局 netdns=cgo。
- 旧的错误证据可以压缩为简短审核历史，但不能与当前设计并列成看似仍有效的事实；删除重复的旧“最终
  证据”清单。
- 修正 MaxRequestBytes 说明中的“128-byte idempotency key”：实际规则是最多 128 rune，最坏 UTF-8
  byte 数应按真实上界描述。
- 将本轮 secret-key、database cleanup、高基数 fixture cleanup 与并发错误码证据写入最终验收。
- 只有真实完成后才恢复任务 done、status working，并由生成工具更新 README。
- docs/architecture/implementation.md 只需在当前描述不准确处同步，不要扩展产品主线。

## 验收顺序

先运行针对性测试并证明资源不再增长，再运行完整门禁。

### 针对性

    make check
    docker compose up -d --build postgres bootstrap workos-core harness-host workos-gateway
    go test equivalent: manifestvalidator credential-shaped key cases
    go test equivalent: App Registry concurrent immutable-version case
    go test -tags=integration -count=1 -run 'TestAppRegistryMigrations' ./tests/integration
    go test -tags=integration -count=1 -run '^TestAppRegistryVerticalSlice$' ./tests/integration

仓库标准测试通过 Docker runner 执行；上面的 go test equivalent 表示使用现有 Makefile runner/环境，不要
绕开依赖或改变 HOME/GOPATH。

对 migration scratch database 集合和高基数本次 fixture 行执行前后只读计数，记录两次连续运行结果。
不得把“测试命令退出 0”当作 cleanup 证据。

### 完整门禁

    make generate
    make generate
    make check
    make test-integration
    make test-deepseek-fixture
    make test-e2e
    buf breaking --against '.git#branch=main'
    git diff --check
    git diff --check main...HEAD

另外确认：

- 第二次 make generate 无差异，README/status/sqlc/Proto 一致。
- 002 与 003 均无变化，没有新增 migration。
- docs/structure.md 未变化。
- 无 root-owned 文件、untracked 测试产物或真实 credential。
- workos_workos-postgres volume 未删除。
- fixture tests 不访问真实 Provider 网络。
- 最终 git status --short 为空。

## 提交与交接

- 全部完成后使用 Conventional Commit，建议：

      fix: close app registry review gaps

- 提交到当前功能分支，提交后工作树必须干净。
- 不要 merge、push、rebase、force、删除分支或执行 docker compose down -v。
- 最终报告包含：
  - credential-shaped map key 的安全拒绝路径和测试；
  - scratch database cleanup 的连接生命周期与连续两次零新增证据；
  - 高基数 fixture 的精确 cleanup 与连续两次不增长证据；
  - immutable-version loser 的 AlreadyExists/mapping 断言；
  - 收敛后的任务记录；
  - 全部门禁、分支和提交 ID。
- 明确说明未使用真实 Key、未删除历史数据库/volume、未 merge、未 push。
- 由审核者进行第三轮静态复审；只有无 blocker 后才执行本地 git merge --ff-only。
