# 下一位智能体 Prompt：Project Knowledge Search——持久索引、授权 App 与可恢复重建闭环

> 将本文件完整交给下一位实现智能体。用户将离线休息，希望你持续自主工作至少七小时（预期九至十二
> 小时）并直接完成实现，不是只输出计划、审查报告或下一份 Prompt。
> 整个批次只有一个最终目标、一个 branch、一个 worktree、一个任务记录和一个写入智能体；所有阶段严格
> 串行，禁止为了并行或审核再创建分支、worktree 或让其他 Agent 修改仓库。

## 你的角色与唯一最终目标

你是 WorkOS `indexer` 第一条真实产品链路的实现与收口智能体。仓库位于
`/home/aquatao/workos`；`docs/structure.md` 是产品架构主线，
`docs/architecture/implementation.md` 是当前代码边界，`docs/status.json` 是唯一状态事实源。

当前 `indexer` 只有 health scaffold：`IndexService` 返回 Unimplemented，`archive`/`rag` capability
均为 false，`docs/status.json` 只记录 “health; indexing unavailable”。另一方面，Project、不可变
Markdown/Diff review Artifact、Artifact → Agent context、Fake/DeepSeek structured output、Gateway
device identity 和 Adaptive Desktop 已经具备真实链路。

本批次的唯一最终目标是：

```text
Project Agent 生成 canonical Markdown / unified-diff review Artifact
  → Core 在同一事务发布不含内容正文的 durable indexing publication
  → indexer 以 at-least-once + lease/replay 方式消费
  → indexer 自有 PostgreSQL projection 持久保存可检索文档与消费事实
  → owner 在当前 Project 的 Knowledge Center 进行有界、确定性的 lexical search
  → 搜索结果返回安全 excerpt 与 exact Artifact id/type/digest
  → 用户将 hit 固定为既有 artifact.review.v1 Agent context
  → 再运行一个 Agent task，证明相同 canonical Artifact 被 Core 重新授权和物化
  → 获得 knowledge.read grant 的 Web Bundle App 通过 App Bridge 搜索同一 Project 投影
  → Runtime 在每次 App 调用时重验 app instance、surface session、grant revision 与 Project scope
  → operator 可经本机管理面查看安全 watermark，并从 Core authority 在线全量重建 Indexer projection
  → shadow generation 在追平 snapshot + live delta 后原子切换，搜索始终读取最后一个完整 generation
  → Core / indexer / Gateway / PostgreSQL 重启后索引、cursor、幂等与搜索结果仍一致
  → foreign owner/project、grant revoke、Project archive、重复 publication、重建中断、服务中断与损坏数据
     全部 fail closed
```

同时，现有 contract-only `IndexContext` 要成为 owner 可触发的、幂等且持久化的 exact Artifact repair/
reindex job，用于修复指定 ref；它服从同一 source authority，不是上传任意文本的第二条入口。

这不是“先搭骨架”。成功结束时必须具备真实 PostgreSQL、真实跨进程 Gateway/Indexer/Core/Runtime、
真实 Chromium owner 与 opaque Web Bundle App 用户链路、可中断恢复的在线重建和确定性视觉证据，才能把
**Project review-artifact indexing + lexical knowledge search + granted app read + recoverable rebuild** 这一
窄切片标为 working。

本批次明确不宣称 semantic RAG 已完成：不接外部 embedding API，不引入假向量，不把 SQL 关键词匹配
包装成语义检索。`rag` / embedding / pgvector capability 必须继续如实 unavailable；Indexer 可以因
这条有界 working slice 升级状态，但 evidence 必须明确限定为 review Artifact 的 durable lexical
search。

## 单分支纪律（不可偏离）

执行时从真实本地 `main` 创建且只创建：

```text
feat/v1-project-knowledge-search
```

并且只建立一个实现任务记录：

```text
docs/tasks/20260901-v1-project-knowledge-search.md
```

强制规则：

1. 只允许上述一个 feature branch、当前一个 worktree、当前一个写入 Agent。不得创建 review、fix、
   candidate、backup 等辅助分支，不得添加 worktree，不得让 sub-agent 或第二个 Agent 写文件。
2. 不 stash 后切分支，不 reset/rebase/squash 已有历史，不修改或删除本地 `main`，不覆盖用户未提交改动。
3. 若目标 branch 已存在，先只读核对它的 merge-base、任务记录和工作树；确认就是本任务后继续使用，
   不得删除重建。无法安全确认时记录事实并停止破坏性操作。
4. 所有阶段在同一 branch 严格串行；完成一个可验证阶段后做聚焦提交，再继续下一阶段。
5. 禁止把整夜工作压成一个巨型提交。建议提交序列：

   ```text
   docs: define project knowledge indexing boundary
   feat: publish durable review artifact index feed
   feat: add idempotent project knowledge indexer
   feat: expose bounded project knowledge search
   feat: add Knowledge Center context workflow
   feat: expose scoped knowledge search to granted apps
   feat: add resumable index projection rebuild
   test: prove knowledge indexing across restarts
   docs: record project knowledge search evidence
   ```

6. 每次提交前运行 `git diff --check` 并审查 staged diff。不得提交 secret、私钥、真实用户内容、数据库、
   容器归档、构建二进制、trace/video、Playwright 临时目录、宿主绝对路径镜像或测试报告 dump。
7. 未经用户新授权，不 merge 到 `main`、不 push、不删除其他分支。最终停在该唯一 feature branch 的
   干净 HEAD，供用户醒来后审查。

## 无人值守与时间安排

用户离线期间不要等待普通澄清。优先从架构、Proto、现有实现和测试推导最保守的正确方案，然后持续
推进。下列时间按有经验的实现智能体估算为九至十二小时，仅用于确保任务量充足和先后依赖清晰，不是
到点停工条件：

| 阶段                                  |    建议投入 | 结果                                            |
| ------------------------------------- | ----------: | ----------------------------------------------- |
| 基线与设计裁决                        |  30–45 分钟 | 任务记录、基线结果、ADR/契约方案                |
| A. Proto + Core publication           |  60–90 分钟 | 原子 feed、private source contract、backfill    |
| B. Indexer domain/storage/worker      | 90–120 分钟 | migration、sqlc、lease/replay、reconcile        |
| C. Search contract/transport/Gateway  |  60–75 分钟 | owner/project-scoped lexical search             |
| D. Knowledge Center + context handoff |  60–90 分钟 | expanded/compact UX 与视觉证据                  |
| E. App Bridge + SDK `knowledge.read`  |  60–90 分钟 | grant-scoped opaque App search 与 revoke        |
| F. Admin status + online full rebuild | 75–105 分钟 | local-only ops、shadow generation、crash resume |
| G. 集成/E2E/restart/故障加固          | 90–120 分钟 | 三个专项门禁、全量回归、状态收口                |

执行规则：

- 不要只写计划；完成基线后立即实现。
- 一个测试慢、镜像构建慢或依赖偶发下载失败都不是停止理由。网络瞬时失败可有界重试；Buf 失败中途
  清空生成目录时，先恢复干净生成状态，禁止手改 `gen/`，远端恢复后完整复跑。
- 每 60 秒以内给用户一条简短进度更新，但持续推进，不因更新打断长测试。
- 不访问真实 DeepSeek/OpenAI/Codex 或收费 embedding 服务；Agent 链路使用现有 Fake/本地 fixture。
- 不搜索 shell history、用户 home、环境变量或 credential store 获取 key。
- 不安装宿主软件、不使用 `sudo`、不改内核/systemd/防火墙。这个任务不依赖 Podman；
  `make test-podman-fixture` 的既有 blocker 不得拖住本批次，也不得被伪造为 PASS。
- 某个独立门禁遇到环境波动时记录、继续其他可执行阶段并在收尾复试；只有本文件“停止条件”允许整个
  批次等待用户。

