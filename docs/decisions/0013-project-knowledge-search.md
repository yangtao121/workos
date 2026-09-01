# ADR-0013: Project Review Artifact 知识索引、授权检索与可恢复重建

日期：2026-09-01。状态：Accepted（实现分支 `feat/v1-project-knowledge-search`）。

## 背景

`indexer` 进程目前只有 health scaffold：`IndexService` 返回 Unimplemented，
`archive`/`rag` capability 均为 false。另一方面，Project Agent 已经能产出两种 immutable
review Artifact（`document.markdown.v1` / `code.unified-diff.v1`，ADR-0008），它们已经是
canonical Agent context（ADR-0010），但没有任何可检索的持久索引：owner 无法在当前
Project 内"找回落过的知识"，获得 `knowledge.read` grant 的 Web Bundle App 也没有任何
可调用的 method。本 ADR 固定第一条真实知识链路的边界：**Project review Artifact 的
durable lexical 索引 + 有界确定性检索 + granted App 只读访问 + Core 权威的可恢复重建**。

明确不在本 ADR 范围：semantic embedding、pgvector、向量检索、reranker、workspace 文件
爬取、Web Bundle bytes 索引、自动 context 注入。这些要么留待独立 ADR，要么继续如实
unavailable。

## 决策

### 1. 索引范围：只有 canonical project review Artifact

首版只索引 `workos_core.project_review_artifacts` 中两种已实现的 review subtype。
Web Bundle bytes（private launch payload）、workspace 文件、日志、task goal、Provider raw
output、聊天全文一律不进入索引；private publication 与 private source response 的字段
白名单里没有它们的位置， 未来增加 source type 必须新 ADR + additive operation enum。

### 2. 所有权：Core 是唯一真相，Indexer 只持有可重建 projection

- `workos-core` 继续拥有 Project、review Artifact 原始内容、Agent task/event，以及新增的
  **index publication source facts**（migration `026`，owner：workos-core Index Feed，
  `internal/core/indexfeed` 模块）。
- `indexer` 只拥有 `workos_index` schema（migration `027`）：可重建的 search projection、
  consumption receipt/cursor、index job、projection generation 与 rebuild job。
- 两个 schema 之间零 FK、零跨模块 SQL；跨进程只走 versioned private RPC 与 durable
  publication。Indexer 损坏/清空都可以从 Core authority 完整重建，因此它永远不是授权或
  内容真相。

### 3. 发布模型：automatic publication + deterministic reconciliation

- Artifact materialization 的同一 Core 事务追加一条 review-artifact upsert publication；
  Project archive 的同一事务追加 tombstone publication。publication 只含操作、publication/
  source ID、owner/project、digest、type、title 与 UTC 时间，**绝不含正文**。
- 同一 immutable Artifact/digest 只有一个 authoritative upsert publication
  （`(artifact_id, operation)` 物理唯一）；tombstone 与 archive revision/event/outbox 同事务，
  对同一 archive revision 幂等。replay/stale revision 不产生第二条 publication。
- 已存在的 review Artifact 由**首次 authoritative reconciliation backfill** 覆盖：Core 提供
  分页 reconciliation read（按 `(created_at, id)` 稳定序、快照边界），indexer 启动/周期比对
  projection 与权威页，逐条收敛（缺→建、漂移→重投影、消失/归档→tombstone）。不写
  migration 数据回填、不伪造正文/digest/时间，归档 Project 不产生可检索 publication。

### 4. 消费模型：at-least-once lease/receipt/ack

- Core claim 使用 `FOR UPDATE SKIP LOCKED` + 持久 lease（worker identity + 过期时间）；
  过期可重领，concurrent worker 不能同时持有同一有效 lease，stale complete 不能完成新 lease。
- Indexer 固定顺序：本地单事务（document/tombstone + receipt + consumer cursor/job 进度）
  提交成功后，才向 Core complete/ack。complete 响应丢失 → lease 过期重领 → same
  publication same digest 精确 no-op replay。same ID different digest/operation 是
  corruption，fail closed 不覆盖。这是 at-least-once，不宣称 exactly-once。
- source corruption 与永久 unsupported 是可观察 terminal/degraded outcome；Core/PostgreSQL
  瞬时不可用保持 retryable，二者不折叠。

### 5. 检索语义：确定性 lexical + versioned snapshot pagination

- 首版检索是 deterministic lexical search：PostgreSQL 内建 `websearch_to_tsquery`/tsvector
  （`simple` 配置，不依赖未声明 extension、不依赖 locale 偶然值）+ 纯函数 excerpt/评分。
- query 规范：valid UTF-8、trim/canonical whitespace、1–256 code points、拒绝 C0/C1、term
  数有界；空/超长/畸形在任何业务读取前 `InvalidArgument`。
- `score` 由固定版本常量的 ranking（title 命中权重 + ts_rank + 确定性 tie-break
  `(source_created_at, source_id)`）计算，finite、非 NaN/Inf、同 snapshot 同 query 同数据顺序稳定。
- page token 是 versioned HMAC-free 结构化 token（版本 + owner/project + canonical query
  digest + ranking version + snapshot watermark + 最后排序键），跨 query/project 重放或篡改
  稳定 `InvalidArgument`；恰好满的最后一页不产生 phantom token。翻页过程中新入库 document
  不插入旧 page chain（snapshot/high-watermark 边界）。
