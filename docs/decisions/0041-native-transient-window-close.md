# ADR-0041：原生子窗口的精确关闭请求

- 状态：已采纳用于 V3 P0；真实 Code 弹窗与浏览器门禁仍须验证
- 日期：2026-09-26
- 关系：[V3 P0](../structure-v3.md)、[ADR-0040](0040-resident-greenfield-window-media.md)、[任务记录](../tasks/20260926-greenfield-native-window-close.md)
- 协议：[native_window.proto](../../api/proto/workos/surface/v1/native_window.proto)

## 问题

Runtime 投影的原生窗口不是 Core 的独立桌面窗口。Code 顶层窗口的 WorkOS 标题栏关闭应删除 Core 的 Native anchor，使此设备的 viewer Detach，不能退出 Code。真实 `Open File` 弹窗有 `parent_window_id`；若它的标题栏关闭也删除 anchor，就会连整个 Code viewer 一起关闭。只在浏览器 reducer 中移除子窗口同样错误：下一次 Runtime 完整快照会重新投影仍在运行的弹窗。

## 决定

有父窗口的原生子窗口用现有 `SendGreenfieldWindowInput` 的新 `close` 事件，请求原生客户端关闭**这个 UUID 对应的 top level**。事件沿用 attachment、owner/device、workload generation、control generation、逐事件屏障及 sequence ledger。观察者与旧控制端不能发送；批次重试沿用原事件序号，不能再次发送一次可能已生效的关闭。Runtime broker 在派发前校验当前快照中此窗口仍存在且 `parent_window_id` 非空；child 再按 UUID 找当前 surface，核对当前父关系与 top-level 身份后调用该 surface 的 `DesktopSurface.role.requestClose()`。绝不调用 `UserShellApi.closeClient()`，它会终止整个 Wayland client。

固定发布包 `@gfld/compositor@1.0.0-rc1` 的 `dist/Desktop.js` 已通过 `this.role.requestClose()` 执行 popup dismiss。`dist/remote/xwayland/XWaylandShellSurface.js` 的方法向此 X window 发 `WM_DELETE_WINDOW`；`dist/XdgToplevel.js` 的方法发 `xdg_toplevel.close()`。这是真实的原生 close 请求，和键盘 Escape/Alt+F4 不同。XWayland 窗口若不声明 `WM_DELETE_WINDOW`，上游只记录错误并不关闭窗口；WorkOS 不把请求 ACK 当作窗口已关闭。

浏览器在输入 ACK 后保留子窗口，直到 Runtime 的新完整窗口快照确认 ID 消失。请求失败或窗口长期未消失时显示明确提示。顶层 WorkOS 窗口仍关闭 Core anchor、Detach viewer，不向原生应用发 close。应用内 Exit 与显式 Stop 分别维持其现有生命周期语义。

## 验证边界

需要同时验证：精确子窗口请求不会触达父窗口或整个 client；无父窗口、过期窗口 ID、旧 generation/attachment/control 均拒绝；真实 Code `Open File` 弹窗关闭后 Runtime 快照移除子 ID，Code PID/starttime 与顶层窗口保持；顶层 WorkOS Close 只移除 viewer anchor 且同实例可重新附着。单元测试与合成截图不能替代真实 Code 浏览器门禁。
