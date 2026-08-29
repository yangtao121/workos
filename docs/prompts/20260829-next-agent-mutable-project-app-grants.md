# 下一位智能体 Prompt：Mutable Project App Grants 与立即撤销纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、UI 视觉证据、文档和聚焦提交，
> 不是只输出计划。

## 你的角色与最终结果

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。当前 Minimal Project-scoped
Agent App Bridge 已完成审核并合入本地 `main`。你的任务是实现一个严格限定的安全纵向切片：

**让用户在不卸载 App 的前提下，显式替换某个 Project App installation 的 grant 集合；每次真实
变更都产生 installation grant revision，并使旧 Surface 的全部 App Bridge 权限失效。用户重新打开
Surface 后，才按新的 grant 建立能力。**

这不是简单地给数组增加一个 UPDATE。必须同时闭合以下事实：

```text
exact pinned manifest requested permissions
  → explicit SetAppGrants replacement command
  → Project revision + installation grant revision + durable idempotency
  → event/outbox in the same transaction
  → old Surface keeps rendering assets but loses every App Bridge method
  → Core re-authorizes every run/watch call and every watch polling round
  → newly opened Surface snapshots the new grant revision and effective capabilities
  → Desktop shows and edits the exact pinned version's permissions
```

持续推进直到实现、测试、UI 前后截图、文档、状态与单一聚焦提交全部完成。只有遇到以下情况才停止并
报告证据与选项：必须破坏已有 v1 字段/编号、改变六进程所有权、修改已执行 migration、削弱现有
iframe/token 边界，或需要引入尚未批准的生产信任根。

## 为什么下一步先做授权生命周期

当前链路已经能够让不可信 Web Bundle App 经显式安装授权调用 Project Agent，但授权还是安装时的
不可变快照：撤权只能卸载并重装。这个限制在单一原型中是安全的，却不适合作为后续 Web Service、
container 和更多 App Bridge capability 的长期基础。

本任务先建立一个可审计、可并发裁决、可立即失效旧会话的 grant 生命周期：

```text
已完成：manifest requested permissions
  → 已完成：install-time explicit grant
  → 已完成：Surface effective capability + Core per-call revalidation
  → 本任务：mutable full-replacement grant + grant revision
             + old-Surface bridge invalidation
             + Desktop permission management
  → 后续：App Agent approval / quota / token-cost policy
  → 后续：rootless Web Service/container Workload + Reliability
```

选择 full replacement，而不是 add/remove 两套命令：一个请求完整表达用户想要的最终集合，便于做
canonical digest、幂等重放、并发裁决和 UI 审核。请求中的空数组明确表示撤销全部权限；省略 repeated
字段也只能得到空集合，绝不能回退为 requested permissions。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时，本地 `main` HEAD 为 `2486ca3`，工作树干净，且本地 `main` 领先
  `origin/main` 7 个提交。执行时必须重新检查，以执行时本地 `main` 为基准；不得从落后的远端分支
  重建或丢弃本地提交。
- `docs/status.json` 是进度事实源。App Registry、Project App Installation、Agent Task Router、
  Desktop Shell、Runtime / Surface 当前均有 working 证据，但 Runtime 仍仅支持 Web Bundle；
  container/native runner 仍 unavailable。
- `api/proto/workos/app/v1/installation.proto` 中 `granted_permissions = 8` 当前描述为安装时不可变
  快照；本任务要通过 additive contract 把它演进为 installation 当前 grant，并新增独立、单调递增的
  `grant_revision`，不能复用或改号已有字段。
- `api/proto/workos/surface/v1/surface_resolver.proto` 当前把 authoritative grant snapshot 从 Core
  返回 runtime，但没有 grant revision。
- `api/proto/workos/agent/v1/app_agent.proto` 是 runtime-host 到 Core 的私有服务，不在 Gateway
  allowlist；当前请求没有携带由 session 派生的 grant revision。
- runtime-host 的 Surface session 会持久化 effective capability 与 bridge-token hash；旧 session
  的 capability 是创建时快照。Core 的 App Agent service 会在每次 run 调用及 watch stream 的每个
  200ms polling round 重新读取 active installation/grant。
