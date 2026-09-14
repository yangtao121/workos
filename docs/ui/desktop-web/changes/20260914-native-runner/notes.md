# 20260914-native-runner 采集说明

- 任务：`docs/tasks/20260903-v1-remaining-capability-sweep.md`（GLM-5.3 续作段，R3）。
- 受影响界面：Home launchpad（新增 Native 入口）；新增 Native 系统窗口（ADR-0029
  虚拟显示 WebRTC 远程面）。
- 采集：`make test-native-surface`（tools/native-surface/gate.sh，
  native-surface-desktop.spec.ts）内联截图，Chromium，1440×900，
  deviceScaleFactor 1，真实 Gateway/Core/Runtime/Xvfb/ffmpeg 栈与确定性
  fixture 项目。
  - home--launchpad：打开 Home 后截取，Native 入口与 Browser/Terminal 并列。
  - native-window--streaming：Native 窗口 streaming 状态（真实 VP8 轨道
    videoWidth>0、帧计数持续增长、canvas 像素方差非零且键入后变化），
    1440×900 / 820×1180 / 390×844 三档 viewport（resize 后会话与视频持续）。
  - 默认栈（无 X11 工具链）的不可用态由 test-desktop-system-apps 断言
    （native-status/native-verdict unavailable），属另一部署形态。
- before：home--launchpad 复制自 prior current（Native 入口为新增）；
  native-window 为新 surface，无历史基线（首张即 current）。
- current 已用 after 同名文件更新。
- 已知边界：runner 仅在配置 X11 工具链的 runtime 可用（默认栈如实
  unavailable）；媒体路径仅回环 host candidates，无 TURN。
