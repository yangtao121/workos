# ADR-0016：真实 Runtime 自愈链——监督验收、fixture engine 边界、遥测矩阵与 Repair/Deployment 语义

- 状态：Accepted
- 日期：2026-09-03
- 关系：细化 ADR-0006 的监督边界与验收标准；衔接 ADR-0012 的版本事实、ADR-0014 的
  通知链路、ADR-0005 的预算/审批。为剩余能力总攻 W2 提供实现边界。

## 背景

`docs/status.json` 中 Reliability 长期为 scaffolded：监督/Incident 状态机与
at-least-once 通知链路有 fake 单元与 PostgreSQL 证据，但缺少"真实 observation →
Incident → restart/stop 动作真实生效"的跨进程证据；`container-runner` 因验收主机
缺失 rootless Podman 保持 unavailable。本 ADR 固定三件事：(1) rootless 验收的精确
前提与 blocker 记录方式；(2) 在无 Podman 主机上证明监督软件链的 fixture engine
边界；(3) 遥测采集的脱敏矩阵与 Repair/Deployment 的语义与 L 级别映射。

## 决策

### 1. rootless 验收前提与 blocker 记录（W2.2）

验收主机必须同时满足：用户级 `podman` 可执行（`command -v podman`）、cgroup v2
（`/sys/fs/cgroup/cgroup.controllers` 存在）、user namespaces 启用
（`/proc/sys/user/max_user_namespaces > 0`）、`podman info` rootless=true、
内部网络 `--network workos-app-internal` 可建。`make test-rootless-runtime`
在任何一项缺失时必须显式 BLOCKED 并输出精确探测结果——绝不伪造 PASS，绝不安装
宿主软件。当前验收主机探测：podman 不可用、cgroup v2 可用、user namespaces=123655
→ 整项 BLOCKED，`container-runner` 保持 unavailable。

### 2. fixture engine：监督软件链的诚实证明工具（W2.3）

- 新增 `internal/runtime/workload/adapters/fakefixture`：实现与 Podman adapter 相同的
  `ports.Engine`，进程内有界对象模拟容器生命周期（create/start/stop/inspect/cgroup
  计数），无网络、无宿主副作用、确定性。
- 失败脚本经 operator/test 专用的有界控制文件驱动（路径由 env 显式配置，缺省关闭），
  使跨进程监督链可稳定复现 crash loop/OOM 分类/健康失败。
- 边界纪律：fixture engine 仅由 compose 的显式 env（`WORKOS_RUNTIME_WORKLOAD_ENGINE=fake-fixture`）
  启用；它证明的是 supervision 软件链（观测→决策→动作→台账→通知），不证明也不宣称
  rootless 容器隔离、cgroup 硬限制或引擎级安全 profile。`docs/status.json` 中
  supervision 软件链的升级必须带 "fake engine" 限定词，`container-runner` 不升级。
- 生产装配不回退 fixture engine：主路径仍是 Podman probe 失败即 UnavailableEngine
  （ADR-0006 §4 不变）。

### 3. 真实监督验收标准（W2.3 门禁）

`make test-real-supervision` 在真实 PostgreSQL + 全六进程栈上证明：
crash-loop workload → reliability supervisor 观测到退出分类 → 每 occurrence 恰一个
Incident → 幂等 restart 动作使 workload generation +1 且旧 generation incident
收敛 → 达到 restart limit 后确定性 stop → workload 终态 stopped → Incident 事实经
ADR-0014 链路可通知。重启任一进程后 pending action 重放不产生重复副作用。

### 4. 遥测脱敏矩阵（W2.4）

- 六进程仅当 `WORKOS_OTLP_ENDPOINT` 配置时导出 OTLP（既有行为）；otel-collector 是
  compose/systemd 编排的外部依赖服务，不是第七个 WorkOS 进程。
- 采集前剥离（在进程内 telemetry decorator 执行，而非 collector 端）：goal/事件
  payload 全文、artifact 内容、通知正文、凭据与 lease secret、owner 可读的项目名。
  允许字段：进程名、RPC 方法、bounded 状态码、duration/histogram、workload/instance
  的 canonical UUIDv7、cgroup 计数等有界数值。
- 拒绝深度/尺寸：attribute value ≤512 bytes、每 span ≤64 attributes、超界直接丢弃该
  attribute（记录丢弃计数）。测试以字段白名单断言（违规即失败）。

### 5. Repair Orchestrator（W2.5，L3）

- 归 reliability-host。Incident 证据包（有界、脱敏：violation 分类、计数、generation、
  workload UUIDv7，无日志正文）作为 `artifact.review.v1`-外的第一种 incident-scoped
  context：AgentTaskInput additive `incident_ref`（type `reliability.incident.v1` +
  UUIDv7，经 Gateway 不可达的私有校验）。修复任务走既有 Task Router 全链路——
  预算/审批/断路/usage 完整适用，L3 不绕过任何治理。
- 路由：project harness 健康则绑定项目 provider；不可用则路由 Recovery Harness
  （Generic CLI 声明的 recovery 用途）；两级都失败 → 现有通知链路等待人工。
- 修复任务终态回写 incident（修复成功 ≠ incident resolved；resolved 仍由既有
  stable-streak 判定），同一 occurrence 不重复建修复任务。

### 6. Deployment Controller（W2.6，L4）

- Deployment 编排归 reliability-host；版本事实归 workos-core（ADR-0012），经 public
  语义等价的私有路径协作，禁止 Reliability 直写 Core 表。
- 状态机：candidate（repair 任务产出的新 Registry version）→ canary（创建新版本
  Surface + 观察窗）→ promote（TransitionAppVersion）或 rollback（既有 pinned 版本）。
  canary 观察窗内出现新的 incident → 自动 rollback。每一转换持久化于 reliability
  台账（幂等 key），同 candidate 不无限重试（预算上限后 terminal）。
- L5（数据迁移/凭据/权限提升）永不自动：不在本控制器状态机内，保持人工确认。

## 后果

- W2 各子项只有取得对应门禁证据才在 `docs/status.json` 升级；fixture engine 证明的
  软件链与宿主证明的容器能力分开表述。
- 遥测/Repair/Deployment 的具体实现落在后续提交；本 ADR 先固定边界与验收。
