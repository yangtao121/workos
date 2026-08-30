# 下一位智能体 Prompt：Project Agent Markdown / Diff Artifact Review 纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、UI 视觉证据、文档和聚焦提交，
> 不是只输出计划。

## 你的角色与最终结果

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。LAN 设备配对与持久 Gateway
会话已经合入本地 `main`；受监督 Rootless Web Service Workload 的真实 Podman 验收仍受宿主环境
阻塞。你的任务不是继续在无真实 runner 证据的 Reliability/Repair 层堆代码，也不是开始移动端壳，
而是完成 Product Alpha 里下一条不依赖特殊宿主能力的用户链路：

**让 Project Agent 通过 canonical、受限、lease-bound 的输出协议产出 immutable Markdown 或
Unified Diff Artifact；Core 为内容生成身份、校验 Project/task provenance、持久化并发布唯一
`ArtifactCreated` 事件；Desktop 可以从 Agent Timeline 或 Artifact Center 打开安全、只读的
Markdown/Diff 审阅窗口。**

最终链路必须闭合：

```text
Desktop Agent Center
  → SubmitTask(output_artifact_types = [document.markdown.v1 | code.unified-diff.v1])
  → Core resolves one provider snapshot and verifies its exact artifact capability
  → harness-host claims the durable task lease
  → provider emits a canonical bounded artifact output (not an ArtifactCreated reference)
  → worker sends a private lease-bound AppendTaskArtifact command
  → Core derives owner/project/task from the active lease
  → Core validates and canonicalizes title/type/content
  → Artifact-owned PostgreSQL facts + durable output-key adjudication
  → Core mints UUIDv7/digest/time and publishes exactly one ArtifactCreated event
  → WatchTaskEvents returns only the Core-minted artifact reference
  → Desktop timeline opens the exact owner/project artifact
  → Artifact Center lists current Project artifacts after restart
  → safe Markdown or unified-diff renderer displays inert content
```

持续推进到实现、生成物、真实 PostgreSQL/跨进程/browser 证据、UI before/after/current、ADR、任务记录、
架构文档、状态事实源和聚焦提交全部完成。不要 merge 或 push。只有遇到以下情况才停止并留下证据与
选项：必须破坏已有 v1 字段或编号、修改已执行 migration、改变六进程所有权、让 harness-host 直接
写 Core SQL、让 Provider 自选 owner/project/artifact ID、把未受限内容写入事件或日志、降低 Gateway
身份边界，或执行环境无法运行本任务要求的 PostgreSQL + Fake Harness + Chromium 链路。

## 为什么现在做这个

`docs/structure.md` 的 Product Alpha / 第一版边界已经完成或拥有真实证据的主线包括：

```text
Project → Harness binding → durable Agent task → canonical event stream
Registry → installation → Web Bundle Surface → least-privilege App Bridge
App policy → approval → quota reservation → usage circuit break
direct HTTPS origin → device pairing → persistent Gateway session
```

当前仍有一个明显断点：

- `Artifact` 状态仍是 `scaffolded`，只实现了作为 App launch payload 的 `app.web-bundle.v1`；
- `clients/artifact-viewer` 只有 TypeScript state 草图，没有真实 renderer；
- `AgentTaskInput.output_artifact_types` 和 `AgentEvent.ArtifactCreated` 已在 v1 Proto 中存在，但没有
  working materialization path；
- generic `AppendTaskEvent` 目前可以接收一个 Provider 构造的 `ArtifactCreated` 引用，Core 不会证明
  该 Artifact 存在、属于同 owner/Project/task，因而不能把它做成可点击的安全入口；
- Fake、DeepSeek、Generic CLI 当前都如实不声明 structured artifact working；
- Desktop Timeline 只显示 `Artifact · <type>` 文本，没有 Artifact Center、内容读取或安全审阅 UI；
- `ListArtifacts(project_id)` 仍明确不支持，现有 Web Bundle metadata 也是 owner-scoped、非 Project
  review artifact。

Rootless Podman 的真实证据受当前宿主 user namespace 条件阻塞；Artifact review 则可以完全使用现有
六进程、PostgreSQL、Fake Harness 与 Chromium 闭合。它也是后续 Artifact/Diff 审批、Agent 上下文引用、
通知 deep link、移动端 review 和 Repair candidate review 的共同前置能力。

本任务只做两个文本型、只读 review subtype 和 Fake Harness 的真实输出链路。不要把 Object Store、
图片/PDF、patch apply、评论审批、DeepSeek 结构化输出或 App Bridge `artifact.read/write` 混入本切片。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时本地 `main` 为 `3f38cf0`，工作树在新增本文件前干净，领先 `origin/main` 6 个提交。
  执行时必须重新检查，以当时本地历史为准；不得 reset、丢弃用户改动或从落后的远端重建分支。
