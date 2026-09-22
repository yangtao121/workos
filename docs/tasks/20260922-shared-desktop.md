# WorkOS 共享桌面与日常接续

- 日期：2026-09-22（UTC）
- 状态：in_progress
- 基线：main@345c7ee，开始时工作树干净。
- 契约分支：feat/shared-desktop-contracts；后续 producer/consumer 使用独立工作树。
- 范围：Core 持久共享桌面、响应式客户端同步、持续会话重连、交互程序主动停止策略、局域网跨设备验收。
- 依赖：既有 V2 会话、Surface continuity、设备身份与授权、Docker Runtime。
- 不包括：公网部署、原生客户端扩展、自动恢复进程内存、任意 Web App 内部页面状态复制。

## 验收

1. 电脑/手机/平板共享项目、窗口、焦点与选中会话；服务端重启、离线重连和并发操作无旧状态覆盖。
2. 恢复窗口精确连接原 workload/preview，关闭窗口不停止程序，停止/撤权/控制代次真实生效。
3. 主动停止策略贯通数据库与真实进程，旧记录与默认调用保留 30 分钟期限。
4. 空闲会话接收远端更新，事件去重补齐，丢失响应不重复发送；本地草稿隔离。
5. Chromium/WebKit 自动化、固定三尺寸 UI before/after/current、跨进程 E2E、生成一致性与 make check。
6. 真机 Android/iPhone/iPad 试用单列证据；无真机证据不声明通过。

## 执行记录

- 已检查 Git：干净。既有开发栈 PostgreSQL 已停止，Core/Runtime 正重启；先恢复依赖并保留所有卷。
- 凭据继续只在仓库外保存，本任务不调用收费 Provider。
- 实施顺序：ADR/Proto → Core 与 Runtime → UI/会话 → 集成和视觉验收。
- 未决验收：上述全部新增行为尚未验证，不据此扩大现有模块 working 声明。