- public `AppBridgeService` 的 body 不接受 owner、device、project、installation、capability 或 token；
  这些边界不能改变。bridge token 仍只能留在可信 Desktop/AppHost 内存和专用 metadata 中。
- 当前 Desktop App Library 安装时 checkbox 默认全不选，并显示 `Granted:` 摘要；文案仍声称更改权限
  必须卸载重装。
- migrations `001`–`010` 已在持久验收数据库执行并受 checksum 测试保护，禁止修改。新 migration
  必须从 `011` 开始；Core Project Installation 与 runtime Surface 的表不能放进同一个 owner 不明的
  migration。
- `docs/ui/README.md` 已定义 UI 视觉记录约定；当前 Desktop 基线在
  `docs/ui/desktop-web/current/`。

## 凭据与最高优先级安全边界

- **本任务不需要真实 DeepSeek、OpenAI、GitHub 或其他 Provider API Key。**
- 不得使用、保存、转述、验证或尝试恢复聊天中曾出现过的任何真实 Key；不得扫描 shell history、
  环境变量、本机文件或聊天历史来搜集凭据。
- Agent E2E 只使用仓库已有 Fake Harness；DeepSeek 回归只允许仓库的 keyless/假凭据 fixture，禁止
  调用真实 Provider 网络。
- requested permission 仍只是 manifest 请求；只有 Project Installation 当前 grant 是授权事实。
- grant revision 不是 bearer credential，也不是客户端可选择的授权值。runtime 只能把 session 持久
  快照中的 revision 传给私有 Core；iframe/public request 不得提交或覆盖它。
- grant 更新不得让 App 获得未由 exact pinned manifest 请求的 capability。
- grant 更新不能给 iframe 暴露 WorkOS cookie、device credential、bridge token、Provider 类型、
  Provider credential、通用 Connect client、Core/runtime 私有地址或 Gateway identity header。
- 不得放宽 iframe 的 `sandbox allow-scripts`、opaque origin、`connect-src 'none'`、HTTP CSP 或
  MessageChannel exact-window/nonce 边界。
- 不得把 grant、token、goal、Agent event 全文、manifest、SQL、DSN、constraint、stack 或 provider raw
  error 写入日志或未净化错误。capability ID 本身不是 secret，但错误仍应使用固定短消息。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`CONTRIBUTING.md`、`docs/ui/README.md`；
   - `docs/structure.md` 中 App、Workload、Harness capability、App Agent API、Credential Vault、
     Surface、Reliability、数据存储和第一版产品边界；
   - `docs/architecture/implementation.md`、`docs/status.json`；
   - `docs/decisions/0001-foundation-boundaries.md`；
   - `docs/decisions/0002-app-bridge-trust-boundary.md`；
   - `docs/tasks/20260825-project-app-installation.md`；
   - `docs/tasks/20260825-minimal-web-bundle-surface.md`；
   - `docs/tasks/20260828-minimal-project-agent-app-bridge.md`，尤其最终审核修复、授权矩阵与未决风险；
   - 上述任务对应的实现、审核、修复 Prompt；
   - `api/proto/workos/app/v1/app.proto`、`installation.proto`；
   - `api/proto/workos/surface/v1/surface.proto`、`surface_resolver.proto`；
   - `api/proto/workos/agent/v1/agent.proto`、`app_agent.proto`；
   - `api/proto/workos/bridge/v1/bridge.proto`；
   - `schemas/workos-app-manifest-v1.schema.json`；
   - `internal/core/project` 的 domain/application/ports/postgres/transport 与全部测试；
   - `internal/core/appregistry` 及中立 `AppCatalog` orchestration adapter；
   - `internal/core/orchestration/app_agent.go`、surface resolver 及 transport；
   - `internal/runtime/surface` 的 domain/application/ports/postgres/coreclient/transport 与全部测试；
   - `apps/desktop-web/src/AppLibrary.tsx`、Desktop window/session teardown、对应 unit/E2E；
   - `sdk/agent-sdk`、`sdk/protocol`、`sdk/app-sdk`、`sdk/surface-sdk`、`clients/app-host`；
   - migrations `004`、`005`、`007`–`010`、migration checksum/forward tests、sqlc 配置；
   - `tests/integration`、`tests/restart` 和现有 App Bridge Playwright E2E。

