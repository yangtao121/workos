# 20260915-v2-agent-workspace-ui 采集说明

- 任务：`docs/tasks/20260915-v2-agent-workspace-web-continuity.md`（B05 Agent 会话窗口 +
  B08 桌面接入：Native/Terminal detach+stop、Running apps、workspace 名称展示）。
- 受影响界面：
  - 新增 Agent Sessions 系统窗口（列表 + 会话视图，B05，无新增常驻侧栏）。
  - Terminal 窗口工具条：新增 Stop（显式停止）与 Take control（单控制器接管）；
    窗口关闭语义改为 detach（程序继续，30 分钟上限策略）。
  - Native 窗口工具条：同上（本栈无 X11 采集工具链，如实呈现 unavailable 判定）。
  - Home 启动台：新增 Running apps 小节（ListProjectSurfaces 服务端事实 + open/stop）。
  - 自适应 shell：home 快捷入口新增 Agent Sessions 按钮（compact/medium 可达）。
- 采集：`make capture-v2-development-journey`（`tools/v2-development-journey/gate.sh`，
  agent-sessions.spec.ts 内联截图），Chromium，deviceScaleFactor 1，确定性 fixture：
  共享 compose 栈 + 确定性 fake provider + 新建 fixture 项目（无真实模型、无真实凭据）。
  - agent-session-window--completed-run：会话视图（一轮已完成输入 + fake provider 时间线）。
  - agent-sessions--list：刷新后的会话列表（同名会话可恢复）。
  - terminal-window--controls：Terminal 窗口工具条（controlling 状态 + Stop），真实
    login shell 输出（fixture 容器，无敏感数据）。
  - home--running-apps：Home 启动台 + Running apps（Terminal workload running 态）。
  - native-window--unavailable：默认栈（无 X11 工具链 runtime）的如实不可用判定；
    与 native-surface 门禁栈的 native-window--streaming 属不同部署形态。
  - 三档 viewport：1440×900 / 820×1180 / 390×844（390 档输入区可用）。
- before：
  - home--launchpad 三档复制自 prior current（Running apps 为新增小节）。
  - agent-session-window / agent-sessions--list / terminal-window--controls /
    native-window--unavailable 均为新 surface，无历史基线（首张即 current，符合
    docs/ui/README.md 首建约定）。
- current 已用 after 同名文件更新。
- 已知边界：会话/输入 ID 为服务器 UUID，不在截图中呈现文本；Terminal 输出为真实
  shell 提示符（fixture 容器固定用户）。
