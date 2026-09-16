# ADR-0032：Runtime 专用 Docker 开发执行与原生恢复

- 状态：Accepted
- 日期：2026-09-16
- 范围：补齐 ADR-0030/0031 的实现选择；六进程边界不变。
- 依据：宿主非特权 user namespace 不可用，用户授权 Runtime 专用 Docker 后端。

## 执行与授权

Runtime 是唯一接触 Docker socket 的产品进程。命令、PTY、Native X 客户端及开发预览
各运行在独立容器，只挂载当前授权项目。Core 每次从 task/lease/session 推导 scope；
Runtime 再核对 Core 当前 binding/source/revision。只读通过容器只读挂载执行。授权改变或
Core 无法确认授权时，持续程序的一秒检查停止旧程序。普通 App 文件 Bridge 同样核对
Core 当前工作区，继续保留原有隐藏路径、链接、32 KiB 和 etag 规则。

容器固定禁网、只读根、drop ALL capabilities、no-new-privileges、128 pids、1 GiB 内存、
2 CPU、256 MiB 临时盘和有界日志。不传宿主环境、模型凭据或 Docker socket。Native
额外仅挂本 display 的 X11 socket；预览额外挂本程序的私有 HTTP bridge 目录。Runtime
和 Docker daemon 必须使用相同绝对项目路径；保护不能实施时返回 unavailable。
此后端是可信 Runtime 使用 Docker 的隔离，不宣称 Docker daemon 是 rootless。

命令最多五分钟，交互/预览程序最多三十分钟；容器自身 timeout 和 Runtime 监督同时
执行时限。PTY 和预览各四个、Native 两个，工作区命令全局四个并发。启动恢复只回收相同
`WORKOS_RUNTIME_CONTAINER_NAMESPACE` 的容器，避免独立部署互相清理。该值跨重启保持
稳定，每个独立测试栈使用不同值。

## 原生 Harness

固定官方 runtime 0.1.1rc1。薄 Cordis 插件使用官方 agents create/resume/followup，
不实现第二个推理循环。原生持久化 flush 后才能确认完成；每次 Task 重新取得凭据，
执行结束关闭带凭据的进程。文件和 Bash 通过官方 fs/shell 扩展点路由 Runtime。
会自行启动宿主进程的 grep 插件和 background bash 不开放。

Core 的输入接受、序号、执行槽及生命周期事件事务提交；Task admission 可先提交，
但 worker 只有在输入关联也提交后才能领取。关闭/取消期间留下的未关联 admission
由恢复流程取消。运行中租约过期标记 outcome unknown/needs_review，禁止自动重放副作用。
已完成轮次可以在 Harness 重启后原生恢复。输出 token 预算按原生每次模型请求累计扣减，
缺 usage 不能假定免费；原生 ask_user_question 经 canonical 持久交互返回浏览器。
问题仅限当前有效执行，过期两分钟；回答/拒绝幂等，过期、撤权或迟到矛盾回答被拒绝。
沙箱权限升级没有可执行授权后端，明确 unavailable；不自动批准。

## 程序与设备

关闭窗口仅 detach。显式 stop 回收程序，restart 保持逻辑 workload ID、增加 generation，
旧 attachment 失效。stop/restart receipt 永久绑定动作，丢响应可安全重试；旧 stop 重放
不能停止后来的 generation。Runtime 重启不声称恢复已死亡的进程，持久事实转 failed。

PTY write/resize 和 Native 信令及每个输入事件绑定 device 与控制 generation。
接管后旧连接、排队事件及迟到重连无效；同一设备重新接管也不能复用自己的旧代次。
观察者不替换当前 Native peer；点击 Take control 后才建立新的屏幕/输入连接。
Native peer 三十秒重新鉴权，前端二十秒更新；peer 数有界，旧计时器不能关闭新 peer。

LAN 模式要求明确私有 CIDR allowlist 和最多 1024 个非特权 UDP 端口；Pion 只发布命中
allowlist 的私有地址。Runtime 使用 host 网络，信令仍经 TLS Gateway/设备配对。
默认 loopback，不提供 STUN/TURN 或公网穿透。物理第二设备验收单独记录。

## 开发预览

真实 Node/Go 等开发服务在隔离容器内监听自己的 PORT。Runtime 通过专属 Unix socket
转发 HTTP，Gateway `/previews/<id>/<opaque capability>/` 仅访问该程序。控制 RPC 仍要求
设备身份；能力路径由 owner 查询得到，模型工具仅返回预览身份和状态。预览响应采用
opaque-origin iframe/CSP，禁止 WorkOS cookie/身份/凭据透传，不记录能力路径的 trace。
它不是任意 URL 代理。权限改变、停止、过期及 Runtime 重启会切断访问。

当前仅支持有界 HTTP 请求/响应，不支持 WebSocket/HMR；应用依赖须预先装入项目/工具链。
静态资源使用 `WORKOS_PREVIEW_BASE`。关闭 iframe 不停止服务，重开保持服务端内存；
重启产生新 generation。此链路是工作区开发预览，不是 P3 发布或构建产物回滚。

## 证据

入口 `make test-v2-completion` 使用独立 PostgreSQL、Vault、六个产品进程、官方 Harness、
本地模型响应 fixture、真实 Docker 文件/命令/预览及 Chromium。另有真实容器、事务、
租约、只读、撤权和问答矩阵测试。真实模型费用及两台物理设备证据独立登记在
[总任务](../tasks/20260915-v2-agent-workspace-web-continuity.md)，不可由 fixture 推导。