2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -12
   git branch -vv
   git diff --check
   ```

   保留所有不属于本任务的改动；不得 reset、rebase、checkout 覆盖用户文件，不得清理或重建用户的
   持久数据库。

3. 从执行时的本地 `main` 创建独立 branch/worktree，建议：

   ```text
   feat/mutable-project-app-grants
   ```

   禁止直接在 `main` 实现，不要 merge 或 push。

4. 从 `docs/tasks/TEMPLATE.md` 创建：

   ```text
   docs/tasks/20260829-mutable-project-app-grants.md
   ```

   初始状态设为 active，写清 requested/granted/effective/revision 的区别、table owner、Proto、
   migration、并发与 replay 语义、旧 Surface 行为、UI、测试与非目标。

5. 新增聚焦 ADR，建议：

   ```text
   docs/decisions/0003-mutable-app-grants.md
   ```

   它必须明确替代 ADR-0002 中“installation grant 在安装生命周期内不可变”的局部决定，但不能改变
   ADR-0002 的 iframe、token、provenance、二次授权和 Gateway 信任边界。ADR 至少决定：
   - full-replacement Set 命令为何优于增量 add/remove；
   - Project revision 与 installation grant revision 各自用途；
   - 何时递增、no-op 是否递增；
   - 为什么任何真实 grant 变更都让旧 Surface 的全部 bridge 方法失效；
   - 旧 Surface 的静态 Web Bundle 资产为何仍可渲染，但 App Bridge 必须重新打开；
   - 已经通过授权线性化点的并发请求与既有 durable task 不会被隐式取消；
   - watch stream 如何在下一次 Core reauthorization 时终止；
   - grant mutation、idempotency mapping、event/outbox 的单事务线性化点；
   - runtime/core 之间只传 session 派生 revision，不建立跨 schema 查询或 FK。

6. 按 `docs/ui/README.md` 建立 before 基线。复制当前受影响截图，必要时从未修改的任务基准提交用同一
   fixture 重采集：

   ```text
   docs/ui/desktop-web/changes/20260829-mutable-project-app-grants/before/
   ```

   至少覆盖 installed/granted 行与安装 consent 文案。截图使用 Chromium、`1440x900`、
   `deviceScaleFactor: 1`、确定性 fixture，不得含真实数据或凭据。

7. 记录并运行基线：

   ```sh
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败要记录证据和归属；禁止通过删 volume、TRUNCATE、broad DELETE、跳过测试、放宽断言、
   固定成功响应或删除历史测试绕过。

## 必须固定的语义

### 1. 四种不同事实

| 概念                           | owner                               | 语义                                                       |
| ------------------------------ | ----------------------------------- | ---------------------------------------------------------- |
| requested permissions          | Core App Registry immutable version | App 只提出请求，永不自动授权                               |
| current installation grant     | Core Project Installation           | 用户最后一次成功确认的 canonical 完整集合                  |
| installation grant revision    | Core Project Installation           | 从 1 开始、仅在 grant 集合真实改变时 +1                    |
| effective Surface capabilities | runtime Surface session             | 创建时的 `current grant ∩ implemented bridge methods` 快照 |

Project revision 是整个 Project 聚合的 optimistic concurrency/事件 sequence；grant revision 是单个
installation 的授权 epoch。两者不能相互替代，也不能由客户端自行递增。

### 2. SetAppGrants 是完整替换

新增 additive public RPC，建议名称：

```proto
message SetAppGrantsRequest {
  string idempotency_key = 1;
  string project_id = 2;
  string installation_id = 3;
  int64 expected_project_revision = 4;
  repeated string granted_permissions = 5;
}

message SetAppGrantsResponse {
  AppInstallation installation = 1;
  int64 project_revision = 2;
}

service AppInstallationService {
  // existing methods...
  rpc SetAppGrants(SetAppGrantsRequest) returns (SetAppGrantsResponse) {}
}
```

