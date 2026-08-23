# 下一位智能体 Prompt：DeepSeek Harness Adapter 纵向切片

> 将本文件的完整内容交给下一位智能体。目标是直接继续实现，不是只输出一份计划。

## 你的角色

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。上一阶段已经完成可运行的 foundation；你的任务是在不破坏既有边界的前提下，实现第一个真实 Provider：DeepSeek Harness Adapter，并证明 Project 绑定能够把 Task 路由到该 Provider。

持续推进实现、测试和文档，直到本任务在不依赖真实密钥的情况下可验收。只有遇到下面列出的架构决策检查点、需要用户提供新的权限，或官方 DeepSeek 能力与当前协议确实冲突时，才停下来和用户讨论。

## 当前仓库事实

- 六个稳定进程是：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、`reliability-host`、`indexer`。
- Foundation 任务已完成，验收记录位于 `docs/tasks/20260823-foundation.md`。
- 当前真实能力以 `docs/status.json` 为准；不要从聊天内容推断进度。
- Harness Provider 接口位于 `internal/harness/ports/provider.go`：
  - `Describe() *HarnessProviderInfo`
  - `Run(context.Context, taskID, *AgentTaskInput, Emit) error`
- 现有 Provider：
  - `internal/harness/adapters/fake`
  - `internal/harness/adapters/genericcli`
- Task Worker 通过 private TaskExecution RPC 租约执行任务；Harness 不直接写数据库。
- Canonical task events 定义在 `api/proto/workos/agent/v1/agent.proto`。
- Project 已经有 `HarnessBinding.provider_id`，定义在 `api/proto/workos/project/v1/project.proto`。
- 当前 `internal/core/agent/application/service.go` 在提交任务时把 `ProviderID` 固定成 `fake`。这是本任务必须显式处理的路由缺口，不能用全局搜索替换掩盖。
- 默认开发与 CI 不得依赖 DeepSeek 网络或真实 API Key；Fake Harness 链路必须继续工作。
- 国内开发默认使用 goproxy.cn、npmmirror 和阿里云 Debian 镜像。不要随意改回慢源。
- 当前工作树中有上一阶段创建的大量未提交文件。这些文件是有效成果，不是垃圾。禁止 `git reset --hard`、`git clean`、`git checkout --` 或删除未跟踪基础文件。
- PostgreSQL Docker volume 中可能有验收数据。禁止执行 `docker compose down -v`。

## 开始前必须完成

1. 依次完整阅读：
   - `AGENTS.md`
   - `README.md`
   - `CONTRIBUTING.md`
   - `docs/structure.md`
   - `docs/architecture/implementation.md`
   - `docs/decisions/0001-foundation-boundaries.md`
   - `docs/status.json`
   - `docs/tasks/20260823-foundation.md`
   - 本 Prompt 中提到的 Proto、Harness、Project 和 Agent Task 代码与测试
2. 运行 `git status --short`，保留所有既有改动。
3. 从 `docs/tasks/TEMPLATE.md` 新建 `docs/tasks/20260823-deepseek-harness.md`，状态设为 `active`，先写清范围、协议/数据影响和验收项。不要修改已经 `done` 的 foundation 任务来冒充新任务进度。
4. 运行 `make bootstrap` 和 `make check` 建立基线。如果基线失败，先判断是否由当前改动造成并记录证据，不要盲目重写代码。
5. DeepSeek API、流式格式、错误码和模型名称具有时效性。实现前必须查阅最新的 DeepSeek 官方 API 文档，只使用官方资料作为技术依据，不要凭记忆猜 endpoint 或 SSE payload。

## 任务目标

完成一条可测试的纵向链路：

```text
Project.HarnessBinding(provider_id="deepseek")
  → SubmitTask
  → task 持久化 provider_id="deepseek"
  → harness-host lease
  → DeepSeek streaming API adapter
  → canonical AgentEvent
  → persisted ordered event stream
```

真实 API smoke test 可以作为显式 opt-in，但常规单元、集成和 CI 验收必须使用本地 `httptest`/fixture，不需要用户密钥。

## 范围内

- 新增 `internal/harness/adapters/deepseek`，实现现有 `ports.Provider`。
- 加入明确、可校验的 DeepSeek 配置和环境变量。
- 使用 Project 的 `HarnessBinding.provider_id` 选择 project-scoped Task 的 Provider，并把选择结果作为快照持久化到现有 `agent_tasks.provider_id`。
- 保留 global task 和没有绑定的 Project 的向后兼容、安全默认行为；默认仍不得触发外网请求。
- 将 DeepSeek 流式响应映射为 canonical events。
- capability/health 能准确区分 disabled、misconfigured、available、temporarily unavailable。
- 完整的无密钥测试、文档、状态和交接记录。
- 如确有必要，做最小的 Desktop/API 调整以选择或显示 Provider；禁止顺手重做整个 UI。

