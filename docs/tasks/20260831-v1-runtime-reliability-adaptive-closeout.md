# Task: v1 closeout — adaptive shell, rootless workload evidence, reliability loop, deterministic rollback

- 状态：active
- Owner/Agent：implementation agent (2026-08-31)
- 进程/模块：apps/desktop-web、apps/mobile-shell、clients/\*、workos-core (App Installation)、
  runtime-host、reliability-host、workos-gateway
- 分支纪律：单分支 `feat/v1-runtime-reliability-adaptive-closeout`（基于执行时本地 main
  `12a53ab`），单任务记录，串行阶段，不 merge/push。
- 依赖：migrations 001–024 已执行（不得修改）；ADR-0002/0003/0006 既有边界不变。

## 目标与范围

Prompt：`docs/prompts/` 下 v1 收口批次（Adaptive Shell、Rootless Runtime、Reliability、确定性回滚）。

阶段：

- A：Adaptive Shell 从 contract-only 到 working（Compact/Medium/Expanded/Fold-separated、
  device-class 隔离 layout state、PWA manifest、`make test-adaptive-shell`、多 viewport 截图）。
- B：真实 rootless Podman + cgroup v2 Workload 证据（宿主探测 → B1 全链路 或 B2 诚实 blocker）。
- C：Reliability observation → Incident → action 闭环（C1 真实链路 或 C2 软件侧加固）。
- D：owner 触发的确定性 previous-pinned-version transition/rollback（ADR、additive Proto、
  Core 事务、App Library/System Monitor UX、`make test-app-version-rollback`、Web Bundle 全栈 E2E）。
- E：文档、ADR、implementation、status.json、README 生成、全量验证收口。

## 验收

- [x] 阶段 A 代码 + Vitest + Chromium 三 viewport + fold fixture E2E + before/after/current 截图
- [x] 阶段 B 宿主探测原始 verdict 记录；`make test-podman-fixture` 真实结论（BLOCKED）
- [ ] 阶段 C capability 裁决与证据一致（真实链路 或 保持 false + 软件测试）
- [ ] 阶段 D ADR-0012 + additive Proto + migration 025 + Core 命令 + UX + rollback E2E
- [ ] `make generate` 幂等、`make check`、`make test-integration`、`make test-e2e`、
      `make test-adaptive-shell`、`make test-app-version-rollback`、既有专项门禁
- [ ] 文档与 status 同步；工作树干净；按阶段聚焦提交

## 基线（执行时真实结果，2026-08-31）

- `git status --short --branch`：main 干净，`12a53ab`（origin/main ahead 1，即本批次 prompt 提交）。
- 分支 `feat/v1-runtime-reliability-adaptive-closeout` 已创建。
- `make bootstrap`：PASS。`make generate` ×2：第二次无生成漂移。`make check`：PASS
  （首轮失败仅为新任务记录 md 的 Prettier 格式，修复后通过）。
- `make test-integration`：PASS（真实 PostgreSQL + 完整 restart battery，基线镜像）。

## 阶段 A（2026-08-31，已完成）

- 提交：`aa6535c feat: add shared adaptive shell device contract`（clients/adaptive-shell +
  mobile-shell 复用共享契约）+ 本提交（desktop 集成 + PWA + 门禁 + 视觉证据）。
- 布局契约：`@workos/adaptive-shell`（纯函数 `resolveDeviceLayout`/`classifyDevice`，
  Proto `DeviceClass` 唯一映射，无 UA sniff；DOM API 全部隔离在 `hook.ts` 适配器）。
  Vitest 36 例覆盖 599/600、1023/1024、portrait/landscape、0/NaN/负/∞ DPR、fold gap、
  segment 变化、resize storm、storage 腐坏/迁移/隔离/并发 CAS。
- Desktop 四模式：Expanded 保持既有自由窗口不回退；Compact 单 pane + 底部导航 +
  Project sheet + 全屏 App Library；Medium 单 pane + Agent slide-over（Escape 关闭）+
  显式 Dock；Fold-separated 仅在真实/注入双 segment 时双 pane（hinge 无点击目标，
  用户可切单 pane），无 segment 按宽度退化为 Expanded/Medium。窗口体渲染统一为
  `renderWindowBody`，expanded 与 adaptive 共享同一实现。