同时给 `AppInstallation` additive 增加未占用字段 `grant_revision`。实际字段号以 Proto 当前状态核对后
选择，禁止复用、重排或删除已有编号。

语义固定为：

- `granted_permissions` 是完整目标集合，不是 patch；空集合撤销全部。
- 输入必须 valid UTF-8/grammar、无控制字符、无重复；application canonical 排序。
- 目标集合必须是 exact pinned app version requested permissions 的子集。
- 不允许客户端提交 app ID/version/manifest digest/requested set/grant revision/new Project revision。
- 同一个 `expected_project_revision` 下，grant update 与 Project update、install、uninstall、binding 等
  Project mutation 必须由同一 Project row lock/guard 串行化。
- current grant 与目标集合完全相同是确定性 no-op：仍验证 Project revision，仍持久消费成功请求 key，
  但 Project revision、grant revision、event、outbox、updated timestamp 均不变化。
- 集合真实变化：grant revision 恰好 +1，Project revision 恰好 +1，installation update、Project
  revision、project event、outbox、idempotency result 在一个事务提交。
- 失败请求不消费 key，不产生局部更新、event 或 outbox。

### 3. 幂等命名空间与精确结果快照

继续使用 `project_app_installation_requests` 的 `(owner_user_id, idempotency_key)` 作为
install/uninstall/set-grants 共用命名空间。不要新建一张互不冲突的旁路 mapping 表。

新增 Set 请求的 canonical digest 必须覆盖：

```text
command version marker
project_id
installation_id
expected_project_revision
canonical sorted target grant set
```

不得包含时间、随机 ID、服务端解析结果或当前 grant。same key/same digest 精确 replay 第一次响应；same
key/different command/project/installation/revision/grant 稳定 `Aborted`。

一旦 grant 可变，现有 mapping 只覆盖 `result_uninstalled_at` 已不足以精确重放：历史 install 或
uninstall key 如果在后续 grant mutation 后重放，不能返回后来更新的 grant/revision。因此 Core-owned
forward migration 必须给请求结果增加至少：

```text
result_granted_permissions
result_grant_revision
```

现有 001–010 绝对不改。建议 migration 规划：

- `011_mutable_project_app_grants.sql`，owner：workos-core Project Installation。
  - `project_app_installations.grant_revision`，existing rows backfill 为 1，`NOT NULL`、正数约束；
  - 扩展 request `command` 约束以接受 `set-grants`；
  - 增加结果 grant/revision snapshot；
  - 对既有 install/uninstall mapping 从其 owner-bound installation 回填当时 grant 与 revision 1；
  - 先做 fail-closed 数据一致性检查，再设 `NOT NULL`/约束；不得静默丢弃或重写历史 mapping。
- `012_surface_grant_revision.sql`，owner：runtime-host Surface。
  - 给 `workos_runtime.surface_sessions` 增加持久的 installation grant revision 快照；
  - existing session backfill 为 1，`NOT NULL`、正数约束；
  - 不建立到 Core schema 的 FK，不查询 Core 表。

如果核对实际 schema 后需要不同的 additive 形态，可以在 ADR 中说明，但必须保持每个 migration 单一
process/table owner、历史精确 replay 和旧 session fail closed。所有 SQLC 产物只通过
`make generate` 生成，禁止手改生成文件。

### 4. application 与 repository 裁决

建议顺序：

```text
validate request + canonicalize target grant
  → compute request digest
  → replay lookup before external/catalog resolution
  → owner-scoped read active installation
  → AppCatalog exact app/version resolve requested permissions
  → verify app/version/manifest digest match the installation's pinned facts
  → validate target ⊆ exact requested set
  → repository transaction re-locks Project + active installation
  → re-checks pinned identity/current invariant/expected revision
  → no-op or atomic mutation + event/outbox + idempotency result
```

Registry facts是 immutable，因此可经现有中立 `AppCatalog` port 解析；Project 模块不得导入
App Registry internal package、adapter 或 SQL。repository transaction 必须重新确认 installation 仍 active、
owner/project/ID/pinned identity 一致，避免与 uninstall 的 TOCTOU。Core 模块之间不能共享 transaction、
entity 或直接 join 对方表。

