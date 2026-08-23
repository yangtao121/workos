# 下一位智能体 Prompt：Harness Provider Catalog 与 Project Binding UX 纵向切片

> 将本文件的完整内容交给下一位智能体。目标是直接继续实现，不是只输出一份计划。

## 你的角色

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。Foundation 和第一个真实
Provider（DeepSeek Harness Adapter）已经完成；你的任务是在不暴露 Harness 私有执行接口、
不让客户端接触凭据的前提下，把已经可运行的 Provider 能力变成用户可以看见、选择和验证的
Project Harness 体验。

持续推进实现、测试和文档，直到默认无密钥环境与本地 DeepSeek fixture 都可验收。不要只写计划。
只有遇到本文列出的架构决策检查点、需要用户提供新权限，或干净实现确实要求破坏 v1 契约/改变
六进程所有权时，才停下来向用户说明冲突和方案。

## 当前仓库事实

- 六个稳定进程仍是：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 当前能力事实只以 `docs/status.json` 为准，不从聊天内容推断。
- 已完成任务：
  - `docs/tasks/20260823-foundation.md`
  - `docs/tasks/20260823-deepseek-harness.md`
- DeepSeek Adapter 位于 `internal/harness/adapters/deepseek`，状态为 `working`，默认 disabled。
- `make test-deepseek-fixture` 已证明：Project binding → durable provider snapshot → lease → 官方
  DeepSeek Harness runtime → 本地 SSE fixture → persisted ordered events。
- Task 路由位于 `internal/core/orchestration/task_router.go`：
  - 幂等命中先返回既有 Task，不重新解析 Project；
  - 新 Project-scoped Task 从 owner 可访问的 Project binding 解析 provider；
  - global/unbound Project 使用配置的安全默认 provider，当前默认 `fake`；
  - provider 在 Submit 时持久化，Project 后续改绑不改变既有 Task。
- Harness 私有协议位于 `api/proto/workos/harness/v1/harness.proto`：
  - `HarnessHostService.DescribeProviders` 返回 canonical `HarnessProviderInfo`；
  - 同一私有 service 还包含 `ExecuteTask` 与 `CancelRun`，绝不能整体暴露给浏览器。
- Gateway 当前将 `/workos.*` 请求转发给 Core；浏览器不能也不应直接访问 harness-host。
- `Project.HarnessBinding` 已支持 provider、instance policy、profile、credential ref 和 resource
  policy，Project 更新使用 `expected_revision` 做乐观并发控制。
- Desktop 当前只创建无 binding 的 Project，然后提交 Task；没有 Provider Catalog、Provider
  health 展示或 binding 编辑入口。
- `createWorkOSClients` 当前没有公开 Harness Catalog client。
- DeepSeek、Fake、Generic CLI 等 Provider 具体实现只能留在 harness-host；Core 和 Desktop 只认识
  canonical provider ID、health、capabilities 和安全的 unavailable reason。
- 默认开发与 CI 不得访问真实 DeepSeek API，不得要求真实 API Key。
- 国内开发默认继续使用 goproxy.cn、npmmirror、阿里云 Debian/PyPI 镜像，不得随意改回慢源。
- PostgreSQL Docker volume 中含有验收数据；禁止 `docker compose down -v`。

## 凭据安全声明

- 本任务不包含任何真实 DeepSeek Key，也不需要用户提供 Key。
- 不得从聊天历史、shell history、进程环境或本机其他文件寻找曾经粘贴的 Key。
- 不得把 Key 写入 Prompt、任务记录、README、YAML、fixture、数据库、日志、错误或事件。
- Desktop 只显示 capability/health，不提供 Key 输入框，不显示 Key 是否存在的细节或任何片段。
- 本任务不实现 Credential Vault、secret manager 或 live DeepSeek smoke。

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
   - `docs/tasks/20260823-deepseek-harness.md`
   - `api/proto/workos/harness/v1/harness.proto`
   - `api/proto/workos/project/v1/project.proto`
   - `api/proto/workos/agent/v1/agent.proto`
   - `internal/core/orchestration/task_router.go` 及测试
   - `internal/core/project` 的 domain、application、transport 与测试
   - `internal/harness/broker`、`internal/harness/transport` 与 provider tests
   - `internal/gateway`、`sdk/agent-sdk`、`sdk/protocol`
   - `apps/desktop-web`、`clients/agent-center` 与现有 E2E