- `docs/status.json` 是唯一进度事实源：Artifact 为 `scaffolded`；Harness Broker、Agent Task Router、
  Desktop Shell、Access Gateway 为 `working`；Runtime 的 container slice 与 Reliability 仍受真实
  Podman/cross-process 证据限制。
- `api/proto/workos/artifact/v1/artifact.proto` 的 `CreateArtifact` 只接受 `WebBundleContent`；
  `GetArtifact`/`ListArtifacts` 只返回 metadata，公开 API 不返回 bundle bytes。
- `internal/core/artifact` 已按 `domain → application → ports ← adapters` 实现 immutable Web Bundle、
  durable idempotency、owner-scoped metadata 与私有 asset read。新 review subtype 必须复用模块，
  不能创建第二个同义 Artifact service 或手写一套 DTO。
- migration `006_web_bundle_artifacts.sql` 属于 workos-core Artifact，并已执行、受 checksum 保护；它的
  owner-only Web Bundle 语义不得被伪装成 Project review artifact。
- `api/proto/workos/agent/v1/agent.proto` 已有 `output_artifact_types`、`ArtifactCreated.artifact_id` 和
  `artifact_type`。这些字段号不可移动或复用；可以 additive 扩展，但本切片无需把内容塞入 AgentEvent。
- `api/proto/workos/taskexecution/v1/execution.proto` 是 harness-host worker → Core 的 private、lease-bound
  执行协议；它当前只有 generic `AppendTaskEvent`，尚无 typed artifact materialization command。
- `internal/harness/ports.Emit` 当前只携带 `AgentEvent`。如果扩展 provider emission，canonical artifact
  output 必须位于中立 Harness port/protocol，Provider-specific 类型只能留在 adapter。
- Fake Provider 当前 deterministic 且 `structured_artifacts=false`；DeepSeek 与 Generic CLI 也必须继续
  如实为 false，除非本任务为该 adapter 建立真实、类型化、受限输出证据。本任务只要求 Fake 升级。
- `HarnessCapabilities` 已有 bool `structured_artifacts`，但没有 exact supported artifact type 列表。
  一个笼统 true 不能证明 adapter 支持任意字符串类型。
- `AgentTimeline` 位于 `clients/agent-center`；`clients/artifact-viewer` 只有接口草图；Desktop Window
  Manager 已支持普通 Agent/System Monitor/Device Center/App Surface 窗口，Artifact Viewer 应成为
  普通内部窗口，不能打开新浏览器 tab。
- Gateway 已 allowlist public `ArtifactService` 并在生产模式由持久 Device Session 注入可信 owner/device；
  private TaskExecution/HarnessHost RPC 继续不得进入 Gateway allowlist。
- migrations `001`–`020` 已在验收卷执行并被校验，禁止修改。预计本任务使用新的 `021`；若执行时该
  编号已占用，按最新编号顺延。
- 验收 PostgreSQL volume 可能包含用户已有数据。禁止 `docker compose down -v`、TRUNCATE、broad
  DELETE、wildcard DROP 或为了测试重建历史 volume。
- 本任务不需要 DeepSeek、OpenAI、Codex、GitHub 或任何真实外部凭据。不得使用、保存、转述、验证或
  搜索聊天中曾出现的真实 Key；DeepSeek 回归只运行仓库已有 keyless fixture。

## 安全、内容与隐私边界

Artifact 内容是用户内容，不是日志字段。以下边界不可放宽：

- Provider/harness-host 只能提交 `output_key + title + typed content`；不能提交 owner、project、task、
  artifact ID、digest、created time、content ref、event sequence 或数据库状态。
- Core 只能从当前 active task lease 派生 task、owner、Project 与 provider snapshot；request body 中即使
  增加同义字段也必须拒绝或根本不存在。
- public `ArtifactCreated` 必须由 Core 在成功 materialization 后构造。generic `AppendTaskEvent` 对
  Provider 自带的 `ArtifactCreated` 必须 fail closed，不能只相信“private network”。
- Markdown 和 diff 只作为 inert text 保存与渲染；不得执行 HTML、JavaScript、SVG、Mermaid、iframe、
  remote image、CSS、shell、patch 或文件系统操作。
- 禁止用 `dangerouslySetInnerHTML` 渲染 Artifact。若引入 Markdown parser，只能消费 AST 并映射到
  明确允许的 React 元素；raw HTML、image、embedded URL、event handler 一律关闭。
- 第一版 Markdown link 不得绕过 WorkOS Browser/窗口策略打开任意新 tab。可以显示安全链接文本，或仅
  对明确允许 scheme 提供无导航的复制行为；不要顺手实现外部 Browser App。
- Unified Diff 的路径、hunk header 和行内容都不可信，只能转义显示。不能根据 path 读取 workspace、
  解析宿主绝对路径、提供 Apply 按钮或声称 patch 可应用。
- Artifact 内容、title、goal、diff path 不进入日志、trace attribute、error message、任务记录、状态
  evidence 或截图 fixture 之外的位置。日志只能记录稳定 ID、type、size、result code。
