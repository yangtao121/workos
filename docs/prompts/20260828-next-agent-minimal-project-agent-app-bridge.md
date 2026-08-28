# 下一位智能体 Prompt：Minimal Project Agent App Bridge 纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、视觉证据、文档和聚焦提交，
> 不是只输出计划。

## 你的角色

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。Minimal Web Bundle Surface 已完成
审核并快进合并；你的任务是实现下一条严格限定的纵向切片：
**Web Bundle Surface 的 Project-scoped Agent App Bridge、显式 capability grant 和短期 bridge token**。

本任务要让一个安装在 Project 中的不可信 Web Bundle App，在用户明确批准最小权限后，通过版本化
`MessageChannel` 调用 WorkOS 的 canonical Agent Task Router，并从 Fake Harness 的持久事件流读取结果。
App 只能请求所属 Project 的任务，不得获得 Provider 类型、Provider API Key、WorkOS cookie、通用
Connect client 或任意其他系统能力。

持续推进直到实现、测试、UI 前后截图、文档、状态和聚焦提交全部完成。只有遇到以下情形才停止并向用户
报告证据与选项：必须破坏已有 v1 契约、改变六进程所有权、修改已执行 migration、削弱 iframe/CSP
隔离，或必须引入尚未批准的生产信任根。

## 为什么下一步是 App Bridge

当前已经具备：

- canonical App manifest、集中 permission vocabulary、immutable Registry version/digest；
- Project-owned installation、pinned version/digest 和稳定 UUIDv7 `app_instance_id`；
- immutable Web Bundle Artifact、Core 私有 installed-instance resolver；
- runtime-host 持久 Surface session、Gateway 路由、逐请求 Core revalidation；
- Desktop Window Manager 中的 opaque-origin sandboxed iframe；
- canonical Agent Task Protocol、Project Harness binding、Task Router、Harness Broker 和可恢复事件流；
- Fake Harness 与 keyless DeepSeek fixture 门禁。

当前仍缺：

- manifest `permissions` 仍只是 requested permissions，没有用户批准的 grant 事实；
- `SurfaceSession.bridge_token` 始终为空，Surface capability flags 如实为 false；
- Desktop 不建立 MessageChannel，iframe 不会收到 WorkOS App Bridge；
- `@workos/app-sdk` 只有接口草图，能力名称与 Registry 的 canonical vocabulary 尚未完全对齐；
- Agent Task 只有用户 owner 范围，没有 App principal/provenance，不能安全地让 App watch 任意 task ID；
- App 调用没有独立、request-bound 的 durable idempotency namespace；
- 没有 server-side capability enforcement，客户端检查不能视为授权。

依赖顺序固定为：

```text
已完成：manifest requested permissions
  → 已完成：immutable app version → installed app instance
  → 本任务：explicit installation grant snapshot
             → active Surface + short-lived capability token
             → versioned MessageChannel handshake
             → server-enforced project Agent run/watch
             → existing Task Router → Harness Broker → persisted events
  → 后续：grant mutation/revocation UX、approval/budget、其他 App Bridge capability
  → 后续：Credential Vault、Web Service/container runner、Reliability enforcement
```

不要把 manifest request 直接当 grant，不要让 Desktop 直接代替服务端做授权，不要让 iframe 调用公开
`AgentTaskService`，也不要把固定成功消息或浏览器内 fake event 冒充 Agent Task 纵向链路。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时本地 `main` 为 `fc18ae0`，工作树干净，领先 `origin/main` 1 个提交；执行时必须重新
  检查，不能把哈希或 ahead 数量当作永久事实。
- `docs/status.json` 中 App Registry、Project App Installation、Agent Task Router、Harness Broker、
  Desktop Shell 为 working；Runtime / Surface 为 working 但证据严格限定 Web Bundle；Artifact 仍是只
  支持 Web Bundle subtype 的 scaffolded。
- `api/proto/workos/surface/v1/surface.proto` 已声明 `bridge_token`，但 transport 当前保持空；
  `resize`、`clipboard`、`file_picker` 仍为 false。
- iframe 与服务端资产响应当前共同强制 sandbox：iframe 只有 `allow-scripts`，CSP 只有
  `sandbox allow-scripts`，没有 `allow-same-origin`、forms、popups、storage 或 network；
  `connect-src 'none'` 必须保留。
- opaque-origin iframe 的 `event.origin` 是 `"null"`，不能用 origin 字符串作为身份。Desktop 必须以
  精确 `iframe.contentWindow`、每次 load 的新 channel/nonce 和被转移的 `MessagePort` 建立信任。
- canonical manifest permission vocabulary 当前集中在
  `internal/core/appregistry/domain/manifest.go`：`agent.task.run`、`agent.event.watch`、
  `artifact.read`、`artifact.write`、`knowledge.read`、`project.read`。Registry 只验证 request，不 mint grant。
- `sdk/app-sdk` 仍含 `agent.project.invoke` 等非 canonical 草图名称；不得再增加第三套同义能力名。
- `clients/app-host` 只有 `APP_BRIDGE_VERSION` 与 session 草图；`sdk/surface-sdk` 只有 envelope 草图；
  它们尚不构成 working bridge。
- 当前 public `AgentTaskService` 以 owner 为范围，`(owner_user_id, idempotency_key)` 全局唯一；它没有
  App installation provenance，也没有 same-key/different-request digest 裁决。不得直接复用为 App
  authority 后只在前端过滤。