2. 运行 `git status --short`，保留所有既有改动。当前提交历史中的 DeepSeek 成果不是可删除的
   临时代码。
3. 从 `docs/tasks/TEMPLATE.md` 新建
   `docs/tasks/20260823-harness-catalog-binding-ux.md`，状态设为 `active`，先写范围、协议影响、
   Catalog/Binding 设计和验收项。不要修改两个已 done 的任务来冒充本任务进度。
4. 运行 `make bootstrap`、`make check`、`make test-integration` 和 `make test-e2e` 建立基线。
   基线失败时先记录证据并判断来源，不要盲目重写。
5. 如果修改公共 Proto，先确认这是 additive v1 变更、字段号未复用，再运行 `make generate`。
   破坏性变更、跨进程所有权变化或新的共享数据库依赖必须先写 ADR 草案并征求用户确认。

## 任务目标

完成两条互相衔接、可测试的用户纵向链路：

```text
harness-host private DescribeProviders
  → workos-core vendor-neutral Catalog facade
  → workos-gateway public read-only RPC
  → Desktop Project settings
  → health/capability/provider selection
```

```text
Desktop selects provider for owner-accessible Project
  → revision-safe Project binding update
  → next SubmitTask snapshots selected provider
  → harness-host lease/provider execution
  → Desktop shows ordered canonical events and actual provider_id
```

默认无 Key 场景必须显示 Fake 可用、DeepSeek disabled 或 misconfigured，并继续正常提交 Fake Task。
DeepSeek fixture 场景必须从浏览器完成 provider 选择并证明 Task 实际由 `deepseek` 执行。

## 范围内

- 新增 Core-owned、只读、vendor-neutral 的 Harness Provider Catalog application/ports/adapters。
- Core 通过 private Connect client 调用 harness-host 的 `DescribeProviders`，不读取 Harness 内部状态或
  数据库。
- 新增最小公开 Catalog RPC，供 Gateway/Desktop 查询：
  - provider stable ID；
  - display name；
  - adapter version；
  - canonical capabilities；
  - health；
  - 已净化、有界的 unavailable reason；
  - Core 当前配置的 global/default provider ID。
- 在 Project 设置中显示当前 binding 和 provider catalog，并支持 revision-safe bind/unbind。
- 使用服务端拥有的安全 binding preset，避免 Desktop 硬编码 fixture policy 或伪造 credential ref。
- UI 明确区分 healthy、degraded、unavailable、starting/unknown，以及 Catalog 本身不可达。
- Task UI 显示 Task 快照中的真实 `provider_id`，不能用当前 Project binding 猜测。
- 默认 Fake E2E 与 DeepSeek 无密钥 fixture 浏览器 E2E。
- 更新 SDK、README、部署说明、任务记录与 `docs/status.json`。

## 明确不在范围内

- 真实 DeepSeek API smoke、把任何真实 Key 写入环境文件或测试。
- Credential Vault、通用 secret manager、Key 录入 UI、Key 轮换产品。
- Codex/MCP/其他新 Provider。
- tools、approvals、sessions、resume、workspace mount、subagents 或 structured artifacts。
- App Registry、Artifact、Runtime、Reliability、Indexer、LAN pairing、生产认证。
- 重做 Desktop Window Manager、Dock 或整个视觉系统。
- 在 Core 或公共 Proto 中新增 DeepSeek 专属字段。
- 让 Desktop/Gateway 直接调用 harness-host 的 `ExecuteTask` 或 `CancelRun`。
- 让 Project 查询 Harness 数据库，或让 Harness 查询 Core 数据库。

## 架构设计：Provider Catalog

实现前先在新任务记录中写一小段 Catalog 设计，并保持以下不变量：

