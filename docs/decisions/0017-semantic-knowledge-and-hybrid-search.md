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
- 索引器读取有界文件列表（≤1000 files、≤1 MiB each），按文件扩展名过滤
  （允许 .md/.txt/.go/.ts/.json 等），计算 feature-hash embedding 并入索引。
- 忽略规则：.git、node_modules、.env、二进制文件。

### 5. 通用 Archive

- 最小实现：有界对象存储（PostgreSQL 大对象或文件系统），超界拒绝。
- archive 不做全文知识图谱。

## 后果

- 语义知识有真实门禁证据（`make test-semantic-knowledge` +
  `make test-workspace-indexing`）后才在 status.json 升级。
- 真模型调用/公网 embedding 服务属于外部账号前提，本批不实现。