- public errors 必须固定、净化且 owner/project scoped；unknown、foreign、wrong-project artifact 不得形成
  存在性 oracle。
- Artifact 内容可以包含普通用户文本；本任务不虚构 Credential Vault 或 DLP。测试 fixture 必须使用
  明显合成内容，禁止把真实凭据或真实用户数据放入 Artifact/截图。

## 固定 subtype 与上限

只实现以下 canonical type：

| Artifact type          | media type                     | 语义                          |
| ---------------------- | ------------------------------ | ----------------------------- |
| `document.markdown.v1` | `text/markdown; charset=utf-8` | 安全 Markdown review document |
| `code.unified-diff.v1` | `text/x-diff; charset=utf-8`   | 只读 unified diff             |

除非 ADR 用仓库证据证明必须调整，使用以下固定上限：

- 每个 task 最多请求 2 个 output artifact type；无重复，保持请求顺序；
- 每次 provider output 只产生一个 artifact；`output_key` 使用
  `^[a-z][a-z0-9._-]{0,63}$`，且在一个 task 内稳定；
- title：trim 后 1–200 Unicode code points，valid UTF-8，无 C0/C1 control；
- content：canonical UTF-8，1–512 KiB；允许 LF 与 TAB，拒绝 NUL、其他 C0/C1；CRLF 统一为 LF，
  bare CR 拒绝；不 trim、不自动补末尾换行；
- 最多 20,000 行，单行 UTF-8 byte length 最多 16 KiB；
- private materialization request 解码前最大 768 KiB；public review read/list/get 请求最大 32 KiB；
- public list page size 默认 50、最大 100；cursor 为 canonical lowercase UUIDv7；
- digest 使用 `sha256:<64 lowercase hex>`；canonical encoding 必须带明确 format version 与长度前缀，
  至少覆盖 artifact type 与 normalized content bytes；create/output request digest 还要覆盖 Project、
  task、output key、normalized title 和 content digest；
- Artifact ID 使用 server-minted UUIDv7，时间使用 UTC。

不要用数据库/HTTP 默认无限上限，不要在 UI 截断后仍假装服务端内容无限安全。若 viewer 为防渲染成本
设置更低的展示上限，服务端 contract 与 UI 提示必须一致且测试覆盖。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`docs/ui/README.md`；
   - `docs/structure.md` 的 0、1.3、2–5、7、10–11、15–18；
   - `docs/architecture/implementation.md` 与 `docs/status.json`；
   - ADR `0001`–`0007`，尤其模块 owner、App Bridge、task policy 和 Gateway identity 边界；
   - `docs/tasks/20260825-minimal-web-bundle-surface.md`、
     `docs/tasks/20260828-minimal-project-agent-app-bridge.md`、
     `docs/tasks/20260829-app-agent-approval-budget-policy.md`、
     `docs/tasks/20260830-lan-device-pairing.md`；
   - `api/proto/workos/{artifact,agent,taskexecution,harness}/v1`；
   - `internal/core/artifact`、`internal/core/agent`、`internal/core/orchestration` 与全部测试；
   - `internal/harness/{ports,broker,worker,adapters/fake,adapters/deepseek,adapters/genericcli}`；
   - `cmd/workos-core`、`cmd/harness-host`、`internal/gateway/gateway.go`；
   - `clients/artifact-viewer`、`clients/agent-center`、`clients/window-manager`、`sdk/agent-sdk`、
     `sdk/protocol`、`apps/desktop-web`；
   - migration `006`、`009`、`014`、`020`、migration runner/checksum tests、`sqlc.yaml`；
   - Agent task、DeepSeek fixture、restart、App Bridge 和 Desktop Playwright tests。

2. 运行并记录基线：

   ```sh
   git status --short --branch
   git log --oneline --decorate -15
   git branch -vv
   git diff --check
   make bootstrap
   make check
   make test-integration
   make test-e2e
   make test-deepseek-fixture
   make test-lan-pairing
   ```

   `make test-podman-fixture` 不是本任务门禁；当前宿主已知不满足 rootless Podman 前提，不要用 Docker、
   rootful Podman 或 privileged container 冒充通过。其他基线失败必须判断归属并写入任务记录，禁止删
   volume、删测试、放宽断言或固定成功响应。

3. 从执行时本地 `main` 创建独立 branch/worktree，建议：

   ```text
   feat/project-artifact-review
   ```

   若已有同名 branch/worktree，先检查并认领，不得覆盖另一位智能体工作。

4. 从 `docs/tasks/TEMPLATE.md` 创建：

   ```text
   docs/tasks/20260830-project-artifact-review.md
   ```

   初始状态 `active`，写清 subtype、content bounds、task provenance、output idempotency、event publish、
   migration owner、错误矩阵、UI、视觉证据、测试和非目标。