- Layout state：owner 为 device-local IndexedDB（origin 隔离 browser profile，key
  `layout/<deviceClass>/<projectId>`），schema v1，仅存有界 canonical UUID 引用与
  single/dual 偏好；多 tab 写入由单事务 read-modify-write 串行仲裁（revision/updated_at
  可观察）；腐坏/未知字段/未知 schema 只重置当前 key；uninstall/grant 变化经
  `pruneAppInstance`、archive/logout 漂移经 `sweep`（对齐服务端 project 列表）清理；
  无 IndexedDB 环境降级内存存储。Desktop 几何永不写入 phone/tablet key（expanded 不写）。
- PWA：`manifest.json` + SVG icon + `viewport-fit=cover` + theme/display/start URL；
  未启用 Service Worker（首版不缓存任何 API 响应面，静态缓存边界由 Gateway 承担：
  `/assets/`（内容哈希）`immutable`，HTML/manifest/icon `no-store`）。
- 门禁：`make test-adaptive-shell`（真实栈：Gateway/Core/harness/runtime/Chromium）
  —— compact 390×844 全链路（project sheet 建 Project、App Library 安装、fake harness
  任务完成、System Monitor、reload 后 shell 恢复）、medium slide-over/dock、fold 双
  pane/单 pane/无 segment 回退、expanded 回归：4/4 PASS。
- 视觉证据：`docs/ui/desktop-web/changes/20260831-adaptive-shell/`（before 采自基线
  `12a53ab` 的真实 dist；after 采自实现后 bundle；current/ 已同步）。before 仅含旧 UI
  可达状态（旧 UI 在 390/820px 不可达的状态没有 before 帧，notes.md 有记录）；
  expanded 帧内容逐像素一致（哈希差异为 Chromium 文字 AA 运行间噪声，同 bundle 两次
  采集哈希亦不同，notes.md 有证明）。

## 阶段 B（2026-08-31，B2 blocker 路径）

宿主探测原始 verdict（一次完整探测，未安装/未放宽任何宿主配置）：

- `command -v podman` → 不存在（exit 1）。
- `stat -fc %T /sys/fs/cgroup` → `cgroup2fs`；`cgroup.controllers` 含
  `cpuset cpu io memory hugetlb pids rdma misc dmem`；`cgroup.subtree_control` 存在。
- `unshare --user --map-root-user true` → `Operation not permitted`（非特权 user
  namespace 不可用，rootless Podman 前提不成立；与 2026-08-29 探测一致）。
- `systemctl --user is-system-running` → running。
- docker 存在，但按边界禁止用作 rootless fallback。

结论：走 B2。`make test-podman-fixture` 在本宿主 **BLOCKED 失败**（快速、安全、无
临时产物退出；guard 在任何构建前退出）。`container-runner` 与 Runtime container
状态不升级；`docs/tasks/20260829-supervised-web-service-workload.md` 保持 active。
阶段 C 按 C2（软件侧）执行，阶段 D 经 Web Bundle 全栈交付。

## 阶段 C（2026-08-31，C2 软件侧路径）

- 宿主无 rootless Podman（阶段 B verdict），无真实 observation → Incident → action
  跨进程链路可执行。`supervisor` / `incident-manager` capability 保持 false，
  `docs/status.json` Reliability 保持 scaffolded，不伪造升级。
- 软件侧交付：为阶段 D 的 owner rollback 提供 eligibility/read model——新增 public
  `ListAppVersionHistory`（owner/project/active-installation scope、有界分页、每次读
  重验）与 rollback 命令本身；System Monitor 的入口由 Desktop 组合 public Incident 读
  与该 public 读推导，Reliability 不读取/复制 Core 事实，二者解耦既有边界不变。
  既有 Reliability domain/application/PostgreSQL 测试（incident 幂等、action ledger、
  terminal outcome、重启收敛）在 `make check` / `make test-integration` 全量回归通过，
  未重复堆叠同质测试。
