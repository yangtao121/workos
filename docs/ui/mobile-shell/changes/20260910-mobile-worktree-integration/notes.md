# Mobile Shell 首次视觉基线

任务：[移动壳工作树整合](../../../../tasks/20260910-mobile-worktree-integration.md)。

- 界面：移动壳 `/` 的 Gateway unavailable 状态。
- Fixture：Playwright 拦截 ListProjects 并固定返回 503，无外部服务或用户数据。
- Chromium，viewport 390×844，deviceScaleFactor 1，等待不可用状态与 key status 稳定。
- 采集命令：`make test-mobile-wrappers`。
- 本 client 首次建立基线，按 UI 约定以本次初始 current 同时建立 before；
  before/after 相同不表示旧 library bundle 曾有可启动 UI。
- after 同步到 `../../current/`；原生设备外观不在本次截图证据范围内。
