# ADR-0031：持续应用与设备接续

- 状态：Accepted
- 日期：2026-09-15
- 关系：structure-v2 §3.4/§3.5；ADR-0002/0006/0016/0027/0028/0029。
- 调整：ADR-0028/0029 的"窗口关闭/项目切换即 Close（回收程序）"生命周期。

## 裁决

1. **运行与访问分离。** App 是软件及配置，Workload 是实际运行实例（带服务端派生
   `generation`），Surface/attachment 是设备访问关系（migration
   `workos_runtime.surface_attachments`）。新增 `workos.surface.v1.
   SurfaceContinuityService`：`ListProjectSurfaces`（发现运行实例）、`AttachSurface`
   （附着既有实例，不重复 Create 程序）、`DetachSurface`（仅释放本设备连接资源）、
   `RequestSurfaceControl`（显式接管）、`GetSurfaceControl`、`StopSurfaceWorkload`、
   `RestartSurfaceWorkload`（新 generation）。
2. **detach 不等于 stop。** 旧 `Close*` 方法保留 stop 语义（杀进程组、完整回收）；
   Native/PTY 新增 `Detach*` 只清理当前 peer、输入订阅与令牌。桌面窗口关闭/项目切换
   迁移为 detach；用户显式 stop 才停程序。停止经现有服务端授权与受监督生命周期执行，
   不开放旧 scaffold `StartWorkload`，不信任浏览器指定运行身份。
3. **有界策略。** 持续应用的 keep-alive/idle/过期策略沿用当前 30 分钟级会话上限
   （`WorkloadPolicy` 如实呈现作用对象、到期行为）；到期停止程序并显示真实原因；
   不把连接 TTL 延长成无限保活。策略到期、卸载、显式 stop、失败都会完整回收
   （进程组、媒体/输入 worker、私有 scratch），detach 不留泄漏。
4. **单控制端。** 交互式 Native/Terminal 每一 workload 同时只有一个控制
   attachment。控制权是服务端事实（`surface_control_leases`：绑定真实 Gateway
   device identity + attachment + 递增 `control_generation`）。新设备先 attach 进入
   可连接/等待控制状态，用户显式 `RequestSurfaceControl` 原子替换控制代次；旧
   controller 的输入、排队输入、resize 与迟到续期全部失效，前端隐藏输入框不算。
   同一设备续期不能夺走另一已接管设备；接管并发/重复/失联/过期收敛到确定结果。
   数据通道（Native 输入、PTY 写/resize）在服务端校验当前控制代次，不只信令入口
   查一次。
5. **真实 LAN 媒体。** 沿用 Pion/Xvfb 链路，解除仅回环限制的方式是提供实际可达的
   接口/候选/端口配置并限制在操作员配置的 LAN 范围；信令继续经 Gateway 并复用
   LAN TLS/配对；不通过移除认证或前端跨域例外"接通"。公网 NAT/STUN/TURN 属后续
   阶段。
6. **Runtime 重启核对。** 重启后按真实进程核对：Pdeathsig 已死者持久化
   `failed/stopped`，按策略重启记新 generation；不凭旧 running 行声称原进程活着。
   Web service 沿用同一运行/连接边界，各自生命周期不受"无限保活"影响。

## 后果

- 桌面 Close 调用迁移到 Detach（PTY/Native/Surface），语义变化在 SDK 与 UI 同步。
- Web App 页面内存状态不承诺跨设备恢复；验证只针对服务端程序与应用自身持久化。
- 控制代次校验是服务端强制，UI 状态只是呈现。

## 验收

- 状态机：`go test ./internal/runtime/surface/...`（attachment/control 状态转换、
  接管原子性、迟到续期/输入失效）。
- 组合链（B06/B07 后）：`make test-surface-continuity`（detach 后程序仍运行、
  stop 真回收、restart 新 generation、接管后旧输入无效）与真实 LAN 双设备验收
  （A16，独立入口）。
