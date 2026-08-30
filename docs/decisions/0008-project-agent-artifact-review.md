# ADR-0008：Project Agent 的 Markdown / Diff Review Artifact 纵向切片

- 状态：Accepted
- 日期：2026-08-30
- 关系：细化 ADR-0001 的模块所有权、ADR-0005 的 lease/task 边界与 ADR-0007 的 Gateway
  身份边界。覆盖 `docs/structure.md` 第 14 节（Artifact / Diff / Markdown Review）的第一阶段：
  只读、文本型、Project/task 绑定的 review artifact，两种 canonical subtype。

## 背景

Artifact 一直是 `scaffolded`：唯一可用 subtype 是作为 App launch payload 的
`app.web-bundle.v1`（owner-scoped，migration `006`）。`AgentTaskInput.output_artifact_types` 与
`AgentEvent.ArtifactCreated` 在 v1 Proto 中存在，但没有 materialization path；generic
`AppendTaskEvent` 甚至接受 Provider 自造的 `ArtifactCreated` 引用而不验证其存在或归属。本 ADR
固定第一条真实链路：Fake Harness 产出 bounded Markdown/Unified Diff → Core 以 active lease
派生 provenance、持久化并发布唯一 timeline 事件 → Desktop 安全只读审阅。

## 决策

### 1. Review artifact 属于 workos-core Artifact 模块

- Review artifact 是 immutable Project/task-bound 事实：owner、project、source task、output
  key、canonical type、title、digest、byte/line counts、created time、content bytes 全部存储在
  migration `021` 的 `workos_core.project_review_artifacts`，唯一 owner 为 Artifact 模块。
- 不新建第二个同义 service，不手写 DTO：复用 `internal/core/artifact` 的
  `domain → application → ports ← adapters`，与 Web Bundle 共享 metadata 读、分页、错误矩阵；
  Web Bundle 的 create/get/private asset 语义零回归，其 bytes 依旧不公开。
- `output key → artifact` 裁决映射（`project_review_artifact_outputs`）同样归 Artifact 所有：
  它是 provider retry / response loss 的 durable materialization identity。映射行额外记录
  `event_id / event_sequence / event_occurred_at` 三个 publication 引用，使 replay 能精确返回
  第一次发布的事件；事件本身与 task stream 仍归 Agent 所有，Artifact 表从不查询 Agent 表。
- 不建跨模块 FK：project/task 是 Core 侧已验证的 snapshot ID；liveness 每次经中立 port
  （`ArtifactProjectScope`，仅公开 list 使用）在运行期重验，绝不 join 其他模块 schema。

### 2. Provenance 由 Core 从 active lease 派生，Provider 只提供 key/title/content

- 私有 RPC `TaskExecutionService.AppendTaskArtifact` 的 request 只含 lease/worker、`output_key`、
  `title` 与 typed content oneof。owner、project、task、artifact ID、digest、created time、
  event sequence、数据库状态一律不存在于 wire 上，全部由 Core 在事务内从 lease 锁定的 task
  snapshot 派生或 server-mint。
- materialize 前校验：task 必须是 project scope（global task 明确拒绝 Project review output，
  提交层与 materialize 层双重 fail closed）；content type 必须在 task 的
  `output_artifact_types` 请求列表内；`(task, output_key)` 未消费且 `(task, type)` 未占用。
- Task Router 在创建任何 task 之前校验 provider 的 exact `supported_artifact_types`：
  `HarnessCapabilities.structured_artifacts=true` 只有在列表非空且每个请求 type 命中列表时成立；
  bool/list 漂移被 catalog 视为 capability corruption（整个 catalog read 返回 unavailable），
  永不 silently 展开"支持所有类型"。DeepSeek/Generic CLI 保持 false/empty，且对非空请求
  fail closed，Core 侧在入队前拒绝（FailedPrecondition、零副作用、不 fallback）。

### 3. Generic `AppendTaskEvent` 必须拒绝 `ArtifactCreated`

- Provider 自带的 `ArtifactCreated` 引用无法证明 artifact 存在、同 owner/Project/task、内容
  完整；"private network" 不是授权。`AppendTaskEvent` 收到该 oneof 一律 `InvalidArgument`
  fail closed，并有测试钉住。timeline 的 `ArtifactCreated` 只能由 materializer 从已持久化、
  已验证的 artifact projection 构造。