- 浏览器只访问 Gateway；Gateway 只把公开 Core service 暴露给浏览器。
- 不得把私有 `HarnessHostService` 原样注册到 Core，因为它同时包含执行和取消接口。
- 推荐在 `workos.harness.v1` 增加一个独立的公开 `HarnessCatalogService`，复用 canonical
  `HarnessProviderInfo`，但使用独立 response 表达 `default_provider_id`。命名可按仓库惯例调整。
- 新服务是 additive v1 契约；不要改变现有 `HarnessHostService` 的语义或字段号。
- Core Catalog 模块遵循 `domain → application → ports ← adapters`。Core 的 private Connect
  adapter 可以依赖生成协议，但 domain 不得依赖 Connect、HTTP、Harness adapter 或厂商类型。
- Core 不得导入 `internal/harness/...`。跨进程调用只使用版本化协议。
- Catalog 失败是可选能力失败：不得拖垮 Core liveness/readiness、Project CRUD、Task lease 或正在
  运行的任务。
- harness-host 不可达时，公开 Catalog RPC 返回确定的 `Unavailable` 和安全消息；不要把地址、原始
  transport error、配置路径或 secret 状态透传给浏览器。
- 不做无语义的永久缓存。若增加短 TTL cache，必须明确 stale/expiry 行为、并发去重、取消语义和
  测试；没有必要时优先直接调用。
- provider 列表顺序必须确定，重复 ID、空 ID、超长字段和非法 health 必须有明确处理。
- Core 的 `default_provider_id` 来自现有配置，不得由 harness-host 或客户端决定。
- 不要把 Catalog 瞬时 health 写入 Project、Task 或数据库；Task 只持久化 Submit 时解析出的稳定
  provider ID。

## 架构设计：Project Binding 写入

这是本任务的架构检查点。现有 `HarnessBinding` 要求显式 `instance_policy` 和
`resource_policy_id`，但 Desktop 不应该硬编码 `foundation`、`fixture-no-tools` 等测试值，也不应
要求普通用户理解这些内部字段。

优先方案：新增一个 Core-owned、vendor-neutral 的 binding orchestration command（可以是
`ProjectService` 的 additive RPC，也可以是职责更清晰的新 Core service）：

```text
project_id + expected_revision + selected provider_id / clear
  → Catalog 校验 provider 是否已知且可选择
  → Core 注入配置拥有的安全 instance/resource policy preset
  → Project application 使用既有 optimistic update
  → 返回更新后的 Project
```

具体命名由现有代码风格决定，但必须满足：

- Project domain 不导入 Harness 模块；需要协调时放在中立 Core orchestration/composition 层。
- Desktop 不提交 `credential_ref`，服务端也不得从 Catalog 推断或回显 credential。
- bind 操作可以依赖当次 Catalog；普通 Project 读取、列表、改名、归档和 clear binding 不得依赖
  harness-host 可用。
- 新绑定只允许选择 Catalog 中 stable ID 已知的 provider。
- healthy provider 可选择；degraded provider可选择但必须显示警告；starting、unavailable、unknown
  默认不可新选。
- 已绑定但后来 unavailable/unknown 的 provider 必须仍可显示和解除，不能在读取时静默清除或
  自动回退。
- 解除 binding 后，新任务使用 Core 的 default provider；已经提交的任务不变。
- `expected_revision` 冲突必须返回现有 conflict 语义，Desktop 刷新 Project 并给出明确提示，不能
  last-write-wins。
- 既有完整 `UpdateProject` 契约保持兼容；不得为本 UI 破坏旧客户端。
- 安全 preset 是非 secret 配置。不要声称当前尚未实现的 cgroup/resource enforcement 已生效；
  文档中如实说明它目前只是 binding policy reference。

如果上述优先方案与现有 v1 所有权发生实际冲突，先在任务记录中写 ADR 草案，并向用户提供：

1. Core orchestration command；
2. Catalog 返回 binding template、Desktop 调用现有 `UpdateProject`；