- `001` 至 `007` migration 已在持久验收 volume 执行并受 checksum 保护，禁止修改。新增 migration
  从 `008` 开始，每个 migration/table 必须有单一明确 owner。
- 当前验收 volume 含用户已有数据和 6 个历史 migration scratch database；不得删除 volume、TRUNCATE、
  broad DELETE、wildcard DROP 或顺手清理历史数据。
- `docs/ui/README.md` 已成为 UI 视觉证据规范；目前还没有 `desktop-web` 基线。本任务会改变 App Library
  和 App Surface，因此必须建立第一组 `before/after/current` 截图。

## 凭据与最高优先级安全边界

- 本任务不需要真实 DeepSeek、OpenAI、GitHub 或其他 Provider Key。
- 不得使用、保存、转述、验证或尝试恢复聊天中曾出现的真实 Key；不得从 shell history、环境变量、
  本机文件或聊天历史搜集凭据。
- DeepSeek 回归只使用仓库已有的本地 fixture 假凭据，禁止访问真实 Provider 网络。
- iframe 永远不得获得 Provider 名称选择权、Provider raw credential、WorkOS cookie、device credential、
  bridge bearer token、生成的 Connect client、Gateway identity header 或 runtime/core 私有地址。
- bridge token 不得进入 URL、query、fragment、cookie、localStorage、sessionStorage、window-manager
  serializable state、DOM attribute、`postMessage`/`MessagePort` payload、日志、错误、trace、截图或任务记录。
- bridge token 只能保留在可信 Desktop/AppHost 内存，并由可信 host 调 public Bridge RPC 时放入专用
  metadata/header；iframe 只持有受控 `MessagePort`，该 port 本身是浏览器内 capability handle。
- token 必须有真实验证方、足够熵、严格 TTL、owner/device/session 绑定和撤销边界；禁止生成装饰性随机串。
- capability 名称不是 secret，但错误不得泄露 foreign owner/project/installation/task 是否存在。
- goal、Agent event 和 bundle 内容属于用户内容：不得写入日志或未净化错误，不得把全文放入任务记录。
- 保持 iframe opaque origin、`connect-src 'none'` 和现有 CSP/HTTP headers；本任务不为 Bridge 放宽
  same-origin、storage 或 network。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`CONTRIBUTING.md`、`docs/ui/README.md`；
   - `docs/structure.md` 中 App、App Agent API、Credential Vault、Surface、App Bridge、Desktop、Gateway、
     数据存储和第一版产品边界；
   - `docs/architecture/implementation.md`、`docs/decisions/0001-foundation-boundaries.md`；
   - `docs/status.json`；
   - `docs/tasks/20260823-app-manifest-registry.md`；
   - `docs/tasks/20260825-project-app-installation.md`；
   - `docs/tasks/20260825-minimal-web-bundle-surface.md`，尤其最终交接和未决风险；
   - 上述任务对应的实现 Prompt、审核 Prompt 与 merge-readiness 修复 Prompt；
   - `api/proto/workos/app/v1/app.proto`、`installation.proto`；
   - `api/proto/workos/agent/v1/agent.proto`；
   - `api/proto/workos/surface/v1/surface.proto`、`surface_resolver.proto`；
   - `schemas/workos-app-manifest-v1.schema.json`；
   - `internal/core/appregistry`、`internal/core/project`、`internal/core/agent`、
     `internal/core/orchestration` 及测试；
   - `internal/runtime/surface`、`internal/gateway`、`cmd/workos-core`、`cmd/runtime-host`；
   - `sdk/protocol`、`sdk/app-sdk`、`sdk/surface-sdk`、`sdk/agent-sdk`；
   - `clients/app-host`、`clients/window-manager`、`apps/desktop-web`；
   - migrations 001–007、`sqlc.yaml`、现有 integration/restart/Playwright E2E。
2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -10
   git branch -vv
   git diff --check
   ```

   保留所有既有改动；不得 reset、rebase、checkout 覆盖用户文件或删除现有数据。

3. 从执行时的本地 `main` 创建独立 branch/worktree，建议
   `feat/minimal-project-agent-app-bridge`。禁止直接在 `main` 实现，不要 merge 或 push。
4. 从 `docs/tasks/TEMPLATE.md` 创建
   `docs/tasks/20260828-minimal-project-agent-app-bridge.md`，状态先设为 active，写清：
   - requested permission、installation grant、effective bridge capability 三者区别；
   - Project/Core、Agent/Core、Runtime Surface 各自事实与 table owner；
   - public Runtime Bridge RPC、private Core App Agent RPC 和 Gateway allowlist；
   - token 生成/存储或签名/轮换/TTL/restart/revocation 的精确语义；
   - MessageChannel handshake、消息上限、App task provenance/idempotency；
   - UI 视觉证据、测试、明确不在范围内和失败映射。
5. 在改 Proto/migration 前，新增一份聚焦 ADR，例如
   `docs/decisions/0002-app-bridge-trust-boundary.md`，至少决定：
   - 为什么 opaque-origin iframe 只通过 exact-window MessageChannel 与可信 parent 通信；
   - token 是否持久、哈希、签名或进程重启轮换，以及 Create replay/Runtime restart 的行为；
   - token 为什么留在 parent、如何绑定 owner/device/session/expiry；
   - grant 的唯一事实源与不可变安装级 snapshot；
   - Runtime 如何调用 Core、Core 如何再次验证 active installation/grant；
   - App task provenance/idempotency 的持久模型；
   - 当前单 runtime 实例限制（如存在）及未来多实例方案。

   ADR 不能用“以后再验证”掩盖本任务 token 没有验证方。若安全模型无法闭合，停止实现并报告选项。

6. 建立 UI `before` 基线。由于当前没有现成截图，必须从未修改的任务基准提交运行 fixture UI，按
   `docs/ui/README.md` 采集受影响界面到：

   ```text
   docs/ui/desktop-web/changes/20260828-minimal-project-agent-app-bridge/before/
   ```

   使用 Chromium、`1440x900`、`deviceScaleFactor: 1`、确定性 fixture；不得包含真实数据或凭据。

7. 记录基线并运行：

   ```sh
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败必须保留证据并判断归属；禁止通过删 volume、放宽断言、跳过服务、删除测试或固定成功响应绕过。