stored current grant 若含 unknown、未排序、重复、不是 pinned requested subset，或 grant revision 非法，
属于 invariant corruption：返回净化 `Internal`，不得借用户更新静默“修好”，也不得继续授权。

### 5. project event 与 outbox

真实变更新增版本化事件，例如：

```text
project.app.grants.updated.v1
```

`sequence = new Project revision`。payload 只含稳定、非敏感事实，例如：

```text
projectId
revision
installationId
appId
version
manifestDigest
grantRevision
canonical grantedPermissions（或 canonical added/removed，ADR 固定一种）
```

不要包含 manifest、goal、task/event 内容、token、credential 或任意 raw user content。事件/outbox 与
installation/Project/idempotency mapping 同事务；no-op 不发事件。consumer 仍按 at-least-once/幂等设计。

### 6. revision-bound Surface 与私有 Core 二次授权

协议必须 additive 地把 Core authoritative grant revision 传过既有私有链路：

```text
Core ResolveWebBundle
  → granted_permissions + grant_revision
runtime CreateSurface
  → persist installation_grant_revision in Surface session
runtime public App Bridge
  → derive project/app instance/grant revision from validated session
runtime → private Core AppAgentService
  → Core re-resolves active installation
  → exact current grant_revision must equal session revision
  → then validate the entire current grant and requested method membership
```

`RunAgentTaskRequest`/`WatchAgentTaskEventsRequest` 的 private additive revision 字段只能由 runtime 的
validated session 派生。public Bridge body、MessageChannel envelope 和 iframe SDK 不得增加该字段。

任何真实 grant mutation 后：

- 所有旧 Surface session 的 App Bridge 方法都失效，即使某个 capability 在新旧集合中都存在；
- mismatch 对 public Bridge 净化为 `PermissionDenied`，不能透露 current revision/current grants；
- bridge token 本身不需要跨进程主动删除，静态 Web Bundle 资产仍按 active installation 规则服务；
- old token 不得通过 runtime-only stale capability 绕过 Core；
- watch stream 必须在 Core 下一次 polling authorization 时结束，不能继续把新事件流给旧 grant epoch；
- 新 grant 增加的 capability 绝不能自动出现在旧 session；用户必须使用新 idempotency key 重新
  CreateSurface；
- Desktop 在本地成功 Set 后应关闭该 installation 的 open window/MessagePort，并 best-effort
  `CloseSurface`，但服务端安全不能依赖这个客户端动作。

对相同 create idempotency key 的旧 session replay：不得轮换出一个看似可用但绑定旧 grant revision 的
新 token。重新通过 Core resolver 比较 revision；不一致时 fail closed，要求调用方使用新的 create key。
错误使用固定、净化的 `FailedPrecondition` 或 `PermissionDenied`，在 ADR 与测试中固定一种。closed/expired
replay 的既有不铸造语义保持不变。

不要让 runtime 查询 Core schema，不要让 Core 查询 runtime schema，也不要为此新增跨 schema FK。

### 7. 并发与“立即撤销”的精确定义

必须在 ADR 和测试中承认并固定线性化语义：

- SetAppGrants 的线性化点是 Core Project transaction commit。
- 在该 commit 后才进入 Core authorization read 的新 run/watch 必须失败。
- 已在 commit 前通过 Core authorization 的并发请求可能完成；本任务不追溯删除已创建 task。
- 已创建的 Agent task 是 durable，撤权不等于 CancelTask；自动取消属于未来显式策略，不得偷偷实现。
- 已打开 watch stream 会在下一次 Core reauthorization polling round 发现 epoch mismatch 并终止。
- Set 与 Set、install/uninstall、Project/binding mutation 的 expected Project revision 冲突由数据库裁决，
  不依赖进程内 mutex。

禁止把“立即撤销”写成无并发定义的营销文案，也禁止声称会取消既有 task。

## 错误映射

