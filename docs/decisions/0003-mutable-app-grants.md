# ADR-0003：Mutable Project App Grants 与立即撤销

- 状态：Accepted
- 日期：2026-08-29
- 关系：局部替代 ADR-0002 中"installation grant 在安装生命周期内不可变、更改只能卸载重装"
  的决定；ADR-0002 的 iframe 边界、bridge token、provenance、每次调用二次授权与 Gateway
  信任边界全部保持不变。

## 背景

Minimal Project-scoped Agent App Bridge 之后，授权仍是安装时的不可变快照：撤权只能卸载并
重装。本 ADR 为 Project App installation 建立可审计、可并发裁决、可立即失效旧会话的 grant
生命周期，同时不引入任何跨进程 schema 耦合。

## 决策

### 1. SetAppGrants 是 full replacement，不是增量 add/remove

一个请求完整表达用户想要的最终 grant 集合。选择该形态是因为 canonical digest、幂等重放、
并发裁决与 UI 审核都需要一个 order-independent 的请求基准：add/remove 的结果依赖历史顺序，
same key 重放无法与"第一次请求"做逐字比较。语义固定：

- `granted_permissions` 是完整目标集合；空数组（以及省略 repeated 字段）明确表示撤销全部，
  绝不回退为 manifest requested permissions；
- 输入 canonical 排序、去重、校验 grammar；目标集合必须是 exact pinned app version
  requested permissions 的子集；
- 客户端不能提交 app ID/version/manifest digest/requested set/grant revision/新 Project
  revision——这些全部由 Core 在事务内重新解析与裁决。

### 2. Project revision 与 installation grant revision 是两个独立事实

- Project revision 是整个 Project 聚合的 optimistic concurrency 基准与事件 sequence（既有）。
- installation grant revision 是单个 installation 的授权 epoch：从 1 开始，仅在 grant 集合
  真实改变时恰好 +1。
- 真实变更：grant revision +1 且 Project revision +1；installation 更新、Project revision、
  project event、outbox、idempotency result 在同一事务提交。
- same-set no-op：两个 revision、event、outbox、updated timestamp 均不变，但成功请求的
  idempotency key 仍被持久消费并可精确重放。
- 两者都不能由客户端提交或递增；grant mutation 与其他 Project mutation 由同一 Project row
  lock/guard 按 `expected_project_revision` 串行化，由数据库裁决，不依赖进程内 mutex。

### 3. 任何真实 grant 变更使旧 Surface 的全部 bridge 方法失效

effective capability 是 Surface session 创建时的快照；一旦 installation grant revision
变化（无论新增、移除还是集合相同路径之外的任何改变，也无论某个 capability 是否在新旧
集合中都存在），所有旧 session 的 App Bridge 方法一律失效。理由：

- epoch 不匹配即失败是最简单、可审计、无 per-capability diff 漏洞的规则；混合状态
  （共有 capability 继续可用、新增 capability 不出现）会扩大审计面并让"新权限生效"的
  语义依赖隐式 diff；
- 失效的是 App Bridge 方法，不是静态 Web Bundle 资产：installation 仍 active 时资产照常
  服务，iframe 可以继续渲染，但每个 bridge 调用都会在 Core 的 revision 比对处失败；
- 用户必须用新的 CreateSurface idempotency key 重新打开 Surface 才能得到新 grant 的能力。
  旧 create key 的重放在 revision 不一致时 fail closed（净化 `FailedPrecondition`），
  不铸造绑定旧 epoch 的可用新 token；public bridge 方法层的 epoch mismatch 净化为
  `PermissionDenied`，不透露 current revision 或 current grants。

### 4. "立即撤销"的线性化语义

- 线性化点是 Core Project transaction commit。
- commit 之后进入 Core authorization read 的新 run/watch 必须失败。
- 已在 commit 前通过 Core authorization 的并发请求可能完成；本任务不追溯删除已创建的
  durable Agent task——撤权不是 CancelTask，自动取消属于未来显式策略。
- 已打开的 watch stream 在 Core 下一次 polling reauthorization（既有 200ms 轮询）发现
  epoch mismatch 时终止，不再向旧 epoch 流送新事件。
- Desktop 在本地成功 Set 后 best-effort 关闭该 installation 的 open window/MessagePort 并
  `CloseSurface`；服务端安全不依赖该客户端动作。

### 5. 事件 `project.app.grants.updated.v1`

真实变更有版本化事件，`sequence = new Project revision`，与 mutation/outbox 同事务；
no-op 不发事件。payload 只含稳定非敏感事实：

```text
projectId, revision, installationId, appId, version, manifestDigest,
grantRevision, canonical grantedPermissions（完整集合）
```

选择完整 canonical 集合而非 added/removed diff：完整集合与请求 digest、幂等重放的基准
一致，consumer 不需要历史状态即可重建当前授权事实；consumer 按既有 at-least-once 幂等
消费。事件不包含 manifest、goal、task/event 内容、token、credential 或 raw user content。

### 6. 幂等命名空间与精确结果快照

沿用 `project_app_installation_requests (owner_user_id, idempotency_key)` 作为
install/uninstall/set-grants 共用命名空间；Set 请求的 canonical digest 覆盖
command version marker、project_id、installation_id、expected_project_revision 与
canonical sorted target grant set，不含时间、随机 ID 或服务端解析结果。same key/same
digest 精确 replay 第一次响应；same key/different digest 稳定 `Aborted`；失败请求不消费
key。请求结果持久化 grant/revision 快照（`result_granted_permissions`、
`result_grant_revision`），保证 grant 可变后历史 install/uninstall key 的重放仍返回第一次
响应的事实，而不是后来被 Set 更新过的行。

### 7. runtime 与 core 之间只传 session 派生的 revision

- Core `ResolveWebBundle` 返回 authoritative `grant_revision`；runtime `CreateSurface`
  把它持久化进 Surface session（migration `012`，backfill 为 1）。
- runtime 调私有 `AppAgentService` 时，Run/Watch 请求携带 session 持久化的 revision；
  Core 每次授权（run 调用与 watch 每个 polling round）重新解析 active installation，
  要求 current grant revision 与 session revision 完全相等，再验证整个 current grant 与
  方法成员关系。
- 该字段只能由 runtime 的 validated session 派生；public App Bridge body、MessageChannel
  envelope 与 iframe SDK 不增加该字段，客户端不能提交或覆盖它。
- runtime 不查询 Core schema，Core 不查询 runtime schema，不新增跨 schema FK；撤销完全由
  私有 RPC 上的 revision 比对实现。

## 后果

- migration `011`（owner: workos-core Project Installation）增加
  `project_app_installations.grant_revision`、扩展 request `command` 约束与结果快照列，
  并从 owner-bound installation 回填历史 mapping；migration `012`（owner: runtime-host
  Surface）增加 session 的 revision 快照列。001–010 逐字节不变。
- Desktop App Library 增加 `Manage permissions`：以 exact pinned version 的 requested set
  为上限渲染 checkbox，初始值来自 current grant，Save 是完整替换；成功后提示重新打开
  Surface 才生效。
- stored grant/revision/digest invariant 漂移属于 corruption：净化 `Internal`，不静默修复，
  不继续授权。