## 目标用户链路

完成并证明以下真实链路：

```text
Register Web Bundle App
  → manifest requests agent.task.run + agent.event.watch
  → Registry keeps them as requests only

Desktop App Library
  → shows exact immutable app version and requested permissions
  → user explicitly selects/approves a subset (default none)
  → InstallApp pins version/digest + sorted granted_permissions atomically
  → Project-owned installation is the durable grant authority

Desktop Open
  → CreateSurface resolves active installation + exact manifest + effective grants
  → runtime-host creates owner/device-bound session and short-lived bridge token
  → token remains only in trusted Desktop/AppHost memory
  → iframe loads with opaque origin and connect-src 'none'

AppHost handshake
  → parent creates a fresh MessageChannel for the exact iframe.contentWindow
  → parent sends versioned hello + one-time nonce + transferred port
  → iframe SDK validates source === parent and acknowledges nonce on the port
  → parent exposes only implemented AND granted method names

iframe: bridge.agent.run({ idempotencyKey, role, goal })
  → MessagePort bounded request (no token)
  → trusted AppHost adds surface ID + token metadata
  → Gateway public AppBridgeService → runtime-host
  → Runtime validates token + owner/device + active session + method capability
  → private Core AppAgentService
  → Core re-resolves active same-owner Project installation and exact grant
  → Core forces target_scope = this Project and records App principal/provenance
  → existing Task Router snapshots Project/default Harness provider
  → Harness Broker/Fake Harness executes and persists canonical Agent events

iframe: bridge.agent.stream(taskId, afterSequence)
  → same token/session/capability validation
  → Core proves task belongs to the same app installation and Project
  → persisted events stream through Runtime/Desktop MessagePort
  → iframe renders a unique terminal result

Close / expiry / uninstall / archive / wrong device / token tamper
  → Bridge call fails closed with sanitized error
  → closing a Surface stops its bridge but does not implicitly cancel a durable Agent task
```

E2E 必须使用真实 Gateway、Core、runtime-host、Task Router、Harness Broker 和 Fake Harness。不得让 iframe
直接调用 Gateway，不得用 Desktop 内存生成假 task/event，也不得用真实 Provider Key。

## 固定进程与模块边界

采用以下边界，除非仓库证据证明必须先修改 ADR：

- `workos-core App Registry` 继续只拥有 immutable requested permissions；它不 mint grant/token。
- `workos-core Project Installation` 拥有安装级 `granted_permissions` snapshot。grant 必须是该 exact
  pinned manifest requested permissions 的显式子集。
- `workos-core Agent` 拥有 Agent task、App task provenance 和 App task idempotency mapping；不得让
  Runtime 写 Core task 表。
- `internal/core/orchestration` 组合 active Installation、grant 与 Task Router，向 runtime-host 暴露
  private、版本化、canonical App Agent RPC；不把 Project/Registry internal package暴露给 Runtime。
- `runtime-host Surface Broker` 拥有 session/token 生命周期、public AppBridge transport 和 Core client
  adapter。它不查询 Core schema、不自行选择 Provider、不保存 Provider credential。
- Gateway 只把 public `SurfaceService`、public `AppBridgeService` 和 `/surfaces/` 路由到 runtime-host；
  private Core resolver/AppAgent RPC、HarnessHost、Workload host RPC 继续 public 404。
- Desktop/AppHost 是可信协议代理但不是授权权威；所有请求必须在 Runtime 和 Core 服务端再次验证。
- iframe App 只依赖 `@workos/app-sdk`/生成协议与 MessagePort；不得 import Desktop、agent-sdk Connect
  clients、window-manager internals 或任何 Provider SDK。

依赖方向必须保持：

```text
core/project:       domain → application → ports ← postgres/transport
core/agent:         domain → application → ports ← postgres/transport
core orchestration: neutral ports/adapters only
runtime/surface:    domain → application → ports ← postgres/core-client/transport
desktop app-host:   protocol adapter; no authorization truth
```

Domain 禁止导入 pgx、SQLC、Connect、Proto、HTTP、React、文件系统、Provider SDK 或其他模块 adapter。
跨模块、跨进程只走中立 port/RPC；不得跨模块 SQL、共享 mutable entity 或引用对方 internal adapter。

## 协议优先与单一事实源

先完成所有 additive `api/proto` 变更，立即运行 `make generate`，确认 Go/TypeScript 生成物，再实现
producer/consumer。v1 字段号不得复用；删除字段/enum 必须 reserved；不得手写第二套同义 DTO。

至少需要覆盖以下 canonical contract；具体文件拆分可依据 ADR，但 public/private 必须清晰：

