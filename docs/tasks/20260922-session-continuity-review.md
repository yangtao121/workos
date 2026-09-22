# 会话接续并发复核修复（2026-09-22）

状态：scaffolded（复核修复完成，待总任务 UI/E2E 验收）。分支 `fix/session-continuity-review`，独立工作树沿用已提交的后端任务工作树。

范围：AgentSessions 生命周期回调、持久草稿/回执跨 tab 合并、refresh 排队、游标边界、服务端 follow 超时及回归测试。不修改桌面客户端其他模块、协议和 E2E。依赖 39daa2e。

验收：查询后已卸载组件不能再提交；慢刷新不会被周期轮询饿死；空闲 tab 不擦除另一 tab 的草稿/回执；提交副作用前回执已持久保存；恢复永不自动重发；有界 cursor/follow timeout；针对性回归通过。

## 实现

- 重试先查同一 input ID；查询后、持久化后、提交前都检查组件代次与 AbortSignal。卸载取消请求；旧回调不能对旧会话执行新提交。
- snapshot 刷新串行批次合并；慢请求仍提交可见结果，同时将期间新请求合并到下一批，避免 5 秒轮询让慢连接永远无法完成刷新。
- 草稿、游标及待确认回执迁入独立 IndexedDB journal。readwrite 事务合并新增回执，仅删除服务端确认的 ID；只在显式编辑时更新草稿。发送前回执必须提交成功，否则保留草稿且不调用 RPC。旧草稿提交只能条件清除自身原值，不擦掉其他 tab 的新编辑。同一设备/会话采用最后一次显式编辑的草稿。
- Forget 清理函数保持 `clearSessionContinuity` 名称，现返回 Promise，调用方必须 await。清理代次立即阻断本页旧写入，数据库 epoch 在同一事务中清空记录并使其他 tab 的旧 writer 失效；旧 localStorage journal 也清除。桌面/AuthGate 接线由总任务负责。
- 游标严格限于 int64 非负范围；超前持久 cursor 相对 GetSession.lastEventSequence 重置，不丢弃草稿/回执。服务端 follow 使用整个请求的 context deadline，卡住的存储读取也在 2 分钟边界取消。
- 添加精确锁定的测试依赖 `fake-indexeddb@6.2.4`；并发测试运行真正的 IDB 事务序列，不使用同步假存储冒充原子合并。

## 验证与交接

固定 Node 24 与 Go 1.26.7 Docker 环境、宿主 UID/GID：目标文件 ESLint、Prettier；Vitest `AgentSessions.test.tsx` + `sessionContinuity.test.ts`；`go test -race ./internal/core/agent/transport`。

回归覆盖两个会话视图的只读刷新与新回执/草稿共存、并发回执合并、提交前持久化、配额失败不提交、卸载后的迟到 NotFound 不重试提交、旧 writer/跨 tab epoch 清理、慢刷新批次、游标上界/未来游标、阻塞数据库读超时。既有断线重连、恢复回执、离线草稿测试继续运行。

全量 desktop tsc 在该工作树只报告已知的旧 Desktop.test.tsx SurfaceSession fixture 缺少新协议 workloadId/workloadGeneration；该文件属于并行 UI 任务，本任务不修改。合并 UI 后由总任务运行完整类型检查、E2E 与截图门禁。状态保持 scaffolded 至总任务完成可见 UI/跨进程验收。

已验证：目标 ESLint/Prettier、24 个会话与事务存储测试、Go transport race 测试全部通过；没有调用 Provider 或改动共享开发栈。

补充：以桌面 tsconfig 为基线、仅 include 本任务四个 TS/TSX 文件的临时配置类型检查通过（保留相同 compilerOptions）；临时配置已删除。最终 24 项测试日志为忽略目录 `tmp/session-continuity-review.log`；Go race 日志为 `tmp/session-continuity-go-review.log`。
