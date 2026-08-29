# Task: supervised rootless web service workload

- 状态：active（真实 rootless 证据被环境阻塞，见交接）
- Owner/Agent：implementation agent (2026-08-29)
- 进程/模块：workos-core (App Registry / orchestration)、runtime-host (Workload Manager / Surface)、
  reliability-host (Supervisor / Incident)、workos-gateway、desktop-web
- 依赖：ADR-0002/0003/0005 已合入 main（907fcdc）；migrations 001–014 已执行；本机 cgroup v2 存在
  但 podman 未安装（执行环境探测结果，见交接）。

## 目标与范围

包含：container manifest 严格 profile（digest-pinned image、bounded argv、port、resources、health、
单一 web-service surface）；Core 私有 `ResolveSurfaceLaunch`（oneof web_bundle | web_service_container）；
runtime-host durable Workload 生命周期（generation、幂等 operation、crash-window 恢复、idle TTL、
uninstall 收敛）；rootless Podman adapter（argv-only、pull=never、只读 rootfs、drop capabilities、
no-new-privileges、WorkOS 内部网络、127.0.0.1 随机发布、cgroup v2 hard limits 核对）；Web Service
Surface 只读反向代理（opaque-origin iframe、header 过滤、固定 CSP）；reliability-host 观测
（中立 observation）→ Incident（幂等、owner-scoped）→ 有限 restart/stop（action key 幂等、
restart limit）；gateway 可选 Incident upstream；Desktop renderer=auto 与最小 System Monitor 窗口。

不包含（非目标）：image pull/build/sign、registry login、secret 注入、writable workspace、
host filesystem/volume 挂载、出站网络、写方法/POST/WebSocket、background-service/native runner、
blue-green/rollback、Agent repair、生产 device enrollment、Docker/rootful fallback。

## 协议/数据影响

- `api/proto/workos/surface/v1/surface_resolver.proto`：additive `ResolveSurfaceLaunch` RPC、
  `ContainerLaunchDescriptor`/`ContainerResourcePolicy`/`ContainerHealthPolicy`、response oneof。
- `api/proto/workos/workload/v1/workload.proto`：additive private `SupervisedWorkloadService`
  （Ensure/Restart/Stop/ListObservations）；既有 `WorkloadService` 保持 scaffold/Unimplemented。
- `api/proto/workos/incident/v1/incident.proto`：additive owner/app/revision/violation/timestamp 字段、
  violation 枚举、Acknowledge idempotency key。
- `schemas/workos-app-manifest-v1.schema.json`：container 条件约束（digest-ref image、argv bounds、
  port、resources/health 形状）。
- migration `015`（runtime-host）：`workos_runtime.workloads`、`workos_runtime.workload_operations`、
  surface_sessions renderer CHECK 演进 + workload 引用列。
- migration `016`（reliability-host）：`workos_reliability.incidents`、`incident_actions`、
  `supervisor_checkpoints`。
- capability：`container-runner`、`supervisor`、`incident-manager` 仅在真实证据后 available。

## 验收

- [x] 行为测试（fake engine 单元/集成；真实 Podman fixture 为 opt-in 门禁）
- [x] `make check` / `make test-integration`（含 restart battery）/ `make test-e2e` /
      `make test-deepseek-fixture` / `make test-podman-fixture`（blocker 失败，见下）
- [x] 文档与 `docs/status.json`（status 按真实证据上限如实标注）
- [x] UI before/after/current + notes（真实 ready 状态受 podman 缺失阻塞，采集可得的诚实状态）

## 交接

### 环境探测（执行时真实结果，2026-08-29）

- `command -v podman` → 不存在；`apt-cache policy podman` → candidate 5.7.0（未安装）。
  按 Prompt 安全边界未经用户明确授权不安装系统软件。
- `stat -fc %T /sys/fs/cgroup` → `cgroup2fs`；`cgroup.controllers` 含
  `cpuset cpu io memory hugetlb pids rdma misc dmem`。
- `unshare --user --map-root-user true` → `write failed /proc/self/uid_map: Operation not permitted`
  （Ubuntu 26.04 + 执行沙箱限制，非特权 user namespace 不可用，rootless podman 前提不成立）。
- docker 存在且用户在 docker 组，但按边界禁止用作 Podman fallback 或 privileged Podman-in-Docker。

结论：真实 rootless Podman + cgroup + cross-process + browser E2E 证据在本机不可得。按 Prompt 规定：
实现完整代码、fake-engine/单元/集成测试与 opt-in fixture；`make test-podman-fixture` 在本机以明确
blocker 失败（不计 PASS）；任务保持 active，`container-runner`/Reliability 不声明 working。

### 已验证命令（执行时真实结果，2026-08-29）

- `git status --short --branch`（起始干净）、`git log`（分支基于本地 main `907fcdc`）。
- `make bootstrap`：通过；`make check`（proto format/lint/vet + gofmt + go vet + go test +
  eslint/prettier/vitest 75 tests + web build + README status check）：通过。
- `make generate` ×2：第二次后工作树无生成漂移（buf/ts/sqlc/status render）。
- `buf lint`、`buf breaking --against '.git#branch=main'`：通过（仅 additive）。
- migrations `001`–`014` 与 main 逐字节一致（`git diff main -- internal/platform/migrations/`
  仅新增 `015`/`016`；checksum pin 测试扩展到 `016`）。
- `go test -race ./internal/core/appregistry/... ./internal/core/orchestration/...`：通过；
  `go test -race ./internal/runtime/... ./internal/reliability/...`：通过。
- `make test-integration`（含 restart battery：seed → core/harness/runtime restart → verify）
  完整两轮：通过（66 顶层测试；survives 重启的 workload/incident/action 幂等由单测与
  integration 前向 migration 测试覆盖）。
- `make test-e2e`（Playwright 全量）：通过；`make test-deepseek-fixture`：通过。
- `make test-podman-fixture`：**BLOCKED 失败**（"podman is not available on this host"，exit 1，
  不计 PASS）。
- UI 证据：`docs/ui/desktop-web/changes/20260829-supervised-web-service-workload/`（before 复制
  当时基线；after 用重建后的 compose 栈经 Playwright 采集，migration 015/016 已应用；
  after 已同步 `current/`）。
- PostgreSQL 对象计数：`workos_runtime.workloads` / `workload_operations` /
  `workos_reliability.incidents` 等新表在验收卷为空（无真实容器启动路径）。
- Podman 对象前后计数：本机无 podman，无对象可枚举（blocker 的一部分）。
- 提交内容检查：无凭据/token/真实用户内容；无 ELF、镜像归档、Playwright trace/视频、
  临时数据库或 root 文件；`git diff --check` 干净。

### 未决风险与下一步

1. 真实 rootless 证据链（fixture image 构建→digest pin→E2E→截图→status 升级）需要在具备
   rootless Podman 的宿主上执行 `make test-podman-fixture` 后回填。
2. write transport（POST/表单/WebSocket）、network capability、生产 auth、rollback/repair 仍
   unavailable，是后续任务。
3. UI "ready" 视觉证据依赖真实容器启动；本机只能采集 Starting/Unavailable/降级状态，
   notes.md 记录采集口径。