两种方案的边界和兼容性对比，得到确认后再改变主线。禁止直接在 Desktop 写死 fixture 值来绕过。

## Catalog 与 UI 行为要求

### Provider 列表

- 每个 provider 显示 display name、stable ID、health 和本阶段真实 capability。
- DeepSeek disabled/misconfigured 时必须显示 unavailable 和安全原因，不能显示为 healthy。
- Fake 必须继续可用，并可识别为 Core 当前默认 provider。
- 未实现 capability 显示为 unavailable/false，不得根据 provider 名称猜测。
- Catalog 不可达与“单个 provider unavailable”是两个不同状态。
- 不展示 adapter 配置路径、Base URL、模型 prompt、原始异常或 Key 片段。

### Project 设置

- 在现有 Desktop 视觉体系中增加最小设置入口，不重做整个桌面。
- 清楚显示：Use Global Default、当前具体 provider、当前 binding 不可用/未知。
- binding 保存期间禁用重复提交；成功后使用服务端返回的 Project revision 更新本地状态。
- revision conflict 时刷新并告知用户设置已变化。
- 不得因为切换 Project、刷新页面或 Catalog 短暂失败而静默覆盖 binding。
- 表单具备可访问 label、键盘操作和确定的 loading/error 状态。

### Task 反馈

- 提交后显示 `AgentTask.provider_id` 快照。
- `RunStarted.provider_id` 与 Task snapshot 不一致时沿用现有服务端拒绝逻辑，UI 不掩盖错误。
- Project 改绑只影响之后创建的新 Task；正在运行、排队和幂等重试的 Task 保持原 provider。

## 安全与错误边界

- Catalog/Binding 日志不得包含用户 prompt、API Key、Authorization header 或 provider 原始错误。
- 所有字符串错误必须净化并设置合理长度上限，防止把上游响应当成 UI 内容。
- Catalog RPC 必须传播 context cancellation/deadline；不得遗留请求或 goroutine。
- Core 到 harness-host 的 client 使用现有 timeout、telemetry 和 trace propagation 约定。
- 不得因为 provider unavailable 就让整个 Core 或 Gateway readiness 失败。
- UI 不得根据 `unavailable_reason` 字符串做业务判断，只使用 canonical health。
- 禁止用字符串匹配推断 retryable、认证状态或 provider 类型。
- 不新增 credential 数据库列、不把 secret 放进 Project binding。

## 测试要求

至少覆盖以下测试，全部不需要真实 Key：

### Core Catalog 单元/传输测试

- provider 字段与 capability 完整映射，顺序确定。
- Core default provider ID 正确返回。
- disabled、misconfigured、healthy、degraded、starting/unavailable 状态保持真实。
- harness-host unavailable、deadline、cancellation 映射为安全的 Catalog 错误。
- 原始 transport error、地址和类似 secret 的内容不会进入响应或日志。
- 重复/空 provider ID 与过长字段有确定处理。
- Catalog 失败不影响 Project service 和 Task Router。
- 公开 Catalog service 无 ExecuteTask/CancelRun 方法，Gateway 不暴露 private HarnessHost service。

### Binding orchestration 测试

- owner scope，不能修改其他 owner 的 Project。
- Project 不存在、已归档、revision conflict。
- bind healthy、bind degraded、拒绝 starting/unavailable/unknown。
- clear binding 即使 harness-host unavailable 也能完成。
- 服务端注入安全 preset，客户端无法写 credential ref。
- 已绑定 provider 后来 unavailable/unknown 时仍可读取、显示和 clear。
- 改绑前后的新 Task 分别固化各自 provider。
- 相同 Task idempotency key 重试继续返回原 Task/provider。

### Desktop 单元/组件测试

- Catalog loading、empty、unavailable 和 retry。
- provider health/capability 展示准确。
- 不可用 provider 不可新选，degraded 有警告。
- current unknown/unavailable binding 不被静默清除。
- bind、unbind、revision conflict、切换 Project。
- Task 显示服务端返回的 provider snapshot。
- DOM、错误文本或测试快照中不出现 credential 字段或 Key。

### 纵向/E2E