## 明确不在范围内

- 生产认证、设备注册或 LAN 暴露。
- App Registry、Artifact 存储、RAG、Reliability、rootless Podman Runtime。
- DeepSeek tool calling、MCP、subagents、approvals、persistent session、workspace mount。
- 凭据中心或通用 secret manager。当前只建立安全配置边界，不实现完整凭据产品。
- 为了适配厂商而把 DeepSeek 专属 request/response 类型加入 Core 或公共 Proto。
- 默认 CI 中调用真实 DeepSeek API。

## 架构决策检查点：Provider 路由

当前 Task Router 硬编码 `ProviderID: "fake"`，而 Project 已持久化 `HarnessBinding`。在改代码前，先在新任务记录中写一小段路由设计。

必须保持这些不变量：

- Project-scoped Task 的 provider 必须从当前 owner 可访问的 Project 绑定解析，不能信任客户端伪造的任意 provider。
- provider 选择在 Submit 时固化到 Task；执行中 Project 绑定变化不得悄悄改变已经排队的任务。
- Agent 模块不得直接查询 Project 表，也不得导入 Project 的 PostgreSQL adapter。
- 不得让 Harness 读取 Core 数据库。
- 允许由一个中立的 Core orchestration/composition 层同时依赖两个 application port；模块本身仍保持单向边界。
- global task 必须有显式、可配置且安全的默认 provider；保持现有 Fake 开发链路可用。
- 现有幂等提交必须保持：同一 idempotency key 重试不能因 Project 绑定后来变化而创建不同 Task。

优先复用已有 `Project.HarnessBinding`，不要为了省事直接把 vendor 字段塞进 `AgentTaskInput`。如果干净实现确实需要改变 v1 Proto、进程边界或模块所有权，先写 ADR 草案，并把具体冲突和两种可选方案告诉用户，得到确认后再实施。

## DeepSeek Adapter 要求

### 配置与安全

- 默认 `enabled=false`。
- API Key 只允许从环境变量或受限 secret 文件读取；仓库配置、数据库、测试 fixture、日志和错误信息中不得出现真实 Key。
- 推荐环境变量命名保持清晰一致，例如：
  - `WORKOS_DEEPSEEK_ENABLED`
  - `DEEPSEEK_API_KEY`
  - `WORKOS_DEEPSEEK_BASE_URL`
  - `WORKOS_DEEPSEEK_MODEL`
  - `WORKOS_DEEPSEEK_TIMEOUT`
- Base URL 必须解析和校验；生产配置要求 HTTPS。为了 `httptest`，只允许 development/test 下的 loopback HTTP。
- 使用带 timeout 的注入式 `http.Client` 或 transport，复用现有 OpenTelemetry HTTP client 约定。
- 不要记录完整 prompt、用户内容、Authorization header、原始响应体或 API Key。
- 不要因为设置了 Key 就隐式启用 Provider。
- 缺 Key 时不要让整个 `harness-host` 崩溃；Provider/capability 应明确报告 unavailable，并给出不含 secret 的原因。

### Provider 描述

- 稳定 ID 使用 `deepseek`，不要包含模型名或版本号。
- `Describe()` 必须返回真实 capability；本阶段最多声明 streaming 和 usage reporting。
- 未实现的 tools、MCP、approvals、sessions、resume 等必须是 `false`。
- `adapter_version` 使用仓库一致的明确版本，不得填动态时间或空值。

### 输入映射

- `goal` 是主要 user message。
- `role` 的映射必须确定、可测试并在 adapter 文档中说明；不要把任意 role 字符串当成高权限隐藏指令。
- 当前 Harness 没有读取 Project workspace、Artifact 或 ContextRef 的 port。不要跨边界直接查库或文件系统。
- 对 `context_refs`、requested tools/capabilities、structured artifact 等当前不支持的输入，必须选择可预测策略并测试：明确拒绝，或只处理协议允许且不会产生虚假能力的子集。不得静默声称已经使用这些上下文。
- 将 `AgentBudget.max_tokens` 和 `max_runtime_seconds` 映射到官方 API 支持的限制，并同时设置本地上限；无效或过大的值必须被 clamp/拒绝。
- 不要根据会变化的价格表硬编码 `cost_decimal`。可以记录官方响应给出的 token usage、模型名，成本留空。

### Canonical event 顺序

成功运行至少满足：

