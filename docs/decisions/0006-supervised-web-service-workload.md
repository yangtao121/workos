# ADR-0006: Supervised Rootless Web Service Workload

日期：2026-08-29。状态：Accepted（实现分支 `feat/supervised-web-service-workload`）。

## 背景

主线已闭合 Project/Registry/Installation/Surface(Web Bundle)/App Bridge/Agent policy 链路，但
`runtime.type=container`、resources、health 只是 schema 声明：runtime-host 不启动用户程序，
reliability-host 无 Incident 与 enforcement。`docs/structure.md` 的核心原则是“确定性的系统负责
制止故障，Agent 负责理解和修复故障”。本 ADR 固定第一个不依赖模型的 L0/L1 闭环的边界。

## 决策

### 1. Workload runner 属于 runtime-host；监督/Incident 属于 reliability-host

- Workload（durable identity、engine container、cgroup、endpoint、generation、幂等操作）是
  “实际运行对象”的事实，与 Surface session、engine、文件系统强耦合，只能由 runtime-host 拥有。
  新表 `workos_runtime.workloads` / `workload_operations`（migration 015）仅 runtime-host 读写。
- 违规判定、Incident、有限 restart/stop 决策是“系统监督”事实，由 reliability-host 拥有
  （migration 016 的 `workos_reliability.*`）。Reliability 不查询 Runtime schema，不导入 Podman
  adapter；它只消费 runtime-host 私有的、版本化的中立 `SupervisedWorkloadService`
  （observation + 幂等 Restart/Stop RPC）。两 schema 之间禁止跨 FK/SQL join。
- 观测（observation）是 runtime-host 的中立只读输出：稳定 ID、generation、状态、health verdict、
  exit category、有界数字计数。它不包含 cgroup path、loopback endpoint、container ID、raw
  stderr/HTTP body/日志；它也不是 Incident 决策——决策只属于 reliability policy engine。

### 2. App 声明的 resources/health 只是 requested policy

manifest 的 `resources`/`health` 是 App 的请求，不是授权。server-owned 上限（PolicyMaxima v1，
版本化常量+启动校验）与请求共同裁决出 effective policy（clamp：effective ≤ min(request, max)），
随 Workload 持久快照。App 填大数只能被裁剪，不能获得宿主资源。restart limit 同样取自持久化的
effective health snapshot 并受 server hard maximum 约束。

### 3. 只运行本地 digest-pinned image

Registry 只做 canonical syntax/security policy（严格 `name@sha256:<64 lowercase hex>`、无 tag、
无 credential URL、bounded argv），不访问 Podman、不检查本机 image。Runtime 启动时能力探测
（有界 `podman info`：rootless=true、cgroup v2、controllers 可用）+ launch 时 `--pull=never` +
exact digest reference；本地缺 image 是 FailedPrecondition，engine 不可达是 Unavailable，绝不
访问 registry。tag、短 digest、大写 digest、`user:pass@` 一律在注册时拒绝。

### 4. 数据库与 Podman 两个事实系统的 crash window

PostgreSQL 事务不能覆盖外部 engine side effect。协议：

1. **DB reserved**：operation 行（幂等键 + request digest）与 Workload 行（generation、
   deterministic container name、state=starting）在一个事务内先落库。
2. **Engine side effect**：以 deterministic name（`workos-wl-<workload_id>`）+ 完整 WorkOS
   identity labels（`workos.managed=workos`、workload id/generation/owner/instance）create+start。
3. **Persist**：inspect 核对（image digest、ports 仅 127.0.0.1、安全标志）后回写
   container_id/endpoint/cgroup path/state。

每个 crash window 的收敛由 reconcile（启动时 + 周期 + lease）确定性完成：DB reserved 未 create →
重试 create；created 未 started → start；started 未 persist → 按 labels 重新附着并 persist；
stop 后未 persist → 重复 stop 直至 engine 确认后置 stopped。Runtime 重启后只收养同时带完整
WorkOS labels 且与 DB 行精确匹配的容器；无 labels 的外部容器永不触碰、永不删除。清理只使用精确
container ID，禁止 wildcard/prefix/prune。两个 Runtime 实例通过 DB 行的 lease + operations 行的
唯一性线性化，同 key replay 返回同一持久化结果。

### 5. 第一版 Web Service proxy 是只读 opaque-origin

`/surfaces/<session>/...` 对 web-service session 只允许 GET/HEAD（405+Allow），拒绝 query、
body、upgrade、写方法；不转发 Cookie/Authorization/bridge token/identity/Forwarded/Host/
hop-by-hop，不透传 Set-Cookie/认证 challenge/Server；backend target 只能是 server-owned、启动时
已验证为 `127.0.0.1` 的 loopback endpoint——任何 client/manifest 提供的 URL 都不参与，杜绝
SSRF/open proxy。文档响应覆盖 WorkOS 固定 CSP（`sandbox allow-scripts`、无 `allow-same-origin`、
`connect-src 'none'`、no-referrer、no-store），backend 的 CSP/X-Frame-Options 不能放宽它。这一版
是 server-rendered/read-only 切片，不声称完整 full-stack transport；POST/表单/WebSocket 是后续
任务。

### 6. “重启相同版本”不是 rollback/repair

restart 用完全相同的 digest-pinned image、argv 与 effective policy 重建容器并递增 generation——
它只消除“进程死了/卡了”这一故障态，不改变任何版本事实。rollback/deploy/repair 需要选择另一个
immutable version 与确定性部署编排，属于后续 Deployment Controller/Repair Orchestrator 任务。
因此本任务的 Incident mitigation/resolve 只表示“工作负载已恢复运行”，不表示“故障被理解或修复”。

## 兼容性（v1）

- Proto 只 additive：新增 RPC/message/enum 值/字段号；既有 `ResolveWebBundle`、
  `ReadWebBundleAsset`、`WorkloadService`（保持 Unimplemented scaffold）不变。危险 scaffold
  `StartWorkload(WorkloadIdentity)` 不实现为“照单全收”；安全启动契约是新的
  server-derived `SupervisedWorkloadService.EnsureWorkload`（public identity 字段全部由
  Runtime 生成）。Workload/observer/control RPC 仍不进 Gateway allowlist。
- Schema 对 container 是收紧而非放宽：container 从未是 working 行为，无存量 working 用户；
  web-bundle profile 及其 fixtures/digest 不变，`buf breaking` 与既有测试回归证明。
- migration 015/016 为 pristine 前向迁移；001–014 逐字节不变。surface_sessions 的 renderer CHECK
  从单值演进为二值 + renderer-specific 互斥 CHECK，旧行（web-bundle）语义不变。
- Create request digest：保留 v1 公式；auto（UNSPECIFIED）在 v2 下携带解析后的 launch kind
  （`auto:web-bundle`/`auto:web-service`），显式 WEB_BUNDLE 沿用 v1 字节，历史 v1 行的 key 按精确
  映射识别并重放（kind 相同且 digest 符合 v1 公式），不发生升级后 Aborted；auto 与显式请求不会
  意外同键。

## 后果

- IncidentService 若公开，只经独立 Reliability upstream、identity 注入与 owner scope 暴露；
  Gateway core readiness 不因 Reliability 不可达而失败。
- 真实 rootless 证据（fixture image、cgroup 数值核对、cross-process E2E、浏览器截图）是
  `container-runner`/`supervisor`/`incident-manager` capability 与 docs/status.json 升级为
  working 的前置条件；缺失时代码只能报告 unavailable，不得 fake 成功。
- CPU hard quota 始终由 kernel 执行；不定义“CPU 违规”Incident，不用单次高 usage 伪造故障。
