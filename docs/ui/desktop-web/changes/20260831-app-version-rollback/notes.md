# 20260831-app-version-rollback 视觉记录

- 任务：`docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md` 阶段 D
  （ADR-0012：owner 触发的 App 版本 transition 与 previous-pinned-version rollback）。
- 受影响界面：App Library 已安装行（新增 `Versions` 入口与对话框：版本历史、Switch
  version、Roll back）、System Monitor 行（eligible incident 的 `Roll back to <v>` 按钮与
  完成反馈）。本宿主无 rootless runtime，System Monitor 帧只在浏览器边界注入 public
  `ListIncidents` 的确定性读响应；history eligibility、rollback 命令、Project revision 更新、
  Surface 关闭与 Runtime/Core 校验均走真实 Gateway/Core/Runtime 链路。这是 UI + Core
  rollback 证据，不冒充真实 observation → Incident 能力证据。
- before/：
  - `app-library--installed-granted--1440x900.png` 来自本任务阶段 D 前的既有 current
    基线（旧行：无 Versions 入口）。
  - `system-monitor--no-incidents--1440x900.png` 在 2026-09-01 审核时从阶段 D 前的精确
    commit `ceca68a` 以 `git archive` 临时只读导出后重新采集；固定 viewport 1440x900，
    视觉层把唯一 Project 名规范为 `Version Rollback Baseline` 并隐藏持久验收库中的无关
    Project/App 行，旧 System Monitor 无 rollback 动作位。临时导出在采集后已删除。
- after/（采集命令：`make test-app-version-rollback` 的同一真实链路加
  `WORKOS_CAPTURE_DIR` 重跑）：
  - `app-library--version-switched--1440x900.png`：Versions 对话框显示完整 history 与
    "Switched to 1.1.0." 反馈。
  - `system-monitor--rollback-eligible--1440x900.png`：确定性 Incident 行显示由真实 Core
    history 推导的 `Roll back to 1.0.0`。
  - `system-monitor--rollback-complete--1440x900.png`：真实 Core 命令完成、Project revision
    更新、旧 Surface 关闭，并明确“切换成功不等于健康”。
- current/ 已同步 after/ 同名文件。
- 旧的 `app-library--rollback-complete--1440x900.png` 只覆盖 App Library 内回滚，未证明
  System Monitor 入口，已从 after/current 删除并由上述两帧取代。
- 确定性：fixture 是本测试注册的合成 web-bundle app；服务端 Project/App/Artifact/
  idempotency 身份每轮唯一，视觉层把 app id 与 Project 名固定显示为
  `e2e-version-fixture` / `Version E2E Fixture`，并隐藏持久验收库中不相关的 Project 卡片；
  bundle marker、Incident、时间、viewport 1440x900、Chromium、DPR 1 均固定。
- 安全：无凭据、无真实用户数据；bundle 内容为固定合成 marker。

复现：

```sh
docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
docker run --rm --network host --user "$(id -u):$(id -g)" \
  -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
  -e WORKOS_E2E_URL=http://127.0.0.1:8080 -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
  -e WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260831-app-version-rollback/after \
  -v "$PWD":/workspace -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 pnpm exec playwright test app-version-rollback.spec.ts
```
