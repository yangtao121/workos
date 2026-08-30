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
  （Ensure/Restart/Stop/ListObservations）；新增 `pids_events_max=20`，保留并 deprecate 错命名的
  field 17；既有 `WorkloadService` 保持 scaffold/Unimplemented。
- `api/proto/workos/incident/v1/incident.proto`：additive owner/app/revision/violation/timestamp 字段、
  violation 枚举、Acknowledge idempotency key。
- `schemas/workos-app-manifest-v1.schema.json`：container 条件约束（digest-ref image、argv bounds、
  port、resources/health 形状）。
- migration `015`（runtime-host）：`workos_runtime.workloads`、`workos_runtime.workload_operations`、
  surface_sessions renderer CHECK 演进 + workload 引用列。
- migration `016`（reliability-host）：`workos_reliability.incidents`、`incident_actions`、
  `supervisor_checkpoints`。
- migration `017`（reliability-host）：Incident acknowledge idempotency key。
- migration `018`（runtime-host）：durable `idle_since` + permanent operation verdict。
- migration `019`（reliability-host）：action ledger 支持真实 `unsupported` outcome。
- capability：`container-runner`、`supervisor`、`incident-manager` 仅在真实证据后 available。

## 验收

- [x] 行为测试（fake engine 单元/集成；真实 Podman fixture 为 opt-in 门禁）
- [x] `make check` / `make test-integration`（含 restart battery）/ `make test-e2e` /
      `make test-deepseek-fixture`（原实现验收，第二轮修复后的复验见下）
- [ ] `make test-podman-fixture`：当前宿主 blocker 失败，不计 PASS（见下）
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

### 审查修复（2026-08-30，第一轮评审 8 项全部落地）

1. **P0 cgroup 路径**：`absoluteSubtree` 把 /proc 的相对路径join 到统一挂载点
   （`/sys/fs/cgroup`），probe/resolve/read 全链路绝对化；`ErrCorrupt`/`ErrFailed` 改为
   非重试分类，corrupt 启动失败现在确定性地移除容器并落 failed（含测试断言容器计数为 0）。
2. **P0 网络**：`ensureInternalNetwork` 对已存在网络 inspect `{{.Internal}}`，非 internal 的
   同名网络直接拒绝能力（不静默重建）；probe 测试覆盖 refuse 路径与 create/verify 路径。
3. **P0 reconcile 身份**：reconcile 的 Core 重验注入 owner+runtime 服务 device identity
   （Config.VerifyDeviceID，启动校验）；postgres `Transition` 支持 verified 自转换
   （专用 guarded UPDATE，不触碰其他列）；启动成功即锚定 `last_verified_at`，nil 回退
   created_at，Core grace fail-safe 现在真实可达。
4. **P0 stop 语义**：stopWorkload 返回 sanitized outcome；仅 ControlStopped 才把 incident
   落 stopped——unavailable/failed 保持 open/pending，下一轮以同一 action key 重驱
   （crash window：创建 Incident 与执行 stop 分离、各自幂等）；预算 Incident 不再自持
   stop 权（单一 stop 权威 = 原 episode 的 terminate key），由 settle 关闭；新增
   unavailable→re-drive→stopped 的两轮 poll 测试。
5. **P1 lease/verified 不刷 updated_at**：两个 bookkeeping UPDATE 不再触碰 idle TTL 锚点。
6. **P1 幂等**：acknowledge key 持久化（migration `017`：incidents.acknowledge_key +
   (owner,key) 部分唯一索引；写前预检 + 23505 分类为冲突）；restart/terminate 的已完成
   非 retryable verdict 按 recorded error 精确 replay（limit-exhausted 重放拒绝而非伪造
   成功，含 manager 测试）。
7. **P1 Location**：`Location` 加入无条件剥除列表，只有通过严格 path grammar 的重写才重新
   附上 session 前缀内的地址（proxy round-trip 测试覆盖 external/protocol-relative/encoded
   丢弃 + 相对路径重写 + cookie 剥除 + 固定 CSP）。
