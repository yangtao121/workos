# 持续会话跨设备接续

- 日期：2026-09-22（UTC）
- 状态：in_progress
- 分支：feat/shared-desktop-integration
- 范围：会话 follow watch、事件重连与去重、空闲会话更新、本地草稿和提交回执。
- 依赖：ADR-0037 与共享桌面契约；桌面负责同步 selectedSessionId、身份结束清除本地 journal。

## 实现与验收

- 旧 WatchSessionEvents 保留有限补齐；follow 模式持续读取持久事件，每 15 秒 heartbeat、两分钟续订，
  每 owner 16 条流限制。每次读取重新校验会话归属；Gateway 中途设备撤权由 Core 桌面任务接线。
- 会话列表与空闲详情可看到远端更新；任务流从已接收 sequence 重连、去重。重新挂载从服务器重建
  transcript，生命周期 cursor 与本地草稿/待确认回执持久保存。
- 浏览器 origin + owner/project/session 隔离本地内容 journal，与仅存引用的桌面布局分开；
  请求发出前保存 idempotency key，重新进入只查询回执。离线发送保留草稿，不建立自动提交队列。
- 已通过：Go transport 会话 watch catch-up/follow/归属失效/非法 cursor；前端 15 项测试涵盖
  空闲远端输入、事件 EOF 重连与重复事件、草稿重挂载、丢失响应重挂载、离线及原会话行为。
- 验证日志：`tmp/shared-session-go.log`、`tmp/shared-sessions-ui3.log`。
- 待验收：最终集成树全仓检查、真实六进程跨浏览器、任务级 UI before/after/current。
  本任务尚不标记 done；物理设备验收与自动化证据分开。