5. 新增聚焦 ADR，建议：

   ```text
   docs/decisions/0008-project-agent-artifact-review.md
   ```

   ADR 至少固定：为什么 review artifact 属于 Core Artifact、为什么 Project/task/output key 是 provenance
   而非 Provider 输入、为什么 generic `ArtifactCreated` append 被禁止、materialization 与 event publish
   的 crash-window/idempotency 协议、为什么 Web Bundle bytes 仍不公开、为什么 Markdown/Diff 只读且不
   apply、为什么 Fake 可以先支持而 DeepSeek 仍报告 unsupported。

6. 按 `docs/ui/README.md` 建立任务级 before：

   ```text
   docs/ui/desktop-web/changes/20260830-project-artifact-review/before/
   ```

   从任务基准提交使用固定 Chromium `1440x900`、`deviceScaleFactor: 1` 和确定性 fixture，至少保留当前
   Agent Timeline 的 artifact 纯文本/无 Artifact Center 基线。不得在截图中出现真实 goal、diff、路径、
   task ID、credential、Cookie 或用户数据。

## 必须保持分离的事实

| 事实                                 | 唯一 owner                       | 语义                                                             |
| ------------------------------------ | -------------------------------- | ---------------------------------------------------------------- |
| task/input/provider/lease/event      | workos-core Agent                | durable task execution 与 canonical event sequence               |
| provider capability/type mapping     | harness-host Adapter/Broker      | adapter 真实支持哪些 canonical output type                       |
| review artifact metadata/content     | workos-core Artifact             | immutable Project/task-bound review fact                         |
| output key → artifact adjudication   | workos-core Artifact             | provider retry/lost response 的 durable materialization identity |
| artifact event sequence/publication  | workos-core Agent                | task timeline 中唯一、Core-minted reference                      |
| Project ownership/archive state      | workos-core Project              | public create/list/read orchestration 的 owner scope             |
| browser viewer/window selection      | Desktop / artifact-viewer client | 非授权状态；服务端每次读取仍校验 owner/Project                   |
| Web Bundle files/launch verification | 既有 workos-core Artifact        | App launch payload，不因 review subtype 变成公开文件下载         |

禁止：harness-host SQL、Artifact repository 直接查询 Agent/Project 表、Agent repository 直接查询 Artifact
表、跨模块 FK、共享可变 entity、Provider 自建 Artifact ID、把 event payload 当内容权威，或让 Desktop 仅凭
Timeline 中的 ID 绕过 ArtifactService。

跨模块组合放在中立 ports / `internal/core/orchestration`；domain 不得 import pgx、Connect、HTTP、生成
Proto、React、Markdown parser 或其他模块 adapter。

## 必须完成的目标链路

### 1. Additive canonical protocol

先修改 `api/proto`，再运行 `make generate`。建议契约可调整命名，但语义必须满足：

- Artifact API 为 review content 提供 typed、bounded read response；不要把任意 `bytes` + client-chosen
  media type 当作类型系统。可以新增 `GetReviewArtifact` RPC，response 使用 oneof
  `markdown | unified_diff`，并同时返回 authoritative `Artifact` metadata。
- `Artifact` additive 暴露 review provenance 所需的 sanitized 字段（至少 `project_id`、可选
  `source_task_id`）；Web Bundle 的既有字段与空 project 语义保持兼容。
- `ListArtifacts(project_id)` 对 review artifact 成为 working，按 UUIDv7 稳定排序、limit+1 探测；
  unknown/foreign/archived Project 的公开语义必须在 ADR/测试中固定。现有 owner-wide Web Bundle list
  不能因新 subtype 回归或泄漏 bytes。
- private `TaskExecutionService` 新增 lease-bound materialization RPC（例如 `AppendTaskArtifact`）。request
  只允许 lease/worker、stable output key、title、typed content；response 返回 Core-minted metadata 与
  已持久化的 `ArtifactCreated` event。不得接受 owner/project/task/artifact ID/digest/time/sequence。
- `HarnessCapabilities` additive 增加 exact `supported_artifact_types` 列表；
  `structured_artifacts=true` 只有在列表非空且 adapter 真实支持时成立。bool/list 漂移视为 provider
  capability corruption/unavailable，不能 silently assume all types。
- 保留 `AgentEvent.ArtifactCreated` 的现有字段号。内容绝不进入 AgentEvent；如新增 title 等便利字段，
  它只能来自已持久 Artifact 的 sanitized projection，且 UI 仍以 ArtifactService 为 authority。
- public/task execution request 都设置构造层 `WithReadMaxBytes`；gzip/JSON 解码后的超限必须在业务代码前
  `ResourceExhausted`，合法最大请求要有 headroom 测试。
- 所有 v1 删除字段/enum 值必须 reserved；本任务应只做 additive 变更，并运行 Buf breaking against
  执行时 `main`。

