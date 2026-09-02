# Notes: 20260902-local-first-notifications

任务文件:`docs/tasks/20260902-v1-local-first-notifications.md`;ADR:
`docs/decisions/0014-local-first-notifications.md`;实现分支
`feat/v1-local-first-notifications`(基线 `4e6e0b8`)。

## 受影响界面

- Desktop Shell(expanded):顶部 system-bar 新增铃铛按钮 + 有界未读 badge
  (`notification-bell` / `notification-badge`),打开普通 Notifications 窗口。
- Adaptive Shell(compact/medium/fold-separated):adaptive-bar-actions 常驻铃铛;
  medium Dock 新增 Notifications 入口;compact 底部导航新增
  `nav-notifications`;home 快捷区新增 Notifications 按钮。
- 新窗口 Notification Center(`NotificationCenter.tsx`):All / Current Project /
  Unread 有界过滤、Mark visible read、逐条 typed action(approval/task/
  artifact/incident/app,打开前经公开服务重验)、固定 stale 文案、
  流状态横幅(reconnecting/resync/unavailable+Retry)、incident freshness 提示。

## 视觉记录

- `before/`:`expanded--desktop--1440x900.png`(改动前 expanded 桌面,无铃铛)、
  `notification-center--bell-badge--390x844.png` / `--820x1180.png`
  (改动前 compact/medium 主界面,取自 `current/compact--home--390x844.png`、
  `current/medium--home--820x1180.png`)。铃铛与 Center 为全新表面,
  按约定无旧基线。
- `after/` + `current/`(同一 fixture、viewport、路由、交互状态):
  - `notification-center--bell-badge--1440x900.png`:expanded 顶部铃铛 + 有界 badge。
  - `notification-center--unread-list--1440x900.png`:Center 未读列表(系统 +
    app 来源),All/Current Project/Unread 过滤、Mark visible read (3)。
  - `notification-center--typed-action--1440x900.png`:Open artifact 打开
    只读 Artifact Review 窗口。
  - `notification-center--bell-badge--820x1180.png` /
    `notification-center--unread-list--820x1180.png`:medium Dock 可达。
  - `notification-center--bell-badge--390x844.png`:compact 底部导航可达。
  - `notification-center--app-origin--390x844.png`:granted App 产生的
    `app.instance.message` 事实带显式 `· app` origin 标签。

## Fixture 与采集

- 采集命令:`make capture-notification-visual`(真实 Gateway/Core/harness/
  runtime + Chromium;`notification-visual.spec.ts`)。
- Fixture:固定标题/正文的 fake-harness 任务(terminal + artifact 通知)与
  固定文本的 granted Web Bundle App(`notifications.create`,固定
  idempotency key `visual-fixture-key-1`);采集前经公开 read 命令把历史
  未读清零,badge 恰为本次 3 条。ID/时间戳不出现在画面内。
- Viewport:1440×900、820×1180、390×844,deviceScaleFactor 1,Chromium。
- 验证:每张 PNG 尺寸人工核对(`file`),内容人工检查(标题、badge、
  origin 标签、按钮可读)。

## 已知有意差异

- 深色窗口主题上 Center 项使用浅色背景与深色文字(可访问性对比度),
  铃铛 badge 为有界计数(>99 显示 99+)。
- 截图中历史已读通知仍出现在 All 列表(durable 保留策略),以未读样式
  区分。
