# ADR-0021：离线模型向量与模型身份

- 状态：Accepted
- 日期：2026-09-07
- 关系：修正 ADR-0017 的 feature-hash 与真实语义能力边界。

## 决策

采用 `intfloat/multilingual-e5-small` 的官方 FP32 ONNX 权重，固定 revision
`614241f622f53c4eeff9890bdc4f31cfecc418b3`。权重、tokenizer SHA-256 与完整预处理
配方保存在 `internal/indexer/adapters/localembedding/model.json`；配方文件的 SHA-256
是模型指纹。CPU Runtime 与 Python 依赖固定版本，运行期不下载模型、不访问 Provider。
公开本地模型不需要真实 API key，撤销 ADR-0017 对此的错误前提。

Indexer 拥有推理子进程，通过 `index/v1/embedding.proto` 的有界 protobuf JSON 行通信；
不增加监听端口或独立常驻服务。每次只允许一个在途请求，20 秒总时限，空闲 30 秒退出。
取消、父进程退出或格式错误清理子进程；失败明确 unavailable，不回退 feature-hash。
子进程不接收设备/Provider 密钥、文件路径形式的内容引用或网络 URL。

Query 使用 `query: `，文档使用 `passage: `，中文同样保留该英文前缀。
文档取最多 16 KiB 的 UTF-8 完整前缀；模型最多 512 tokens，attention mask 均值池化、
L2 归一化，输出 384 个有限 float32。长文全文仍保留词法检索；单个文档向量不能表示
未进入该有界片段的全部内容，不声称已经完成分块语义索引。禁用 Runtime 遥测。

migration 047 将可再生的 real[] 缓存清空并转换成 public.vector(384)，同时记录模型指纹；
文档、来源、receipt、cursor 不变。数据库约束要求向量与指纹成对存在、384 维且归一化。
应用层先完成推理，再调用存储事务；正常摄取、workspace 整批同步、重建快照共用模型 port。
后台每批最多八条，按 generation/source/digest/publication CAS 回填已有索引文本；更新、
归档、退休或清理的快照不再接受旧向量。重建切换在复制 workspace 后检查目标向量齐备。

混合搜索使用同一个 repeatable-read 快照检查向量齐备并在 PostgreSQL 内完整排序。
分数为 0.5 × 归一化 ts_rank + 0.5 × clamp(cosine, 0, 1)，候选必须词法命中或 cosine ≥ 0.75；
该阈值是固定召回策略，不是相关性概率。排序 score DESC / created DESC / UUID ASC，
只取 page size + 1，取消原先 2000 个候选的静默截断，不声称已有 ANN 索引。
内部 ranking version 为 3，查询 token 同时绑定模型指纹，旧 feature-hash token 失效。
缺失或不匹配的模型向量返回 unavailable，词法搜索仍可用，不静默降级为不完整混合结果。

统一镜像内置固定 Python runtime、worker 与已校验权重；只有 Indexer 启动推理子进程。
构建期允许下载公开模型，BuildKit 保留校验后的缓存；运行期没有下载器调用。
本地代理需要宿主网络时可设置 WORKOS_BUILD_NETWORK=host，仅影响构建网络。

## 已验证与待完成

禁网、只读文件系统、2 CPU/2 GiB 容器中，真实 Go adapter → Python child → 固定权重
通过三项中英跨语言检索、同进程重复及子进程重启后的向量一致性测试。
并发、EOF 恢复、超时、错误指纹、非有限向量、维度/归一化错误、超大/畸形响应均有
Go race 回归。`make test-model-postgres` 已验证真实模型 → pgvector → 中英查询、
摄取/重建向量一致性及子进程重启回填后的排序一致；独立 PostgreSQL/race 回归覆盖
047 保留文档/receipt/cursor、五种回填竞争、2102 文档分页与未齐备代际拒绝切换。
默认栈升级、`make test-semantic-knowledge` 与 `make test-workspace-browser` 六阶段均通过；
浏览器在首次摄取、重启与重建后用中文查询英文文件，独立词法 RPC 零命中。
这证明固定模型的真实检索链，不声称已具备生成式 RAG 或更广泛的语义质量评测。

依据：[官方模型卡及固定版本](https://huggingface.co/intfloat/multilingual-e5-small/tree/614241f622f53c4eeff9890bdc4f31cfecc418b3)、
[ONNX Runtime CPU 线程配置](https://onnxruntime.ai/docs/performance/tune-performance/threading.html)。

## App 知识入口

Runtime 使用既有 SearchHybrid RPC，并在索引排序/分页前固定 review 来源；App 的输出
契约是 review artifact 引用，不把 workspace 快照当成可供 Agent 读取的 review 引用。
每次调用仍先由 Core 重验安装 grant revision 和 owner/project 绑定。`knowledge.read`
不产生文件读取授权。模型分数只接受 [0,1]；无效 token 保持 InvalidArgument，存储损坏
保持净化 Internal，模型/网络暂不可用保持 Unavailable，不回退词法接口。

真实 Connect 与 PostgreSQL 测试证明更靠前的 workspace 行不会挤掉合法 review；
opaque App 浏览器门禁用中文查询英文 review（词法零命中），并验证无授权不协商、
撤销后拒绝。SDK 仅更新语义说明，已有消息结构与字段号不变。
