# 网络接续视觉记录

- 任务：[网络接续](../../../../tasks/20260921-v2-network-continuity.md)。
- Native 连接配置属于不可见实现；同一 UI 现在可以通过 TURN 接续。截图记录真实解码的远程画面。
- 初始 current 基线检查后，重新运行 `2c3eb28` 的 Desktop 构建，覆盖 before 为同一确定性 fixture；没有用旧的红色模拟画面冒充前后对照。
- before：`WORKOS_V2_SKIP_BUILD=1 WORKOS_NETWORK_BASELINE_DIST=/home/aquatao/workos-v2-native/apps/desktop-web/dist sh tools/network-continuity/gate.sh`，`tmp/v2-completion.oi7j7p`。只替换桌面静态文件，后端和生产 HTTPS/配对均为当前测试栈；LAN 模式支持旧版无 TURN 的桌面。
- after/current：`WORKOS_V2_SKIP_BUILD=1 sh tools/network-continuity/gate.sh`，`tmp/v2-completion.0RRDDo/visuals-relay`。LAN 和 NAT 两个用例均执行，旧 baseline 运行不计入完整网络通过。
- Chromium 1.62.1 对应浏览器、deviceScaleFactor 1；1440×900、820×1180、390×844。
- 路由 `/`，Development fixture 的 Native 窗口：两端显式接管、关闭重开后同一程序，绿色终端、隐藏光标，无真实内容或凭据。after 还经过 TURN 停止/恢复。
- 预期布局与像素无功能性变化；连接从私有 LAN host candidate 改为 relay candidate。三尺寸已人工查看。
