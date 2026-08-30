# notes — 20260829-supervised-web-service-workload

## 任务

- 任务记录：`docs/tasks/20260829-supervised-web-service-workload.md`；ADR：`docs/decisions/0006-supervised-web-service-workload.md`。
- 受影响界面：App Library（Open 提交 server-selected renderer、失败文案 renderer 中性、
  in-flight 防抖沿用）；新增普通（非永久）System Monitor 窗口（dock ⏻ 按钮）。

## 采集环境与口径（诚实证据边界）

- 浏览器/视口：Playwright Chromium，1440×900，deviceScaleFactor 1。
- 采集命令：在 `workos-playwright:1.62.1` 容器内运行
  `pnpm exec playwright test visual-capture.spec.ts`（`WORKOS_E2E_URL=http://127.0.0.1:8080`，
  `WORKOS_CAPTURE_DIR` 指向本目录 `after/`），栈为 `make test-integration` 的 compose
  全套（含重建后的 runtime-host 与 reliability-host，migration 015/016 已由 bootstrap 应用）。
- fixture：`e2e/visual-capture.spec.ts` 通过公开 `AppRegistryService/RegisterApp` 注册严格
  container manifest（digest-pinned `localhost/workos-web-fixture@sha256:0123…cdef`，合成
  fixture 值，非真实凭据），经真实 UI 创建 Project、安装（空 grant consent）并 Open。
- **真实 ready 状态不可采集（环境 blocker）**：执行宿主没有 podman 且非特权 user namespace 被
  限制（`unshare --user --map-root-user` 失败），compose 内 runtime-host 如实上报
  `container-runner unavailable`（日志："verified rootless container capability unavailable"）。
  因此本任务只提交可诚实复现的状态：安装行 + 有界失败文案、System Monitor 空态。
  "web-service ready"与 Incident 出现的视觉证据需在通过 `make test-podman-fixture` 的宿主上
  采集后补录；不得在本机伪造 ready 截图。

## 文件

### before/

- `app-library--installed-granted--1440x900.png`：改动前基线（复制自当时
  `docs/ui/desktop-web/current/`，任务开始前 App Library 无 renderer=auto 行为、无
  System Monitor 入口）。

### after/（同时已更新 `docs/ui/desktop-web/current/`）

- `app-library--web-service-start-unavailable--1440x900.png`：安装的 container App（pinned
  digest 行）点击 Open 后，App Library 内的有界失败文案 "This app version has no supported
  surface renderer in this deployment, so it cannot be opened yet." —— 未消费 create key，
  不出现伪造窗口。
- `system-monitor--no-incidents--1440x900.png`：dock ⏻ 打开的 System Monitor 普通窗口，
  reliability upstream 可达、当前 Project 无 Incident 的固定空态。

## 有意差异

- 库中存在大量历史 E2E 注册应用（持久验收卷累积），属预期环境事实，不影响本任务断言。
- 截图中不含任何 token、host endpoint、container/cgroup ID 或真实用户数据。
