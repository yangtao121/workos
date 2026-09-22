# ADR-0037：共享逻辑桌面与主动停止生命周期

- 日期：2026-09-22（UTC）
- 状态：accepted（用户已确认实施）

## 决策

单 owner 的设备共享一个服务端逻辑桌面：当前项目、各项目窗口、顺序、焦点及会话/应用引用。
Core 独占桌面表与事件。窗口矩形、屏幕布局、滚动、键盘和未发送草稿留在设备本地。
同一项目切换和窗口开关影响全部在线设备；桌面多窗口、手机单焦点、平板响应式渲染。

命令使用 owner-scoped 幂等键与事务串行化；不提供客户端整份覆盖接口。
服务端保存 UUIDv7 窗口 ID、受限 kind 与经授权校验的 canonical 引用，不保存 Surface token、URL 或内容。
Watch 返回有序 revision 快照，持久 cursor 可补齐；缺口显式 reset，重连先恢复服务器事实。
首次初始化可原子导入旧浏览器中可验证引用，后续初始化不能覆盖已有桌面。
关闭后迟到的 focus/select 不复活窗口；切项目保存各项目窗口。移除无权引用通过所属模块 port 校验。

程序启动必须是显式幂等操作，成功后才发布桌面引用；恢复路径只 attach 精确实例。
共享焦点不授予 Native/PTY 控制权，仍采用显式接管和代次检查。

新增 lifecycle_mode：默认/BOUNDED 保留 PTY／Native／Preview 原 30 分钟策略及安装应用原空闲回收策略，
MANUAL_STOP 用于新版桌面显式启动/重启。
Runtime 持久化实际策略，MANUAL_STOP 的 expires_at 缺省；独立枚举不能以零秒或极远日期冒充无限。
数据库、清扫器、进程 context、容器 timeout 同时遵循策略。此决策替代 ADR-0031 及 preview
文档对交互程序固定 30 分钟的限制，不改变工具命令/Agent 时限、资源配额和授权检查。
连接、控制租约、媒体/TURN capability 继续短期有效。关闭全部设备不停止程序；主动停止、撤权、
真实进程退出或宿主故障结束程序。宿主重启不宣称进程内存恢复。

会话 watch 的 follow 参数默认 false，保留旧 catch-up 行为；新版客户端启用持续订阅。
任务与会话事件按持久 sequence 去重重连，响应丢失先查询提交结果。草稿独立本地存储，
按部署/owner/project/session 隔离，Forget/退出清除；不得混入仅保存引用的布局存储。

## 兼容与验证

协议仅增加字段，不复用 v1 字段号。旧行保留 bounded，旧客户端默认调用不延长程序。
六个进程边界、表所有权和 canonical Provider 隔离保持不变。
以真实数据库/Runtime 和独立浏览器上下文验证并发、幂等、断线、撤权和精确实例接续；
Chromium/WebKit 证据与物理 Android/iPhone/iPad 证据分开记录。