1. `AppInstallation` 返回 immutable、排序后的 `granted_permissions`。
2. `InstallAppRequest` 允许客户端提交显式 `granted_permissions`；老客户端省略字段仍表示空 grant。
3. Core private Surface resolver 返回该 active installation 的 grant snapshot，供 Runtime 计算 effective
   Bridge capability；任何 manifest/install digest 漂移都是净化 Internal，不得降级为无权限后继续。
4. public Runtime-owned `AppBridgeService` 至少提供：
   - project-scoped Agent task submit；
   - same-App task event watch/resume。
5. private Core-owned `AppAgentService`（名称可调整）只供 runtime-host 调用，负责 authoritative grant、
   Project scope、task provenance 和 Task Router 组合。
6. `SurfaceSession` 或单独 bridge descriptor 返回：短期 token、明确 expiry、只包含 working 且 granted 的
   capability IDs。未实现的 `resize`/`clipboard`/`file_picker` 保持 false。
7. MessageChannel hello/ack/request/response/event/error 使用明确版本和有界 envelope；业务 payload 复用
   Proto 生成类型或从单一 Proto 契约投影，不在 `app-sdk`、`surface-sdk`、`app-host` 各写一套字段。

public Bridge request body不得接受或信任 `owner_user_id`、`device_id`、`project_id`、`app_instance_id`、
Provider ID 或 capability list；它们全部从 Gateway identity、validated token 与 stored session派生。

### 最小 App Agent 输入

本任务只支持 project-scoped run/watch。为避免把已有宽泛 `AgentTaskInput` 直接暴露给不可信 iframe，Bridge
输入应限制为：

```text
Run:
  client idempotency key
  bounded role (optional)
  bounded non-empty goal

Watch:
  task id
  after_sequence >= 0
```

Core 必须构造 canonical `AgentTaskInput`：`target_scope.project_id` 强制等于 installation Project；
`requested_capabilities`、`output_artifact_types`、`parent_task_id`、`incident_id`、global scope 和未实现 budget
字段不得由 iframe smuggle。本任务不支持 context refs；收到额外/未知方法或越界 payload 必须 fail closed。

建议边界：idempotency key 1..128 rune、role ≤64 rune、goal 1..16 KiB UTF-8、MessageChannel 单消息
≤64 KiB、单 Surface 同时在途请求 ≤32。可以收紧，但必须在 Proto/SDK/服务端一致并有边界测试。

## 显式 grant 语义

本任务固定采用**安装时不可变 grant snapshot**，不新增可变 SetGrant API：

- manifest `permissions` 永远只是 request；Registry 的语义和状态文档不得改成“已授权”。
- Desktop 安装前显示 exact `WorkOSApp.version` 的 requested permissions，checkbox 默认全部未选。
- Desktop 必须提交明确 version，避免用户看到一个 version 的权限、服务端却因 current 漂移安装另一个版本。
- Core 通过中立 AppCatalog port 取得 pinned identity + exact requested permission set；Project module 不读取
  Registry SQL/canonical manifest。
- granted set 必须 canonical 排序、无重复、且严格为 requested set 子集；空 grant 合法。
- malformed/duplicate grant 为 `InvalidArgument`；不在 exact requested set 中的 capability fail closed，
  返回净化的 `PermissionDenied` 或 ADR 中定义的稳定安全错误。
- `Installation` 持久保存 grant；`ListInstalledApps`、restart、idempotent replay 都返回同一 snapshot。
- 同 app + 同 version + 同 grant 才允许原有确定 no-op；同 version 但 grant 不同不得静默改变权限。
- 本阶段更改 grant 必须 uninstall 后重新 install；不要暗中增加 mutable grant update。
- uninstall/archive 立即使新的 Bridge authorization 失败；已运行 Agent task 不被隐式取消。

安装幂等 digest 必须覆盖排序后的 grant，但要保护历史 replay：对于空 grant 的老请求，必须保持旧版本
digest/裁决兼容，不能让升级后的服务把已消费 key 错判为不同请求。非空 grant 使用明确版本化 canonical
digest；增加真实 PostgreSQL replay/backfill 回归。

`agent.task.run` 和 `agent.event.watch` 是两个独立 grant：

- 只有 run → 可以创建 task，但不能通过 Bridge watch events；
- 只有 watch → 不能创建 task，也不能 watch 其他 App/用户 task；
- 两者都有 → 可以运行并恢复读取同一 installation 发起的 task；
- 未授权或未实现 capability 不出现在 effective Bridge capability list。

其他已知 permission（artifact/project/knowledge）可以作为安装 grant 事实保存，但本任务没有对应 Bridge
executor，绝不能因“已 grant”就暴露方法或在状态中宣称 working。

## App task provenance 与幂等

现有 user-scoped `(owner, idempotency_key)` 不能直接作为 App 安全模型。本任务必须增加 Core Agent-owned
持久事实，使每个 Bridge task 能回答：谁、哪个 Project、哪个 app installation、哪个 canonical request
创建了它。

要求：

- App client key namespace 至少为 `(owner, app_instance_id, client_idempotency_key)`；两个 App 使用相同
  client key 不冲突。
- mapping 持久化 canonical request digest、task ID、Project/app principal 和首次结果；same key/same
  request replay exact task，same key/different request 为 `Aborted`。
- task、requested event/outbox 和 App mapping 必须在一个 Core-owned PostgreSQL transaction 内提交；
  并发 loser 不得留下 orphan task/event/outbox。
