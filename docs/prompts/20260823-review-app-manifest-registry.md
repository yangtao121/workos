# 下一位智能体 Prompt：App Manifest Registry 审核修复

> 将本文件完整交给修复智能体。当前实现未达到合并条件；直接修复、验证并提交，不要只输出计划。

## 你的角色与审核结论

你是 WorkOS 的 App Manifest Registry 修复智能体。仓库位于 /home/aquatao/workos，目标分支是
feat/app-manifest-registry。审核时该分支 HEAD 为 8e48d92（feat: implement app manifest registry），
本地 main 为 484c057；开始时必须重新检查，不能把哈希当成永久事实。

原实现已经建立 Schema validator、domain/application/ports/PostgreSQL/Connect 纵向结构，也报告完整门禁
通过；git diff --check main...HEAD 在本次审核中通过。但静态审核确认幂等语义、分页可达性和输入资源
边界存在可复现缺陷，部分测试实际上没有覆盖其声称的安全策略，因此当前分支不得合并。

只修复本文列出的 App Registry 问题及对应测试、任务记录、架构/status 事实。不要实现 Project
install/uninstall、Runtime、Surface、App Bridge、Credential Vault、权限授予或其他后续功能；不要改动
六进程边界。

## 凭据与安全

- 本任务不需要真实 DeepSeek 或其他 Provider 凭据。
- 不得使用、保存、转述、验证或尝试恢复聊天中出现过的真实 Key。
- 不得从聊天历史、shell history、进程环境或本机文件搜集凭据。
- 所有测试继续使用仓库已有的 fixture 假 credential，不访问真实 Provider 网络。
- manifest 测试只能使用明显虚构的合成字符串，并且错误、日志和快照不得回显这些值。

## 开始前必须完成

1. 完整阅读：
   - AGENTS.md
   - docs/prompts/20260823-next-agent-app-manifest-registry.md
   - docs/tasks/20260823-app-manifest-registry.md
   - docs/architecture/implementation.md
   - docs/status.json
   - schemas/workos-app-manifest-v1.schema.json
   - api/proto/workos/app/v1/app.proto
   - internal/core/appregistry 下全部实现与测试
   - internal/core/orchestration/project_directory.go 及测试
   - internal/platform/migrations/migrate.go 与 files/001、files/002
   - cmd/workos-core/main.go、internal/platform/httpserver、internal/gateway
   - Dockerfile、Makefile、compose.yaml、sqlc.yaml
   - tests/integration/app_registry_test.go
   - tests/integration/app_registry_migration_test.go
   - tests/restart/main.go
2. 运行并记录：

   git status --short --branch
   git log --oneline --decorate -8
   git branch -vv
   git diff --check
   git diff --check main...HEAD

3. 保留不属于本任务的已有改动。必须继续在功能分支工作，不得 reset、rebase 或直接改 main。
4. 将 docs/tasks/20260823-app-manifest-registry.md 的状态从 done 改回 active，先记录本审核结论。
5. App Registry 在修复和重新验收前不得宣称 working。将 docs/status.json 暂时改为 scaffolded，并通过
   生成工具同步 README；全部真实验收通过后才能恢复 working。
6. 002_app_registry.sql 已在现有验收 volume 中执行并受 checksum 保护，绝对禁止修改。任何表结构修复
   必须使用新的前向 migration，预期编号为 003（若执行时已占用则顺延）。
7. 禁止 docker compose down -v、删除 workos_workos-postgres volume、手改 gen/、src/gen/ 或 README
   生成状态区块。

## 阻断项一：新幂等键在成功响应后没有被持久化

当前 app_versions 每个 immutable version 只有一个 idempotency_key。Repository.Register 在
InsertAppVersion 冲突后先查相同 app/version；若 manifest digest 相同，直接返回旧记录并让事务回滚。
因此下列时序会破坏文档声称的不变式：

    K1 注册 manifest M → 创建 version row，row 只保存 K1
    K2 注册同一 manifest M → 返回成功和旧 row，但 K2 没有写入数据库
    K2 再注册另一份 manifest N → 当前实现可以成功

