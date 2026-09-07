# ADR-0017：语义知识——确定性 embedding、pgvector 混合检索与 workspace 源

- 状态：Accepted
- 日期：2026-09-05
- 关系：扩展 ADR-0013 的 lexical-only 边界；新增确定性本地 embedding 与
  pgvector 混合检索；为剩余能力总攻 W4 提供实现边界。

## 背景

ADR-0013 交付了 review-artifact 的确定性 lexical 索引与混合检索的基金会。
本 ADR 固定语义层边界：本地确定性 embedding、pgvector 存储、混合检索 RPC、
workspace 文件源与通用 archive 的最小实现。

## 决策

### 1. 确定性本地 embedding

- 384 维 feature-hash 向量：每个 token（小写字母/数字/Unicode 序列）经 SHA-256
  哈希到 384 个桶之一，正负号由第二个哈希位决定，累加后 L2 归一化。
- 零外部依赖、完全离线可复现。不调用外部模型 API。
- 诚实声明：这是词法语义的有界近似——捕捉 token 重叠与粗粒度词频，不声称
  深层语义相似性。真模型调用需要外部 API key（停止条件）。

### 2. pgvector 混合检索

- postgres 镜像切换至 pgvector/pgvector:pg18（含 pgvector 扩展）。
- migration 037（owner: indexer）：documents 表加 `embedding real[]`（384 维）。
  单 owner 本地规模（数百 docs/project）用 Go 侧余弦即可；ANN 索引延迟至规模需要。
- 混合检索 RPC：`SearchHybrid` 在同一 RPC 表面上结合 lexical ts_rank 与语义
  cosine。排序确定性：fused score DESC, source_created_at DESC, source_id ASC。
- SearchHybridRequest/SearchHybridResponse 为独立消息类型（不与 Search 复用）。

### 3. Embedding 管道

- 文档写入时计算确定性 embedding（同一 feature-hash 函数）并存入
  `embedding real[]` 列。
- 语义搜索路径：查询 embedding 在 Go 侧计算，与每个文档 embedding 做余弦
  相似度，按融合分数排序。有界：每代文档数 ≤2000。
- 无 embedding 的文档（旧数据）：语义分数为 0，仅 lexical 路径有效。

### 4. Workspace 文件源

- workspace 文件源 = 已安装 app 的本地目录经 runtime 映射到逻辑路径。
- 索引器读取有界文件列表（≤1000 files、≤512 KiB each），按文件扩展名过滤
  （允许 .md/.txt/.go/.ts/.json 等），计算 feature-hash embedding 并入索引。
- 忽略规则：.git、node_modules、.env、二进制文件。

### 5. 通用 Archive

- 最小实现：有界对象存储（PostgreSQL 大对象或文件系统），超界拒绝。
- archive 不做全文知识图谱。

## 后果

- 语义知识有真实门禁证据（`make test-semantic-knowledge` +
  `make test-workspace-indexing`）后才在 status.json 升级。
- 真模型调用/公网 embedding 服务属于外部账号前提，本批不实现。

## 2026-09-07 工作区扫描修正

- 注册及每次扫描通过 MountReader 使用 Linux openat2 打开根目录，禁止所有路径段的
  符号链接；子路径还限制 BENEATH/NO_XDEV，并以 NONBLOCK 打开、fstat 确认常规文件。
  不支持该内核能力时明确 filesystem-unavailable，不退回 check-then-open。
- 每次扫描最多 1000 个文本文件、10000 个目录条目、16 MiB 实际读取，单文件 512 KiB；
  路径最多 1024 bytes/16 层，每段最多 255 bytes。读取前检查大小，读取仍有硬上限。
  忽略树直接裁剪；文本与路径校验 UTF-8 和控制字符，长标题按 code point 截断。
- 扫描错误或总预算超限为 degraded，不把部分结果交给 set-difference tombstone。
  状态原因使用固定类别，不泄露原始文件系统错误；后续完整扫描恢复 active 并清空原因。
- 挂载路径由 operator 明确绑定，当前实现不等于 Runtime 自动 workspace 映射；
  并发变动目录不承诺文件系统快照。

## 2026-09-07 工作区收敛修正

- 完整 pass 的所有文档、publication receipts、cursor、缺失文档 tombstone 和源统计
  由单个 Indexer PostgreSQL 事务提交；失败不返回已应用数量，也不留下部分结果。
- 事务锁定 workspace source 后比较扫描起始版本（updated_at）、原始 root/owner/project
  和 stopped 状态；版本失效返回 Aborted，由调用者重新扫描。更新时间至少推进一微秒，
  同时刻重试也不能绕过 CAS；重新绑定及迟到的 degraded 结果遵循同样版本边界。
- 活跃 generation 指针的共享锁与 promotion 互斥；同 project 的 live upsert/archive
  使用同一事务 advisory lock，避免 archive 之后被在途写入恢复。
- 以上不宣称 filesystem snapshot。Rebuild 对 workspace 的完整复制/验证仍需组合门禁审查。

## 2026-09-07 混合来源重建修正

- Core authority 只验证 review-artifact 集合；workspace 不伪装成 Core artifact。
- promotion 锁住 active generation 后，以旧 active 中最后一次成功 sync 的 workspace
  集合替换 target 中的 workspace，再与 generation 切换一起提交。期间完整 sync 被共享锁
  正确排序；删除与归档文件不会从旧 target 恢复。重建不表示重新读取文件系统。
- promotion 复制最多 2000 个文件、64 MiB 文本；旧 active 的 live 集合和 target 的 workspace
  集合均预检，超限将 job 标记 workspace-copy-limit 并保留原 active。该限制是本地规模
  promotion 的显式预算，后续大规模实现需要分页 checkpoint，不能暗中截断。
- indexer schema 灾难删除会失去 operator 挂载登记；review 可由 Core 重建，workspace
  需要重新绑定并 sync。普通重建保留 workspace 登记、degraded 状态和已索引快照。

## 2026-09-07 Operator 停用

- IndexWorkspaceSource 提供 etag；私有 StopWorkspaceSource 和
  `workosctl index workspace stop --source <id> --etag <etag>` 操作精确绑定版本。
- 源行锁与版本检查保护重绑/并发 sync，旧 etag 返回 Aborted。停用状态和所有 writable
  generation 的 workspace tombstone 在同一事务提交；失败不撤下已索引文档。
- 停用后 sync 拒绝，旧 snapshot ReadDocument 返回 NotFound；operator 重新 register + sync
  可恢复。当前 stopped etag 可重复停用，已消费的旧 etag 不复用。Gateway 不公开此 RPC。

## 2026-09-07 真实链路补漏

- review snapshot 与 live ingest 必须计算相同的检索向量；重建后结果和分数有一致性 golden。
- Core review-artifact / archived-project reconciliation 的仓储均使用 LIMIT+1 probe；
  返回的 active source 页可短于请求大小，consumer 必须按 continuation 遍历。
- 真实 CLI/admin socket/Gateway/Chromium 门禁验证文件分页、混合来源、重启、重建与停用。
  这补齐 workspace 的端到端证据，不将 feature-hash 标记为真实模型语义搜索。

## 2026-09-07 同分分页修正

Hybrid 游标与实际排序一致：较低分数，或同分且创建时间更早，或同分同时间且 source ID
更大。原 After 判断会重复较新结果并漏掉较旧结果；真实 PostgreSQL 同分四文档覆盖
不同时间与同时间 ID 排序，以 page size 1/2/3/4 逐页验证完整性、无重复和正常终止。