不要把 review content 放进 `google.protobuf.Struct`、tool output、provider raw event 或
`RunCompleted.summary` 后让 Desktop 猜类型。

### 2. Artifact-owned immutable review facts

扩展 `internal/core/artifact`，保留标准依赖方向：

```text
domain → application → ports ← adapters/postgres
                              ← transport/connect
```

要求：

- domain 定义两种 canonical type、title/content/output-key grammar、CRLF normalization、line/byte bounds、
  media type 和 versioned digest；所有入口共享同一实现，禁止 transport/worker/renderer 各写一套规则。
- review Artifact 永久绑定 owner、Project、source task、provider output key、type、title、content digest、
  byte/line counts、created time。公开 metadata 不含 owner ID、raw content 或 storage locator。
- content immutable；本任务没有 Update/Delete。重复 task output 使用 durable key 裁决，而不是覆盖旧 row。
- same task + same output key + same canonical request 精确 replay第一次 Artifact 与事件；same key + different
  type/title/content 稳定冲突并使 run fail closed；失败校验不消费 key。
- 两个不同 task、owner 或 Project 可以复用同一 output key，彼此独立。
- 并发相同 output 只有一个 Artifact；并发不同 content 同 key 恰一个 winner，loser 不留下 orphan content、
  request mapping 或第二个 public event。
- store 每次 read/replay 都重验 UUIDv7、type/media、title/content bounds、digest、counts、UTC timestamps、
  owner/project/task/output binding。stored corruption → sanitized `Internal`，不能伪装 `InvalidArgument`、
  `NotFound` 或 `Unavailable`。
- PostgreSQL transient error 通过现有 `dbtransient` 类型/SQLSTATE 分类为 sanitized `Unavailable`；不要读
  constraint 名或 error text 做业务判断。
- 新 migration（预计 `021_project_review_artifacts.sql`）只属于 workos-core Artifact；不得修改 `006`
  或其他历史 migration。表/索引/CHECK 下沉 canonical type、长度、digest、timestamps 和唯一 output key
  约束；不得建立到 Agent/Project 表的跨模块 FK。
- 现有 Web Bundle create/get/list/private asset verification、Registry registration 与 Surface launch 全部
  保持兼容。review read RPC 对 Web Bundle 必须明确 fail closed，绝不能返回 bundle file bytes。

如果为了统一 `ListArtifacts` 需要跨两个 Artifact-owned subtype 表做 ordered union，可以在 Artifact
adapter 内完成；不要为省事把 Web Bundle 迁成伪 Project artifact，也不要在 application 内加载全部历史
数据再分页。

### 3. Lease-bound materialization 与 event publication

必须设计并测试一个真正可恢复的协议，不能假装一次 RPC 横跨 harness-host 与 Core PostgreSQL 是原子操作。

固定语义：

1. worker 只有持有 task 的 active lease 才能 materialize；wrong worker、expired/replaced lease、terminal
   task 均拒绝，且不创建 Artifact。
2. Core 从 lease 对应 task 的 immutable snapshot 派生 owner/Project/provider；global-scope task 在本切片
   不允许 Project review output，必须在入队前或 materialize 前明确拒绝。
3. Artifact create/output mapping 先以 durable canonical digest 裁决。response loss 后，同 lease 或后续
   reclaim worker 用相同 output key/content 可以取得同一个 Artifact。
4. Agent timeline 的 `ArtifactCreated` 只能由 Core 根据已验证 Artifact projection 构造，并且对
   `(task, artifact)` 最多发布一次；event sequence 必须保持 task 内严格递增。
5. generic `AppendTaskEvent` 收到 `ArtifactCreated` 必须拒绝。Provider 不能跳过 materializer 伪造 foreign
   ID/type。
6. materialization RPC 返回失败时，worker 不得继续发布对应 `RunCompleted`。provider run 应得到明确失败，
   由现有 worker 规则产生至多一个净化 terminal failure；不得将 Artifact 内容放入 failure reason。
7. Core crash、worker crash、Artifact 已创建但 event response 丢失、event 已提交但 RPC response 丢失、
   lease expiry/reclaim、owner cancellation 与重复 provider emission 都必须收敛：无重复 Artifact、无重复
   ArtifactCreated、无 terminal 后继续写普通事件、无永远公开但永远不可引用的半成品。
8. 若实现使用 persisted operation/publish state，只有完成 publication 的 Artifact 才出现在 public list/read；
   pending operation 必须能由合法 retry/reconcile 收敛。若使用 Core 内单事务协调，必须通过中立 ports/
   transaction boundary 保持模块所有权，不能让一个模块直接 SQL 另一模块表。

任务记录和 ADR 必须逐个列出 crash window 与恢复动作。只在 happy path 用内存 map 去重不算完成。

### 4. Provider capability 与 Fake Harness output