- replay 先返回首次 provider snapshot，不因 Project Harness binding 后续变化而漂移；但每次 Bridge 调用
  仍先验证 active installation 和当前 grant。
- watch 必须同时验证 owner、Project、app installation provenance；拥有另一个 task ID 不能成为读取能力。
- 用户自己的 Agent Center 可以继续按 owner 查看 App 发起的 task，但 App 只能读取自己的 task。
- 不得把 raw token、goal 全文或 event 内容写入 mapping、日志或错误之外的非必要位置；task 自己的
  canonical input/event persistence 保持现有产品语义。

如果采用新增 mapping table而不是改变 `agent_tasks` principal 字段，table owner、唯一约束、复合 owner
绑定和 transaction 必须同样明确。禁止用 task ID 前缀、拼接 client key 或 payload 隐藏字段冒充 provenance。

## bridge token 与 Runtime session

token 设计必须在 ADR 中闭合，并满足：

- 使用 `crypto/rand` 或等价 CSPRNG；禁止 `math/rand`、时间戳、UUID 或可预测 session ID 充当 bearer secret。
- token 至少 256 bit entropy，使用 canonical base64url/等价无歧义编码并有严格长度上限。
- 验证使用恒定时间比较或经过审计的 MAC/token primitive；不自行发明可延长/可篡改格式。
- token 绑定 owner、device、surface session、Project、app instance、issued/expiry；expiry 不得晚于 Surface
  session expiry。
- Runtime 必须先验证 Gateway 注入 identity，再验证 token/session；知道 token但来自错误 device 仍拒绝。
- 每次 run/watch 都检查 active、未关闭、未过期 session，并通过 private Core 再验证 active installation
  与 grant；token claim/snapshot 不能成为长期授权真相。
- Close、expiry、uninstall、archive、token tamper、错误 session、错误 owner/device全部 fail closed。
- Runtime restart 后 token 是继续有效还是轮换，必须与 Create replay、Desktop reconnect 行为一致并有
  integration/restart 测试；不得在文档说 durable、测试却只覆盖同进程内存。
- 如果 token at rest，说明存储形态、查询边界和 close/expiry 后处理；如果 token 签名，说明 key 生命周期、
  单/多实例限制和轮换。不得为了本任务偷偷引入生产 Credential Vault 或长期静态仓库密钥。
- token 验证失败统一净化为 `Unauthenticated`/`PermissionDenied` 之一，不返回 token 片段、session 状态、
  SQL、constraint 或 foreign resource 存在性。

`SurfaceSession.bridge_token` 可以承载可信 parent 所需的 bootstrap credential，但必须明确它是 ephemeral
credential 还是 durable session snapshot 的一部分；idempotent `CreateSurface` replay 是否返回相同或轮换
token 必须文档化并测试。不要破坏 session ID、URL、TTL、Close 和 asset revocation 的既有语义。

## MessageChannel 与 App SDK

保持 iframe `sandbox="allow-scripts"`。推荐握手：

1. 每次 iframe load，Desktop/AppHost 关闭旧 port、拒绝旧 pending request，生成一次性 nonce 和新
   `MessageChannel`。
2. parent 只向该 React ref 的精确 `iframe.contentWindow` 发送 versioned hello 并 transfer `port2`；
   opaque origin 需要 `targetOrigin="*"`，因此安全性不能依赖 target/origin 字符串。
3. iframe SDK 只接受 `event.source === window.parent`、正确 version/type、恰好一个 port 的 hello；在 port
   上回传匹配 nonce 的 ack。
4. parent 在超时前只接受一次正确 ack，随后移除全局 message listener；后续 RPC 只走该 MessagePort。
5. iframe navigation/reload、Surface close、window close、Project switch、unmount 都关闭 port、取消 stream、
   使迟到 response inert，并 best-effort CloseSurface；Agent task 本身继续 durable。

要求：

- 不用 `event.origin === "null"` 当授权；任何其他 window/source、旧 nonce、旧 port、错误 version均拒绝。
- request ID、method、payload shape/size、inflight count、timeout、stream cursor 必须有界；未知字段/method
  fail closed。
- 只允许 `agent.run`、`agent.stream` 两个本任务 working method；不要实现 window/project/files/artifacts/
  notifications/theme 的固定成功 stub。
- MessagePort error 只返回稳定 code + 固定安全消息，不传 raw Connect error、stack、SQL 或内部地址。
- stream 的 AsyncIterable 提前结束时只取消该本地/server stream，不取消 durable Agent task。
- AppHost 把 token 加到 server RPC metadata，但绝不把 token放进 hello、ack、response 或 event。
- `@workos/app-sdk` 暴露可实际工作的初始化/API；修正 capability union 为 canonical vocabulary。
- `sdk/surface-sdk`、`clients/app-host`、`sdk/app-sdk` 复用单一协议，不保留互相矛盾的 envelope/version。

为 handshake、wrong source、nonce replay、double ack、reload、timeout、oversize、too many inflight、unknown
method、port close、late response 和 token non-transfer 增加确定性 TypeScript 测试。

## Core App Agent authorization

private Core AppAgent service必须：

