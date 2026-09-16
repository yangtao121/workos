# P3 VersionDialog 视觉复核

- 使用真实 desktop-web 应用、VersionDialog 组件和项目 CSS；仅 RPC 数据由 Playwright fixture 提供。
- 原实现的手写 HTML 截图已替换，不能作为组件视觉证据。
- `before/`：本轮审查修复前的真实组件；`after/`：加入发布状态轮询及 unavailable 提示后的真实组件。
- fixture：固定项目 Studio、board-app 1.1.0、固定版本历史和假摘要；无真实用户信息或凭据。
- 状态：published、rollback_pending、failed；viewport：1440×900、820×1180、390×844；Playwright 1.62.1 的 Chromium 环境，deviceScaleFactor=1。
- 命令：启动 desktop-web Vite，然后设置 `WORKOS_E2E_URL`、`WORKOS_CAPTURE_DIR`，运行 `playwright test e2e/version-dialog-visual.spec.ts --workers=1`。
- 前后采集各 3 个测试通过；复核了手机尺寸截图，版本切换与回滚按钮均可见。
- `after/` 的九张 PNG 已同步 `current/`。这些固定状态的外观不变；本轮修复针对轮询和故障行为。