- Fake Provider 才能在本任务中声明：

  ```text
  structured_artifacts = true
  supported_artifact_types = [document.markdown.v1, code.unified-diff.v1]
  ```

  它必须真实产生 bounded canonical output，并通过 worker/private RPC/Core/PostgreSQL 进入 Timeline；不能让
  browser test 直接插 DB、mock ArtifactService 或伪造 `ArtifactCreated`。

- Fake output 必须由显式 `output_artifact_types` 驱动，不使用 goal 中的隐藏 magic string。每种 type 的
  output key、title、content 是确定性的，测试可以复现；只使用合成路径/文本。
- Core Task Router 在创建 fresh task 前校验：type grammar、数量、重复、Project scope、provider exact
  supported list。provider 不支持 → sanitized `FailedPrecondition`，零 task/outbox/lease/artifact 副作用，
  不 fallback 到另一个 provider。
- replay 继续返回第一次 provider/task snapshot，不因 Catalog 后续变化重新裁决。不要在本任务顺手改变
  Project binding 语义。
- DeepSeek 与 Generic CLI 保持 `structured_artifacts=false`、supported list empty，并继续拒绝非空
  `output_artifact_types`。不得调用真实 DeepSeek API 来获得本任务证据。
- bool/list 组合、unknown/duplicate type、adapter 发出未请求 type、同一请求 type 输出零次或多次的语义
  必须明确。建议每个 requested type 恰好一个 terminal 前 output；任何缺失/多余均使 run fail closed，
  不要把“provider completed”冒充完整 artifact contract。
- provider raw content 不得进入 log。错误只报告 type/output key 的安全规则类别，必要时只记录 task ID。

### 5. Public read/list 边界

- `GetArtifact` 继续只返回 metadata；新增 typed review read 返回 metadata + exact canonical content，且只对
  authenticated owner 可用。
- Project list 必须先通过中立 Project directory/port 验证同 owner Project；Artifact module不得 import
  Project adapter 或直接 SQL `projects`。是否允许已归档 Project direct read/list 必须在 ADR 固定，并与
  Desktop 行为一致；foreign/unknown 不泄漏。
- `source_task_id` 是 provenance，不是授权 token。知道 task/artifact UUID 不能跨 owner/Project 读取。
- list 使用 repository `limit+1`，恰好满最后一页不生成 phantom token，页面间无重复/遗漏。不要在
  transport 猜 next token。
- metadata/content row 必须来自同一个 authoritative read snapshot，避免 metadata type 与 content subtype
  的 TOCTOU/错配。response 只能返回 canonical media type，client 不能覆盖。
- `GetReviewArtifact` 对 Web Bundle、unknown subtype、stored digest/count drift 与 transient outage 的错误
  矩阵分别固定并测试：unsupported/not-reviewable、Internal、Unavailable 不能混淆。
- Gateway 只公开 Artifact public service；private materialization、TaskExecution、HarnessHost、Core
  orchestration RPC 必须继续 deterministic 404。Gateway 必须继续剥离客户端 identity/Cookie/bridge
  headers 并从 Device Session 注入身份。

### 6. Artifact Center 与安全 Viewer

把 `clients/artifact-viewer` 从 state 草图变成可测试组件/纯 renderer，并在 Desktop 中加入普通窗口：

- Dock 增加明确的 `Open Artifact Center` 按钮；它打开可关闭、可移动的普通内部 window，不是永久侧栏，
  不打开浏览器新 tab。
- Artifact Center key/remount 于 active Project，列出 title、type、created time/source task 的安全摘要；
  支持分页/Load more、loading、empty、Unavailable、NotFound 和 retry 状态。
- Project 切换时清空旧 Project list、selected content、error 和 in-flight result；使用 generation/abort guard，
  迟到响应不能在新 Project 窗口中出现。
- 选择 list item 后读取 authoritative typed content，并在同一窗口或新的 artifact-viewer window 中显示。
  同一 artifact ID 重复点击不创建无限重复窗口。
- Agent Timeline 的 `ArtifactCreated` item 变成可访问按钮；点击后仍调用 ArtifactService 获取权威内容。
  foreign/missing/wrong-project reference 显示固定 `Artifact unavailable`，不能泄漏详情或 crash Timeline。
- Markdown renderer 至少支持 headings、paragraph、emphasis、lists、blockquote、inline/fenced code；raw HTML、
  image、embedded object、style 和 active URL 全部 inert/disabled。所有文本由 React escaping 输出。
- Diff renderer 至少识别 file header、hunk header、addition、deletion、context/no-newline marker，并显示
  line-safe styling；畸形但符合文本上限的 diff 要么作为 plain diff text 安全显示，要么以固定 invalid
  content verdict fail closed，规则必须由共享 parser 测试固定。
- Viewer 不提供 edit/apply/download-as-executable，不读取本地文件，不把内容放进 URL、DOM attribute、
  localStorage/sessionStorage、window title、telemetry 或 console。