1. 第一条事件是 `RunStarted`，包含 UUIDv7 run ID 和 `provider_id="deepseek"`。
2. 每个有效流式文本增量映射为 `AssistantDelta`。
3. 流结束后可发一个聚合后的 `AssistantMessage`，但必须设置有界内存/响应大小，防止无限累积。
4. 官方响应提供 usage 时发 `UsageRecorded`。
5. 最后一条且唯一 terminal event 是 `RunCompleted`。

失败和取消必须满足：

- 认证/请求错误、限流、5xx、畸形 SSE、响应过大、timeout、主动 cancellation 都有确定行为。
- 429、可恢复 5xx 和临时网络错误与永久配置/认证错误需要区分 retryable 语义。
- 如现有 `Provider` error 接口无法安全表达 retryable，请先提出最小的 vendor-neutral typed error 设计；不能用字符串匹配厂商错误。
- `emit` 返回错误后立即停止读取和请求。
- 不得在 terminal event 后继续发事件。
- Provider 不能设置 Core-owned 的 `AgentEvent.id/task_id/sequence/occurred_at`。
- 上下文取消必须中止 HTTP request，不能遗留 goroutine 或连接。

## 测试要求

至少覆盖以下无密钥测试：

- 配置默认关闭、显式启用、缺 Key、非法 Base URL、timeout 校验。
- `Describe()` 的 ID、health、unavailable reason 和 capability 准确。
- 使用 `httptest.Server` 模拟官方流式响应：多 chunk、空 delta、usage、完成标记。
- 成功事件严格排序，第一条 RunStarted，最后且仅一个 terminal event。
- API Key 只进入 Authorization header，且不会出现在错误或日志中。
- 401/403 永久失败；429、临时网络错误和适用的 5xx 可重试。
- 畸形 JSON/SSE、未知 event、提前 EOF、超大单行/响应、非预期 content type。
- context cancellation、deadline 和 `emit` 失败会及时关闭请求。
- Project 绑定解析：owner scope、缺 Project、无 binding、绑定 deepseek、绑定变化后的幂等重试。
- task 的 `provider_id` 在 lease、事件和重启恢复后保持不变。
- 现有 Fake、Generic CLI、Gateway 和浏览器 E2E 不回归。

可以增加一个只在显式环境变量开启时运行的 live smoke test，但必须满足：

- 默认跳过。
- 不输出 prompt、response 全文或 Key。
- 设置严格 token/runtime/cost 上限。
- CI 不要求 secret。

## 配置与部署资产

同步检查并按需更新：

- `deploy/config/dev.yaml`：只能放非 secret 默认值，DeepSeek 默认关闭。
- `deploy/systemd/harness-host.env.example`：只放变量名/空占位和安全说明。
- `compose.yaml`：不得硬编码 Key；如透传环境变量，缺失时仍能安全启动并报告 unavailable。
- `README.md` 或 adapter 附近的 README：说明如何启用、如何运行 fixture 测试和可选 live smoke。
- `docs/status.json`：只有取得相应证据后才更新；不要把整个 Harness 或 DeepSeek 标为 verified。
- `docs/tasks/20260823-deepseek-harness.md`：持续记录决策、命令、证据和未决风险。

## 质量与验收命令

完成前至少执行：

```bash
make generate
make check
make test-integration
make test-e2e
```

如果增加专用 fixture/live target，也要执行 fixture target，并把 live target 标记为可选。再次确认生成代码无漂移、`docs/structure.md` 未被格式化或意外修改、仓库没有测试产物/secret/root 所有文件。

最终验收标准：

- 默认无 Key、无网络时，WorkOS 全栈和现有测试照常启动、运行。
- DeepSeek disabled/misconfigured 时 capability 如实报告，不伪装 healthy。
- 本地 fixture 能证明 Project binding → durable Task → DeepSeek adapter → persisted event stream。
- 真实 DeepSeek API 只需 opt-in smoke，不是常规 CI 前提。
- Core、Proto 和数据库没有 DeepSeek 专属 DTO/字段。
- 凭据不入库、不入日志、不入事件。
- 任务记录、README、状态事实源与代码一致。

## 最终交接格式

完成后向用户简洁报告：

1. 实现了什么，重点说明 Provider 路由如何解决。
2. 哪些测试和纵向链路实际通过。
3. 是否运行真实 DeepSeek smoke；若未运行，要明确说“未使用真实 Key”，不能暗示通过。
4. 当前限制与下一步建议。
5. 提供关键文件的可点击路径。

不要自动 commit、push、删除 Docker volume 或扩大到其他 Product Alpha 功能，除非用户明确要求。
