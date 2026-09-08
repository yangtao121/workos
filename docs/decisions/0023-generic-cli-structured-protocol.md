# ADR-0023：Generic CLI 的结构化执行协议

- 状态：Accepted
- 日期：2026-09-08
- 关系：补齐 ADR-0008/0010 的 Generic adapter 实现，为 ADR-0016 Recovery 输入输出提供前置能力。

Generic CLI 使用新的 `workos.harness-cli/v2` 单请求、NDJSON 响应协议。请求和响应定义在
Harness Proto，复用 canonical AgentTaskInput、ResolvedTaskContextDocument 与
TaskArtifactOutput，不另写同义 JSON DTO。不保留 v1 CLI 格式分支；内置 fixture 同步升级。
这只改变显式配置的 CLI 子进程协议，不删除或复用公共 v1 Proto 字段。

请求携带 task ID、任务输入和 worker 已授权解析的上下文。adapter 验证上下文与任务中的
ref ID/digest/type 一一对应，仍只接受 `artifact.review.v1`；CLI 不收到凭据、lease、
存储路径或工作区挂载。请求总量仍限定 1 MiB，超过上限明确拒绝。

响应是 event 或 artifact 的互斥消息。仅接受原有 streaming 事件及明确请求的两种 review
产物（markdown、unified diff），禁止 CLI 伪造 Core artifact-created 或其他服务端事实。
每种请求类型恰一份输出；总响应仍受 4 MiB/1024 条/单条 1 MiB 限制。产物先留在内存中，
完整流合法、run-completed 且子进程退出成功后，才经 worker 的 lease-bound batch sink
原子发布。失败、取消、缺失产物、重复类型或 sink 失败都不得发布 completed。

运行期限取配置上限与任务请求期限的较小值；Generic 只声明实际可执行的 hard runtime
上限、结构化 review 和上下文能力。硬 token budget、usage reporting、内核隔离仍不支持；
请求 token/cost 预算时明确拒绝，不能因为支持 review 就绕过 App/Recovery 的治理门禁。
此协议不包含 source bundle、构建成功或 Registry receipt，不能把 review 产物当作可部署候选。
