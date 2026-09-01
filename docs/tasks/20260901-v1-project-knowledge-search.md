# Task: v1 Project Knowledge Search——持久索引、授权 App 与可恢复重建闭环

- 状态：in-progress
- Owner/Agent：overnight implementation agent（单一写入智能体）
- 进程/模块：workos-core（index publication + private source）、indexer（projection/search/worker/admin）、
  workos-gateway（可选 Indexer upstream）、runtime-host（App Bridge `knowledge.search`）、desktop-web（Knowledge Center）
- 依赖：Artifact review subtypes（ADR-0008）、artifact.review.v1 Agent context（ADR-0010）、
  App grants/revision（ADR-0003）、surface session/token（ADR-0002）、Gateway identity（ADR-0007）
- Branch：`feat/v1-project-knowledge-search`（自本地 `main` @ `0f89def`）
- 实现依据：`docs/prompts/20260901-next-agent-project-knowledge-search.md`

## 目标与范围

唯一最终目标链路：

```text
Project Agent 生成 canonical review Artifact
  → Core 同事务发布 durable indexing publication（不含正文）
  → indexer at-least-once + lease/replay 消费
  → indexer 自有 PostgreSQL projection（workos_index schema）
  → owner 在 Knowledge Center 有界确定性 lexical search
  → 安全 excerpt + exact artifact.review.v1 ref/digest
  → hit 固定为既有 Agent context（Core Submit/execution 重验，不经 Indexer）
  → granted knowledge.read Web Bundle App 经 App Bridge 搜索同一 Project 投影
  → Runtime 每次 App 调用重验 app instance/session/grant revision/Project scope
  → 本机 admin 面：安全 watermark + Core authority 在线全量重建（shadow generation 原子 promote）
  → 重启/中断/损坏数据 fail closed
```

范围内：review Artifact（document.markdown.v1 / code.unified-diff.v1）的 durable lexical 索引与检索；
owner-triggered 幂等 repair/reindex job（IndexContext）；三个专项门禁。

明确非范围（保持 unavailable）：semantic embedding/pgvector/RAG、Web Bundle bytes 索引、workspace 文件、
自动 context 注入、App knowledge.write、跨 Project/global search、第七个进程、生产库 DROP/TRUNCATE。

## 协议/数据影响

- Proto（additive，`make generate` 生成）：
  - `workos.index.v1`：SearchHit typed source ref/digest/title/type/created_at；SearchResponse freshness
    投影；IndexJob 严格 enum/计数；IndexContext → typed artifact refs + idempotency key（≤32，
    artifact.review.v1 only）；新增 private `workos.index.source.v1`（claim/resolve/complete/reconcile）；
    新增 private `workos.index.admin.v1`（status/rebuild/job，仅 Unix socket）。
  - `workos.bridge.v1`：additive read-only `SearchKnowledge`（request 无 owner/project/source 字段）。
  - Core private：`workos.agent.v1.AppKnowledgeAuthorizer`（grant revision 重验）或等价 additive service。
- Migration（forward-only，执行时确认空闲编号）：
  - `026_core_index_publications.sql`（owner：workos-core Index Feed）：publication/lease/claim 事实 + backfill。
  - `027_index_projection.sql`（owner：indexer）：`workos_index` schema documents/receipts/consumer
    state/jobs/generations。
- Capability：`project-review-index` / `project-knowledge-search` / `project-knowledge-rebuild` 按真实证据
  available；`archive`/`rag`/`embedding` 保持 false（固定原因）。

## 阶段计划与提交序列

A. ADR + Proto + Core publication（`docs: define project knowledge indexing boundary`、
`feat: publish durable review artifact index feed`）
B. Indexer domain/migration/worker（`feat: add idempotent project knowledge indexer`）
C. Search + Gateway（`feat: expose bounded project knowledge search`）
D. Knowledge Center + context 闭环（`feat: add Knowledge Center context workflow`）
E. App Bridge + SDK + fixture（`feat: expose scoped knowledge search to granted apps`）
F. admin + shadow rebuild（`feat: add resumable index projection rebuild`）
G. 门禁 + restart + 文档/状态（`test: prove knowledge indexing across restarts`、
`docs: record project knowledge search evidence`）

## 基线记录

- 基线 HEAD：`0f89def`（本地 main，ahead origin/main 9）。branch `-vv`：仅 main。
- `git diff --check`：干净。
- 基线命令结果见文末「必跑命令结果汇总」。

## 验收

- [ ] ADR（下一空闲编号）覆盖 10 项裁决
- [ ] Core 同事务 publication（artifact materialization + project archive）+ 回滚/幂等测试
- [ ] Core private source/reconcile/claim/complete（lease、crash window、corruption、transient 分离）
- [ ] indexer 模块边界 domain→application→ports←adapters；migration 026/027 owner 注释
- [ ] at-least-once worker：receipt/cursor 同事务、complete-after-commit、replay 安全
- [ ] Search RPC：query 规范、versioned page token、finite score、snapshot pagination、净化错误矩阵
- [ ] IndexContext repair job：typed refs、idempotency、≤32、失败不消费 key
- [ ] Gateway：可选 Indexer upstream、精确 allowlist、private/admin 404、spoof 清洗、wire budget
- [ ] Knowledge Center：Expanded/Compact/Medium 可达、generation guard、inert excerpt、Use as context
- [ ] App Bridge `knowledge.search`：manifest+grant+revision 重验、revoke fail closed、Indexer 零误调用
- [ ] admin Unix socket + workosctl index status/rebuild/job + shadow generation rebuild 状态机
- [ ] `make test-project-knowledge-search` / `make test-app-knowledge-search` /
      `make test-project-knowledge-rebuild` 三个专项门禁 PASS
- [ ] restart battery 扩展：索引/cursor/幂等/重建跨重启收敛
- [ ] UI before/after/current + notes.md（1440×900 + 390×844，含 granted App surface）
- [ ] docs/status.json + implementation.md + README（make generate）同步

## 交接

（收尾时填写：命令结果、提交列表、风险、未决项）
