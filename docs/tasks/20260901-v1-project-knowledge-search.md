# Task: v1 Project Knowledge Search——持久索引、授权 App 与可恢复重建闭环

- 状态：done（本批次窄切片；semantic RAG 与泛化 archive 继续 unavailable）
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

### 提交列表（branch `feat/v1-project-knowledge-search`，merge-base `0f89def`）

```text
851afa5 docs: define project knowledge indexing boundary
caecf78 feat: publish durable review artifact index feed
f1ff536 feat: add idempotent project knowledge indexer and bounded search
d785414 test: prove the knowledge search stack end to end
908f980 feat: add Knowledge Center context workflow
0e5ebaf feat: expose scoped knowledge search to granted apps
d76b760 fix: negotiate knowledge.search from a configured indexer and prove it end to end
aa90e54 docs: record project knowledge search evidence
4f64c04 feat: add resumable index projection rebuild
（后续 lint/gateway-table/gitignore 修复提交见 git log）
```

### 协议/migration owner

- `026_core_index_publications.sql`：owner workos-core Index Feed（publication/lease/claim 事实，无正文）。
- `027_index_projection.sql`：owner indexer（`workos_index` schema：documents（generation 作用域）、
  publication_receipts、consumer_state、index_job\*、projection_generations/active_generation、
  rebuild_jobs、project_tombstones）。001–025 逐字节未动，checksum pin 测试沿用既有链。

### Core publication / private source 边界

- materializer 与 archive 事务经 tx-scoped sink 追加 publication；失败回滚零残留（集成测试证明）。
- `IndexPublicationSourceService`（claim/resolve/complete/reconcile/CountPending）挂 Core HTTP mux，
  不在 Gateway allowlist → TCP 404；resolve 在 claim 事务内权威复验 owner/project/digest/正文与
  project 活性（archived → tombstoned verdict）。

### indexer 侧

- lease/receipt/cursor 同事务；complete-after-commit；same-publication same-digest replay no-op；
  digest 漂移 = corruption 拒写。集成测试覆盖：同事务回滚、双 claimant、lease 过期、stale complete、
  archive race、存储损坏 terminal verdict、reconciliation 分页。
- Search：query 规范（1–256 cp、C0/C1、词法预处理仅字母数字 token）、versioned checksum page token
  （绑定 owner/project/query digest/ranking v1/generation/snapshot）、simple tsquery + title×2 权重、
  tie-break `(score DESC, created_at DESC, source_id)`、limit+1 无 phantom token、纯函数 excerpt。

### 门禁结果（收尾日实测）

| 命令                                                        | 结果 |
| ----------------------------------------------------------- | ---- |
| make bootstrap / generate ×2（零 tracked diff）             | PASS |
| make check（proto/go/web + status render --check）          | PASS |
| make test-integration（含 index restart battery）           | PASS |
| make test-e2e（19 spec）                                    | PASS |
| make test-project-knowledge-search                          | PASS |
| make test-app-knowledge-search                              | PASS |
| make test-project-knowledge-rebuild                         | PASS |
| make test-artifact-review / test-artifact-context           | PASS |
| go test -race（indexer / runtime surface / core indexfeed） | PASS |
| buf lint / buf breaking vs base `0f89def`                   | PASS |
| docker compose config --quiet；git diff --check             | PASS |
| after/current 截图 hash 相等；before/after 不同             | PASS |

### 视觉证据

`docs/ui/desktop-web/changes/20260901-project-knowledge-search/{before,after,notes.md}`；
after/current 对应文件 hash 相等。expanded（results、chip）+ compact（results + Use as context）

- granted App surface。

### 未决风险

- semantic RAG/embedding/pgvector、泛化 archive、workspace 文件源：未实现，capability 保持 false。
- 多 owner 部署：系统当前单 owner（users_single_owner_idx），rebuild fixture 以"无产出的 owner id"
  验证搜索隔离；真实多 owner 需要未来身份体系演进。
- rebuild catch-up barrier 以 Core drained feed 为准（at-least-once 语义下的最终一致）；
  极端高频写入下 promote 可能需要多次 catch-up pass。
- 修复提交 `d76b760` 曾误入一个根目录 `runtime-host` ELF（`go build` 副产物），已在后续提交
  从索引删除并加入 .gitignore；历史对象中仍有残留（未做历史重写）。
- 工作树干净；未 merge 到 main、未 push（等待用户审查）。
