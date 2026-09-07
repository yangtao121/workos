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

后续存储接入必须将向量与模型指纹绑定，禁止混合 feature-hash/模型向量或不同配方。
排序算法变更更新内部 ranking version，使旧页 token 明确失效。pgvector、重建回填、
workspace 与 review 混合检索的完整链路验收前，不升级 Indexer 的语义能力状态。

## 已验证与待完成

禁网、只读文件系统、2 CPU/2 GiB 容器中，真实 Go adapter → Python child → 固定权重
通过三项中英跨语言检索、同进程重复及子进程重启后的向量一致性测试。
并发、EOF 恢复、超时、错误指纹、非有限向量、维度/归一化错误、超大/畸形响应均有
Go race 回归。该证据目前只证明模型 adapter；尚未接入生产摄取、pgvector 和搜索。

依据：[官方模型卡及固定版本](https://huggingface.co/intfloat/multilingual-e5-small/tree/614241f622f53c4eeff9890bdc4f31cfecc418b3)、
[ONNX Runtime CPU 线程配置](https://onnxruntime.ai/docs/performance/tune-performance/threading.html)。