## 本 Prompt 编写时的仓库事实（执行时必须重新核对）

- 本文件编写前本地 `main` 为 `0360ce5`，比 `origin/main` 领先；执行时以真实本地 `main` 为准，禁止
  reset 到较旧远端。
- 本地清理后只有 `main`；不要复活已删除的历史 feature branch。
- 六个进程固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。本批次不增加第七个进程。
- `cmd/indexer/main.go` 目前只挂载 Unimplemented `workos.index.v1.IndexService` 与 degraded
  `SystemService`；还没有 `internal/indexer` 实现。
- `api/proto/workos/index/v1/index.proto` 已有 contract-only 的 `IndexContext` / `Search`、字符串
  `context_refs`、`IndexJob.state` 和最小 `SearchHit`。v1 字段号不可复用或破坏；必须 additive 演进。
- migrations `001`–`025` 已存在且可能已在持久卷执行，禁止修改。执行时重新确认下一个空闲编号；若
  Core publication 与 Indexer projection 分属两个 migration，必须分别标明唯一 owner。
- Core Artifact 已持久化两种 immutable review subtype：`document.markdown.v1` 与
  `code.unified-diff.v1`，内容有严格 UTF-8/C0-C1/大小/行数/digest 规则；Web Bundle bytes 是 private
  launch payload，绝不能进入知识索引。
- `artifact.review.v1` 已是 Agent context 的 canonical ref type：ID + exact sha256 digest，由 Core 在
  Submit 与 execution 时重新验证。搜索命中应复用它，不能再造近义 context DTO 或绕过 Core。
- Core 的 Artifact materialization 已能在同一 PostgreSQL 事务写 Artifact、output mapping 与
  Core-minted `artifact_created` timeline event；本批次必须在同一事务追加安全 indexing publication，
  不能靠日志抓取或进程内 callback 假装 durable。
- Gateway 已对 Core、Runtime、可选 Reliability 使用显式 public allowlist、device session gate 和可信
  owner/device header 注入；Indexer URL 已存在于共享 config 结构和 dev YAML，但 Gateway 尚未路由它，
  env override/compose/systemd 也需按实际代码核对。
- App manifest capability vocabulary 已包含 `knowledge.read`，但 `workos.bridge.v1.AppBridgeService`、
  `sdk/app-sdk` 与 `clients/app-host` 目前只实现 Agent run/watch；Runtime 的 effective bridge capability 测试也
  明确把 `knowledge.read` 视为没有可调用 method。本批次要在既有 surface token/grant-revision 体系内把它
  变成真实 read-only 方法，不能另开浏览器直连 Indexer 后门。
- Gateway/Core 已有本机 Unix admin socket 与 `workosctl` 的安全模式可参考；Indexer 尚无管理面。本批次
  只能复用本机、owner-only、非 Gateway 路由的运维模式，不能增加公网 admin RPC 或第七个 daemon。
- Desktop 已有 Project、Agent Center、Artifact Center/Viewer、App Library、System Monitor、Device
  Center 与 adaptive Compact/Medium/Expanded/Fold-separated shell。新增 Knowledge Center 必须复用
  system-window 和 Project-generation 隔离模式。
- `docs/status.json` 当前 Indexer 为 scaffolded，evidence 只有 health；README 状态区块由工具生成，
  禁止手改。
- Runtime container 与 Reliability 真实能力仍受 rootless Podman acceptance host 阻塞；本批次不得
  修改其 capability/status 或顺手解决。

## 开始前必须完成

完整阅读，不要只靠关键词片段：