- 只信任 runtime-host 转发的 trusted identity context，不信任 public request body中的 owner/device/project；
- owner-scope ResolveActiveInstallation，验证 Project 未归档、installation active；
- 验证 exact pinned version/manifest digest 与 grant snapshot 不漂移；
- method 级检查 canonical grant；
- 强制 Project target scope，不允许 global、specific Provider 或 Harness instance；
- 通过现有 Task Router选择 Project Harness binding或 global default，并保留首次 provider snapshot；
- 以 App principal transaction创建 task/mapping/outbox；
- watch 时验证 task provenance并从持久 cursor恢复；
- 对暂时 Core/PostgreSQL/Harness 路由故障返回净化 `Unavailable`，不伪装 NotFound；
- 对 foreign/unknown/archived/uninstalled资源统一安全映射，避免存在性 oracle。

不要复制 Task Router、Project binding 或 Agent event state machine 到 Runtime。Provider 类型只存在 Provider
adapter；Core/Runtime/SDK只处理 canonical Agent contract。

## Gateway 与 public Bridge transport

- Gateway allowlist 只新增 public AppBridge service prefix并路由 runtime-host。
- private Core AppAgent/Surface resolver、HarnessHost、Workload服务继续返回 public 404。
- Gateway 必须删除客户端伪造 identity header并写入可信 owner/device；现有 loopback DevBypass 边界不扩张。
- bridge token metadata只转发到 runtime-host；不得转发到 Desktop assets、Core public services或日志。
- Bridge RPC 必须通过与 Surface 相同的 device-session gate；无 device identity fail closed。
- Runtime/Core unavailable 映射固定、安全、可区分 retryable `Unavailable`，不得回落 Desktop SPA HTML。
- 设置 protobuf/Connect 解码上限，防止压缩后大消息、无限 goal或 event cursor滥用。

## Desktop 安装授权与 Bridge UX

最小 UX：

- App Library 在安装前显示 exact app/version requested permissions。
- 点击 Install 后出现可访问的确认界面；每个 permission checkbox 默认未选，用户可明确选择子集。
- UI 明确区分 `Requested` 与 `Granted`，不得使用“App 已拥有全部权限”等误导文字。
- 已安装行显示 granted capability摘要；空 grant也允许 Open，但 App Agent方法必须安全返回未授权。
- Open Surface 后，AppHost 建立 handshake；loading/ready/bridge unavailable状态清晰且可恢复。
- 合成 E2E App在 iframe内提供“Run project task”动作，并显示 streamed Fake Harness terminal文本，证明
  结果来自真实任务链路。
- token、Provider ID、credential、内部错误不得出现在 UI/DOM、可访问名称或截图。
- Project switch、Remove、window close、iframe reload和 Desktop unmount继续使旧 Surface/port/response inert。

不要增加真实 Key输入框，不要让用户选择 Provider，不要实现 global Agent开关，不要给 iframe
`allow-same-origin` 或 network。

## UI 视觉记录（强制）

本任务改变用户可见 UI，必须遵守 `docs/ui/README.md`：

```text
docs/ui/desktop-web/changes/20260828-minimal-project-agent-app-bridge/
├── before/
├── after/
└── notes.md

docs/ui/desktop-web/current/
```

至少记录：

- before：基准提交的 App Library / Web Bundle window；
- after：permission confirmation（requested、默认未选、用户选择后的状态）；
- after：App Surface 内完成真实 Fake Harness task并显示 terminal result；
- current：用 after中对应文件更新当前基线。

所有截图使用 Chromium `1440x900`、deviceScaleFactor 1、确定性 fixture，单张小于 2 MiB。`notes.md`
记录任务、路由、fixture、viewport、浏览器、采集命令和状态。截图必须由实际页面生成，不得用设计稿、手工
拼图或测试报告代替；禁止提交 Playwright report、trace、video或包含 token/真实数据的图片。

## 数据与 migration

禁止修改 001–007。推荐按单一 owner拆分（可在 ADR 中用同等严格模型调整，但不能混淆 owner）：

- `008`：workos-core Project Installation owner，增加安装级 canonical granted permissions/backfill；
- `009`：workos-core Agent owner，增加 App principal/provenance + request-digest idempotency事实；
- `010`：runtime-host Surface owner，增加 Bridge token/grant/session所需事实（若 token设计确实需要持久表）。

要求：

- 每张表/column/migration owner写入 migration注释、任务记录和 implementation文档。
- 现有 installation全部 backfill为空 grant，不能把历史 requested permissions自动授权。
- 新增 App task mapping不得跨模块 FK到 Project/Registry表；通过 port/RPC验证，Agent表只存稳定 snapshot ID。
- Runtime表不得 FK/查询 Core schema；Core表不得查询 Runtime session表。
- 所有时间 UTC、资源 ID UUIDv7；token不是资源 ID，不得用 UUIDv7代替 CSPRNG secret。
- grant array canonical排序、无 duplicate/null；数据库约束与应用校验共同 fail closed。
- App mapping通过 composite owner/task约束防止跨 owner结果映射。
- token/session/provenance concurrent create和失败回滚必须无 orphan row/outbox。
- pristine database、当前持久 acceptance volume、二次 migration no-op全部验证。
- scratch database fixture使用 run-unique名称并精确 cleanup；禁止删除既有 6 个历史 scratch DB。

更新 migration checksum测试，钉死 001–010（或实际新增末尾编号）；001–007对任务基准逐字节不变。

## 错误与隐私映射

至少稳定区分：

- malformed envelope/input/idempotency/sequence → `InvalidArgument`；
- missing/invalid/expired/tampered token → 净化 `Unauthenticated`；
- valid session但未 grant method → 净化 `PermissionDenied`；
- same App key/different canonical request → `Aborted`；
- closed/expired/foreign/uninstalled/archived Surface或 App task → 统一安全 `NotFound`/`PermissionDenied`，
  具体选择写入 ADR且不得形成存在性 oracle；