这违反了：

    (owner_user_id, idempotency_key) → exactly one normalized registration request

并发时还存在分类顺序问题：同一个 key 被另一事务用于不同请求，而目标 version 恰好已存在且 digest
相同时，失败事务可能先按 version 返回成功，忽略已经提交的 key 冲突。

现有测试遗漏了关键断言：

- application fake 与 PostgreSQL 测试只断言“相同 version/digest + 新 key”返回成功，没有随后复用该
  新 key。
- 八个不同 key 并发注册相同 manifest 的测试只看响应成功；数据库最终仍只保存 winner 的一个 key。

### 必须达到的行为

- 每个返回成功的非空幂等键都必须在同一事务中持久化，包含“version 已存在且 digest 相同”的成功。
- 同 owner、同 key、同 normalized request 重放第一次结果。
- 同 owner、同 key、不同 normalized request 永远返回 Aborted；不能因为目标 version 已存在而绕过。
- 同 owner、同 app/version、同 digest 可由多个不同 key 成功引用同一 immutable version。
- 同 owner、同 app/version、不同 digest 返回 AlreadyExists，不覆盖旧事实。
- key 映射与 version 插入必须原子提交；竞争失败不得遗留 orphan version 或未消费的成功 key。
- 不同 owner 的相同 key 继续隔离。

### 数据修复要求

- 新增前向 migration，建立独立、权威的 registration request/idempotency mapping；建议以
  owner_user_id + idempotency_key 为主键或 UNIQUE，并保存 request_digest 及所指向 immutable
  app-version 的稳定引用。
- 从 002 现有 app_versions 行回填原有 key 映射，保证已有 volume 和空库迁移路径一致。
- app_versions 上的旧 idempotency_key 列/约束若保留，必须明确其兼容角色；不得让旧列与新 mapping
  成为两个会产生不同裁决的事实源。需要调整约束时只能在新 migration 中完成。
- 外键、owner 隔离和删除/更新策略必须 fail closed。App version 仍是 immutable fact。
- Repository 事务必须以数据库约束裁决竞争，不得使用进程内 mutex、先查后写的竞态窗口或依赖错误
  constraint 名称字符串。
- sqlc 源与生成代码同步；不能手改生成文件。

### 必须新增的 PostgreSQL 证据

1. K1 注册 M，K2 注册相同 M，二者返回同一个 immutable version；随后 K2 注册 N 必须 Aborted。
2. N 个不同 key 并发注册相同 M：全部成功、只存在一个 version；每个 key 随后用于不同请求都必须
   Aborted，并验证数据库有 N 条 request mapping。
3. 同一个 key 并发注册两个不同 manifest：只有一个请求成为确定事实，另一个 Aborted；事务失败侧不能
   留下 version 或 mapping。
4. 相同 app/version 的不同 digest 竞争仍为一胜一 AlreadyExists，旧 manifest 不被覆盖。
5. migration 从包含 002 数据的现有 volume 前向执行并正确 backfill；从空库执行 001→002→新 migration
   也通过。

这些语义必须由真实 PostgreSQL integration test 证明，不能只修 fake repository。

## 阻断项二：ListApps 使用原始 page_size 计算 next token

application.Service.List 会把 page_size <= 0 规范为 50，把大于 100 的值收紧为 100；transport.ListApps
却仍使用请求中的原始 page_size 判断是否生成 next_page_token。

可复现结果：

- 请求不带 page 或 page_size=0 时，service 最多返回 50 条，但 transport 永远不生成 token；第 51 条
  以后不可达。
- 请求 page_size=101 时，service 返回 100 条，transport 比较 101，因此也不生成 token。
- 恰好装满最后一页时，当前实现仍会产生一个指向空页的假 token。

### 必须达到的行为