- 默认栈：无 Key、无网络；Fake 可用、DeepSeek unavailable；创建 Project、保持 global default、提交
  Task，Fake 链路通过。
- DeepSeek fixture 栈：Catalog 显示 DeepSeek healthy；浏览器选择 DeepSeek；Project revision 更新；
  提交 Task；Task 和 `RunStarted` 均为 `deepseek`；流式结果与 usage 按序持久化并显示。
- 改绑后新 Task 使用新 provider，旧 Task/event stream 不变。
- Core/Harness 重启后 Project binding、Task provider snapshot 与事件仍保持。
- 现有 Fake、Generic CLI、Gateway、DeepSeek fixture 和 foundation browser E2E 不回归。

如新增专用浏览器 fixture target，必须默认使用假的 fixture credential、本地 loopback API，并在结束
时只停止本 target 创建的容器；禁止删除 volume。

## 配置与部署资产

按需同步更新：

- `deploy/config/dev.yaml`：只加入非 secret Catalog/binding preset 默认值。
- `compose.yaml`：Core 到 harness-host 的 private 地址使用现有 service discovery；不得透传 Key 给
  Core/Gateway/Desktop。
- `deploy/systemd/*.env.example`：如需新增变量，只写安全默认/空占位与说明。
- `sdk/agent-sdk`、`sdk/protocol`：生成并公开最小 Catalog/Binding client。
- `README.md`：说明 provider 状态、Project binding 和无密钥 fixture 测试方式。
- `docs/status.json`：只有获得测试证据后才更新；Catalog/Binding UX 没有真实 E2E 时最高只能
  `scaffolded`。
- `docs/tasks/20260823-harness-catalog-binding-ux.md`：持续记录协议选择、命令、证据和未决风险。

不要修改自动生成的 README 状态区块、`gen/` 或 `sdk/protocol/src/gen`；只能通过生成工具更新。

## 质量与验收命令

完成前至少执行：

```bash
make generate
make check
make test-integration
make test-deepseek-fixture
make test-e2e
```

如果新增专用 Catalog/Binding 浏览器 target，也必须执行并记录。最终再次确认：

- `make generate` 二次执行无漂移；
- `docs/structure.md` 未被格式化或意外修改；
- `git diff --check` 通过；
- 仓库中没有真实 secret、测试产物或 root 所有文件；
- 默认无 Key、无网络时全栈仍可启动并执行 Fake Task；
- Gateway/Core 没有公开 Harness private execution/cancel 方法；
- Core、Project Proto 和数据库没有 DeepSeek 专属 DTO/字段；
- 未执行 `docker compose down -v`。

## 最终验收标准

- 用户可以从 Desktop 查看 canonical provider catalog 和真实 health/capabilities。
- 用户可以对自己的 Project revision-safe 地 bind/unbind provider。
- DeepSeek 不可用时 UI 如实报告；默认 Fake 链路不受影响。
- 本地 fixture 证明浏览器选择 DeepSeek 后，Task 实际固化并执行 `provider_id="deepseek"`。
- Project 改绑不改变既有 Task，幂等和重启语义不回归。
- 浏览器、Gateway 和 Core 均不接触真实 provider credential。
- private Harness ExecuteTask/CancelRun 没有变成公开浏览器 API。
- 文档、任务记录、生成 SDK 和 `docs/status.json` 与实际证据一致。

## 最终交接格式

完成后向用户简洁报告：

1. Catalog 公共边界如何实现，以及如何避免暴露 Harness 私有执行接口。
2. Project binding 的服务端 preset、owner scope 和 revision 语义。
3. 默认 Fake 与 DeepSeek fixture 哪些纵向/E2E 实际通过。
4. 明确说明“未使用真实 Key，未运行真实 DeepSeek smoke”。
5. 当前限制、下一步建议和关键文件的可点击路径。

不要自动 commit、push、删除 Docker volume、读取聊天中的 Key，或扩大到 Credential Vault、App
Registry、Artifact、Reliability 等其他 Product Alpha 功能，除非用户明确要求。