- Core/Runtime/PostgreSQL暂时故障 → `Unavailable`；
- invariant drift/corrupt stored grant/provenance → 净化 `Internal`，不得降级继续执行。

MessagePort侧把这些映射成有限稳定 error code与固定短消息。任何错误都不得包含 token片段、goal、event
全文、manifest、SQL、constraint、DSN、本地路径、Provider raw error或 stack。

## 必须测试的行为

### Grant / Project Installation

- manifest requested permissions仍不等于 grant；历史 install默认空 grant。
- explicit version + empty/partial/full requested subset成功，排序确定。
- duplicate、malformed、not-requested grant拒绝；system/trusted路径仍 fail closed。
- install same key/same request replay exact grant；same key/different grant `Aborted`。
- 老 empty-grant digest/replay升级兼容。
- same app/version/same grant no-op；grant不同不静默更改。
- owner/project/revision并发与 foreign资源安全映射不回归。
- restart后 ListInstalledApps仍返回 exact grant。

### Runtime token / authorization

- token entropy/shape/TTL/binding/constant-time validation或签名验证。
- token不进入 URL、session path、asset、DOM、MessageChannel、日志或错误。
- wrong owner/device/session、tamper、expiry、Close、uninstall、archive全部拒绝。
- token capability list = requested ∩ explicit grant ∩ implemented methods。
- run/watch独立授权；未知 capability/method拒绝。
- Create replay、Runtime restart和token轮换/持久语义与 ADR一致。
- session/token concurrent create、close/rpc race、rollback无 orphan/越权。

### Core App Agent / PostgreSQL

- Core强制 Project scope；global/provider/harness/requested-capability smuggling不可达或拒绝。
- same App key/same request exact replay；different request `Aborted`；两个 App相同 key互不冲突。
- PostgreSQL并发 same-key只有一个 task + mapping + outbox；loser无 orphan。
- provider snapshot首次确定后 replay不随 binding变化。
- watch只允许 same owner/project/app installation provenance；其他用户/App/task ID拒绝。
- after_sequence resume无重复遗漏，terminal后正确结束；disconnect不取消 task。
- active installation/grant每次 revalidate；uninstall后已知 task ID也不能继续通过 App Bridge读取。
- existing user AgentTaskService、Agent Center、Harness worker和event cursor不回归。

### MessageChannel / SDK / Desktop

- exact iframe window + source parent + nonce + version + single ack成功。
- wrong source、`origin="null"`冒充、旧 nonce/port、double ack、reload、timeout拒绝。
- oversize、unknown method、malformed payload、too many inflight、late response安全失败。
- token never transferred to iframe；iframe没有 WorkOS Connect client/cookie/network。
- `agent.run`和AsyncIterable `agent.stream`使用生成 canonical payload并可resume。
- port/window/Project/unmount teardown使迟到 response inert，stream取消但task继续。
- permission consent可访问性、默认未选、错误/并发/loading状态有组件测试。

### Gateway / Integration / E2E

- public AppBridge只到 Runtime；private Core services/public Workload/HarnessHost仍404。
- spoofed identity被覆盖，无 device session拒绝，runtime不可达净化503/Unavailable。
- integration真实走 Artifact → Register → explicit grant Install → Surface → Bridge → Task Router → Fake Harness
  → persisted events；不是 fake repository。
- restart workos-core/harness-host/runtime-host后，按 ADR语义重新 attach/replay并从 event cursor恢复。
- close/uninstall/archive/token tamper后纵向链路fail closed。
- Playwright通过真实Gateway让合成bundle内JS发起project task并显示唯一terminal文本。
- E2E断言iframe/CSP仍无same-origin/network/storage，token不在DOM/URL。
- `make test-deepseek-fixture`只跑本地fixture并继续通过，不访问真实Provider。

## Migration 与持久验收

- 008及后续新增migration必须能从 pristine database与当前持久 acceptance volume前向执行。
- 001–007逐字节不变并保留checksum；绝不重写历史migration“让测试简单”。
- migration tests连续运行两次，scratch database集合前后精确一致、零新增泄漏。
- integration fixture使用run-unique UUID/prefix；cleanup只精确匹配本轮数据并在单事务按FK顺序执行。
- 连续两次 `make test-integration` 前后记录 installation/grant、agent task/mapping/event/outbox、
  surface/token行数；解释本任务固定增量与已有测试增量，不能只写退出码。
- 禁止删除 `workos_workos-postgres` volume或既有scratch databases。

## 明确不在范围内

- global Agent invocation、specific Provider选择、Provider API、Provider credential或任何真实Key。
- Credential Vault、credential lease、OAuth/device production authentication。
- Agent approval、tool approval、budget enforcement、cancel、steer、parent task、incident、context refs。
- App Bridge的project.current、files、artifacts、knowledge、notifications、theme、window control。
- clipboard、file picker、resize/maximize capability；现有bool继续false。
- mutable grant update/revoke API、后台permission editor；本阶段通过uninstall/reinstall改变grant。
- iframe same-origin、cookie、storage、service worker、direct fetch/WebSocket或network access。
- Web Service/Declarative/Remote Native Surface、container/native runner、Podman、Workload Start/Stop。
- ZIP/TAR、Object Store、通用Artifact、App upgrade/downgrade。
- Reliability enforcement、Indexer/RAG、Mobile adaptive shell、LAN pairing。
- 多 runtime副本共享token key（除非ADR与本任务测试已完整解决）；不能假装已支持。
- 修改Harness Provider语义、DeepSeek adapter或Project binding选择规则。