- content loading 时不要复用前一个 artifact 的正文；失败后旧内容必须清空。
- 保持现有 Auth Gate、Device Center、Agent Center、App Library、System Monitor、App Surface 和 App Bridge
  行为不回归。

### 7. UI 视觉证据

以确定性 Fake Harness / API fixture 采集至少：

```text
artifact-center--project-list--1440x900.png
artifact-viewer--markdown-review--1440x900.png
artifact-viewer--unified-diff-review--1440x900.png
agent-center--artifact-created--1440x900.png
```

要求：

- fixture 在导航/请求前建立，固定 viewport、deviceScaleFactor、locale、timezone 和时间；
- 固定 Project/title/content，隐藏随机 UUID、实时 timestamp、cursor、session facts；
- diff 路径只能是合成相对路径，例如 `src/example.ts`；
- 不依赖历史 PostgreSQL volume、真实 Provider、真实 task 内容或外网；
- `after/` 与 `current/` 同名文件逐字节一致；
- `notes.md` 记录采集命令、fixture、viewport 和有意差异；
- 截图之外仍要有真实跨进程 browser E2E，不能用 route mock 截图冒充后端链路。

## 测试与验收矩阵

### Domain / application

- canonical type、title/output-key/content grammar 的边界值；UTF-8、C0/C1、NUL、CRLF/bare CR；
- byte/line/single-line limits，normalized content 与 digest golden vectors；
- same content 不同 CRLF 表示得到相同 digest；空格/末尾换行等有效内容变化必须改变 digest；
- same task/output key replay、different content conflict、不同 task 独立；
- stored UUID/type/media/digest/count/time/provenance corruption → Internal；
- project/global task、terminal/lease lost/wrong worker、unrequested/unsupported/duplicate output；
- generic ArtifactCreated append rejection；Core-minted event projection正确。

### PostgreSQL / concurrency / restart

- forward-only `021` 在空库和已有 `001`–`020` 数据上迁移；旧 migration checksum 不变；
- 两个真实 pool 并发 same output only one artifact/event；different payload one winner/no orphan；
- injected transaction failure 不消费 mapping、不留下 visible half-state；
- response-loss replay 与 worker reclaim 返回相同 artifact/event；
- restart 后 list/get/content、output replay 和 event resume 仍成立；
- foreign owner/project/task 与 malformed cursor 不泄漏；
- exact-full-last-page 无 phantom token；
- PostgreSQL transient disconnect → Unavailable，恢复后成功；corruption 不被分类成 transient。

### Harness / protocol

- Fake capability bool/list 一致并真实输出两类；DeepSeek/Generic remain false/empty；
- Task Router 对 unsupported provider/type 在入队前拒绝且零副作用；
- worker 保持 output 与普通 Agent events 的顺序，materialization 完成后才允许 terminal；
- provider 发 raw `ArtifactCreated`、额外 type、重复 output key、超限 content 均 fail closed；
- private request oversize/gzip bomb 在 handler decode 前拒绝；合法最大内容通过；
- Buf format/lint/breaking 与 Go/TS generated output idempotent。

### Public API / Gateway

- real Gateway + Device/dev identity → Core Artifact metadata/list/read；
- public 请求不能访问 private TaskExecution materializer；
- owner/project isolation、Web Bundle content non-disclosure、fixed error matrix；
- request limits、unknown enum/oneof、server-owned field injection；
- production Gateway session/header cleaning 回归。

### Browser

- 真实 Fake task 请求 Markdown output → durable event → timeline click → exact viewer content；
- Artifact Center 重载后仍列出同一 artifact；Core/harness-host restart 后仍可读；
- Diff viewer 使用真实 persisted diff；
- Project A 的迟到 list/content response 不污染 Project B；
- missing/corrupt reference 的 sanitized unavailable UI；
- malicious Markdown HTML/script/image/link 与 malicious diff path 作为 inert text，不执行、不发网络请求；
- 现有 App Bridge、approval/quota、Web Bundle、DeepSeek fixture、LAN Auth E2E 不回归。

## 必跑命令

根据实现补充聚焦命令，但至少真实运行并在任务记录中写结果：

```sh
make generate
make generate
git diff --check

make check
make test-integration
make test-e2e
make test-deepseek-fixture
make test-lan-pairing

buf breaking --against '.git#branch=main'
go test -race ./internal/core/artifact/... ./internal/core/agent/... ./internal/harness/...
```

另新增一个明确的 artifact browser/fixture 门禁（名称可调整），例如：

```sh
make test-artifact-review
make capture-artifact-review-visual
```

`make test-artifact-review` 必须启动真实 PostgreSQL、Core、harness-host、Gateway 与 Chromium，并走
Task Router/Fake Provider/private materialization/public read；不能只是 Vitest route mock。测试完成只清理
自己以精确 ID 创建的 fixture 数据/临时目录，不删除共享 volume。视觉 capture 可以使用固定 network
fixture 以保证像素确定，但必须与真实链路门禁分开命名和说明。

