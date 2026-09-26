# Task: Greenfield P0 同实例接续与多设备审计

- 状态：done（只读源码审计；P0 功能未通过）
- Owner/Agent：Codex greenfield_reconnect_audit
- 分支/worktree：`docs/greenfield-reconnect-audit` / `/home/aquatao/workos-v3-p0-audit`
- 基线：`dc6aa20`，开始时本 worktree 干净；不修改运行中的容器、端口、Proto、migration 或 Runtime 代码
- 范围：固定 Greenfield `6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57`、`@gfld/compositor@1.0.0-rc1`、现有 WorkOS 代理和客户端的 A06/A07 可行性
- 依赖：[V3 P0 任务](20260924-v3-p0-native-experience.md)、[P0 任务书](../prompts/20260924-grok-4.7-v3-p0-native-experience.md)、[ADR-0038](../decisions/0038-greenfield-native-experience.md)
- 验收：列出代码证据、未获证明的事项、可测试的无 Xpra/VNC 架构方案和 A06/A07 验证步骤；本审计不把 P0 标记为完成

## 结论与证据边界

当前直连方案不能完成 A06/A07。已观察到的运行证据只有[首次连接白色画布、Code 仍存活](evidence/20260924-v3-p0-native-experience/results.md)：此环境当时未给容器映射 DRM render node，因而尚未通过真实桌面 GUI 编辑。以下重连与控制结论是固定版本源码审计及当前代码路径推论，**不是**已完成的浏览器端端到端复现。相同 PID 存活不能证明新浏览器能恢复未保存缓冲。

上游源码行号均对应固定 commit `6c578f4` 的 `packages/compositor-proxy/src/`，本地核对源为 `/tmp/workos-greenfield-6c578f4.tar.gz`。

