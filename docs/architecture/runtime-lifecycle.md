# Runtime 程序生命周期（ADR-0037）

交互程序与设备访问独立。PTY、Native、Workspace Preview 和安装应用的 Web Service
均持久化 `lifecycle_mode`。新桌面显式启动／重启选择 `MANUAL_STOP`，关闭所有窗口或
断开全部设备后继续运行，直到主动停止、撤权、进程退出或宿主故障。资源配额、容器隔离、
工作区版本校验、Core 不可用时的保护和 Native 媒体授权没有放宽。

## 兼容与持久化

Migration 077 归 Runtime 所有。旧记录默认 `BOUNDED`；PTY／Native／Preview 保留原
30 分钟期限，安装应用保留配置的无 Surface 空闲回收期限。交互程序的 MANUAL_STOP
`expires_at` 为 SQL NULL，公开时间字段缺省；不使用极远日期或仅把零秒当作策略。
停止时间来自终态更新时间，不能把 NULL expiry 显示成停止时间。

创建与重启的幂等判断包含策略。默认／显式 bounded 保持历史摘要兼容，同 key 改为
manual-stop 拒绝。显式重启持久化新策略；可靠性驱动的安装应用重启未指定策略时保留
已经记录的策略。恢复访问不能改变程序策略。

## 真实执行与控制

Docker 交互容器的 bounded 命令仍带 `/usr/bin/timeout`，MANUAL_STOP 直接执行目标命令，
监督 context 只接受显式取消。Xvfb、Native 客户端、PTY shell 与 Preview 容器遵循同一策略。
安装应用只跳过空闲回收，健康、撤权与故障协调仍执行。周期维护会将实际退出的无期限
PTY 标为失败并释放配额；宿主重启后的 PTY／Native／Preview 不伪装成原进程仍存活。
安装应用沿用已有的严格容器身份核验与协调机制。

设备 attachment、输入控制代次、媒体授权和 TURN 凭证继续有限期并独立续约。共享窗口
焦点不接管输入。PTY／Native attach 可以指定工作负载 generation；不匹配时在创建
attachment 或授予控制之前拒绝。

## 精确恢复和程序管理

`GetSurfaceWorkload` 是只读、owner 范围的精确事实查询，包含终态；读取不会 attach 或启动。
`ListProjectSurfaces` 同时列出 PTY、Native 和安装应用的运行程序，报告真实 policy。
Native 还保留每个项目最近最多 16 条（含活跃条目）的终态会话，以便同一
workload ID 执行精确 Restart。项目列表读取会立即核对仍标记运行的 Native
子容器，不能靠后台扫描周期把已退出的程序继续显示为 running。
安装应用的显式停止／重启复用 Workload Manager 的持久操作回执。

安装应用恢复使用 `CreateSurface.attach_only` 加准确 workload ID／generation。
Runtime 经 Core 重新解析安装与版本，比较目标的 owner、project、app instance、app ID、
版本和 manifest digest，再创建当前设备自己的 Surface。失败时不得 Ensure 或启动替代容器。
静态 Web bundle 没有服务端进程，可以创建新的设备视图。Surface 凭据仍按原期限失效，
其生命周期不会随程序 MANUAL_STOP 变成无期限。

## Resident Native 应用退出

Greenfield proxy 的精确应用 PID 会与 Linux `/proc/<pid>/stat` 的启动时刻一起由
child bridge 监视。固定 rc1 的 APP_TERMINATED 信令透过仓库的 pinned patch
传出真实 `exitCode`／`signal`；只有 exit code 0 生成 STOPPED 窗口快照，
非零退出或信号生成 FAILED。PID 消失、僵尸态或 PID 被复用而没有终态信令时，
经过短暂信令宽限后以 FAILED 收敛。窗口列表变空本身不表示应用退出。

Runtime broker 只接受同 session／generation、递增 revision 的终态快照，并立即
停止旧 generation 的输入和媒体订阅。Native Service 将可信 STOPPED 持久化为
`closed`（Surface 显示 `stopped`），FAILED 或未收到终态快照的 child 死亡
持久化为 `failed`。Docker 监督对已接受的终态快照让位给该结果；没有快照的
child 消失在两秒宽限后仍报故障。Restart 复用原 workload ID，但增 generation
并启动新的精确 Docker child；旧附件与窗口不能作用于新 generation。

## 验证边界

Runtime 单元测试、独立 PostgreSQL 存储／幂等测试、实际 shell 与 Xvfb 进程测试和 Docker
程序保留测试在任务记录中列出。多浏览器 UI、部署及真机验收由共享桌面主任务统一记录。