- page size 只在 application 边界规范化一次；transport 不得重新猜测 effective limit。
- application/repository 返回明确的 page result（items + next token，或等价结构）。
- 查询使用 effective limit + 1 判断是否确有下一页，只返回 effective limit 条；只有存在额外记录时才
  生成 token。
- token 继续基于最后一条已返回 app ID，排序、cursor 和 owner scope 必须一致且可恢复。
- 空结果和最后一页 next token 为空；翻页无重复、无遗漏。
- page_size < 0 必须按明确的请求规则处理，不能静默形成意外默认；保留已记录的默认 50、上限 100
  语义。

### 必须新增的测试

- nil page 与 page_size=0，在超过 50 个 app 时都能拿到下一页。
- page_size > 100，在超过 100 个 app 时能拿到下一页且首屏不超过 100。
- 数据量恰好等于 effective limit 时不返回假 token。
- limit+1、第二页、末页、空页、owner 隔离与稳定 app-ID 顺序。
- transport 测试必须证明它使用 application 返回的 token，而不是原始请求值。

## 阻断项三：256 KiB 限制发生在 Connect 解码之后

transport.ValidateManifest 和 RegisterApp 对 bytes 字段调用 len 时，请求已经被 Connect 解码。当前
Core handler 没有 read max，通用 http.Server 也只有 timeout；超大 protobuf/Connect JSON 或压缩请求
可以在字段检查前占用无界或远高于业务上限的内存。原任务明确要求 Connect/body 层和 application 层
都有边界，当前只实现了解码后的 transport/application 检查。

### 必须达到的行为

- 在 AppRegistryService 的 Connect handler 构造层设置库原生的有限 read/decompressed-message 上限，
  或提供有同等证明的 body middleware；限制必须在完整 protobuf 解码前生效。
- 该上限需容纳合法的 256 KiB manifest、protobuf framing、idempotency key 和 Connect JSON 的 base64
  膨胀，同时仍是一个明确的小常量。记录选择依据，不得沿用库的宽松默认值。
- application 的 256 KiB manifest 字段检查必须保留，不能只依赖 HTTP Content-Length。
- 考虑压缩请求；只限制压缩后的 Content-Length 不算完成。
- 限制只作用于正确范围，不能意外破坏其他 public/private RPC。
- 超限错误必须有稳定 Connect code 和净化文本，不回显 body。

### 必须新增的测试

- 用真实 Connect HTTP handler 发送超限 protobuf 请求，证明业务 handler/repository 未执行。
- 覆盖 Connect JSON 或仓库实际支持的另一编码，证明 base64 overhead 下合法边界不会误拒绝。
- 覆盖压缩路径或以库级测试证明限制针对解压后的 message。
- 256 KiB 以内仍由 validator/application 正常处理，256 KiB + 1 的业务字段稳定拒绝。

## 阻断项四：字符串 key 未校验，secret 测试在错误路径上通过

scanStrings 只遍历 map value，不检查 map key。resources、health、maintainer 在 canonical Schema 中允许
自由 additional properties，因此带 C0/C1/NUL 等控制字符的 quoted YAML key 可进入 canonical JSON 和
数据库。该 key 还会先进入 JSON Pointer，导致 violation 文本携带控制字符，形成日志/UI 注入面。

此外，当前 secret 测试中的 password key、private key value、token-shaped value 都是在已有
resources 根字段之后追加第二个 resources。它们首先因 duplicate mapping key 被拒绝，所以没有证明
secret policy 生效；任务记录的“secret 4 子项”证据不准确。

secret key 正则也遗漏常见 compound/camelCase 名称，例如 accessToken、clientSecret、
credentialValue、awsSecretAccessKey。这些明显是 secret-bearing key，但只要 value 不匹配已知 token
前缀，当前实现可能接受。

### 必须达到的行为

- mapping key 在任何 pointer 构造、map 插入、Schema 校验或持久化之前检查有效 UTF-8、C0/C1/NUL
  控制字符及合理长度。
