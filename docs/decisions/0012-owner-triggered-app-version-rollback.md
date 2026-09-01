# ADR-0012: Owner-Triggered App Version Transition and Previous-Pinned-Version Rollback

日期：2026-08-31。状态：Accepted（实现分支
`feat/v1-runtime-reliability-adaptive-closeout`）。

## 背景

App Registry 已经持有 immutable SemVer 版本（canonical manifest + digest），Project
Installation 把其中一个 exact version pin 成 active installation
（`project_app_installations`）。但安装后没有任何正式的版本切换路径：换版本只能
uninstall + reinstall（产生新 installation id，丢失 Surface/incident 的连续绑定），
且"回滚到上一个版本"完全没有产品化。`docs/structure.md` 9.5 把"回滚已知稳定版本"列为
**L1 确定性操作——不需要 Agent**；结构 9.3/9.6 的 Deployment Controller/自动修复是后续
任务。本 ADR 固定第一版的边界：**owner 明确触发的版本 transition 与 previous-pinned-
version rollback**，不是模型自动修复，不宣称候选版本经过 canary。

## 决策

### 1. 所有权：命令与历史都归 workos-core Project Installation

- immutable App versions 仍由 App Registry 持有；active installation、grant、版本
  history、Project revision/event 仍由 Core Project Installation 持有。Reliability
  只拥有 Incident/action facts，不读取、不复制、不修改 Registry/Installation 的任何
  表——rollback eligibility 由客户端（可信 Desktop）组合两个 public 读（Incident
  list + Installation version history）推导，服务端命令本身无条件信任 history。
- 新表 `workos_core.project_app_installation_versions`（migration `025`，owner：
  workos-core Project Installation）保存“条目写入后不可变、只允许按保留策略删除最旧条目”的
  有界版本历史：
  `(installation_id, sequence)` 主键、复合 FK 绑定同 owner 的 installation、
  `source ∈ ('install','transition','rollback')`、每行快照 version + manifest digest +
  UTC 时间。写入只发生在 install/transition/rollback 的同一事务里；超过每 installation
  20 条时裁剪最旧快照（bounded，裁剪策略明确，不依赖日志/事件反推）。
- 读路径（public `ListAppVersionHistory`）每次读都重验 version/digest/source、canonical
  UUIDv7、UTC 微秒时间、严格 sequence 顺序，并要求最新保留快照与 installation 当前
  pinned identity 完全一致；installation 与 idempotency 首响应投影同样在 adapter 出口及
  snapshot 叠加后重验。损坏 fail closed（净化 Internal），绝不静默修复。

### 2. 协议：additive RPC，现有 public `AppInstallationService` 承载

不新增服务、不让浏览器直连 Reliability/Core 私网。既有 service 增加：

- `TransitionAppVersion`：显式目标 version（owner 输入）。请求只有
  idempotency key、project、installation、expected Project revision、目标 version——
  绝不接受 image digest、container ID、host endpoint、credential ref 或任意
  "数据库版本"；digest/version 由 Core 从 immutable Registry 重新解析并重验。
- `RollbackAppVersion`：无目标字段。目标完全由服务端从该 installation 的 durable
  history 选择"最近一个与当前 (version, digest) 不同的快照"，并经 Registry 重验该
  version 仍存在且 digest 逐字一致。无 previous snapshot → 稳定 FailedPrecondition，
  零副作用。
- `ListAppVersionHistory`：owner/project/installation scope、有界分页（limit+1）。
- 幂等沿用 `project_app_installation_requests (owner_user_id, idempotency_key)`
  共享命名空间；canonical digest 覆盖命令版本标记（`transition/v1`、`rollback/v1`）、
  project、installation、expected revision（transition 另含目标 version）。same
  key/same digest 精确重放第一次响应（新增 `result_version`/`result_manifest_digest`
  快照列，历史行由 migration 从 owner-bound installation fail-closed 回填）；same
  key/different request 稳定 `Aborted`；失败不消费 key。物理仲裁 = Project 行锁 +
  mapping PK，与 install/uninstall/set-grants 同一 expected-revision 串行域。

### 3. 权限绝不扩大

目标 version 的 requested permissions 若不完全覆盖当前 grant 集合，命令以
`FailedPrecondition`（"permissions need review"）失败，零副作用：先 `SetAppGrants`
显式重选（只能收缩到新版本 requested 集合内），再 transition/rollback。绝不自动授予
新增权限，也绝不通过 rollback 恢复历史中更宽的 grant——rollback 保持当前 grant 原样
（并重验其仍是目标版本 requested 的子集）。

### 4. 事务与事件

transition/rollback 在一个 Core-owned 事务内提交：installation 行
（version/digest/updated_at）+ history 追加（含裁剪）+ Project revision(+1) +
`project.app.version.updated.v1` 事件（sequence = 新 revision，payload 只含稳定
ID/fromVersion/toVersion/manifestDigest/source，不含 manifest 原文/credential/用户
内容）+ outbox + 幂等结果快照。同 version 同 digest 的 transition 是确定性 no-op
（key 仍消费并可重放，两个 revision 均不动、无事件）。rollback 总是产生真实变更
（目标定义即"不同于当前"）。

### 5. Surface 失效

Surface session 的每请求 Core revalidation（active installation + exact pinned
digest/kind/app/version，ADR-0006 既有链路）使 transition/rollback 提交后旧 pinned
descriptor 的全部 asset/代理请求立即 fail closed（404）；不需要新的失效协议，也没有
宽限期。Desktop 对确认的版本变更 best-effort 关闭该 installation 的窗口（与 grant
变更同一 teardown 路径）；迟到响应 inert。下一次 Open 解析新 pinned descriptor。

### 6. 与 Reliability 的关系

System Monitor 对绑定同一 Project/app instance 的 Incident 显示
"Rollback previous version" 的**入口**，但命令走 public Gateway → Core（owner
identity 注入、expected revision、幂等 key）。Reliability upstream 不可达只降级
incident 列表；Core 不可达只让 rollback 按钮报净化错误。自动（非 owner 触发）的
rollback/repair/deployment 仍 unavailable；未来 Deployment Controller 可复用同一
Core command，本批次不建立任何未认证的 privileged service call。

## 兼容性

- Proto 仅 additive：新 RPC + 新 message；v1 字段号不复用，无删除。
- migration `025` forward-only：新表 + request mapping 新列（fail-closed 回填）+
  command CHECK 扩展；001–024 逐字节不变。
- 无 Podman 主机上 rollback 经 Web Bundle 全栈验收；container 链路的同命令语义相同，
  待 rootless acceptance host 后复验（Workload 收敛跟随 installation digest 既有
  verifier）。

## 后果

- Desktop App Library 提供 version history 视图 + 显式 transition consent；
  System Monitor 仅对有 previous snapshot 的 incident 显示可执行 rollback。
- rollback 完成不等于容器已健康：Web Bundle 立即可开；container App 的健康仍由
  Runtime workload 链路独立裁决。
- 状态裁决：本能力按 Web Bundle 链路标 working；container rollback 因 Podman
  blocker 未验收，自动 canary/repair/deployment controller 仍 unavailable。