- 任务记录明确：本批次的 Incident→rollback 联动是**软件实现证据**（组件级确定性
  fixture 测试 + Core 命令真实链路）；**真实能力证据**（真实 Incident 驱动的
  System Monitor rollback）仍需 rootless acceptance host，属 20260829 任务的未决项。

## 阶段 D（2026-08-31，已完成）

- ADR：`docs/decisions/0012-owner-triggered-app-version-rollback.md`。
- Proto：`AppInstallationService` additive `TransitionAppVersion` /
  `RollbackAppVersion` / `ListAppVersionHistory`（无字段复用、无删除；`make generate`
  幂等；buf lint/breaking 通过）。
- migration `025_app_installation_version_history.sql`（owner：workos-core Project
  Installation）：append-only 有界版本历史（每 installation 最近 20 条、CASCADE 绑定
  同 owner installation）+ request mapping 的 `result_version`/`result_manifest_digest`
  NOT NULL 快照列（owner-bound fail-closed 回填）+ command CHECK 扩展
  transition/rollback。001–024 逐字节不变（checksum pin 通过）。
  注：025 在本分支内曾以 RESTRICT 版本在本机持久卷试运行，按 CASCADE 修正后重建了
  本机 compose 卷（仅分支开发卷，001–024 未动）。
- Core：domain（transition/rollback digest、grant 兼容 fail closed、history 校验）、
  ports、application（Transition/Rollback/ListVersionHistory；回滚目标锁内外双推导）、
  postgres（单事务：installation + history + 裁剪 + revision + event + outbox + 幂等
  快照；install 写入 origin 快照）、transport（3 RPC + 固定错误矩阵：
  no-previous/permissions-review → FailedPrecondition、conflict/幂等 → Aborted、
  unknown → NotFound）。
- Desktop：App Library `Versions` 对话框（历史 + 显式 Switch consent + Roll back
  预览）；System Monitor 对 eligible incident（同 Project/app instance、非 resolved、
  历史存在 previous）显示 `Roll back to <v>`，文案区分 completed / no previous /
  permissions review / conflict / Core 不可达，并声明"Core 切换成功 ≠ App 健康"；
  版本变更确认后 best-effort 关闭该 installation 的窗口。
- 验证：
  - `make test-app-version-rollback`：PASS（真实 web-bundle 全栈：注册 v1/v2 →
    consent 安装 v1 → v1 Surface → UI transition → 旧 Surface 失效 → v2 Surface →
    UI rollback → Surface 失效 → API known-key rollback + exact replay → stale
    revision Aborted → unknown version NotFound → 回滚后 v2 Surface 重开）。
  - Go 集成 `TestAppVersionTransitionAndRollback`：PASS（no-previous fail closed、
    transition/history/replay/no-op、grant 扩张 fail closed 零副作用、bounded
    history 裁剪、分页、stale/foreign/unknown）。
  - restart battery 扩展 `tests/restart version-seed/version-verify`：transition/
    rollback 事实与两次 exact replay 跨 workos-core 重启成立（`make test-integration`
    全量回填见阶段 E）。
  - Desktop 单测：VersionDialog 4 例、SystemMonitor rollback eligibility/action 5 例；
    desktop-web 全套 107 tests PASS。

## 已验证命令（最终真实结果，2026-08-31）

| 命令                                                                                 | 结果                                                                                         |
| ------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------- |
| `make bootstrap`                                                                     | PASS                                                                                         |
| `make generate` ×2（幂等，无生成漂移）                                               | PASS（buf.build 一度对本机不可达，期间 gen 以 HEAD 恢复并验证字节等同；恢复后复验幂等 PASS） |
| `make check`                                                                         | PASS                                                                                         |
| `make test-integration`（含扩展 restart battery：version-seed/verify 跨重启 replay） | PASS                                                                                         |
| `make test-e2e`（全量 Playwright，含 adaptive 与 rollback spec）                     | PASS（13 passed）                                                                            |
| `make test-adaptive-shell`                                                           | PASS（4 tests，真实栈）                                                                      |
| `make test-app-version-rollback`                                                     | PASS（真实 web-bundle 全栈）                                                                 |
| `make test-lan-pairing`                                                              | PASS                                                                                         |
| `make test-artifact-review`                                                          | PASS                                                                                         |
| `make test-artifact-context`                                                         | PASS                                                                                         |
| `make test-deepseek-structured-review`                                               | PASS                                                                                         |
| `make test-credential-vault`                                                         | PASS                                                                                         |
| `make test-podman-fixture`                                                           | BLOCKED（宿主无 podman，按设计 loudly fail，不计 PASS）                                      |
| `docker compose config --quiet`                                                      | PASS                                                                                         |
| `buf breaking --against .git#branch=main`                                            | PASS（additive only）                                                                        |
| `go test -race ./internal/core/project/...`                                          | PASS                                                                                         |
| `git diff --check`                                                                   | PASS                                                                                         |