完成 `make generate` 后再运行一次，第二次不得产生任何 tracked diff。检查生成 Go/TS 与 sqlc 文件，
禁止手改 `gen/`、`src/gen/` 或 README 状态区块。

## 文档与状态同步

完成后更新：

- `docs/architecture/implementation.md`：Artifact subtype、task materialization、owner/Project/provenance、
  crash recovery、public read、viewer 安全边界；
- ADR-0008：所有决定与被拒绝方案；
- `docs/tasks/20260830-project-artifact-review.md`：范围、migration、测试、截图、风险、提交；
- `docs/status.json`：只有真实 PostgreSQL + Fake Harness + browser 链路通过后，Artifact 才可从
  `scaffolded` 升为带明确 subtype 限定的 `working`；否则最高仍为 `scaffolded`；
- Harness Broker / Agent Task Router / Desktop Shell evidence 只追加本任务真实证明的内容；
- README 状态区块仅由 `make generate` 更新；
- `docs/ui/desktop-web/changes/20260830-project-artifact-review/{before,after}/`、`notes.md` 与
  `docs/ui/desktop-web/current/`。

不要把 Fake Artifact 证据写成 DeepSeek structured artifact working，也不要把 Markdown/Diff 的内联
PostgreSQL 存储描述为 generic Object Store、Artifact editing、patch apply 或移动端 review 已实现。

## 明确非目标

- DeepSeek/Codex/Generic CLI 的 structured artifact 输出；
- 真实 Provider API smoke 或任何真实 API key；
- PDF、image、chart、JSON、HTML、binary、archive、notebook、video、audio；
- generic Object Store、filesystem blob store、S3、content CDN；
- Artifact edit/delete/versioning/comments/approval/share/export；
- apply patch、Git checkout/commit、workspace file read/write、syntax execution；
- Agent `context_refs` 读取 Artifact、RAG/indexing、Indexer；
- App Bridge `artifact.read` / `artifact.write`；已有 grant 名称仍只是未实现 capability；
- notification/deep link、mobile viewer、offline cache；
- Credential Vault、Provider credential migration；
- Rootless Podman、Reliability real acceptance、Repair/Deployment/Rollback；
- mDNS、native certificate pinning、public-internet access。

任何上述能力必须保持明确 unavailable/unimplemented，禁止返回固定成功或空内容冒充 working。

## 完成定义

只有同时满足以下条件才可标记 done：

1. 两种 canonical review subtype 有明确协议、边界、digest 与 immutable PostgreSQL authority；
2. Provider 不能伪造 ArtifactCreated，Core 只从 active lease 派生 provenance；
3. materialization/output/event 对并发、response loss、crash/reclaim、restart 幂等收敛；
4. Fake Provider exact capability + typed output 是真实实现，DeepSeek/Generic 如实 unsupported；
5. public list/get/read owner/Project scoped，Web Bundle bytes 仍不公开；
6. Desktop Timeline/Artifact Center/Markdown/Diff viewer 走真实 API，并安全渲染恶意 fixture；
7. unit、race、PostgreSQL integration、restart、Gateway、browser E2E 和视觉证据全部通过；
8. `make generate` idempotent，`make check`、Buf breaking、既有 DeepSeek/LAN/E2E 回归通过；
9. ADR、implementation、task、status、generated README、before/after/current 同步；
10. 工作树只包含本任务文件，无凭据、日志、trace、视频、临时 DB、构建产物或真实用户内容；
11. 创建聚焦提交，任务记录写明 commit、验证命令、未决风险和下一步；不 merge、不 push。

## 交付前自查

```text
[ ] 我没有修改 migrations 001–020 或手改生成文件
[ ] 我没有让 harness-host/Provider 直接写 Artifact SQL
[ ] 我没有信任 Provider 提交的 owner/project/task/artifact ID/digest/time
[ ] generic AppendTaskEvent 明确拒绝 ArtifactCreated
[ ] Core-minted ArtifactCreated 引用一个已发布、owner/project/task-bound Artifact
[ ] same output replay 与 different-output conflict 在真实 PostgreSQL 并发/重启下成立
[ ] Markdown 没有 raw HTML/image/active URL/script 执行路径
[ ] Diff 路径和正文只作为 escaped inert text，不能 apply/read host files
[ ] Web Bundle public bytes 仍不可读，现有 Surface/App Bridge 没有回归
[ ] DeepSeek/Generic CLI 没有虚报 structured_artifacts
[ ] Artifact Center 的 Project 切换与迟到响应隔离已测试
[ ] UI before/after/current 使用确定性 fixture 且不含真实数据/凭据
[ ] docs/status.json 的状态没有超过真实证据
[ ] make generate（二次无差异）、make check、integration、E2E、fixture、LAN、race、breaking 全通过
[ ] 只提交本任务变更，不 merge、不 push
```
