# 共享逻辑桌面

遵循 [ADR-0037](../decisions/0037-shared-desktop-daily-continuity.md)。Core `desktop`
模块持有 owner 唯一桌面；Gateway 只公开 `workos.desktop.v1.DesktopService`。
桌面保存当前项目、窗口 UUIDv7、顺序、焦点和 canonical 资源引用。设备尺寸、位置、
输入内容、URL、凭据和 Surface capability 不进入桌面表。

## 原子操作与恢复

076 迁移的 `desktop_states`、`desktop_operations`、`desktop_events` 仅由 Core Desktop
访问。owner 行锁串行化 switch/open/close/focus/select/initialize；快照、revision 事件、
幂等摘要同事务提交。不能提交整份客户端快照。失败不消费 key；相同 key 不同操作返回
Aborted；相同 key 重放返回当前授权投影，不重做操作或恢复历史窗口。

首次 initialize 可导入至多 64 个语法合法引用，跳过所属模块明确拒绝的引用；已初始化
桌面不被后来的初始化覆盖。不存在窗口的 close/focus/select 不复活窗口。聚焦项目窗口
切换共享项目，其他项目窗口保留。Agent Sessions、预览管理器各项目单例，安装应用按
项目/installation 单例；显式打开更新选择的会话、预览或 workload 代次。其他窗口按完整
目标去重。窗口 ID 只由 Core 生成；关闭桌面窗口没有停止 Runtime 程序的副作用。

窗口类型是固定允许表：全局 home/device-center/notification-center/system-monitor/
mission-control/browser；项目工具 agent-center/agent-sessions/app-library/settings/files/
docs/code/artifact-center/knowledge-center/workspace-previews；资源窗口 app-surface/
artifact-viewer/terminal/native。会话、预览、安装、成果、程序 ID 均为 UUIDv7。
app-surface、terminal、native 可携带成对的 expected workload UUID/generation；终端和原生程序的 expected ID 必须等于 resource workload ID。同一程序的迟到旧代次不会覆盖新 pin；Runtime 在 attach 时裁决
精确身份，桌面引用本身不授予访问或输入控制权。

## 授权与状态清理

中立 orchestration 调用 Project、Installation、Agent Session、Artifact application port，
通过 Runtime canonical GetSurfaceWorkload/GetWorkspacePreview 校验程序引用，禁止跨模块 SQL。
每次读、命令和 watch 轮询重新检查项目归属/归档和资源归属；失效引用删除并产生新 revision。
历史事件含已撤权目标时直接 reset 到当前投影，不把失效历史发送给客户端。

Runtime 程序已经停止或 failed 仍是有效引用，恢复只显示真实状态并连接准确实例；不新建
替代程序。Runtime/所属模块明确 NotFound 才允许删除；依赖 Unavailable 保留此前授权的
引用，创建新引用仍返回 Unavailable。保留引用不绕过每次资源 RPC 的独立授权。

Desktop 使用独立数据库连接池，避免 owner 锁等待者耗尽其他模块授权读需要的连接。
命令/读取事务限时 10 秒，Runtime 引用 RPC 限时 2 秒。每请求缓存授权结果，仅在该事务
内合并 catch-up 历史中的重复查询，不跨请求缓存权限。

## Watch 与边界

WatchDesktop 依据持久 revision 补齐最多最近 256 份快照。游标早于保留窗口、位于未来、
事件不连续或历史授权改变时发送 reset_required，客户端替换投影。心跳不推进 revision；
500ms 轮询、15 秒心跳、2 分钟连接上限、每 owner 8 条流、1024 个并发 owner 上限。
连接断开不会删除桌面或取消程序。客户端必须在 EOF、网络恢复和前台恢复后重连。

Gateway 对 Desktop watch、AgentSession watch 和 Notification watch 每 30 秒重新校验
设备 session；明确失效中止流。鉴权库短暂不可用不自动判作撤销，服务端有界连接生命周期
及重新握手仍提供上限。私有 namespace 不进入公共路由允许表。

后端单元、Connect、Gateway 和隔离真实 PostgreSQL 用例见
[任务记录](../tasks/20260922-shared-desktop-core.md)。进程级 E2E、客户端投影和物理设备验收
由共享桌面总任务提供；本模块后端测试不能替代这些证据。