- unsafe key 的错误只能报告安全的父路径/位置，不能把原始危险 key 拼进 violation。
- value 字符串的既有规则继续生效；key 和 value 都要覆盖自由结构块。
- secret key policy 以可解释的 tokenization/normalization 或等价方式覆盖常见 snake_case、
  kebab-case、compound 和 camelCase；同时用邻近非 secret 名称证明不会把任意包含字母片段的字段全部
  误杀。
- violation 仍确定排序、去重、有数量与单条长度上限，且绝不回显 key/value credential material。

### 必须修正的测试

- 不再追加重复 resources；在一份结构合法、Schema 合法的 manifest 内替换或注入自由字段。
- 对每个 secret case 断言具体安全路径和 policy 消息，而不只是断言 violations 非空。
- 分别覆盖 secret-bearing key、PEM-like value、token-shaped 合成 value、Bearer/JWT/AWS-like 合成值。
- 覆盖 accessToken、clientSecret、credentialValue、awsSecretAccessKey 等 compound/camelCase key。
- 覆盖 resources/health/maintainer 内 quoted control-character key；断言响应中没有该控制字符或原始
  key。
- 添加合理的非 secret 邻近名称，防止正则因过宽而产生明显误报。

测试 fixture 只能使用虚构值；不要复制聊天中的任何真实 Key。

## 阻断项五：current/List 读取会物化无界版本和完整 manifest

GetApp(app_id) 当前调用 GetAppVersions，把该 App 的所有 version 及每条最多 256 KiB 的
canonical_manifest 一次性载入内存，再在 Go 中选择 current。ListApps 对最多 100 个 app ID 又把这些
App 的所有 version 和完整 manifest 全部载入并按 app 分组。注册版本数量没有上限，因此公开查询的
内存与数据库传输量不受 page size 约束。

### 必须达到的行为

- Get current 和 List current 不得物化所有 canonical_manifest；公开摘要查询只选择响应和 SemVer
  比较真正需要的列。
- 进程内存必须受请求 page size 的明确常量约束，不能随历史 version 总数线性增长。
- 选择 current 仍必须严格遵守已有 SemVer precedence，不能改成 version 文本排序或 created_at。
- 可以采用事务维护的 current projection/pointer、可证明正确的持久化 sort key，或数据库 row
  streaming + 固定大小 accumulator；选择方案必须记录并用并发注册证明一致性。
- 若增加 current 表、列、索引或约束，继续使用新前向 migration，不能修改 002。
- 精确 version 查询仍返回同一 immutable version；canonical manifest 只在真正需要它的内部路径读取，
  不进入日志或公共响应。

### 请求字段边界同时补齐

- Register idempotency key 必须有一致的 UTF-8、控制字符、byte/rune 长度规则；非法编码不能掉到 pgx
  后变成 Internal，也不能被静默 trim。
- GetApp 的 app_id 必须按 canonical app-ID grammar 校验；畸形值是 InvalidArgument，不是数据库错误或
  假 NotFound。
- List cursor 既然定义为 last app ID，就必须有小而确定的长度/grammar 校验；超长或畸形 token 返回
  InvalidArgument。
- 非空 project_id 在进入 UUID 数据库参数前必须安全校验；外部 owner/不存在/归档仍保持净化的
  NotFound 语义。

增加单元和 Connect 测试验证 InvalidArgument/Internal 分类；不得把其他模块的 adapter 或 SQL 导入
App Registry。

## 阻断项六：Makefile 和 DNS workaround 引入无关回归

当前 Makefile 的 USER_FLAGS 已设置 HOME=/tmp，但 GO_RUN 又重复设置 HOME。GO_HOST_RUN 更把原来的
GOPATH=/tmp/workos-go 误改成 HOME=/tmp/workos-go，导致 GOPATH 丢失且两个 Go runner 语义不一致。

本提交还因为单个开发网络的 AAAA/DNS 问题，在全部 Go runner、Docker build stage 和 DeepSeek fixture
compose 中强制 GODEBUG=netdns=cgo。这是机器相关 workaround，不应成为 App Registry 的全局、硬编码
运行语义，也与本功能无直接关系。