8. **P1 门禁本体**：`make test-podman-fixture` 改为容器内编译测试二进制与 fixture payload、
   在宿主执行（podman/user session/cgroup 都在宿主）；fixture 构建目录修正为只含
   Containerfile + 定名 payload；无 podman 宿主仍 loudly fail。

### 未决风险与下一步

1. 真实 rootless 证据链（fixture image 构建→digest pin→E2E→截图→status 升级）需要在具备
   rootless Podman 的宿主上执行 `make test-podman-fixture` 后回填。
2. write transport（POST/表单/WebSocket）、network capability、生产 auth、rollback/repair 仍
   unavailable，是后续任务。
3. UI "ready" 视觉证据依赖真实容器启动；本机只能采集 Starting/Unavailable/降级状态，
   notes.md 记录采集口径。

### 审查修复（2026-08-30，第二轮：全部已确认阻断项）

1. stop/reconcile 仅在 full identity + inspected immutable/security facts 匹配、exact ID stop/remove
   且复查 absent 后落 stopped；engine failure 保持 stopping 并重驱，不再伪造成功。
2. restart 在推进 generation 前清理旧容器；目标 generation 在副作用前持久化，reconcile 找回
   原 operation key；覆盖 transition/launch 成功但 operation commit 未落的重放窗口，不二次推进。
3. Podman adoption 核对 workload ID/generation/owner/instance、persisted container ID、image、argv、
   read-only/privileged/cap-add/no-new-privileges/network/restart/mount/device/tmpfs；managed list 对每个
   ID inspect，orphan cleanup 不再因 labels 为空失效。
4. Reliability 增加真正的全局 private pending-action query，并在观察前独立重驱；restart 后新
   generation、stop 后 terminal observation 都能恢复 action commit 窗口；旧 generation mitigated
   Incident 可由健康替代 generation resolve。
5. restart limit 使用 Connect `ResourceExhausted`，unsupported/image/runner precondition 只映射
   `ControlUnsupported`；migration 019 补齐 action `unsupported` CHECK。
6. Incident resolve SQL 只允许 mitigated→resolved，open 保持 open；真实 PostgreSQL 测试证明不再
   触发 `incidents_coherent_mitigation`。
7. `pids.events` 读取真实 `max` counter，startup baseline/observation delta 贯通；Proto additive
   field 20，旧 field 17 保留兼容；unit 与 opt-in real fixture 都有受控 pids.max probe。
8. migration 018 引入 durable `idle_since`，active→idle 后从当刻获得完整 TTL，不再借用历史
   lifecycle `updated_at`。
9. proxy 在提交 backend status 前验证 64-header/64KiB header 与完整 8MiB body；Connection
   nominated header 被剥除，超限固定安全头 502，不再返回截断成功。
10. Reliability 的 `supervisor`/`incident-manager` capability 在真实跨进程 E2E 前固定 false；
    Podman fixture 使用唯一 tag、after snapshot 前精确删 image、fallback source path 修正，Make
    trap 在失败时也清理临时二进制。此轮没有用户可见 UI 改动，因此不产生新的截图证据。

### 最终收口（2026-08-30）

1. workload state 与 operation terminal verdict 改为同一 PostgreSQL transaction；terminal operation
   不可被迟到的 unavailable/failed 写降级，restart 数据库 guard 只允许相邻 generation/restart_count。
   ambiguous commit、failed/superseded target generation 的重放均保守失败，不再越代或伪造成功。
2. Reliability 只有 `ControlUnavailable` 可重驱；`ControlFailed` 为 owner 可见 terminal outcome，预算
   stop 的永久失败会关闭 pending action 而非形成无限队列；terminal action outcome 同样不可降级。
3. Podman exit 125 只在独立 `container exists` 明确返回 exists/absent 后分类，storage 125 不冒充
   name collision/not-found；adoption 额外要求 declared port 恰好一个 loopback publish、cap-drop=all。
