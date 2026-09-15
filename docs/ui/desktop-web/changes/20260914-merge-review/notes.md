# Native 合并审查视觉记录

任务：[v1 remaining capability sweep](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。

- 界面：Desktop Home 与 Native；路由 `/`；Chromium，deviceScaleFactor=1。
- Viewport：Home 1440×900；Native 1440×900、820×1180、390×844。
- Fixture：隔离 PostgreSQL/Core/Runtime/Gateway，真实 Xvfb + xterm + ffmpeg，
  项目名固定 Native Desktop Fixture，不访问外部服务或使用真实用户数据。
- 命令：`make test-native-surface`；本机通过 `wmake` 容器工具链执行。
- 验证：真实键入固定 ANSI 命令将 xterm 背景改为红色并隐藏光标，canvas 验证
  红色像素，确认解码时间持续递增、鉴权续期、布局切换保持同一会话，关闭后持久行 closed。
- 四张 after 已逐张目视检查并同步 current；媒体比例由 object-fit: contain 保持。
  黑色区域属于 800×600 的实际 X 根窗口；红色是原生客户端，不是截图替换或图片 fixture。
- 有意差异：去除每帧调试计数/会话 ID；保留连接状态。原 before 的随机 fixture 名、
  未等待解码及尺寸错误无法视为稳定基线，原图保留用于审计。修正后的门禁固定 fixture。
- 原 current 的 1440×900 文件实际上是 1280×720；原件以真实尺寸名保留在 before，
  Native 1440×900 before 使用 GLM 原任务中尺寸正确的图；Home 原图同样保留为 1280×720。
- 原生设备、远程网络/TURN 和内核隔离不由这些浏览器截图证明。