### 4. 单事务 materialization：crash window 与幂等收敛协议

一次 RPC 天然横跨 harness-host 与 Core PostgreSQL，不假装原子；协调器
（`orchestration.TaskArtifactMaterializer`）用一个共享事务把全部事实变成原子：

```text
BEGIN
  1. Agent port：按 (lease_id, worker_id) 锁定 task stream（FOR UPDATE）
     —— lease 失效 → ErrLeaseLost；task terminal → ErrTerminal；零写入。
  2. 校验 project scope 与 requested type（失败 → InvalidArgument，key 不消费）。
  3. Artifact port：读 (task, output_key) 映射
     —— 命中且 request digest 相同 → replay：返回首个 artifact + 首个 publication；
        命中但 digest 不同 → ErrOutputConflict（稳定冲突）。
  4. 纯 domain 准备：CRLF→LF、bounds、digest、title 归一、server-mint UUIDv7/时间。
  5. Artifact port：插入 artifact 行 + 映射行（ON CONFLICT DO NOTHING 是
     (task,key) 主键与 (task,type) 唯一索引的物理仲裁；0 行 → 事务内重读分类）。
  6. Agent port：插入 Core-minted artifact_created 事件（sequence = last+1）并推进
     last_event_sequence；task 状态不变。
COMMIT
```

- task 行在步骤 1 即被 FOR UPDATE 锁定：同一 task 的并发 materialization 在锁上串行化，
  后到者必然看到先到者已提交的映射 → replay 或冲突；`ON CONFLICT DO NOTHING` 只是存储层
  兜底仲裁。并发相同 output：恰一个 artifact、恰一个事件；并发不同 content 同 key：恰一个
  winner，loser 整个事务回滚、不留 orphan artifact/映射/事件，并得到稳定冲突。
- crash window 逐个列出：
  - commit 前崩溃：什么都没发生；provider run 继续或失败，重试走全新路径。
  - commit 后、RPC 响应丢失：映射已存在，同 lease（或 reclaim 后的新 lease）以相同
    output key/content 重放，返回同一个 artifact 与同一个事件，绝不二次发布。
  - lease 过期/被抢：旧 worker 的一切写入以 `ErrLeaseLost` 拒绝；新 worker 的 provider run
    重新产出（Fake 输出确定）→ 走 replay → 收敛；产出不同内容 → 稳定冲突 → run fail closed。
  - 事件已提交但无 RPC 响应、owner cancellation、重复 provider emission：均由映射 + 锁收敛；
    terminal 后的任何 materialize 被 `ErrTerminal` 拒绝，不存在 terminal 后继续写普通事件。
- 失败校验永不消费 key；冲突语义（同 key 不同 canonical request、同 type 二次 materialize）
  是稳定 verdict，使 run fail closed，而不是覆盖旧行或产生第二个 artifact。
- request digest（`workos.review-artifact-output.v1`，长度前缀编码）覆盖 project、task、
  output key、归一化 title 与 content digest（content digest 再覆盖 canonical type + 归一化
  内容字节）；提交顺序、服务端身份、时间不参与。

### 5. Web Bundle bytes 依旧不公开；review 读 typed

- `GetArtifact`/`ListArtifacts` 只返回 sanitized metadata（新增 `source_task_id` 与已有
  `project_id` 仅对 review subtype 非空）。新增 `GetReviewArtifact` 用 typed oneof
  （markdown | unified_diff）返回 canonical bytes 与 server-derived media type；对 Web Bundle
  ID 明确返回 unsupported（not-reviewable），绝不落回 bundle bytes 或伪装 NotFound。
- 公开错误矩阵固定：unknown/foreign/wrong-project → 统一 NotFound（无存在性 oracle）；stored
  corruption（UUID/type/media/digest/counts/binding 漂移，每次读都重验含 digest 重算）→
  sanitized Internal；transient（dbtransient 分类）→ Unavailable；不支持 → Unimplemented。
- Project list 经中立 Project scope port 校验同 owner；归档 Project 保持可读（immutable
  history，桌面审阅入口在归档后仍一致）；分页 repository limit+1 探测，满页无 phantom token。
- `ListArtifacts` 无 project 过滤时保持 owner-wide，并跨两个 subtype 表做 ordered union（同一
  模块内的 adapter 实现合并）；project 过滤时列出该 project 的 review artifacts。