1. `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`docs/ui/README.md`；
2. `docs/structure.md` 的 0、1、2、3、4、5、6、7、10、11、14–18 节；
3. `docs/architecture/implementation.md` 全文与 `docs/status.json`；
4. ADR `0004`、`0007`、`0008`、`0010`、`0011`、`0012`；
5. `docs/tasks/20260830-project-artifact-review.md`、
   `docs/tasks/20260830-artifact-agent-context.md`、
   `docs/tasks/20260830-deepseek-structured-review.md` 与相关视觉 notes；
6. Index、Artifact、Agent、TaskExecution、Project、Common Proto 及生成代码；
7. Core Artifact domain/application/ports/PostgreSQL/transport、artifact materializer、Project Archive、
   event/outbox worker与 restart tests；
8. `cmd/indexer`、共享 config/migrations/sqlc/httpserver/identity/dbtransient、Gateway proxy/allowlist/tests；
9. Bridge Proto、Runtime AppBridge/surface token/grant revision、`sdk/app-sdk`、`sdk/surface-sdk`、
   `clients/app-host` 与现有 opaque Web Bundle browser E2E；
10. Gateway/Core admin socket、`workosctl doctor`/管理命令与 Unix socket 权限/路径测试；
11. Desktop system-window、Artifact Center、Agent context chip、adaptive shell、Playwright fixture 与截图工具；
12. Compose、Dockerfile、systemd env、Makefile、CI targets 与当前 migration checksum tests。

随后创建唯一 branch 和唯一实现任务记录，写明：baseline SHA、范围/非范围、阶段依赖、预期 migration
owner、验收、提交计划和风险。开始改代码前实际运行并记录：

```sh
git status --short --branch
git log --oneline --decorate -20
git branch -a -vv
git diff --check
make bootstrap
make generate
make check
make test-integration
make test-e2e
```

不得为了基线清理 PostgreSQL volume。若基线失败，先分类为代码、环境或已有 blocker；记录证据并继续
所有不依赖的设计/实现，禁止把基线失败归咎于尚未产生的改动。

## 全批次不可违反的边界

### 架构与数据所有权

- 依赖方向固定为 `domain → application → ports ← adapters`。
- Domain 不得导入 PostgreSQL/pgx/sqlc、Connect/Proto、HTTP、文件系统、浏览器 API、厂商 SDK 或其他
  模块 adapter。
- `workos-core` 继续拥有 Project、review Artifact 原始内容、Agent task/event 与 publication source；
  `indexer` 只拥有可重建 search projection、消费 receipt/cursor、index job 和检索读模型。
- `runtime-host` 只负责验证 App Bridge session/grant 并把有界 search 转发给 Indexer；它不得复制索引、缓存
  授权真相、读取 Core/Indexer 表或让 App 直接选择 upstream。`sdk/app-sdk`/`clients/app-host` 只实现协议
  envelope 与前端 API，不成为权限裁决者。
- Indexer 禁止直接 SQL 查询/写入 `workos_core` 或 `workos_events` schema；Core 也禁止写
  `workos_index`。跨进程只能走 versioned private RPC/public RPC 与 durable publication。
- 不共享 mutable entity，不从另一个进程 import `internal` package。跨进程 DTO 必须先改
  `api/proto`，再运行 `make generate`。
- Core 内部的 Artifact、Project、Event Backbone/Index Publication 模块之间同样禁止直接 SQL 查询对方
  表或引用对方 adapter；publication append 必须通过 neutral port/orchestration，并参与 source mutation
  已有 transaction。若复用 `workos_events.outbox`，必须使用独立 event type/claim filter，不能抢占 Harness
  的 `agent.task.requested.v1` 消费状态。
- v1 字段号不得复用；删除字段/枚举值必须 reserved；已有 contract-only 字段必须保留。真正无法 additive
  表达时写新版本与 ADR，不能悄悄破坏。
- migration `001`–`025`（以及执行时已存在的更高编号）逐字节不变。新表用 forward-only migration，
  每张表/索引/constraint 注释 owner；sqlc package 与进程边界一致。
- 所有资源 ID 使用 canonical UUIDv7，时间 UTC microsecond，digest 使用 `sha256:<64 lowercase hex>`。
- 外部写操作必须有 durable idempotency key/etag。at-least-once consumer 必须持久化 receipt/cursor；
  禁止内存 map、更新时间戳或“查不到就插入”冒充幂等裁决。

### 身份、安全与隐私

- 浏览器提交的 owner/user/device header 全部不可信；Gateway 必须先清洗，再从 device session 注入。Indexer
  transport 只从可信 context 读取 owner，不从 request body 接受 owner。
- Gateway 只公开 `IndexService` 中明确面向 owner 的方法；Core private publication/source service、
  Indexer admin/worker API、SystemService 的错误路由均不得因通配 allowlist 暴露。
- Private publication 不携带 review Artifact 正文、task goal、Provider raw output、credential、workspace
  URI 或用户 display name；只含操作、publication/source ID、owner/project/artifact ref、digest 和 UTC time。
- canonical content 只通过有界 private source response 进入 Indexer projection；不得写日志、event、error、
  metrics label、trace attribute、URL、DOM data attribute、截图或测试名称。
- Search excerpt 是有界 plain text，不是 HTML；UI 禁止 `dangerouslySetInnerHTML`，Markdown/HTML/script
  片段只能作为 inert text 渲染。响应不得返回全文或隐藏字段。
- 搜索必须始终以 trusted owner + canonical project ID 过滤；foreign/missing/archived 的错误或空结果语义
  固定且无存在性 oracle。
- 搜索 hit 只提供 Artifact ref/digest，不授予权限。真正作为 Agent context 时仍走现有 Core Submit +
  ResolveTaskContext owner/project/digest/lease 重验，绝不从 Indexer 把正文直塞 Harness。
- App Bridge `knowledge.search` 只在 manifest 请求且当前版本实际获得 `knowledge.read` grant、surface token
  与 app instance/session 都有效、Runtime 已配置真实 Indexer adapter（非固定成功 stub）时协商出来；不能因
  vocabulary 中存在该字符串就宣称可用。
- 每次 App search 都必须在服务端重新验证 token grammar/digest/expiry、owner、installation/version、app instance、
  surface session、Project binding 与 **exact current grant revision**。grant revoke、uninstall、version/grant
  变化或窗口关闭后，旧 token 即使未过期也必须立即 fail closed，且不得调用 Indexer。
- App request 不得带 owner/project/source override；Runtime 从可信 launch/surface context 派生 scope，并用
  内部受信身份调用 Indexer。App 永远拿不到 device cookie、Indexer URL、Core private source、publication
  lease、admin socket 或 raw Artifact content。
- App search 返回与 owner UI 相同的有界 title/type/plain-text excerpt/exact ref；不得提供全文、任意 source
  resolve、跨 Project/global search 或自动 Agent context injection。SDK/app-host 对请求和响应都做独立上限
  与 schema 校验，opaque iframe 不能借 postMessage 绕过 origin/source/nonce 校验。

### 一致性与检索语义

- publication 是 at-least-once，不宣称 exactly-once。正确顺序固定为：Indexer 本地 document + receipt +
  cursor/job outcome 同事务提交成功后，才向 Core complete/ack；响应丢失必须安全 replay。
- Core claim 使用持久 lease 与 worker identity，过期可重领；concurrent worker 不得同时拥有同一有效 lease。
- Artifact upsert publication 必须与 immutable review Artifact/Agent publication 在同一 Core transaction；
  Project archive tombstone publication 必须与 Archive revision/event/outbox 在同一 Core transaction。
- 已存在 review Artifact 必须有 deterministic backfill/reconciliation；不能只索引本分支之后的新数据。
- Indexer projection 是可重建副本，不成为 Artifact/Project 授权真相。digest/source drift 或 Core stored
  corruption 必须 fail closed/degraded，不能悄悄索引新字节覆盖旧事实。
- 全量重建必须以 durable rebuild job + generation/epoch 建模：搜索始终读取最后一个 completed generation；
  新 generation 从 Core authoritative snapshot/high-watermark 构建，再消费该 boundary 之后的 live delta，
  完成 count/digest/tombstone 校验后单事务 promote。不得 truncate 当前 projection 后边读边补。
- 重建与 live worker 并行时，一个 publication 在目标 generation 最多产生一份 receipt/effect；archive
  tombstone 优先级高于旧 upsert。进程崩溃后从 durable phase/cursor 恢复；取消或永久失败保留旧 generation，
  不暴露半成品。成功 promote 后才能有界清理旧 generation，清理可重试且不能影响搜索。
- 首版搜索是 deterministic lexical search。可以使用 PostgreSQL 内建 text search、受控 tokenization 或
  二者组合，但不得依赖未声明 extension、locale 偶然值、regex 拼接或未经参数化的 SQL。
- query 规范至少包括：valid UTF-8、trim/canonical whitespace、1–256 code points、C0/C1 拒绝、term 数
  有界、wire body 有界。空 query、超长、未知 enum、畸形 page token 必须在任何业务读取前拒绝。
- `score` 必须 finite、非 NaN/Inf、范围固定并有确定 tie-break；同 snapshot、同 query、同数据的顺序稳定。
- page token 必须 versioned，绑定 owner/project、canonical query digest、ranking version、snapshot/high
  watermark 和最后排序键；跨 query/project 重放或篡改稳定 InvalidArgument。恰好满的最后一页不得产生
  phantom token。
- excerpt 长度、换行/控制字符处理、match window 与无匹配行为必须由纯函数定义并用 Unicode/code/diff
  fixtures 测试；不得让 PostgreSQL 或前端任意截断产生破损 UTF-8。

### UI 与视觉证据

- 任何用户可见改动必须按 `docs/ui/README.md` 建立任务级 `before/`、`after/`、`notes.md` 并更新
  `docs/ui/desktop-web/current/`。
- before 必须在实现前从本批次 base commit 的真实 bundle 截取/复制，并在 notes 标明 source SHA；禁止
  把 after 复制成 before。
- 至少固定 1440×900 Expanded 与 390×844 Compact viewport；使用确定性 Project/Artifact/query fixture，
  隐藏随机 UUID/time，不依赖外部服务。
- Project 快速切换、窗口关闭、query 变化和分页都必须 abort/失效旧 generation；迟到结果不能污染新
  Project、复活已关闭窗口或自动固定 context。
- Compact/Medium 必须可达 Knowledge Center、搜索、分页和 Use as context；不能只在 desktop hover 中
  暴露操作。
- Opaque Web Bundle fixture 也属于用户可见 UI：至少在 Expanded viewport 留下 granted search working
  evidence；revoke/error 可由 E2E 断言，不要求把敏感诊断画进截图。

## 阶段 A：ADR、additive Proto 与 Core publication source

### A1. 先写 ADR 与协议裁决

新增下一空闲 ADR（执行时核对编号），至少明确：

1. 首版只索引 canonical project review Artifact，不索引 Web Bundle、workspace 文件、日志、聊天全文；
2. Core 是原始文档与 Project lifecycle 的唯一真相，Indexer 只持有可重建 projection；
3. automatic publication + deterministic backfill/reconciliation；
4. at-least-once lease/receipt/ack crash window；
5. lexical ranking/pagination snapshot 与 async consistency；
6. 搜索 hit 复用 `artifact.review.v1` context，不新增自动 RAG 注入；
7. Project archive 如何 tombstone，未来 semantic embedding 如何独立演进；
8. owner browser、granted opaque App 与 operator 三个入口如何共享一个 Indexer application contract，同时
   保持不同 trust boundary；
9. shadow generation rebuild 的 snapshot/live-delta/promotion/cancel/crash 语义；
10. `rag` capability 继续 false 的原因与后果。

Additive 扩展 `workos.index.v1`，不要删除/改号：

- 为现有 `SearchHit` 增加 typed source ref、artifact ID/type/digest/title/created_at 等必要字段；保留原
  `context_ref` 字段并定义唯一兼容语义，不能维护两套互相漂移的 ref。
- 为 `SearchResponse` 增加有界 index freshness/status 投影（例如 indexed-through/high-watermark/last
  indexed time）；不能用固定 READY 冒充追平。
- 将现有 `IndexJob.state` 字符串保留兼容，同时新增严格 enum/计数/时间/错误类别字段，或用 additive 新
  message/RPC 表达；未知 enum fail closed。
- 将 `IndexContext` 从 contract-only 变为真实 owner-triggered repair/reindex job，必须 additive 增加
  idempotency key 与 typed source refs，限制为 `artifact.review.v1`、最多 32 个，并明确 same key replay/
  conflict/失败不消费。不能继续信任无语法的 `repeated string context_refs`；旧字段非空时按文档化
  兼容策略解析或明确 InvalidArgument，绝不猜。
- 新增独立 private Core source/publication service。它不能进入 Gateway allowlist；claim/resolve/complete、
  reconciliation page 必须使用有界消息与明确 lease identity，不直接暴露通用 Artifact bytes API。
- 每个 public/private Connect handler 设置从最大合法消息推导的 `WithReadMaxBytes`，先于 protobuf/JSON
  decode 生效；覆盖 gzip 解压后上限。

Additive 扩展 `workos.bridge.v1` 与 surface contract：

- 新增一个 read-only `SearchKnowledge`（最终命名按仓库规范裁决）RPC；request 只允许 query、page size/token
  等有界搜索参数，**没有** owner/project/source/backend 字段，scope 必须由 Runtime trusted context 注入。
- response 复用 canonical Index search hit/ref 语义并只暴露 App 所需的 sanitized projection。若 Proto package
  import 不能直接复用 message，必须只有一个显式 mapper + contract tests，禁止在 Proto、surface-sdk、
  app-sdk、app-host 四处维护漂移的手写 DTO。
- 在 `surface-sdk` 增加严格的 method/payload/result discriminator；在 `app-sdk` 暴露
  `workos.knowledge.search(...)`；`app-host` 建立 `knowledge.search` → Bridge RPC 的唯一映射。未知字段/
  method、超界 page/query、malformed hit/token 在跨 iframe 之前 fail closed。
- capability negotiation 必须把 surface method `knowledge.search` 精确绑定 manifest/grant 的
  `knowledge.read`；不得把 capability name 当 RPC method，也不得让 `agent.run` grant 隐式获得知识检索。
- 为 Runtime→Core 新增有界 private App knowledge authorization contract，携带 trusted owner/device、session
  派生 project/app instance 与 exact grant revision，只返回 sanitized allow/deny binding；它不进入 Gateway。
- 为本机 Indexer admin 增加 versioned private Proto service/message，表达 status、project/all rebuild、job get/
  cancel、generation phase 和 idempotency；它后续只绑定 Unix socket。禁止在 `workosctl` 手写另一套 JSON DTO。

先运行 `make generate`，检查生成 diff 只来自 Proto/schema generator；运行 Buf lint/breaking against 执行基线。禁止手改
`gen/` 或 `sdk/protocol/src/gen/`。

### A2. Core-owned durable publication

使用执行时下一个空闲 migration 创建 Core-owned publication/lease facts。具体表形状可按现有 outbox
模式收敛，但必须满足：

- publication ID canonical UUIDv7，operation 至少区分 review-artifact upsert 与 project tombstone；
- owner/project/source type/source ID/exact digest/occurred_at 有界且有 CHECK；publication 不含 content；
- 同一 immutable Artifact/digest 只产生一个 authoritative upsert publication；Project archive tombstone
  有唯一物理仲裁；
- claim/lease/attempt/completion/terminal outcome 有持久事实，`FOR UPDATE SKIP LOCKED` 或等价数据库裁决；
- lease owner/token 不由浏览器提供，过期前其他 worker 不能窃取；stale complete/release 不得完成新 lease；
- source corruption 与永久 unsupported 是可观察 terminal/degraded outcome，transient Core/PostgreSQL
  outage 保持 retryable，二者不能折叠；
- 既有 active Project review Artifact 必须通过 migration-safe publication backfill 或首次 authoritative
  reconciliation 做 deterministic backfill，不伪造正文/digest/time，不用跨模块直接 SQL 捷径，也不为
  archived Project 暴露 searchable publication；选择与理由写进 ADR 并有约束/重启测试。

修改 Artifact materialization 与 Project Archive 的 Core transaction：

```text
review Artifact insert + output mapping + artifact_created event
  + indexing upsert publication
  = one Core transaction

