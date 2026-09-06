# App window Bridge repair

任务：[能力修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线 514373e 构建，after 为 shell 授权与窗口修复。真实本地 Gateway/Core/Runtime，
只创建 `Bridge workspace` 与 `Bridge Full E2E App` 专用 fixture；不调用外部服务。
Chromium，viewport 1440×900、deviceScaleFactor 1，路由 `/`，截图裁剪到应用窗口；
没有时间、随机 ID、真实内容或凭据。

`app-bridge-full.spec.ts` 先安装授予 project.read 的 Web Bundle，读取项目/主题，
重命名窗口；after 还设置徽标、最大化/最小化并经 Dock 恢复。before 的新方法未协商，
只记录原有窗口。旧应用库固定高层级遮挡 iframe，基线需手动 Close App Library；
after 断言启动后应用库已收起。截图中的白色内容是专用未经装饰的 Bridge fixture，
属于 App；深色标题栏属于 WorkOS。

采集：固定 `workos-playwright:1.62.1` 容器执行
`pnpm exec playwright test app-bridge-full.spec.ts`，`WORKOS_E2E_URL=http://127.0.0.1:8080`，
`WORKOS_BRIDGE_CAPTURE_DIR` 分别指向 before/after；before 另设 `WORKOS_VISUAL_BASELINE=1`。
两次均 PASS，after 同步 current。实际专项 `make test-app-bridge-full` PASS，
包含后续独立 API 撤销 grant 后旧窗口拒绝 project/rename/close 的断言。
