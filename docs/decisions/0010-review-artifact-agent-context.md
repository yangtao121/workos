# ADR-0010：Review Artifact 作为 Agent Context

- 状态：Accepted
- 日期：2026-08-30
- 关系：建立 ADR-0008 review artifact 之上的第一种 canonical Agent context；复用 ADR-0009 的
  authenticated harness execution channel 与 task lease 派生协议；受 ADR-0002 的 App Bridge
  边界约束（iframe 永不接受 context）。

## 背景

`AgentTaskInput.context_refs`（proto 字段已存在，`ContextRef{type,id,revision}`）从未有语义；
DeepSeek 明确拒绝所有 context。review artifact 已有 immutable content、digest、owner/project/task
provenance 与安全只读 viewer，正好成为第一种真实 context source。本 ADR 固定其第一版语义与
执行路径。

## 决策

### 1. 唯一 canonical ref type（第一版）

```text
type     = "artifact.review.v1"
id       = canonical lowercase UUIDv7 Artifact ID
revision = exact immutable Artifact digest："sha256:" + 64 lowercase hex
```

固定上限：每 task 最多 4 个 ref；保持请求顺序；拒绝 duplicate `(type,id,revision)` 与同 ID 不同
digest（同一 artifact 钉两个 digest 是矛盾，不是集合）；每个 artifact 仍受既有 512 KiB /
20k 行 / 16 KiB 行限；**resolved context 聚合 ≤ 1 MiB**（在 wire encode 前强制）。global task
不接受 Project Artifact context；App Bridge 首版完全不接受 context（canonical App payload 无
refs 字段，结构上不可达）。

### 2. Submission-time authorization（入队前，零副作用）

- Agent transport 校验 grammar/set discipline（type canonical、UUIDv7 小写、digest 格式、≤4、
  无重复、global+refs 拒绝）→ `InvalidArgument`，业务代码零执行。
- Task Router 在 provider 解析后、任何 task/outbox/lease 存在前：
  1. resolved provider 的 `supported_context_ref_types` 必须 exact 覆盖请求的 type 集合
     （bool-free：没有"支持全部"的魔法值），否则 `FailedPrecondition`，不 fallback；
  2. 通过中立 Artifact port（`ArtifactContextVerifier`，orchestration 包装 Artifact
     application 的 typed read）验证每个 ref：存在、同 owner、同 project、review subtype、
     stored digest 与 recompute digest 均等于 ref.revision。unknown/foreign/wrong-project/
     digest-mismatch 统一 `NotFound`（无 foreign 存在性 oracle）；stored corruption →
     `Internal`；transient → `Unavailable`。
- 成功后 task 仍只保存三元 ref（payload 即 protojson 输入快照）；ref 顺序进入既有 task
  payload digest/replay 事实。失败不创建任何行。idempotency replay 返回第一次 task，不按新
  capability/新 artifact 重新裁决。

### 3. Execution-time lease binding（ResolveTaskContext）

私有 authenticated execution listener（ADR-0009 通道）additive 增加 `ResolveTaskContext`：

- request 只含 `lease_id + worker_id`；不接受 refs/owner/project/provider；
- Core 在单事务内：Agent tx-scoped authority 锁定 active lease 并读取 task input → 重新校验
  canonical refs → Artifact tx-scoped read port 读取每个 pinned artifact 的 metadata+content
  → 重验 owner/project/subtype/exact digest → 聚合 ≤ 1 MiB → commit（锁持有即 lease 仍由该
  worker 持有）；
- response 按 ref 顺序给出 canonical ref type、artifact type、ID、digest、server-stored
  title、media type 与 bytes；**没有** storage path/content_ref/owner/project 或其他 Project
  信息；
- same lease 重复解析逐字节相同；lease lost/terminal/foreign worker 拒绝；Core restart 后仍可
  解析（refs 在 durable payload，artifacts immutable）；
- worker 在 provider 启动前解析一次，失败 → 唯一 terminal failure，不启动 provider；文档只传
  给该次 provider execution，不落本地文件、不缓存跨 task、provider 不能反向调用 ArtifactService。

### 4. TOCTOU 与 archive

- digest pin 使 ref 不可变：artifact 内容永不改变，因此 submission 与 execution 之间不存在
  内容漂移；execution 时的重验证捕获的是"ref 指向的事实漂移"（artifact 缺失/owner/project
  绑定漂移＝stored corruption → fail closed）。
- Project archive 后：artifact facts 保持可读（与 ADR-0008 review read 一致）；archive 不使
  context ref 失效，execution 重验证仍按 stored binding 判定。

### 5. Provider capability

`HarnessCapabilities.supported_context_ref_types`（additive, exact list）：列表值必须属于
canonical 词表（第一版仅 `artifact.review.v1`）、无重复、≤16；未知值 = capability corruption →
provider 整体投影 unavailable。Fake 与 DeepSeek 只在 materialized-context tests 通过后声明；
Generic CLI 保持空列表，且其 adapter 对非空 resolved context fail closed。

### 6. Prompt-injection 边界与不可信内容

- Artifact content 是不可信用户/模型内容，不是 system instruction。DeepSeek 将
  goal/context/output contract 编码成 versioned canonical JSON task envelope
  （`workos.deepseek.task-envelope.v1`）作为唯一 user content block；artifact bytes 放在
  `untrusted_contexts` 数组，JSON 转义/长度前缀表达边界，**禁止**手写 sentinel delimiter、
  字符串拼接伪 system prompt、或让 context 覆盖固定 persona。Envelope 由本地 fixture 验证：
  内层 goal 必须仍在 allowlist 内。
- Fake provider 以 deterministic receipt 证明收到 exact count/order/digest（消息只含
  artifact type + ref type + digest，不回显 content bytes 进 event）。
- resolved content 不进 task row、outbox、event、日志或错误。

### 7. Desktop "Use as Agent context"

- Artifact Center/Viewer 对当前 Project 的 review artifact 提供 "Use as Agent context"；
- 选择后 Agent Center composer 显示可移除 chip（title + Markdown/Diff 类型），不显示
  digest/内部 ID/content 预览；duplicate 选择幂等；≥4 给出固定可访问提示；submit request
  携带 exact id+digest；成功提交后清空 chips；
- Project 切换 abort + generation-invalidate 并清空旧 Project context；迟到响应不能进入新
  Project composer；
- ContextRef 只在可信 Desktop 主界面构造，不进入 iframe App Bridge SDK、DOM data attribute、
  URL、sessionStorage 或截图中的 raw content/digest。

## 错误矩阵

| 场景                                                        | verdict                                         |
| ----------------------------------------------------------- | ----------------------------------------------- |
| grammar/bounds/duplicate/同 ID 不同 digest/global+refs      | InvalidArgument，零副作用                       |
| provider 不支持请求的 context type                          | FailedPrecondition，零副作用                    |
| unknown/foreign/wrong-project/digest mismatch（submission） | NotFound（不可区分）                            |
| stored corruption                                           | Internal                                        |
| transient                                                   | Unavailable                                     |
| lease lost/terminal/foreign（execution）                    | ErrLeaseLost 语义（Aborted/FailedPrecondition） |
| aggregate > 1 MiB / invalid UTF-8 / adapter 拒绝            | child 启动前确定性失败                          |

## 非目标（保持 unavailable）

workspace/file/URL/RAG/index context、foreign Project context、App Bridge context API、
多 modal（图片/PDF/binary）context、context 编辑/reorder UI 超出 chip 增删、DeepSeek
structured output（阶段 C 另行 ADR）。