Project archive revision + project event/outbox
  + project tombstone publication
  = one Core transaction
```

任一 publication insert/event/commit 失败，整个业务 mutation 零残留；不可在 commit 后 best-effort
enqueue。已有 Artifact replay 不能二次 publication，Archive replay/stale revision 不能重复 tombstone。

### A3. Private source 与 backfill/reconciliation

Core private service 只返回 Indexer 所需的 canonical snapshot：operation、owner/project/source identity、
title/type/digest/created_at 与 bounded review content。每次 resolve 必须从 Core authority 重验：

- source 是 implemented review subtype，不是 Web Bundle；
- owner/project/source_task/digest/content/type/media/count/time 全部满足既有 invariant；
- Project 仍 active；若 archive 已与 upsert 并发发生，tombstone/authoritative lifecycle 决定最终不可搜索；
- publication ref 与 resolved Artifact exact match；不接受 Indexer 提交替代 digest/content；
- private response body 总量有界，batch 不因一个最大 512 KiB Artifact × 无界数量导致内存放大。

实现 deterministic reconciliation page，供启动/周期修复历史遗漏：分页顺序、cursor、snapshot boundary
明确；Indexer 不能直接查 Core 表。Reconciliation 与 live publication 并发时必须靠 document digest +
source version/operation ordering 收敛，旧 upsert 不得复活已经 tombstone 的 Project。

阶段 A 必须有 domain/application/PostgreSQL/transport 测试，包含 backfill、事务回滚、双 claimant、lease
过期/response loss、stale completion、Artifact replay、Archive race、source corruption 与 transient outage。

## 阶段 B：Indexer domain、PostgreSQL projection 与 worker

### B1. 建立真正的 Indexer 模块边界

在 `internal/indexer` 内按仓库结构建立 `domain → application → ports ← adapters`，至少区分：

- canonical source document / tombstone / publication lease；
- ingestion/reconciliation service；
- lexical query、ranked hit、excerpt 与 versioned cursor；
- ports：Core publication/source client、projection repository、clock/ID（仅必要处）；
- adapters：PostgreSQL/sqlc、Core Connect client、public/private transport；
- composition root：DB/migration readiness、worker lifecycle、graceful shutdown、System capability。

不要把所有逻辑堆进 `cmd/indexer/main.go`，也不要复制 Core domain entity。Domain 使用自己的 immutable
projection value，且不 import Proto/pgx/HTTP。

### B2. Indexer-owned migration 与持久事实

使用另一个 forward-only migration 创建 `workos_index`（或执行时已确定的唯一 Indexer schema），至少
持久化：

```text
documents
  owner_user_id / project_id / source_type / source_id
  exact source_digest / title / artifact_type / canonical bounded content projection
  searchable representation / source_created_at / indexed_at
  source/publication ordering fact / active-or-tombstoned state / projection_generation

publication_receipts
  publication_id / exact request digest / terminal local outcome / processed_at

consumer_state
  worker/stream identity / durable cursor-or-high-watermark / observed source watermark / updated_at

index_jobs + job_sources
  owner/project/idempotency digest / first-response snapshot / bounded counters/outcome

