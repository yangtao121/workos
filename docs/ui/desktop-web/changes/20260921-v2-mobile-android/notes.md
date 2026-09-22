# 移动触控入口

- 任务：[移动与 Android](../../../../tasks/20260921-v2-mobile-android.md)。
- before 从 `49227c9` 的构建运行，after 从本任务构建运行；使用同一
  `adaptive-visual.spec.ts`、固定 RPC fixture、Chromium、deviceScaleFactor=1、UTC。
- 390×844 compact home 新增 Native；820×1180 medium home 同样新增 Native；
  medium dock 新增 Home，以便在触控平板返回项目首页。其他导航保持原语义。
- 固定路由 `/`，项目 `Fixture Project`，无真实用户数据、凭据或外部模型。
- 命令：分别启动基线/当前静态构建，设置 `WORKOS_E2E_URL` 和 `WORKOS_CAPTURE_DIR`，
  执行 `playwright test adaptive-visual.spec.ts --grep 'captures the expanded'`。
  此任务仅保留上述三个受影响画面；after 同名文件已更新 current。
- 命令结果：`tmp/adaptive-visual-before.log`、`tmp/adaptive-visual-after.log`，各 1 passed。
  业务验收另由两个独立触控浏览器执行真实 HTTPS 配对、Agent 续写和 Native 接管。

- Native 触控输入的两张 `touch-streaming` 截图使用真实六进程固定 Development fixture，
  两个独立 Chromium context，`isMobile/hasTouch=true`、DPR=1、390×844/820×1180。
  before 来自输入栏修改前的 `tmp/v2-completion.znnbqt/visuals-lan`；after 来自最终
  `tmp/v2-completion.XWsdqN/visuals-lan`，同样的受信 HTTPS、原生绿色窗口和接管后状态。
  不包含真实用户数据，原生应用完全由固定脚本产生。
- 有意差异：触屏专用文本框、Send text 和 Enter/Backspace/Tab/Esc/Ctrl+C；按钮至少
  44px。测试用这些 UI 控件发送实际 SCTP 输入，核对远端颜色与内存状态。
  采集命令 `WORKOS_V2_SKIP_BUILD=1 sh tools/android-acceptance/gate.sh`；桌面普通鼠标
  截图仍使用原有 `native-window--streaming` 文件，避免混淆不同 pointer profile。
