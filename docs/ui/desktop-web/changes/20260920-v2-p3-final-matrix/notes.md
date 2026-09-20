# P3 验收视觉记录

- 任务：[P3 剩余矩阵](../../../../tasks/20260920-v2-p3-final-matrix.md)。
- 本包不改变用户可见 UI；修复 V2 completion 门禁未设置截图目录导致视觉用例跳过的问题。
- `before/` 来自任务起始的 `current/`；`after/` 和更新后的 `current/` 来自本次真实 Chromium 采集。
- 命令：`sh tools/v2-completion/gate.sh`；运行目录 `tmp/v2-completion.D2WoWE`。
- 用例：`apps/desktop-web/e2e/v2-completion-visual.spec.ts`；固定 desktopFixture、UUID 和时间，模拟的项目、文件、预览、会话、Native 与 PTY 状态；不调用真实 Provider。
- 路由：`/` 中 Home、Settings/Project workspace、Files、Development previews、Agent sessions、Native display、Terminal 窗口。
- Chromium，deviceScaleFactor 1；viewport 为 1440×900、820×1180、390×844，每个尺寸八种状态，共 24 张 PNG。
- 三个视觉测试和一个真实浏览器业务测试全部通过；门禁现在拒绝 skipped/unexpected/flaky，并逐一验证三个 viewport 测试通过。
- 无预期像素变化。解码后比对 22 张完全相同；`files--conflict-draft--820x1180` 的边框有 7 个抗锯齿像素差异，`workspace-preview--running--390x844` 的窗口边框有 2 个像素差异。已人工查看两组，内容和布局一致。
