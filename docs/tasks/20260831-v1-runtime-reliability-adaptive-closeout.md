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

## 已验证命令

- 基线：bootstrap/generate×2/check/test-integration → PASS（见上）。
- 阶段 A：`pnpm --filter @workos/adaptive-shell check`（36 tests）、
  `pnpm --filter @workos/desktop-web test`（102 tests）、`pnpm --filter @workos/desktop-web build`、
  `make check`、`make test-adaptive-shell`（4 E2E）→ 全部 PASS。
- 阶段 B：`make test-podman-fixture` → BLOCKED（见上）。

## 交接

（收口时回填：branch/HEAD、阶段提交、Adaptive 模式与截图路径、layout state owner/schema、
transition/rollback 事务与幂等证据、Podman 探测 verdict、capability 升级裁决、
命令 PASS/FAIL/SKIP/BLOCKED、未决风险、工作树、merge/push=否）