| 条件                                                                       | public Connect 结果                   |
| -------------------------------------------------------------------------- | ------------------------------------- |
| malformed key/UUID/revision/grant、重复 capability                         | `InvalidArgument`                     |
| target grant 不是 exact requested set 子集                                 | `PermissionDenied`                    |
| unknown/foreign/archived Project，unknown/foreign/uninstalled installation | 净化 `NotFound`                       |
| stale expected Project revision                                            | `Aborted`                             |
| same idempotency key / different canonical request                         | `Aborted`                             |
| Registry exact version 不存在或不属于 owner                                | 净化 `NotFound`                       |
| pinned digest/存储 grant/revision invariant 漂移                           | 净化 `Internal`                       |
| PostgreSQL/Core/Runtime 暂时不可用                                         | `Unavailable`                         |
| old Surface grant revision mismatch                                        | public Bridge 净化 `PermissionDenied` |

错误文本必须是固定短消息，不包含 capability 输入原文、current grant/revision、foreign resource
存在性、SQL/constraint/DSN、manifest、token、goal、event 或 stack。

## Desktop UX

在 App Library 的已安装行增加明确的 `Manage permissions` 操作：

1. 点击后通过 `AppRegistryService.GetApp(app_id, exact pinned version)` 读取 immutable requested set；
   不能用 Registry current version 的 permissions 替代 pinned version。
2. 客户端核对返回的 app ID/version/manifest digest 与 installation 一致；漂移时 fail closed 并显示固定
   错误，不渲染可提交的 checkbox。
3. dialog 显示 app 名称、exact pinned version、current grant revision、requested permissions；checkbox
   初始值必须来自 current installation grant，不得默认全选。
4. 清楚标识本次 added/removed 权限；保存是完整 replacement。没有差异时 Save disabled，不能制造
   revision/event。
5. 增权与撤权都必须由用户点击明确的 Save；取消不发送请求。空集合文案明确为 revoke all。
6. 保存使用 fresh `crypto.randomUUID()` key 与 UI 当前 Project revision；revision conflict 时重新读取
   Project、installation 和 exact app version，提示用户重新确认，禁止用旧选择自动重放。
7. 成功后以服务端 response + 重新 List/Get 为准，更新 `Granted:` 与 grant revision；关闭该
   installation 的 open App window/MessagePort/session，提示“重新打开后新权限生效”。
8. Project 切换、dialog 关闭、组件 unmount、重复点击与迟到 response 使用 generation guard；迟到结果
   不能污染另一个 Project，也不能关闭错误 App 的 window。
9. 更新安装 consent 文案，删除“以后只能卸载重装”这一过时说明；安装时默认全不选语义保持不变。
10. installed-but-current-catalog-unavailable 行仍可安全 remove；只有成功解析 exact pinned version 后才
    能管理权限，不能猜 requested set。

不要把权限编辑做成只改 React state 的假功能；E2E 必须走 Gateway → Core → PostgreSQL，并证明旧
Surface server-side 失效。

## UI 视觉证据

遵守 `docs/ui/README.md`。任务目录：

```text
docs/ui/desktop-web/changes/20260829-mutable-project-app-grants/
├── before/
├── after/
└── notes.md
```

after 至少记录：

- installed row 的 current grant + grant revision + `Manage permissions`；
- permission dialog 初始 current selection；
- 有 added/removed diff 的确认状态；
- revoke-all 状态或成功后的重新打开提示（二者至少一个，优先最能说明行为者）。

用 after 中对应文件更新 `docs/ui/desktop-web/current/`。全部为 Chromium、`1440x900`、
`deviceScaleFactor: 1`、相同 fixture/route；单张小于 2 MiB。`notes.md` 记录任务文件、基准提交、
fixture、交互步骤、采集命令和有意差异。不得出现 token、真实 key、真实用户内容或随机不稳定数据。

## 必须覆盖的测试

### Domain / application

- grant shape：空集合、排序、重复、控制字符、非法 capability grammar。
- exact requested subset；unknown/unrequested capability fail closed。
- Set request digest 顺序无关；任何 project/installation/revision/grant/command 变化都改变 digest。
- consumed same key/same digest 精确 replay；different digest `Aborted`；失败不消费 key。
- real change：Project revision +1、grant revision +1。
- same-set no-op：两个 revision 均不变，无 event/outbox，但 key 被持久消费并可 replay。
- stored grant/revision/digest invariant corruption → Internal，不静默修复。
- catalog unavailable、repository unavailable、transient PostgreSQL → `Unavailable`。

