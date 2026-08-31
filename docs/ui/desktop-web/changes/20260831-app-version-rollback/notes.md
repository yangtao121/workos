# 20260831-app-version-rollback 视觉记录

- 任务：`docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md` 阶段 D
  （ADR-0012：owner 触发的 App 版本 transition 与 previous-pinned-version rollback）。
- 受影响界面：App Library 已安装行（新增 `Versions` 入口与对话框：版本历史、Switch
  version、Roll back）、System Monitor 行（eligible incident 的 `Roll back to <v>` 按钮，
  需要真实 Incident，本宿主无 rootless runtime，故以组件测试证明，不含可采集状态）。
- before/（来源：本任务开始前的 `current/` 基线，commit `12a53ab` 之后、本任务改动之前的
  采集，复制并注明）：
  - `app-library--installed-granted--1440x900.png`（旧行：无 Versions 入口）
  - `system-monitor--no-incidents--1440x900.png`（旧行：无 rollback 动作位）
- after/（采集命令：`make test-app-version-rollback` 的同一真实链路加
  `WORKOS_CAPTURE_DIR` 重跑）：
  - `app-library--version-switched--1440x900.png`：Versions 对话框显示完整 history 与
    "Switched to 1.1.0." 反馈。
  - `app-library--rollback-complete--1440x900.png`：rollback 完成反馈 "Rolled back to
    1.0.0."。
- current/ 已同步 after/ 同名文件。
- 确定性：fixture 均为本测试注册的合成 web-bundle app（`Version E2E <stamp>`）；
  viewport 1440x900、Chromium、DPR 1。stamp 仅存在于行内小字，不在断言区域。
- 安全：无凭据、无真实用户数据；bundle 内容为固定合成 marker。

复现：

```sh
docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
WORKOS_CAPTURE_DIR=docs/ui/desktop-web/changes/20260831-app-version-rollback/after \
  docker run --rm --network host --user "$(id -u):$(id -g)" \
  -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
  -e WORKOS_E2E_URL=http://127.0.0.1:8080 -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
  -e WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260831-app-version-rollback/after \
  -v "$PWD":/workspace -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 pnpm exec playwright test app-version-rollback.spec.ts
```
