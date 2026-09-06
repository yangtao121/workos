# Desktop repair visual evidence

任务：[能力修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线 3da0478；after 为本任务实现。路由 `/`，Chromium，deviceScaleFactor 1。
固定 viewport：1440×900、820×1180、390×844；固定时钟 2026-09-06T09:00:00Z。

`apps/desktop-web/e2e/desktop-repair-visual.spec.ts` 拦截 RPC 并提供固定 Studio / Field notes 项目、空任务/应用/通知。
无真实凭据、真实数据或外部服务。该 fixture 只用于视觉证据，不能证明跨进程功能。
四种状态是 Agent composer、Home、Mission Control、Command Palette，before/after 使用相同操作。
旧版 820px 的命令动作不能切换主视图，因此 before 记录操作后的实际失败界面；
`WORKOS_VISUAL_BASELINE=1` 仅允许旧版截图跳过对应可见性断言，after 必须通过。

采集：固定 `workos-playwright:1.62.1` 容器，`pnpm exec playwright test desktop-repair-visual.spec.ts`，
设置 `WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260906-desktop-repair/after`；
开发验证使用 `WORKOS_E2E_URL=http://127.0.0.1:5173`。before 使用基线构建的 gateway 8080。
三个测试 PASS。after 同名文件同步 current。其他功能状态截图和完整 E2E 仍在继续。

有意变化：统一深色控件与线性图标；紧凑 Dock；窗口标题栏有真实操作；
Home 图标卡片；手机内容在面板内滚动，底部导航保持可见；平板命令切换主窗口。