### PostgreSQL / migration / concurrency

- pristine database 正向执行 001–012、二次执行 no-op、migration checksum 全钉住。
- 001–010 与本任务基准逐字节一致。
- 011 对 existing install/uninstall request mapping 的 grant/revision snapshot 回填正确；错误 owner/result
  数据 fail closed，不被静默删除。
- 012 existing Surface session backfill revision 1；Core/runtime schema 无跨边界 FK。
- install key 成功后 set grants，再 replay install key，返回第一次 grant/revision，而不是 current row。
- set key 成功后再次 set/uninstall，再 replay旧 key，返回第一次 response 的 grant/revision/
  uninstalled_at/Project revision。
- same expected Project revision 的两个不同 grant 恰好一个 winner；loser `Aborted`。
- Set 与 uninstall、Set 与普通 Project update、same key 跨 Project/command 的真实并发裁决。
- transaction 中任何 event/outbox/mapping 失败都整体回滚。
- UTC timestamp、UUIDv7 event/outbox ID、sequence = Project revision。
- `go test -race` 覆盖新增并发测试，不允许以测试桩 data race 让门禁假失败。

### Proto / transport / Gateway

- Proto additive，`buf lint`、format、breaking check 通过；生成物只来自 `make generate`。
- request body 上限仍在解码前；畸形/超大压缩请求 fail closed。
- Gateway 只因现有 AppInstallationService prefix 自动公开新 RPC；private AppAgent/Surface resolver/
  Workload RPC 继续 404。
- public Set identity 只来自 Gateway；spoofed owner/device header 被剥除。
- public App Bridge body 仍不能提交 grant revision；token header 仍只进入 AppBridge runtime route。
- 所有错误映射为上表固定 code/message，secret/content 扫描无泄漏。

### Runtime / revocation integration

使用真实 PostgreSQL、Core、runtime-host、Gateway 和 Fake Harness 证明：

1. 安装 grant `agent.task.run` + `agent.event.watch`，创建 Surface，旧 token 能 run/watch。
2. Set 为只保留其中一个或空集合；事务返回后，用旧 session/token 调任意 Bridge 方法都被 server-side
   `PermissionDenied`，不是仅由 UI 隐藏按钮。
3. 在变更前打开的 watch stream，于有界的下一次 Core polling reauthorization 后终止；不再接收后续
   event。测试不能靠 arbitrary 长 sleep，使用 deadline/channel 与可解释上限。
4. 用旧 CreateSurface idempotency key replay，不铸造绑定旧 revision 的可用新 token。
5. 用 fresh key 新建 Surface，effective capabilities 精确等于新 grant 与 implemented method 的交集。
6. grant 增加后，旧 session 不能自动使用新增 capability；fresh session 才可用。
7. Surface/Core/runtime restart 后 revision snapshot、失效语义、idempotent replay 仍成立。
8. 静态 Web Bundle 资产在 installation active 时仍可读取；grant mutation 不被误当 uninstall。
9. foreign owner/device/token/task、closed/expired session、uninstalled app 的既有矩阵不回归。
10. 已创建 durable task 不因撤权被隐式取消；撤权后新授权读取才失败。

### Desktop unit / browser E2E

- dialog 使用 exact pinned version，而不是 catalog current；digest mismatch fail closed。
- checkbox 初始 current grant、diff、no-op disabled、revoke-all、Cancel、busy、防重复提交。
- stale Project revision reloads facts and requires reconfirmation；不自动重放。
- Project switch/unmount/late success/error inert；成功只关闭目标 installation 的 window/session。
- 完整浏览器 E2E：install with grants → open → App 经真实 Fake Harness 得到 terminal event → Manage
  permissions/revoke → old bridge server-side denied → reopen with new revision → UI/bridge 与新 grant 一致。
- E2E DOM、截图、console/network diagnostics 不含 bridge token、Key 或 raw credential。

## 明确不在范围内