### 6. Markdown 与 Diff 只读、inert、不可 apply

- 内容是 untrusted 文本：保存前 CRLF→LF、拒绝 NUL/其他 C0/C1（允许 LF/TAB）、≤512 KiB、
  ≤20,000 行、单行 ≤16 KiB UTF-8、valid UTF-8、不 trim 不补尾换行。
- Viewer 无 HTML parser、无 `dangerouslySetInnerHTML`、无 image/iframe/active link/event
  handler/network/storage/telemetry 路径：Markdown 只映射 heading/paragraph/emphasis/list/
  blockquote/inline+fenced code 到 React 转义文本；diff 只按 file header/hunk header/addition/
  deletion/context/meta 分类样式，路径与正文一律转义文本。不提供 edit/apply/download-as-
  executable/本地文件读取。本切片不引入 Markdown/语法高亮依赖，允许列表由共享组件测试钉住。
- Desktop 侧 Artifact Center 是普通内部窗口（非永久侧栏、不开浏览器 tab），key/remount 于
  active project，generation/abort guard 隔离迟到响应；Timeline 的 artifact 事件是可访问按钮，
  点击仍走 `ArtifactService` 拿权威内容；同 artifact 重复打开聚焦既有窗口。

### 7. Fake 先行、DeepSeek 如实 unsupported

- Fake Provider 是本切片唯一声明 `structured_artifacts=true` + exact list 的 adapter：它的
  输出真实穿过 worker → 私有 RPC → Core → PostgreSQL → Timeline → viewer（浏览器 E2E 门禁
  `make test-artifact-review` 走全链路，不是 mock）。每个请求 type 恰好在 terminal 前产出
  一个 deterministic、合成内容的 artifact；worker 在完成事件前校验"请求 type 全部 materialize"，
  缺失 → run 确定性失败；同 type 第二次 emission → 协议错误。
- DeepSeek/Generic CLI 继续如实 `structured_artifacts=false`、列表为空，并拒绝非空
  `output_artifact_types`。这不是能力降级而是诚实边界：为真实 provider 声明 structured
  output 需要该 adapter 的类型化、受限输出证据（本切片明确非目标）。

## 被拒绝的方案

- **Provider 上报 owner/project/artifact ID（或任何服务端身份）**：伪造面、跨 owner 泄漏与
  存在性 oracle；身份只能由 lease 派生。
- **generic AppendTaskEvent 放行 `ArtifactCreated`（信任私有网络）**：无法证明存在性/归属，
  timeline 会引用不可读或不可信的 ID；已在传输层 fail closed。
- **两阶段（先建 artifact、后发布事件）+ pending 状态**：引入"公开但不可引用"的半成品与
  额外 reconcile 状态机；单事务把 artifact + 映射 + 事件变成原子，crash window 收敛为
  "重试即 replay"。模块所有权由事务级中立 port 保持（各模块只写自己的表）。
- **把 review 内容塞进 `AgentEvent`/`RunCompleted.summary`/`google.protobuf.Struct`**：事件
  payload 成为内容权威，UI 被迫猜类型；内容留在 Artifact，事件只携带 Core-minted 引用。
- **把 Web Bundle 迁成伪 Project artifact / 为省事共享一张表**：006 的 owner-only 语义会被
  稀释；新 subtype 用独立表 + adapter 内 union 读，历史 migration 零修改。
- **用 markdown 渲染库（react-markdown 等）**：引入 rehype/raw HTML 通道与供应链面；受限
  allowlist 用 ~100 行映射即可，且允许/拒绝行为可被测试完整钉住。

## 后果

- Artifact 状态在真实 PostgreSQL + Fake Harness + browser 链路证据齐备后，可从 `scaffolded`
  升级为带 subtype 限定的 `working`；generic Object Store、PDF/image、edit/versioning、
  approval、`context_refs` 读取、App Bridge `artifact.read/write` 依旧明确 unavailable。
- v1 Proto 全部 additive（`Artifact.source_task_id=11`、`GetReviewArtifact`、
  `AppendTaskArtifact`、`supported_artifact_types=16`）；`ArtifactCreated` 字段号 16 不变；
  Buf breaking 对 main 通过。
- migration `021` checksum 已钉住；`001`–`020` 零修改。