- excerpt 由纯函数生成（match window、换行/控制字符处理、Unicode 边界、固定长度上限），
  是有界 plain text，不是 HTML；响应不返回全文。

### 6. 搜索命中 = 既有 artifact.review.v1 context，不新增自动注入

`SearchHit` 携带 typed source ref（`artifact.review.v1` + artifact UUIDv7 + exact sha256
digest），与 ADR-0010 的 context ref 是同一语义、同一 Proto 类型投影，不造第二套 ref DTO。
"Use as Agent context" 走既有 Desktop context chips → Core Submit → ResolveTaskContext
owner/project/digest/lease 重验；搜索本身绝不自动注入 Agent，Indexer 不持久任何
task 关联。旧 `context_ref` string 字段保留：非空时必须等于 typed ref 的 canonical
字符串投影，否则 `InvalidArgument`（文档化兼容策略，不猜）。

### 7. 生命周期：tombstone 仲裁，embedding 独立演进

- Project archive tombstone 在 Indexer 单事务内使该 owner+project 全部 document 退出
  检索（保留物理行以便审计/重建对比），并持久阻止迟到的旧 upsert 复活它们
  （tombstone 序单调，`archived_at` 晚于 upsert 的 source version 时 upsert 不生效）。
- 未来 semantic embedding：schema 预留 per-generation `searchable representation` 的
  演进位（列 + ranking version），但 v1 不建向量列、不接外部 API。升级路径是新的
  generation + 新 ranking version + 全量重建，不原地改语义。

### 8. 三个入口，一个 application contract，三条信任边界

同一 Indexer application service 承载：

- **owner browser**：Gateway public `IndexService.Search/IndexContext`，身份只能来自
  Gateway device-session 注入的 trusted owner header；scope 恒为 owner + canonical
  active project；Gateway allowlist 精确到 `/workos.index.v1.IndexService/`，private
  source/admin/System 管理 RPC 一律 404。
- **granted opaque App**：不直连 Indexer。App→Runtime 只发有界 query/page 参数（无
  owner/project/source 字段）；Runtime 用 surface session 派生 scope，经 Core private
  authorization（app instance、Project、`knowledge.read` grant、**exact current grant
  revision**）后以内部受信身份调用 Indexer，再投影 sanitized hit。App 拿不到 Indexer
  URL、device cookie、publication lease 或全文。
- **operator**：Indexer 本机 Unix admin socket（复用 Gateway/Core credential admin socket
  的安全模式：canonical absolute path、owner-only parent/socket、拒 symlink/world-writable、
  0600），`workosctl index status/rebuild/job`；永不进 Gateway/TCP。

### 9. 重建：shadow generation 状态机

全量重建是 durable `rebuild_jobs` + `projection_generations`：

```text
requested → snapshotting → catching_up → validating → promoting → completed
                                  ↘ canceled / failed（单调终态）
```

- snapshot：从 Core authoritative reconciliation 分页读取 active review Artifact 全集
  （owner/project/source ordering、固定 boundary），逐 batch 单事务写入 target generation
  的 document/receipt/cursor/counts；checkpoint 可重放，重启不从零开始。
- catch-up：消费 snapshot boundary 之后的 live publication delta，直到 Core-confirmed
  barrier；期间 live upsert/archive 同写 active 与 target（每 publication 在每个 generation
  至多一份 receipt/effect，tombstone 优先级高于旧 upsert）。
- validate：比较 source count、exact digest、tombstone 与 watermark；任何 mismatch/
  corruption 终止目标 generation，保留当前 active generation 可搜。
- promote：数据库单事务 CAS 更新 active generation pointer；此前查询全部走旧 generation，
  此后新查询走新 generation；旧 versioned page token 按 ADR 固定**失效**（snapshot 已变，
  InvalidArgument），不混页。成功后异步、有界、幂等清理旧 generation；清理失败只告警。
- 并发 rebuild 按 scope 串行或明确拒绝；worker/rebuild 双实例由 generation 级 receipt
  唯一约束 + durable phase 收敛。正常生产路径无 `DROP/TRUNCATE`；灾难恢复销毁 projection
  只发生在专项测试的临时 schema。

### 10. capability 诚实裁决

- 只有专项 E2E 证据成立后，`project-review-index` / `project-knowledge-search` /
  `project-knowledge-rebuild` 才 available。
- 泛化 `archive`（Object Store/通用归档）与 `rag`/`embedding`/pgvector 继续 false，固定
  原因文案"generic archive not implemented / semantic RAG and embeddings not implemented；
  evidence limited to review-artifact lexical search"。
- Runtime `knowledge.search` 只在 manifest 请求 + 当前 version 实际获得 `knowledge.read`
  grant + Core 重验通过时协商；capability vocabulary 存在该字符串不构成可用性。

## 后果

- 三个新专项门禁：`make test-project-knowledge-search`（owner 全链路）、
  `make test-app-knowledge-search`（granted App + revoke）、
  `make test-project-knowledge-rebuild`（在线重建 + 灾难恢复）。
- Indexer 状态最高升到 working，evidence 明确限定为 review Artifact durable lexical
  search；RAG/泛化 archive 的证据与状态不动。
- 已知代价：lexical 检索不解决语义匹配；reconciliation 分页在 Artifact 数量大时是 O(n)
  轮询（有界 batch + watermark）；两代 generation 并存期间有双份存储（有界、可清理）。
