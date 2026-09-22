# WorkOS 共享桌面与日常接续

- 日期：2026-09-22（UTC）
- 状态：done（实现与自动化交付完成；真机试用单列 pending）
- 基线：main@345c7ee，开始时工作树干净。
- 契约分支：feat/shared-desktop-contracts；后续 producer/consumer 使用独立工作树。
- 范围：Core 持久共享桌面、响应式客户端同步、持续会话重连、交互程序主动停止策略、局域网跨设备验收。
- 依赖：既有 V2 会话、Surface continuity、设备身份与授权、Docker Runtime。
- 不包括：公网部署、原生客户端扩展、自动恢复进程内存、任意 Web App 内部页面状态复制。

## 验收

1. 电脑/手机/平板共享项目、窗口、焦点与选中会话；服务端重启、离线重连和并发操作无旧状态覆盖。
2. 恢复窗口精确连接原 workload/preview，关闭窗口不停止程序，停止/撤权/控制代次真实生效。
3. 主动停止策略贯通数据库与真实进程，PTY／Native／Preview 旧记录与默认调用保留 30 分钟期限，安装应用保留原空闲回收。
4. 空闲会话接收远端更新，事件去重补齐，丢失响应不重复发送；本地草稿隔离。
5. Chromium/WebKit 自动化、固定三尺寸 UI before/after/current、跨进程 E2E、生成一致性与 make check。
6. 真机 Android/iPhone/iPad 试用单列证据；无真机证据不声明通过。

## 执行记录

- 已检查 Git：干净。既有开发栈 PostgreSQL 已停止，Core/Runtime 正重启；先恢复依赖并保留所有卷。
- 凭据继续只在仓库外保存，本任务不调用收费 Provider。
- 实施顺序：ADR/Proto → Core 与 Runtime → UI/会话 → 集成和视觉验收。
- 契约先合入 main，再按独立工作树实现 Core／Runtime／UI，最终进入 `feat/shared-desktop-integration`；未推送远端。
- 已通过 Chromium/WebKit 20 项门禁（0 skipped／unexpected／flaky）、真实安装应用第二设备接续与停止、Core/Gateway 重启后完整桌面投影不变。
- 全仓 `make check`、生成一致性、全新隔离共享桌面门禁与完整旧 V2 回归均通过；真机验收独立 pending。

## 交付与证据

- [Core 任务](20260922-shared-desktop-core.md)、[Runtime 任务](20260922-shared-desktop-runtime.md)、[客户端任务](20260922-shared-desktop-ui.md)。
- [会话接续](20260922-session-continuity.md)及[并发复核](20260922-session-continuity-review.md)：事务草稿/待确认回执、退出清理、迟到回调与慢刷新。
- [自动化证据](evidence/20260922-shared-desktop/results.md)与[物理设备待验收](evidence/20260922-shared-desktop/physical-devices.md)。
- 桌面 [before](../ui/desktop-web/changes/20260922-shared-desktop-ui/before/)、[after](../ui/desktop-web/changes/20260922-shared-desktop-ui/after/)、[说明](../ui/desktop-web/changes/20260922-shared-desktop-ui/notes.md)。
- 会话及 Forget [before](../ui/desktop-web/changes/20260922-session-continuity/before/)、[after](../ui/desktop-web/changes/20260922-session-continuity/after/)、[说明](../ui/desktop-web/changes/20260922-session-continuity/notes.md)。对应 `current/` 已同步。

已确认的边界：不收费调用 Provider，不同步任意 App 内部 DOM/表单，不恢复宿主故障后的进程内存；手动停止策略仍受撤权、真实退出与资源保护约束。

## 最终验证与清理

- `make check` 通过；Desktop31个测试文件、213项测试全部通过，其余workspace检查/测试与Go全仓检查通过。
- `make generate` 重跑217个生成文件哈希一致；最后整合的纯E2E预期修订另经ESLint/TypeScript检查。
- 新 namespace 共享桌面：20项Chromium/WebKit、37.44秒实际安装应用生命周期、Core/Gateway重启持久性全部通过。
- 新 namespace 原V2门禁：实际官方Harness fixture开发/问答/预览、Runtime中断恢复、12项主要跨进程回归及工作区引擎测试，4项浏览器/三尺寸视觉全部通过（32.5秒）。刷新后直接恢复原Native窗口并验证进程内存，避免旧测试在连接尚未就绪时重复发起启动动作。
- UI比较累计34个状态/尺寸（Home3、会话及Forget7、开发状态24），对应current已同步，所有图小于2MiB。
- 子任务提交与集成分支逐一核对patch等价后清理3个临时worktree；任务容器/网络按namespace回收，原默认开发数据卷保留。完成提交后快进到本地main，删除本任务分支；不推送远端。
- API凭据仍仅在仓库外、权限0600保存；本轮未读取/提交真实凭据或调用收费Provider。