### 必须达到的行为

- 恢复一致的 USER_FLAGS、HOME 与 GOPATH：不重复 HOME，GO_RUN/GO_HOST_RUN 都保留
  GOPATH=/tmp/workos-go。
- 保留 Dockerfile 为 schema embed 所必需的 COPY schemas。
- 移除全局硬编码的 netdns=cgo。若有可跨环境复现的必要性，改成默认关闭、调用方显式选择的最小范围
  override，并在任务记录写证据；不能把本机网络条件冒充产品依赖。
- 不改变现有 fixture 假 credential、网络隔离和测试目标语义。

## 测试与证据修正原则

- 不得只修改 fake repository 让测试变绿；fake 必须镜像新的持久化语义，关键竞争由 PostgreSQL 证明。
- 并发测试使用 barrier/channel 等确定同步，不能依赖随机 sleep。
- 任何“成功”测试都要检查持久化后果和随后重放/冲突，不只检查第一次响应。
- migration 测试必须验证新 migration 名、backfill、约束和 checksum 不变；明确断言 002 文件未修改。
- 请求大小测试必须经过真实 HTTP/Connect 解码边界，直接调用 Handler 方法不足以证明。
- 分页测试必须覆盖默认值、clamp、limit+1 与最后一页。
- owner isolation 至少在真实 PostgreSQL 路径覆盖两个 owner。
- Gateway private service allowlist 不得扩大。

## 文档与状态修正

docs/tasks/20260823-app-manifest-registry.md 当前关于以下内容的完成证据不真实或不完整，必须先更正，再在
修复后写入新的实际证据：

- “每个 owner/idempotency key 恰好一个 registration”；
- “同 version/digest 不同 key 的持久化重放”；
- “secret 4 子项”；
- 默认/上限分页；
- Connect/body 层 256 KiB 边界；
- Makefile/Dockerfile 的强制 DNS workaround。

同步修正 docs/architecture/implementation.md 中的幂等、分页和 bounded-read 描述。README 状态区块只
能由 make docs/make generate 从 docs/status.json 生成，禁止手改。

只有下列全部满足后才能：

- 将任务状态恢复 done；
- 将 App Registry 从 scaffolded 恢复 working；
- 写入 schema-backed immutable registration + durable idempotency + bounded paging/read +
  restart persistence 的具体 evidence。

不得提升 Runtime / Surface、App SDK、App Host、Desktop App Library、Access Gateway 或权限授予状态。

## 完整验收

修复后实际运行并把逐项结果写入任务记录：

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

- 第二次 make generate 无差异，sqlc/Proto/README 均一致。
- 002_app_registry.sql 与 main...原功能提交中的内容完全相同；只新增前向 migration。
- 新 migration 在空库和当前已有 volume 都成功，旧幂等数据完成 backfill。
- docs/structure.md 未变化。
- 没有 root-owned 文件、untracked 测试产物或真实 secret/credential。
- workos_workos-postgres volume 未删除。
- 默认、DeepSeek fixture 和 E2E 都不访问真实 Provider 网络。
- git status --short 最终为空。

如果完整门禁暴露本任务之外的基线失败，记录精确命令和证据；不要放宽测试、删除数据或扩大产品范围来
绕过。

## 提交与交接

- 全部修复和验收通过后使用 Conventional Commit，建议：

      fix: harden app registry invariants

- 提交到当前功能分支，提交后工作树必须干净。
- 不要 merge、push、rebase、force、删除分支或执行 docker compose down -v。
- 最终报告必须包含：分支与提交 ID、新 migration 设计/backfill、幂等事务裁决、分页 token 归属、
  pre-decode body limit、bounded current read、validator key/secret 测试、所有门禁结果和未决风险。
- 明确说明未使用真实 Key、未访问真实 Provider、未 merge、未 push、未删除 volume。
- 由审核者重新静态复审；只有复审确认无 blocker 后才执行 git merge --ff-only 合并到本地 main。
