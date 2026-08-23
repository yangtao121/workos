# Task: DeepSeek Harness Adapter vertical slice

- 状态：done
- Owner/Agent：Codex
- 进程/模块：workos-core Agent/Project orchestration；harness-host DeepSeek adapter
- 依赖：PostgreSQL 18、DeepSeek Harness runtime 0.1.1rc1、Docker

## 目标与范围

实现 `Project.HarnessBinding(provider_id="deepseek")` → SubmitTask → durable
provider snapshot → TaskExecution lease → 官方 DeepSeek Harness runtime → canonical
AgentEvent → persisted ordered event stream。默认 disabled，无 Key、无网络时现有 Fake
链路必须继续工作。

本任务包含 Project-scoped provider 路由、官方 Harness JSON-RPC adapter、配置/凭据
边界、流式 canonical event 映射、无密钥本地 API fixture、部署资产和文档。不包含
tools、MCP、subagents、approvals、persistent sessions、workspace mount、通用 secret
manager、生产认证或其他 Product Alpha 模块。

### Provider 路由设计

Submit 由中立的 Core orchestration 层协调 Agent 与 Project application ports。它先按
`owner_user_id + idempotency_key` 查询既有 Task；命中时直接返回既有 provider 快照，
不重新读取 Project。新提交的 Project scope 通过 owner-scoped Project application
读取当前未归档 Project，并从 `HarnessBinding.provider_id` 解析 provider；global 或无绑定
Project 使用显式配置的安全默认 `fake`。解析结果作为 `AgentTask.ProviderID` 传入 Agent
application，并由 Agent-owned repository 与 Task 一起原子持久化。

Agent 模块不读取 Project 表、不导入 Project adapter；Harness 不读取 Core 数据库。
并发相同 idempotency key 由数据库唯一键决定唯一 winner，loser 返回 winner 的原始
provider。执行时 `RunStarted.provider_id` 必须与 lease 中的 provider 快照一致，状态推进
不得覆盖它。

## 协议/数据影响

- 不修改 v1 Proto，不新增 migration；复用现有 `HarnessBinding.provider_id`、
  `AgentTask.provider_id`、TaskExecution lease 和 canonical AgentEvent。
- Agent repository port 增加 owner-scoped idempotency lookup；Harness ports 增加
  vendor-neutral typed execution error（仅内部接口）。
- 新增配置 `agent.default_provider` 与 `harness.deepseek`；API Key 仅来自
  `DEEPSEEK_API_KEY`，不进入 YAML、数据库、事件或日志。
- DeepSeek vendor request/response 和官方 Harness JSON-RPC 类型只存在于 adapter。
- 官方 Harness runtime 作为 `harness-host` 的每 Task 子进程，不改变六个稳定进程边界。

## 验收

- [x] 配置、health/capability、输入/预算和 secret redaction 单元测试
- [x] JSON-RPC 流式事件、错误分类、取消、deadline、emit failure 单元测试
- [x] Project owner scope、binding、default provider 和 idempotency snapshot 测试
- [x] 官方 Harness runtime + 本地 DeepSeek API fixture 的无密钥纵向集成
- [x] provider 在 lease、事件、Project 改绑和进程重启后保持不变
- [x] Fake、Generic CLI、Gateway 与浏览器 E2E 不回归
- [x] `make generate` 无二次漂移
- [x] `make check`
- [x] `make test-integration`
- [x] `make test-deepseek-fixture`
- [x] `make test-e2e`
- [x] README、部署资产与 `docs/status.json` 同步

## 交接

开始时工作树 clean；保留现有提交与 PostgreSQL volume。实现前已查阅 DeepSeek 官方
API 文档和官方 `deepseek-ai/deepseek-harness`：本实现固定已发布 runtime
`0.1.1rc1`，通过 newline-delimited JSON-RPC stdio 使用其 Agent Loop、Session event
和 DeepSeek SSE adapter，不在 Go 中复制厂商 SSE DTO。

基线 `make bootstrap` 与 `make check` 均通过。真实 DeepSeek smoke 非常规验收，本任务
不使用聊天中暴露的 Key。

### 已实现决策与证据

- Core 新增中立 `internal/core/orchestration.TaskRouter`。它先读取 owner-scoped
  idempotency snapshot，再读取 owner-scoped Project；Agent repository 不再跨模块查询
  Project 表。数据库冲突 loser 返回 winner 的 provider snapshot。
- `RunStarted.provider_id` 必须等于 lease 中固化的 provider，否则 Core 拒绝事件；Task
  状态推进不再改写 provider。
- DeepSeek adapter 使用 stable WorkOS ID `deepseek`，runtime 内部选择官方
  `deepseek-official` route。每 Task 一个进程，子环境为显式白名单，stderr 丢弃，stdout
  单行/总量和聚合回答均有界；取消和 emit 失败终止整个进程组。
- 官方 runtime wheel 固定为 `0.1.1rc1`。amd64 SHA-256 为
  `8eb31e3ab2bc3ff45474fe419eb389e32553391f1a40789ea2cc3dc8d6de137b`，arm64 为
  `e73987c6c08d8322bce2b8b2ce75db6a139ecf546417b6015ce7a8de5e5f19b5`；镜像实际完成
  下载校验，并安装 matching `-rg` sidecar。
- WorkOS-owned Cordis composition 不加载 bash、filesystem backend、persistence、MCP、
  subagent 或 workspace context；skills、jobs tools 与 goals 均关闭。官方 request retry
  上限固定为 2。
- `make test-deepseek-fixture` 已通过：Project binding → durable Task provider snapshot →
  lease → official runtime → local HTTP/SSE fixture → ordered persisted canonical events；
  Project 改绑后的幂等重试仍返回原 Task，Core/Harness 重启后 provider、RunStarted 和
  event stream 保持不变。
- adapter 单元测试已通过：disabled/misconfigured/healthy/degraded health、输入与预算、
  JSON-RPC 多 chunk/空 delta/usage、唯一 terminal、401/403/429/5xx/transport 分类、
  cancellation/deadline/emit failure、未知/畸形/提前 EOF/超大输出和 secret redaction。

真实 DeepSeek smoke 未运行，也未使用真实 Key。当前 canonical budget 无法在请求前执行
vendor-neutral 金额硬上限，因此没有新增自动 live target；不得通过硬编码价格表绕过该
边界。

### 最终验收命令（2026-08-23 UTC）

- `make generate`：通过；两个中间重试因 buf.build 短暂不可用失败，最终重跑恢复，
  工作树差异 SHA-256 与第一次成功后相同，生成结果无二次漂移。
- `make check`：通过。首次收尾检查仅发现 Cordis YAML 格式差异；使用仓库 Prettier
  格式化该文件后，全量检查通过。
- `make test-integration`：通过；包含 Fake 纵向链路和 Core/Harness 重启恢复。
- `make test-deepseek-fixture`：通过；包含成功流、usage、malformed SSE、非预期
  content type、提前 EOF、429、503、Project 改绑幂等与重启恢复。
- `make test-e2e`：通过；Playwright 1/1。
- `git diff --check`、secret 文件名扫描与仓库文件所有权检查：通过。