未实现项必须返回真实 unavailable/unimplemented。不能因为两个 Agent方法 working就把完整 App Bridge、
Credential Vault、所有 capability或生产认证描述为完成。

## 文档与状态

完成时同步：

- `docs/tasks/20260828-minimal-project-agent-app-bridge.md`：最终契约、grant/token/provenance不变式、
  table owner、错误映射、真实命令、资源计数、视觉证据、风险和下一步；
- 新 App Bridge ADR；
- `docs/architecture/implementation.md`：requested → explicit grant → Surface token → MessageChannel →
  Runtime/Core authorization → Task Router/Harness完整真实链路；
- `docs/status.json`：
  - Project App Installation evidence增加immutable explicit grant snapshot；
  - Agent Task Router evidence增加project-scoped App principal/provenance，仅在真实integration/restart成立后；
  - Runtime / Surface evidence增加token-validated minimal Agent Bridge；
  - Desktop evidence增加opaque-origin MessageChannel + permission consent + browser E2E；
  - evidence必须限定仅 `agent.task.run`/`agent.event.watch`，不得声称完整 App Bridge/credential已完成；
- `docs/ui/desktop-web/...` 的before/after/current与notes；
- `sdk/app-sdk`/`clients/app-host`模块文档或注释，说明token不进iframe与支持方法边界；
- README状态区块只用 `make docs`/`make generate`生成，禁止手改；
- `docs/structure.md`原则上不改；若实现必须偏离主线，先更新ADR并报告。

建议下一独立任务记录为以下二选一，不要在本任务顺手实现：

1. mutable capability grant/revocation + approval/budget policy；
2. rootless Web Service/container runner + Workload/cgroup/Reliability最小链路。

## 验收顺序

### 基础、协议与生成

```sh
make generate
make generate
git diff --check
make check
buf breaking --against '.git#branch=main'
```

第二次generation后必须无新增差异；Proto、SQLC、Go、TypeScript、SDK和README/status生成物一致。

### 数据、纵向与浏览器

```sh
make test-integration
make test-integration
make test-deepseek-fixture
make test-e2e
```

更新完整integration/restart流程，确保Core/Runtime/Harness真实启动。DeepSeek门禁只使用target自带fixture
credential。Playwright必须生成并校验本任务UI视觉记录所依据的确定性状态。

### 定向安全与并发

至少实际运行并记录：

```sh
go test -race ./internal/core/agent/... ./internal/core/project/... ./internal/core/orchestration/...
go test -race ./internal/runtime/...
```

TypeScript至少定向运行 `sdk/app-sdk`、`sdk/surface-sdk`、`clients/app-host`、`clients/window-manager`、
`apps/desktop-web`测试。若本机依赖必须通过仓库Docker toolchain执行，可使用等价容器命令；任务记录必须写
真实命令和退出结果，不能只复制本Prompt。

### 最终一致性与对象卫生

```sh
git diff --check
git diff --check main...HEAD
git diff --exit-code main -- internal/platform/migrations/files/001_foundation.sql
git diff --exit-code main -- internal/platform/migrations/files/002_app_registry.sql
git diff --exit-code main -- internal/platform/migrations/files/003_app_registry_idempotency.sql
git diff --exit-code main -- internal/platform/migrations/files/004_project_app_installations.sql
git diff --exit-code main -- internal/platform/migrations/files/005_project_app_installation_request_owner.sql
git diff --exit-code main -- internal/platform/migrations/files/006_web_bundle_artifacts.sql
git diff --exit-code main -- internal/platform/migrations/files/007_surface_sessions.sql
git status --short --branch
```

另外确认：

- 只有预期新增migration且owner/checksum/pristine/current-volume证据完整；
- `docs/structure.md`无意外变化；
- screenshots符合 `docs/ui/README.md`，无Playwright report/trace/video；
- Git新增对象无ELF、编译二进制、大型临时图片或测试产物；记录最大新增blob大小；
- 无root-owned文件、token、credential、raw goal/event/bundle内容或临时数据库信息；
- iframe/CSP仍无`allow-same-origin`、network、storage；
- bridge token不在URL/DOM/MessageChannel/日志；Provider credential永远不进入App；
- container/native、完整App Bridge、Credential Vault和生产device auth继续如实unavailable；
- worktree最终干净，功能分支提交聚焦；未merge、未push。

## 完成与交接

- 完成所有范围内实现，不以TODO、空adapter、只在前端检查permission、内存fake task/event、固定成功
  handshake或没有验证方的token冒充working。
- 只有explicit grant、validated token、MessageChannel、Runtime/Core双重授权、App task provenance、真实
  Task Router/Harness事件、restart/revocation和浏览器E2E全部成立，才能更新working evidence。
- 在 `feat/minimal-project-agent-app-bridge` 创建聚焦提交；提交信息建议：
  `feat: add project-scoped app bridge`。
- 最终交接必须写明提交哈希、实际命令、新migration checksum/前向升级证据、两次integration资源计数、
  token/restart语义、authorization matrix、MessageChannel安全证据、UI前后截图路径、未决风险、下一任务和
  worktree状态。
- 不要merge到`main`，不要push；留给审核者静态复审和本地`--ff-only`合并。