- App Agent approval、human approval decision API、token/cost/day budget、rate limit、quota、billing。
- 自动取消、暂停或删除撤权前已创建的 Agent task。
- mutable manifest requested permissions、App upgrade/downgrade、installation version 变更。
- 新增 `artifact.*`、`project.*`、`knowledge.*` 等 Bridge executor；它们即使被 grant 仍只是存储事实。
- global Agent invocation、provider 选择、Provider credential、Credential Vault。
- Web Service/Declarative/Remote Native Surface、Podman/container/native runner、Workload、cgroup、
  Reliability/Incident/Repair、Indexer/RAG、Mobile。
- 多用户分享、远程公网访问、生产 device authentication（现有 loopback DevBypass 风险不在本任务修）。
- 通过跨 schema SQL、共享 mutable entity、runtime 读 Core 表或 Core 读 runtime 表实现撤销。
- 修改 `docs/structure.md` 产品主线；若实现确实与主线冲突，先停止并提 ADR 选项，不得顺手改愿景。

## 验收门禁

至少执行并在任务记录写明真实结果：

```sh
make bootstrap
make generate
git status --short
make generate
git status --short
make check
buf breaking --against '.git#branch=main'
make test-integration
make test-integration
make test-deepseek-fixture
make test-e2e
go test -race ./internal/core/project/... ./internal/core/orchestration/... ./internal/runtime/...
git diff --check
git diff --check main...HEAD
```

要求：

- 第二次 `make generate` 后无新增生成差异。
- `make check`、breaking、integration ×2、fixture、E2E、race 全部 exit 0。
- `make test-deepseek-fixture` 只使用仓库假凭据；不得设置真实 `DEEPSEEK_API_KEY`。
- 两轮 integration 前后记录相关表资源计数，解释固定增量；不得删 volume 或历史数据。
- pristine scratch database 测试精确清理自己创建的唯一命名数据库，不碰既有 scratch DB。
- 历史 migrations 001–010 逐字节不变；011/012 checksum 被测试固定。
- 检查 `main..HEAD` 与最终树没有 ELF、构建产物、Playwright report/trace/video、临时 DB、大型无关
  blob、root-owned 文件或 credential。
- README 状态区块只由 `docs/status.json` + renderer 生成，禁止手改。

## 文档与状态同步

完成后同步：

- ADR-0003，并在 ADR-0002/implementation 中明确局部 superseded 关系；
- `docs/architecture/implementation.md`：Set 链路、revision-bound Surface、table/migration owner、
  replay、事件、错误与并发语义；
- `docs/tasks/20260829-mutable-project-app-grants.md`：最终范围、验证命令、资源计数、风险、下一步；
- `docs/status.json`：只更新真实有证据的 Project App Installation、Runtime / Surface、Desktop Shell
  evidence 与日期；不要声称 approval/budget/container/reliability working；
- UI before/after/current/notes；
- 相关模块 README/注释，删除“grant 只能卸载重装”的过时表述。

只有真实 Gateway/Core/PostgreSQL/runtime/restart/browser 证据全部成立后，才能把任务状态改为 done。
如果旧 token 的失效只由 Desktop 关窗证明、Core revision 没有参与授权，任务不得标记完成。

## 提交与交接

1. 保持一个聚焦 branch，提交实现与相关文档；可以有少量清晰修复提交，但不要混入无关重构。
2. 不要 merge 到 `main`，不要 push；由后续审核者审查并决定合并。
3. 交接必须写进任务记录，不依赖聊天。至少包含：
   - branch/HEAD/base；
   - Proto 与 migration 说明、checksum、001–010 unchanged 证据；
   - 所有验证命令和真实 exit/result；
   - idempotency/replay/concurrency/revocation authorization matrix；
   - 两轮 integration 资源计数与 scratch database 卫生；
   - UI visual record 路径与采集命令；
   - secret/对象/生成物审计；
   - 未决风险与下一独立任务建议。

建议后续任务仍保持单一边界，二选一：

1. **Project App Agent approval + durable quota/budget policy**；或
2. **rootless container Workload lifecycle，再独立接 Web Service Surface 与 Reliability monitoring**。

不要在本任务顺手实现两者。