注：lan-pairing / artifact / deepseek / credential-vault 门禁在 replay 快照修复前的镜像
上通过；该修复仅影响 version 命令 replay 的版本快照投影，上述门禁不经过 installation
replay 路径；受影响的 integration / e2e / adaptive / rollback 门禁均在最终代码上复跑
PASS。

## 交接

### Branch / 提交

- 分支：`feat/v1-runtime-reliability-adaptive-closeout`（基于 main `12a53ab`）。
- 阶段提交（串行，未 merge、未 push）：
  1. `aa6535c feat: add shared adaptive shell device contract`
  2. `ceca68a feat: add adaptive project shell layouts`
  3. `feat: add deterministic installed app version rollback`（Core + Desktop UX + 门禁）
  4. （收尾提交）任务记录/状态/门禁结果
- 收尾纠偏：`feat:` 提交误纳入本地构建产物 `workos-core` 二进制，已在同一提交内
  amend 移除（提交尚未交接/未 push），并更新 `.gitignore` 根路径规则。

### Adaptive Shell

- 四种模式与截图：`docs/ui/desktop-web/changes/20260831-adaptive-shell/`
  （before 4 帧采自基线 `12a53ab` dist；after 9 帧采自实现后 bundle；`current/` 已同步）。
- layout state owner：device-local IndexedDB（origin 隔离），schema v1，key
  `layout/<deviceClass>/<projectId>`；仅存有界 canonical UUID 引用 + single/dual 偏好；
  多 tab 写入单事务串行仲裁；腐坏仅重置当前 key；uninstall/archive 漂移 sweep；
  无 IDB 环境降级内存。

### 版本 transition / rollback

- owner：workos-core Project Installation；单事务（installation + bounded history +
  revision + event + outbox + 幂等快照）；回滚目标锁内从 durable history 重推导。
- 幂等证据：same key 精确重放第一次响应（含 version 事实）——浏览器链路
  （`make test-app-version-rollback`）与 PostgreSQL restart battery（version-seed/
  verify 两次 replay 跨重启）共同覆盖；same key/different request 稳定 Aborted。
- 权限不扩张：grant 不兼容 → FailedPrecondition，rollback 不恢复更宽历史 grant。

### Podman / capability 裁决

- 宿主探测：podman 缺失 + user namespace 不可用（阶段 B 原始 verdict）→
  `container-runner`/`supervisor`/`incident-manager` 不升级，Runtime container 与
  Reliability 状态保持 scaffolded/unavailable；`docs/tasks/20260829-supervised-web-service-workload.md`
  保持 active。
- Rollback 能力按 Web Bundle 链路独立标 working；container rollback 未验收、自动
  canary/repair/deployment controller 仍 unavailable（ADR-0012 后果）。

### 未决风险与下一台 acceptance host

1. rootless Podman + cgroup v2 + 浏览器的真实 Workload/Incident/rollback-in-action
   证据待合格宿主：执行 `make test-podman-fixture`，通过后再执行
   `make test-supervised-workload-e2e`（若按 20260829 任务补建）与
   `make test-adaptive-shell` / `make test-app-version-rollback` 复验。
2. Fold-separated 的真机验证（真实 Window Segments API）待 Chromium flag 或
   foldable 设备；当前为注入 segment fixture。
3. System Monitor 的 incident 驱动 rollback 在真实 Incident 出现后需一次真机链路
   复验（当前为组件级确定性证据 + Core 命令真实链路）。

### 工作树 / 远端

- 最终验证后工作树干净（见门禁结果）；未 merge 到 main、未 push。