1. **新浏览器无法重建已有窗口。** [`SessionController.ts:210-217`](https://github.com/udevbe/greenfield/blob/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57/packages/compositor-proxy/src/SessionController.ts#L210-L217) 拒绝第二条同时存在的 signaling WebSocket。断开后 [`NativeAppContext.ts:83-99,143-150`](https://github.com/udevbe/greenfield/blob/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57/packages/compositor-proxy/src/NativeAppContext.ts#L83-L99) 只发送断开期间排队的新消息，没有向新订阅者重放已有 channel、Wayland 资源、窗口层次或当前 buffer。浏览器包每次 `Session.create` 均构建空 `Display`（`node_modules/.pnpm/@gfld+compositor@1.0.0-rc1*/node_modules/@gfld/compositor/dist/Session.js:30-56`），协议 channel 事件才会 `display.createClient`（同包 `dist/remote/RemoteAppLauncher.js:193-248`）。WorkOS 的 [`0001`](../../deploy/patches/greenfield/0001-keep-clients-without-browser.patch) 与 [`0002`](../../deploy/patches/greenfield/0002-preserve-client-on-navigation.patch) 只取消 SIGHUP，不增加重放。
2. **仅重放 channel 描述仍不够。** 上游 [`NativeWaylandClientSession.ts:393-405`](https://github.com/udevbe/greenfield/blob/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57/packages/compositor-proxy/src/NativeWaylandClientSession.ts#L393-L405) 只缓存 WebSocket 未打开期间的增量消息，打开后清空；[`Channel.ts:223-233`](https://github.com/udevbe/greenfield/blob/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57/packages/compositor-proxy/src/Channel.ts#L223-L233) 在正常导航关闭时释放 ARQ/KCP 状态。已有 `wl_surface`、xdg/XWayland 层次、资源 ID、当前 buffer/FD、焦点和帧状态没有供新 compositor 使用的完整快照。将历史消息原样重放也会与已销毁/复用的对象和 FD 冲突；这需要专门的状态镜像及资源重绑定设计。
3. **无客户端时 frame-driven UI 可能停顿。** [`FrameFeedback.ts:69-80`](https://github.com/udevbe/greenfield/blob/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57/packages/compositor-proxy/src/FrameFeedback.ts#L69-L80) 在客户端反馈超过 1500 ms 未到后把 frame callback 放入 parked queue；[`83-89`](https://github.com/udevbe/greenfield/blob/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57/packages/compositor-proxy/src/FrameFeedback.ts#L83-L89) 仅在新的反馈到达后释放。此逻辑不会证明应用所有后台任务停顿，但不满足仅凭 PID 存活就声称 GUI 持续工作的门禁。
4. **观察者与控制隔离均未实现。** WorkOS [`application/greenfield.go:53-67`](../../internal/runtime/nativehost/application/greenfield.go) 在获取显示入口前就调用 `AuthorizeInputGeneration`，观察者无法通过 RPC 获取画面；[`proxy.go:57-68,109-140`](../../internal/runtime/nativehost/adapters/greenfield/proxy.go) 却只核对 owner，并向同 owner 的浏览器返回可复用的原始 signaling key。上游 [`SessionController.ts:225-229`](https://github.com/udevbe/greenfield/blob/6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57/packages/compositor-proxy/src/SessionController.ts#L225-L229) 接受 signaling `KILL_APP`。Greenfield display 的 [`inputGate` 仅保存、从未调用](../../internal/runtime/nativehost/adapters/greenfield/engine.go)（`engine.go:328,335`）；当前代理没有将 device、control generation 或有效期绑定到每个 WebSocket 输入事件。WorkOS [`NativeApp.tsx:416-423`](../../apps/desktop-web/src/NativeApp.tsx) 还把 `expectedWorkloadGeneration` 传作 `controlGeneration`，而真正的控制代次在 [`nativeSession.ts:113-116`](../../apps/desktop-web/src/nativeSession.ts) 的 attachment 中。
5. **UI 状态不是显示证据。** [`greenfieldCompositor.ts:34-40`](../../apps/desktop-web/src/greenfieldCompositor.ts) 调用 `launcher.launch()` 后立即返回；[`GreenfieldApp.tsx:43-55`](../../apps/desktop-web/src/GreenfieldApp.tsx) 在 WebSocket、窗口和首帧实际到达前就报告 `attached`/`ready`。白色画布仍可显示成功状态，与[现有实验](evidence/20260924-v3-p0-native-experience/results.md)一致。

## 两条无 Xpra/VNC 技术路径

**推荐的最小可行架构：Runtime 常驻的 Chromium Greenfield compositor。** Runtime 为每个 Native workload 监督一个 Chromium 进程或隔离 context，浏览器内运行现有 `@gfld/compositor`，持有唯一 signaling/channel 连接与完整 Wayland 状态；它与 Code 同生命周期，外部标签页全部关闭也不会销毁该状态。外部标准 Chromium 浏览器通过 WorkOS 授权的画面通道接收该 compositor 的窗口媒体，并通过受门禁的输入通道发事件。只对活跃控制租约、匹配 device + generation 的每个输入、resize、clipboard 写操作放行；切换控制时丢弃旧连接排队事件。Greenfield 原始 key 和代理端口仅留在 Runtime 内部。Runtime 常驻 Chromium 异常退出时不能伪装同实例恢复：若没有完整快照能力，应将 workload 标为显示失败，并由显式 Restart 产生新 generation。此方案保持 Greenfield 为原生 Wayland compositor，不使用 Xpra/VNC，但涉及新媒体桥、输入桥、生命周期和测试，不是 rc1 的小补丁。

**备选：扩展 Greenfield 代理支持快照/重建及多 viewer。** 必须完整捕获并重放 Wayland object graph、xdg/XWayland 窗口关系、当前 buffer/FD 和编码关键帧，给每个 viewer 独立协议状态，解决离线 frame callback、资源 ID 复用、控制端事件合流与授权。`rc1` 没有这些基础接口；仅补 signaling 重连或 SIGHUP 不足以通过 A06/A07。该路径是一项上游级协议工程，风险与范围高于常驻 compositor。

## 常驻 compositor 的最小契约分解（待方案选择，不在本审计修改 Proto）

1. **窗口/媒体**：增加“附着既有显示”的 viewer 契约，返回 workload ID、运行 generation、显示身份、尺寸/DPR、窗口 ID/层级及首帧可用状态；媒体提供实时帧和文本静止后的无损刷新。观察者与控制者都可只读观看，不以 `openGreenfieldDisplay` 的控制鉴权充当观看授权。首帧到达后才显示 ready。
2. **控制门禁**：输入消息携带连接 lease、device、control generation、递增序号与事件类型；Runtime 在每个事件执行前重查当前控制租约和会话有效性。resize、剪贴板写入、关闭应用同样受对应能力约束，观察者没有原始 Greenfield key。接管时废弃旧世代队列，按连接撤销键盘/鼠标按下状态。
3. **生命周期**：Runtime 监督 Greenfield proxy、Code、常驻 Chromium 三者；关闭普通 WorkOS 窗口只 detach viewer；零 viewer 保留 compositor 与 Code；Stop 清理三者；应用自然退出呈真实终态；compositor 故障呈 unavailable/failed，Restart 以新 generation 建立实例。记录 Code PID 加 `/proc/<pid>/stat` 启动时刻、proxy PID 与 Chromium PID，防止 PID 复用伪证。
4. **文本与显示**：独立有界 UTF-8 剪贴板桥，接系统 Wayland selection 和浏览器 Clipboard API；IME composition 以确定性 commit 路径注入且不能双发；DPR 1/2 由真实画布尺寸、`wl_output` 与 Code 重绘验证。以上均需要真实 GUI 端到端证据。
5. **测试**：先用受控 fixture 验证单客户端真实 Code 修改保存；再执行 A06/A07。协议测试覆盖旧世代输入/resize/clipboard、观察者只读、会话撤销、首帧超时；E2E 用两个隔离浏览器设备身份，最后由用户在物理 Mac Chrome/Edge 与第二设备复核。

## A06/A07 可复现实验与验收

- **前置门禁**：确认容器可访问 `/dev/dri/renderD128`，画布有真实 Code 像素且 GUI 可操作。记录官方 Code binary/version、固定 Greenfield commit、workload ID/generation、容器身份、Code PID 与 `/proc/<pid>/stat` starttime。现有白色画布证据不满足此前置。
- **A06**：在真实 Code 打开确定性 `note.txt`，键入唯一未保存标记，检查磁盘仍无标记。关闭所有外部客户端，至少超过 1500 ms frame-feedback 阈值，记录常驻 compositor 与 Code 仍在；再以全新浏览器 profile 附着同 workload。必须在保存前可见标记和相同窗口，且 Code PID/starttime、容器身份、generation 未变。随后 GUI 保存并从磁盘读到该标记。只核对 PID 或重开文件不算通过。
- **A07**：使用两个不同的设备会话同时附着。A 控制、B 观察，B 能看实时更新但 B 的输入/resize/剪贴板写入被拒绝；B 显式接管后 A 已发送但尚未执行的事件全部无效，B 可继续修改同一 Code 未保存缓冲。断开 B、A 再接管，检查旧 B 连接/迟到续约仍失效，Code PID/starttime 与 workload generation 全程不变。自动化记录拒绝结果、画面和事件序号；物理 Mac/第二设备另留执行记录。

## 交接

- 已验证：固定上游 tarball 与本仓库源码静态审计；没有运行容器、浏览器或端到端测试；本提交仅文档。
- 未决风险：真实 Code EGL/像素尚未验证，常驻 Chromium 的 WebGL/WebCodecs、媒体延迟、文本清晰度和资源占用需要原型与 A01–A12 实测。
- 下一步：根据用户对常驻 compositor 方案的选择实施上述契约/原型，或按 P0 未通过保留真实状态；不能因本审计完成而更新 `docs/status.json` 为 done。