4. 完整 canonical argv 以 JSON exec-form `--entrypoint` 覆盖镜像 ENTRYPOINT/CMD，并从 inspect 的
   Entrypoint+Cmd 重建核对；real fixture 故意携带错误镜像默认 argv。当前 create 返回的 exact ID 在
   安全 read-back 漂移时仍会被精确清理，恢复期不匹配的未知对象仍不触碰。
5. Podman fixture 的 OOM probe 只有 engine OOM verdict 或 `memory.events` 增量才通过；PIDs probe、
   唯一 image tag、network name snapshot、失败路径临时产物清理均已固定。此轮无用户可见 UI 变更，
   因而不新增截图。

### 合并前全量审计收口（2026-08-30）

1. create/start 后新增完整 container identity + immutable/security profile read-back；engine 若悄然增加
   publish 等配置，runtime 在落 running 前按 create 返回的 exact ID 清理并把 generation 关闭为
   permanent failure。restart 的内存态同步清空旧 container facts，避免新 generation 被旧 ID 误判。
2. Podman 不再以相对默认能力计算的 `HostConfig.CapDrop` 证明零权限；inspect 强制要求实际
   `EffectiveCaps` 与 `BoundingCaps` 字段存在、可解析且均为空。stdout/stderr 超出 1MiB 改为整次
   unavailable，fixture 同步校验实际 capability sets，并在 image build 前注册唯一对象 cleanup。
3. shutdown 将“归属验真”和“可采用 profile 验真”分离：restart/adoption 仍要求 full profile；
   terminate/reconcile-stopping 可精确清理 labels + persisted ID 证实归属但 profile 漂移的对象，防止
   安全配置放宽把 live container 永久卡在 stopping；identity/旧 generation 不匹配仍绝不触碰。
4. Web Service 每次代理请求除 digest 外再精确核对 Core launch kind/app/version；Workload store outage
   保持 503。最终 target 再验证 canonical session UUID、精确 loopback port/path；禁用透明压缩并拒绝
   encoded body，剥除 backend CORS/reporting/navigation 等浏览器控制头。
5. Core verifier 的未知/非法 verdict 归入 uncertainty 并受 grace fail-safe；Runtime app version grammar
   与 Registry 的 SemVer 子集对齐；PostgreSQL reserve 仅把 SQLSTATE 23505 当并发冲突，其他约束错误
   不再伪装 conflict；Incident 分页 boundary lookup 同样受 owner/project scope。
6. 此轮只收紧后端状态机、adapter 与代理边界，没有用户可见 UI 变化，因此沿用既有截图证据；真实
   rootless Podman 与跨进程浏览器证据 blocker 未变化，任务继续保持 active/scaffolded。

### 最终复验（2026-08-30）

- `make generate` 重复执行无生成漂移；`make check` 通过（含 proto/sqlc、gofmt/vet、全量 Go、
  architecture/eslint/prettier、前端 75 tests 与 production build、status render check）。
- `go test -race ./internal/runtime/... ./internal/reliability/...`：通过。
- `make test-integration`：通过（真实 PostgreSQL migration/约束/事务测试与完整 restart battery）。
- `make test-e2e`：6 passed，2 个条件 fixture skipped；`make test-deepseek-fixture`：故障矩阵、重启
  持久化与浏览器链路全部通过，只使用仓库固定的本地假密钥。
- `go test -c -tags podmanfixture ./tests/podmanfixture` 与 fixture payload 交叉环境编译：通过。
- `make test-podman-fixture`：按设计 **BLOCKED 失败**（宿主没有 podman，exit 1，不计 PASS）。
- `git diff --check` 与 credential-shaped secret 扫描：通过。

任务仍为 active/scaffolded，直到具备 rootless Podman 的 acceptance host 提供真实 adapter/cgroup
证据，并补齐 observation→Incident→action 的跨进程与 web-service ready 浏览器证据。