projection_generations + rebuild_jobs
  generation ID / owner-or-project scope / source snapshot watermark / live catch-up watermark
  durable phase/cursor/counts/digests / promoted_at / failure-or-cancel category
```

要求：

- 表和索引只由 Indexer adapter 使用；Core migration 不 FK 到它，Indexer 也不 FK 到 Core schema；
- document key 物理隔离 owner + project + source；foreign owner 相同 UUID 不能碰撞；
- receipt 与 document/tombstone/cursor/job progress 在一个本地 transaction 原子提交；
- generation state 和每个 generation 的 receipt/effect 有物理唯一约束；active generation pointer 的 promote
  是单事务 compare-and-swap，旧/失败 worker 不能覆盖后来成功的 generation；
- same publication same digest 精确 no-op replay；same ID different digest/operation 是 corruption，不能覆盖；
- Project tombstone 单事务使所有该 owner/project document 不再 searchable，并持久阻止迟到旧 upsert 复活；
- stored row 每次读出重验 UUIDv7/digest/type/UTF-8/size/time/score inputs/order；corruption → Internal/degraded，
  不静默修正；
- 索引与 source content 都有明确上限；不得把 Web Bundle 或超过 Core contract 的内容写入。

### B3. At-least-once worker 与 crash convergence

worker 循环必须：

1. 有界 claim；
2. resolve canonical source；
3. 在 Indexer DB 单事务写 projection + receipt + consumer state/job progress；
4. commit 成功后才 complete Core publication；
5. complete response loss 时重领/replay，不能重复 document、计数或 resurrect；
6. shutdown 取消新 claim，允许当前 DB transaction 有界退出，不留下进程内“已完成”假状态。

覆盖 crash windows：claim 后崩溃、resolve 后崩溃、本地 commit 前/后崩溃、Core complete 前/响应丢失、
lease 过期、两个 Indexer 实例竞争、Core unavailable、Indexer DB unavailable、永久 source corruption。
Backoff 有上下界和 context cancellation；日志只记录 publication/job ID 与净化类别，不记录 query/content。

启动时跑一次有界 reconciliation，再进入周期 reconciliation/live claims；频率/批量/timeout 必须从 config
读取并有安全默认/上下界。不能用 `time.Sleep` 写不可取消循环，也不能 readiness 等待完整历史 backfill。

### B4. System health/capability

- liveness 只表示 event loop 存活；readiness 至少绑定 Indexer DB/migration 与 public handler 必需依赖，
  不因暂时追赶 publication 永久不 ready。
- capability 增加精确、窄语义（例如 `project-review-index`、`project-knowledge-search`）。只有真实专项 E2E
  通过后才 available=true。
- 既有泛化 `archive` 与 `rag` 不得因为这条切片自动 true；原因文案明确 generic archive、embedding/
  semantic RAG 尚未实现。
- 暴露有界 lag/freshness 数字，不把 query/content 放进 metrics label；负数、倒退或损坏 watermark fail
  closed。

## 阶段 C：确定性 lexical search、public API 与 Gateway

### C1. Search application contract

实现现有 `Search` RPC 的真实行为：

- trusted owner + canonical active project scope；page size 默认 20、上限 50（若审计得出更合适上限，
  在 ADR/测试中固定）；query 规范按前述边界；
- 只返回 active、exact owner/project 的 review Artifact projection；Project tombstone 后零 hit/固定
  NotFound 语义，不能只靠 UI 隐藏；
- 结果包含 title/type/excerpt/finite score/source time 与 exact `artifact.review.v1` ID/digest；不返回全文、
  raw tsvector、内部 row ID、owner ID、publication/lease token；
- 排名算法有版本常量与纯函数/SQL golden。至少覆盖标题命中、Markdown 正文、diff path/hunk、大小写、
  Unicode、重复 term、标点、无匹配和稳定 tie-break；
- snapshot/high-watermark cursor 保证翻页过程中后来入库的 document 不插进旧 page chain；最后页语义准确；
- Indexer PostgreSQL transient outage → Unavailable；stored corruption → Internal；malformed request/token →
  InvalidArgument；foreign/missing Project 不泄漏；错误消息固定净化。

同时把 `IndexContext` 做成 owner repair job：

- 请求只接受 trusted owner/current Project 下最多 32 个 typed artifact refs + idempotency key；
- Core private resolve 逐项权威校验，不允许 Indexer 信任客户端 title/type/content；
- job、sources、first response 同事务；same key/same canonical request 精确 replay，different request Aborted，
  validation/source failure 不消费 key；
- job 状态单调 pending→running→completed/failed，进程重启恢复 pending/running；重复 source 不重复计数；
- 这只是 repair/reindex，不是上传任意 knowledge 文本的后门。

### C2. Gateway 与部署

为 Gateway 增加 **可选且精确** 的 Indexer upstream：

- config env override、validation、compose、dev config、systemd env/example 与 docs 一致；URL 非空时必须是
  absolute http(s)+host，错误启动 fail-fast；为空时 Knowledge route 404/Unavailable，但 Core/Runtime/
  Auth/静态 Shell readiness 不受影响；
- 新建独立 reverse proxy field，不把 Index RPC 错路由到 Core；proxy failure 固定 503，日志无 query；
- allowlist 精确到 public IndexService prefix。private source/claim/complete、Indexer admin/System management
  都必须 404；不要把整个 `workos.index.v1` namespace 通配公开；
- 同一 device-session gate、Host/Origin policy、Cookie stripping、identity header 清洗与可信注入；客户端
  spoof owner/device/forwarded header 到不了 Indexer；bridge token 永远不进入 Indexer；
- public request wire budget 在 Gateway/Indexer transport 都有效，gzip bomb 在业务读取前拒绝。

补 Gateway table tests：dev/production session、spoof cleaning、missing/invalid Indexer URL、upstream outage、
private path rejection、其他服务回归。`workosctl doctor` 若展示 Indexer，必须区分 process health、lexical
capability 与 rag unavailable，不能只看 HTTP 200。

## 阶段 D：Knowledge Center 与现有 Agent context 闭环

### D1. Knowledge Center 产品行为

在 Desktop system-window 体系中新增 Knowledge Center，入口复用结构文档的 Dock/compact navigation，
不要增加永久 sidebar。至少包括：

- 当前 Project 标题、search input、submit/clear；空 query 不发 RPC；
- idle、searching、catching-up/stale、empty、results、load-more、Unavailable、retry、malformed response 状态；
- result 显示 inert title/type/excerpt/相对 source 类型，不显示内部 ID/digest/time 随机值；
- `Use as Agent context` 通过 Desktop 现有 canonical context state 加入 chip；duplicate 幂等，最多 4 个的
  既有上限/提示保持；成功搜索本身绝不自动把内容送给 Agent；
- 点击 title 可继续打开既有 Artifact Viewer（再由 Core ArtifactService 权威读取），不能从 Indexer
  response 渲染全文；
- Project 切换、窗口关闭、query 变化、分页、retry 都有 AbortController/generation guard；迟到响应 inert；
- Indexer 不可达只降级 Knowledge Center，不影响 Project、Agent Center、Artifact Center 或已打开 App；
- Expanded、Compact、Medium 可达；Fold-separated 遵循现有 projection，不创造专用布局状态。

对 Indexer response 做边界验证：canonical UUID/digest/type、finite score、excerpt/title length、Project binding
与 ref grammar。任何一条畸形 response 整页 fail closed 或按文档化安全策略拒绝该 hit，不能把 server
corruption 注入 DOM/context。

### D2. 视觉证据

建议目录：

```text
docs/ui/desktop-web/changes/20260901-project-knowledge-search/
  before/
  after/
  notes.md
