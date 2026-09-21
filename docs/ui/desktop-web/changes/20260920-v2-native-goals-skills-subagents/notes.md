# 原生目标与子任务界面

- 任务：[原生自动化](../../../../tasks/20260920-v2-native-goals-skills-subagents.md)。
- 入口：Project → Agent Sessions → 固定 fixture 会话；状态为 active、paused、children。
- 固定 viewport：1440×900、820×1180、390×844，device scale factor 1，Chromium 1.62.1 对应镜像。
- 固定时间：2026-09-06 09:00 UTC；`desktopFixture` 与 `native-automation-visual.spec.ts` 提供无凭据数据。
- before：P3 基线构建 `/home/aquatao/workos/apps/desktop-web/dist`，本地静态服务器 38911，
  相同 fixture，设置 `WORKOS_NATIVE_BASELINE=true`。旧客户端忽略新增目标和子任务字段。
- after/current：完整 `sh tools/native-automation/gate.sh` 的三尺寸视觉用例，
  `tmp/v2-completion.N4kvoP/visuals`；四个浏览器用例全通过，无 skipped/flaky。
- 独立视觉命令：设置 `WORKOS_NATIVE_CAPTURE_DIR` 与 `WORKOS_E2E_URL` 后运行
  `playwright test native-automation-visual --workers=1`。
- 有意变化：会话内展示目标/轮数/暂停恢复按钮，以及独立子任务状态、摘要和差异审阅入口。
  390 宽度使用原有内部滚动区域；children 截图滚动到结果区域，后续子任务可继续滚动查看。
- 三种尺寸的 before/after 已目视检查；截图仅用 deterministic fixture，不含真实模型输出或凭据。
