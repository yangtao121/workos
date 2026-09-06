# Notification settings and contrast repair

任务：[能力修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线为本任务桌面检查点 514373e，after 为 Web Push 设置与通知对比度修复。
Chromium、deviceScaleFactor 1、viewport 1440×900 / 820×1180 / 390×844；
固定 UTC 2026-09-06T09:00:00Z。路由 `/`，固定 Studio / Field notes 项目与空通知。

`apps/desktop-web/e2e/push-settings-visual.spec.ts` 使用共享 `desktop-fixture.ts`，
没有真实内容、凭据或外部服务。before 点击通知铃铛；after 相同操作后展开新设置。
基线的移动顶部铃铛只创建窗口而没有切换主视图，before 如实记录主页，
基线模式仅跳过这两个旧缺陷状态的可见性断言。after 必须显示通知和免打扰控件。

固定容器 `workos-playwright:1.62.1` 执行 `pnpm exec playwright test push-settings-visual.spec.ts`；
`WORKOS_PUSH_CAPTURE_DIR` 指向本目录 before 或 after；before 8080 的基线构建，
after `WORKOS_E2E_URL=http://127.0.0.1:5173`。前后各 3 PASS，after 同步 current。

有意变化：深色通知筛选、通知卡片与高对比文本；可展开浏览器提醒/UTC 免打扰设置；
未配置 Web Push 时显示明确原因。另修复知识结果与遥测面板遗留浅色背景，其证据仍待补齐。