docs/ui/desktop-web/current/
```

至少记录：

1. Expanded before（基线 Dock/桌面，无 Knowledge Center working state）；
2. Expanded after：Knowledge results；
3. Expanded after：命中已固定为 Agent context chip；
4. Compact after：Knowledge Center results + 可触达 Use as context；
5. Expanded after：granted opaque Web Bundle App 内的 knowledge results；
6. 可选但建议：Indexer unavailable 的清晰降级状态。

固定 Project=`Knowledge Lab`、Artifact 标题、query 与 excerpt；截图不得含真实用户内容、随机 UUID、真实
时间、credential、raw task goal。after/current 对应文件 hash 相等，before/after 必须来自不同真实状态。

## 阶段 E：授权 App Bridge、SDK 与 opaque Web Bundle 搜索

### E1. Runtime application ports 与服务端重验

在 Runtime Surface 模块沿现有 App Agent 模式增加中立的 `AppKnowledgeAuthorizer` 与
`KnowledgeSearchClient` ports；adapter 分别调用 Core grant authority 和 Indexer，禁止 Runtime import 两边
internal package。一个 App search 的固定顺序是：

1. 用 Gateway trusted owner/device + bridge token digest 读取 open、unexpired、device-bound surface session；
2. 检查 session effective method 精确包含 `knowledge.search`；
3. 通过 Core private authority 重新核对 active installation、pinned version、app instance、Project、
   `knowledge.read` grant 与 session 保存的 exact grant revision；
4. 只有重验成功后，才以 session 派生的 owner/project 和有界 query 调用 Indexer；
5. 对 Indexer hit/page token/freshness 再做边界验证并投影为 Bridge response。

Core 重验 RPC 只返回授权 verdict/binding，不返回 credential、manifest 全文或 Artifact content；Runtime 不缓存
成功 verdict 跨调用使用。Core deny/not-found/revision drift 统一映射 sanitized PermissionDenied，且 Indexer fake
必须证明调用次数为零；Core/Indexer timeout 分别映射稳定 Unavailable，不泄漏哪条内部记录存在。

为 Runtime 增加可选 Indexer upstream/config/deploy wiring 和合理 deadline/body/concurrency 上限。沿用现有 private
loopback/service-to-service identity 模式，每次 adapter 调用覆盖而非透传浏览器 identity header；不能仅因
“同一容器网络”就信任任意 caller。Indexer 未配置时 Runtime 仍可启动和服务不依赖知识搜索的 App，
`knowledge.search` 不进入新 session 的 effective capability；已配置后瞬时 outage 只让调用 Unavailable，不能
篡改 grant 或自动关闭 surface。

### E2. Surface SDK、App SDK 与 App Host

- `surface-sdk` 的 method union、payload/result validator、request/response byte budget 和 capability negotiation
  增加 `knowledge.search`，保持 unknown method fail closed；不能用 `any`/unchecked cast 绕过生成类型。
- `app-sdk` 暴露一个 abortable/paginated `workos.knowledge.search` API；每个 promise/MessageChannel request 有
  nonce、timeout、single settlement，App teardown 时清理 listener 和 pending map。
- `app-host` 只接受当前 opaque iframe window/origin/channel 的消息，验证 capability 后调用 Bridge RPC；
  query/page 参数及 response hit 均两次验证，错误只返回固定 code/category，不把 Connect detail/URL/token
  送进 App。
- 提供一个确定性、无外部网络的 Web Bundle fixture/sample surface，真实展示 query、results、load more、
  empty、denied 与 unavailable；渲染 excerpt 为 inert text。fixture 只能通过 App SDK，禁止直接 fetch
  Gateway/Indexer 或读取宿主 DOM/cookie。
- 保留 Agent run/watch 行为和没有 `knowledge.read` 的现有 App；新 capability 不能改变它们的 method 列表、
  quota、token rotation 或 close semantics。

### E3. App 专项门禁与视觉证据

新增：

```sh
make test-app-knowledge-search
```

门禁使用真实 Core/Runtime/Indexer/Gateway/PostgreSQL/Chromium 和 opaque-origin iframe：安装一个 manifest 明确
请求 `knowledge.read` 的 deterministic Web Bundle，授予 capability，打开 surface，从 App 内搜索 Core 产生的
唯一短语并分页；响应的 ref/digest 必须与 owner Knowledge Center 相同，DOM 只有安全 excerpt。

同一门禁还必须证明：未请求/未授予时 method 不协商；surface 打开后 revoke grant、修改 grant revision、
uninstall、close 或 rotate token 时旧调用立即失败；revoke path 在调用计数上未触达 Indexer；App 不能覆盖
owner/project/app instance、不能跨 Project 重放 page token；恶意 postMessage source/origin/nonce、超大 query、
malformed response、Indexer outage 与快速 close 的迟到响应全部 fail closed。把 granted App search working
state 纳入任务级 after/current 截图及 notes。

## 阶段 F：本机运维面与可中断在线全量重建

### F1. Local-only admin contract 与 `workosctl`

为 Indexer 增加独立本机 Unix admin listener，复用仓库既有管理 socket 安全策略，但不要复用 public Index
listener。最低命令面：

```text
workosctl index status [--json]
workosctl index rebuild --owner <uuidv7> --project <uuidv7> --idempotency-key <key>
workosctl index rebuild --all --idempotency-key <key>
workosctl index job get --job <uuidv7> [--json]
workosctl index job cancel --job <uuidv7>
```

执行时可按现有 CLI 风格调整词序，但能力不能减少。socket path 必须是 canonical absolute path，parent 与
socket owner/mode 仅允许当前服务账户；拒绝 symlink/non-socket/world-writable parent/stale unsafe target，
启动与 shutdown 有确定 cleanup。该 service 不进入 Gateway、App Bridge、TCP public listener 或 SystemService
通配路由。

`status` 只输出 generation、phase、safe counts、source/index watermark、lag、last success、bounded sanitized
failure category；禁止输出 query、title、excerpt、content、owner display name、credential 或 lease token。
人类文本和 `--json` schema 都要有 golden tests，exit code 区分 healthy/catching-up/degraded/unavailable。
rebuild/cancel 是 durable、幂等、可审计命令：same key/same canonical scope replay 同一 job，different scope
冲突；validation/permission/socket failure 不消费 key。`--all` 是显式全量 rebuild，不得隐式清空数据库。

### F2. Shadow generation rebuild 状态机

实现可恢复的 durable 状态机，阶段至少为 `requested → snapshotting → catching_up → validating → promoting →
completed`，并有单调的 canceled/failed 终态。具体 enum 必须 Proto/DB additive 且 unknown value fail closed。

- Project rebuild 与 `--all` 都从 Core private reconciliation/source contract 读取权威 active review Artifact；
  不从旧 Indexer rows 克隆，不扫描 Core SQL，不信任 CLI 提交的 content/digest/count。
- 创建新 generation 后按固定 owner/project/source ordering 分页，逐 batch 单事务保存 document/receipt/cursor/
  counts；每个 checkpoint 可重放，重启不从零开始，也不把 batch 成功仅存在内存。
- snapshot boundary 后继续应用 live publication delta，直到达到一个 Core-confirmed barrier；validation 比较
  source count、exact digest、tombstone 与 watermark。任何 mismatch/corruption 终止目标 generation 并保留
  当前 active generation 可搜。
- promote 用数据库单事务/CAS 更新 active generation pointer；在 promote 前旧 generation 承担全部查询，
  promote 后新请求只读新 generation，已有 versioned page token 按 ADR 固定为继续旧 snapshot 或明确过期，
  不能混页。
- cancel 在安全 checkpoint 生效；promoting 后的取消语义必须固定。成功后异步、有界、幂等清理旧 generation；
  cleanup 失败只产生安全告警，不回滚已完成 promote。并发 rebuild 按 scope 串行或明确拒绝，不能互相 promote。
- rebuild 期间 live upsert/archive 同时写 active 与 target，或通过 durable delta journal 追平；二选一写进 ADR
  并证明 response loss、duplicate、archive race、worker/rebuild 双实例都收敛。
- 正常生产路径禁止 `DROP/TRUNCATE`。灾难恢复只允许在专项测试的临时 Indexer-owned database/schema 中销毁
  projection；Core source 与其他进程 schema/volume 必须保持不动。

### F3. Disaster-recovery 专项门禁

新增：

```sh
make test-project-knowledge-rebuild
```

门禁先 seed 两个 owner、多个 Project、Markdown/diff、archived Project 与 live publications，保存 owner search
golden（ref/digest/order/excerpt/freshness）。随后至少证明：

1. 在线 project rebuild 时持续 search，promote 前没有空窗/半结果，成功后 golden 等价；
2. `--all` 从 Core authority 重建全部 active scope，foreign/archived 内容仍不可见；
3. 在 snapshot batch、catch-up、validation、promote commit 前、promote response loss 与 cleanup 各故障点重启
   Indexer/CLI，job 从 durable state 收敛且最多一次 promote；
4. rebuild 同时新增 Artifact、重复 publication、Archive Project，最终新 generation 无 skip/duplicate/resurrect；
5. same idempotency key replay、different scope conflict、cancel/retry 与两个 operator 并发有数据库裁决；
6. 在**临时测试 schema** 删除全部 Indexer-owned projection 后重新 migration + `--all`，结果与销毁前 golden
   一致，Core schema/publication/Artifact 字节与其他服务 volume 未被修改；
7. status/JSON/日志/metrics/trace 不出现 content、query、title、excerpt、raw owner 或 token。

所有等待使用可观测 watermark/job phase 的有界 polling，不用固定 sleep。门禁退出时删除临时 socket/schema/
process，保留规定的文本测试证据，不提交数据库 dump。

## 阶段 G：真实全栈、重启、故障与状态收口

### G1. 新增聚焦门禁

新增：

```sh
make test-project-knowledge-search
```

它与阶段 E/F 的 `make test-app-knowledge-search`、`make test-project-knowledge-rebuild` 共同组成三个必须通过的
专项门禁；不能把后两者合并成只跑 owner UI 的同义测试，也不能由 mock-only test 代替。

门禁必须启动真实 PostgreSQL、bootstrap、Core、harness-host、Indexer、Gateway 与 Chromium；Runtime 仅在
现有 Desktop 启动确实需要时启动，不依赖 Podman。不得以直接 SQL 插入 Indexer document 代替产品链路。

浏览器/跨进程旅程至少证明：

1. 创建 deterministic Project，使用 Fake Harness 生成 Markdown 或 diff review Artifact；
2. Core 自动产生 publication，Indexer worker 消费，UI 以有界 polling 等到 freshness/result，不使用固定
   sleep；
3. Knowledge Center 搜到唯一短语，excerpt/title/type 与 exact Core artifact ref 一致；
4. 点击 `Use as Agent context`，chip 出现；再次提交 Fake task，Fake receipt/事件证明 Core 使用 exact
   artifact.review.v1 ID+digest 并在 execution 重新授权，不读取 Indexer 正文；
5. 第二个 Project/owner 的同词条不可见；spoof header、foreign project/ref fail closed；
6. page size/token/query mismatch、恰满最后页与新增文档 snapshot isolation 正确；
7. Archive Project 后 tombstone 收敛，旧搜索结果不可再加载/固定，迟到 upsert 不复活；
8. 停掉 Indexer 时 Knowledge Center 显示 unavailable，Project/Agent/Artifact 其他窗口继续工作；恢复后
   search 重新可用且没有重复结果。

专项门禁失败时保留有界诊断，但提交前删除 trace/video/screenshots 临时产物；只提交规定视觉证据。

### G2. PostgreSQL、并发与 restart battery

真实 PostgreSQL integration 至少覆盖：

- pristine migration chain、执行时前序 migration checksum、既有 Artifact backfill；
- artifact+publication 与 archive+tombstone 的事务注入回滚；
- 双 claimant/双 Indexer connection、lease expiry、stale complete、local commit/ack response loss；
- exact replay、same publication different digest corruption、manual job idempotency；
- reconciliation/live publication/archive 三方并发，无 orphan、重复 hit、resurrection 或 cursor skip；
- stored projection/publication/cursor/job corruption 每次读 fail closed；
- Core DB/Indexer DB/private source/Gateway/Runtime/App Bridge upstream 分别 outage 的固定错误矩阵；
- query SQL injection 字符、Unicode、最大内容、最大 page、NaN/Inf score 防线；
- owner/project isolation、grant revision/revoke 与 archived lifecycle；
- active/target generation、rebuild cursor/phase、CAS promote、cancel 与旧 generation cleanup corruption。

扩展 restart battery：seed 一个已索引 Artifact、一个未 ack 但已本地 commit 的 publication、一个分页/consumer
state；重启 Core + Indexer（必要时 Gateway）后证明：

- committed document 仍可搜；
- response-loss publication exact replay 后只一个 document/receipt；
- consumer/reconciliation 从 durable state 继续，无从零扫描风暴；
- Project archive/tombstone 与 manual job 跨重启收敛；
- 搜索 page token 的 snapshot/version 行为按文档成立；
- Runtime 重启后旧 bridge token/session 的既有 rotation/revocation 语义不弱化，granted App 仍只看到本
  Project；
- snapshot/catch-up 中途的 rebuild 从 durable generation phase 继续，promote response loss 不产生两个
  active generation。

对 Indexer domain/application/PostgreSQL worker 运行聚焦 `go test -race`。不要只在 fake repository 上证明
并发；至少一组双真实 pgx pool/barrier 测试，无 `time.Sleep` 猜时序。

### G3. 文档与状态裁决

同步：

- 新 ADR；
- `docs/architecture/implementation.md` 的 Indexer/Core feed/Gateway/Runtime App Bridge/Knowledge Center/admin
  rebuild 边界；
- 唯一任务记录（实现、提交、命令、证据、风险）；
- `docs/status.json`；
- 必要模块/deploy README；
- UI before/after/current notes。

状态规则：

- 只有三个新增专项门禁全部以真实 PostgreSQL/进程/Chromium 通过，且 ingestion/restart、owner/archive/
  grant isolation 与灾难重建全过，本唯一任务才能标 done；任何一条缺端到端证据最高为 scaffolded。
- evidence 必须分别精确写成 “Project review Artifact durable lexical index/search”、
  “granted Web Bundle App project-scoped knowledge read” 与 “Core-authoritative shadow-generation rebuild”；
  不能写泛化 “RAG working”。
- `project-review-index` / `project-knowledge-search` / `project-knowledge-rebuild` 可按真实证据 available；
  Runtime/App surface 只把 `knowledge.search` 暴露给真实 `knowledge.read` grant。泛化 `archive`、`rag`、
  `embedding` 保持 false 并给固定原因。
- Desktop evidence 增加 Knowledge Center 与 App surface；Artifact/Agent context 只描述复用既有权威验证，
  不夸大为自动 memory/RAG。运维重建不冒充 Object Store/通用 disaster recovery。
- README 状态表只通过 `make generate` 更新，禁止手改。

## 必须覆盖的失败矩阵

至少在 unit/integration/transport/E2E 的合适层覆盖并固定安全语义：

- malformed/non-v7 owner/project/artifact/publication/job ID；大写/短 digest；未知 type/operation/job state；
- invalid UTF-8、C0/C1、NUL、超大 title/content/query、过多 terms/sources、gzip 解压后超限；
- Web Bundle ref、foreign artifact/project/owner、digest mismatch、archived Project、source task binding drift；
- Artifact 已提交但 publication insert 失败、publication 已提交但 claim response loss；
- claim 后崩溃、resolve 后崩溃、本地 commit 前/后崩溃、complete response loss、lease 过期与 stale token；
- duplicate delivery、same ID/different digest、reconciliation 与 live upsert 竞争、archive tombstone 与迟到
  upsert 竞争；
- backfill 含 active/archived/损坏历史行；损坏不能阻塞无关 Project 的有界进展，也不能被标成功；
- Indexer DB/Core DB/private source/Indexer HTTP/Gateway proxy 分别不可达；Unavailable 与 Internal 分开；
- empty query、超长 query、SQL/tsquery 特殊字符、Unicode/code path/diff hunk、无结果；
- page token 跨 owner/project/query/ranking version 重放、篡改、过期 snapshot、最后一页 phantom token；
- score NaN/Inf/越界、tie 顺序、excerpt Unicode 边界、HTML/script 内容 inert；
- UI 快速 Project/query 切换、窗口关闭、load-more 双击、retry、late response、duplicate context、4-chip 上限；
- 客户端 spoof identity/Forwarded/Cookie/bridge token；private RPC 经 Gateway 必须 404；
- App 未请求/未 grant、stale grant revision、revoke/uninstall/close/token rotation、foreign page token、malicious
  postMessage origin/source/nonce、malformed Bridge hit、Core authorization 成功前误调用 Indexer；
- admin socket 相对路径/symlink/non-socket/错误 owner-mode/world-writable parent、TCP/Gateway 路由尝试、CLI
  same-key conflict 与两个 operator 并发；
- rebuild snapshot/catch-up/validation/promote/cleanup 各阶段 crash/cancel/response loss、live upsert/archive race、
  target corruption、old page token、failed generation 被误 promote；
- restart 后 duplicate document/receipt/job count、cursor 倒退、两个 active generation、tombstone resurrection。

## 明确非目标

为保证一个 branch 在一夜内可控，禁止顺手扩展：

- semantic embedding、pgvector、向量模型、hybrid/vector ranking、reranker、知识图谱；
- workspace/Git/NAS/filesystem crawler、PDF/OCR、图片/音频、多模态、网页抓取；
- Web Bundle bytes、App 私有数据库、日志、聊天全文、Provider raw event 索引；
- 自动把搜索结果注入所有 Agent task、长期 memory 总结、跨 Project/global search；
- App `knowledge.write`、raw Artifact content/source resolve、自动 context 注入、公开上传任意 knowledge document；
- Object Store、通用 Archive lifecycle、retention/export/delete policy；
- 新 Provider、真实收费 API、Codex adapter、credential 扩张；
- Runtime workload/container/Podman、Reliability repair、Push/mobile native wrapper；
- 远程 admin dashboard、公网 rebuild API、生产库 `DROP/TRUNCATE`、跨集群 backup/restore；
- 第七个常驻进程、拆分/合并既有六进程、Kubernetes 或外部搜索集群；
- 为追求“更智能”而弱化 owner/project/digest/lease/分页或隐私边界。

发现这些需求时只写入唯一任务记录的后续项，不实现、不创建额外 branch。

## 最终验证清单

完成代码与文档后至少实际运行：

```sh
make generate
make generate                         # 第二次无生成差异
make check
make test-integration
make test-e2e
make test-project-knowledge-search    # 本批次新增
make test-app-knowledge-search        # 本批次新增
make test-project-knowledge-rebuild   # 本批次新增
make test-artifact-review
make test-artifact-context
make test-deepseek-structured-review
git diff --check
docker compose config --quiet
```

同时：

- 对改动的 Indexer/Core/Runtime 并发包运行聚焦 `go test -race`；
- 对 Proto 运行 Buf lint 与 breaking check against 本批次 base `main`；
- 确认所有前序 migrations 字节未变，只新增 forward migrations 且 checksum pin/owner 测试通过；
- 确认 admin socket 未监听 TCP、Gateway private/admin 路径全 404、App iframe 无直连 Indexer/Gateway 权限；
- 以 checked-in deterministic fixture 扫描三条专项门禁的日志与截图，确认无 content/query/raw owner/token；
- 连续两次 generate 后 tracked diff 为零；
- after/current 对应截图 hash 相等，before/after 不同且实际尺寸匹配文件名；
- 扫描 tracked 文件，不含 ELF、大数据库、私钥/证书、credential/token、容器归档、trace/video、绝对
  `/home/...` 路径镜像或测试临时目录；
- `git status --short --branch` 最终干净，当前只在 `feat/v1-project-knowledge-search`。

`make test-podman-fixture` 不是本批次完成条件；若顺手运行仍因宿主 blocker 失败，只记录既有事实，不改
Runtime/Reliability 状态。

## 停止条件

用户离线期间，只有以下情况允许停止整个批次等待决定：

- 必须破坏已有 v1 字段/编号或修改已经执行的 migration；
- 必须增加第七个常驻进程、让 Indexer 直接 SQL 读取 Core、或违反固定模块所有权；
- 必须把 Web Bundle/credential/task goal/Provider raw output 等禁止内容放入索引或日志；
- 必须让浏览器/App 获得 raw credential、private publication lease 或绕过 Core Artifact context 授权；
- 必须把 Indexer admin/rebuild 暴露到 Gateway/TCP，或必须先清空当前 generation 才能重建；
- 必须删除非本任务数据、清空持久 PostgreSQL volume、运行 privileged/rootful 容器或修改宿主安全策略；
- 工作树出现无法安全归属且与本任务目标文件直接冲突的用户未提交改动；
- 同一不可绕过 blocker 连续满足仓库规定的 blocked 判定，且所有独立阶段、测试和文档都已完成。

普通 API 命名选择、表/索引细节、ranking 实现、测试组织、偶发网络失败、一个 flaky test 或任务量大都
不是停止理由。做最保守的 additive 选择，写进 ADR/任务记录，继续到最终用户链路成立。

## 最终交接格式

不要只在聊天里说“完成”。把以下事实写入唯一实现任务记录，并在最终回复中简洁复述：

```text
branch / HEAD / merge-base
阶段提交列表
新增 Proto/RPC、migration 编号与每张表 owner
Core publication 的同事务证据与 private source 边界
Indexer lease/receipt/cursor/reconciliation/crash convergence
lexical ranking、excerpt、snapshot pagination 的版本与边界
Knowledge Center expanded/compact 与 granted App surface 截图路径
搜索 hit → artifact.review.v1 context → 第二次 Agent task 的真实 E2E 证据
knowledge.read → bridge method → Core grant-revision 重验 → Indexer 的证据
admin socket/CLI 权限、status schema 与未暴露公网的证据
shadow generation snapshot/catch-up/validation/CAS promote/cancel/crash-resume 证据
临时销毁 Indexer projection → Core authoritative full rebuild → search golden 等价证据
owner/project/archive/grant-revoke/foreign/outage/restart 证据
每条验证命令的真实 PASS/FAIL/SKIP/BLOCKED
Indexer/App Bridge/Archive/RAG capability 的最终裁决与理由
未决风险（尤其 embedding/semantic RAG、其他 source type）
工作树是否干净
是否 merge/push（默认均为否）
```

最终验收不是“写了很多代码”，而是用户醒来后可以在唯一分支上复现：生成 review Artifact → 等待 durable
Indexer 摄取 → Knowledge Center 搜索 → 固定 exact hit 为 Agent context → 再运行任务；同一个 Project 内，
获得 `knowledge.read` grant 的 opaque App 也能经 Bridge 搜索，revoke 后立即失效；operator 能在线重建以及在
临时丢失整个 Indexer projection 后从 Core authority 恢复相同 search golden；跨重启和故障仍正确。同时
semantic RAG 与其他未实现能力继续诚实 unavailable。
