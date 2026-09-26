# ADR-0040：Runtime 常驻 Greenfield 合成器与窗口 PNG 媒体契约

- 状态：已采纳为 P0 实施契约；实现与验收仍待完成
- 日期：2026-09-26
- 关系：[structure-v3 P0](../structure-v3.md)、[ADR-0038](0038-greenfield-native-experience.md)、[重连审计](../tasks/20260926-greenfield-reconnect-audit.md)
- 协议：[native_window.proto](../../api/proto/workos/surface/v1/native_window.proto)

## 背景与裁决

Greenfield 的 Wayland object graph 和当前 buffer 活在浏览器 compositor 中。固定 proxy 只允许一条 signaling 连接，断线后仅转发新消息，不重放已有窗口和 buffer；关闭第一个浏览器后新浏览器无法据此恢复同一 Code 未保存缓冲。详见重连审计。P0 采用 **Runtime 管理的常驻 Chromium/Greenfield compositor**：它与官方 Code、proxy 同属一个 Native workload 生命周期，持有唯一 Greenfield signaling/channel；普通浏览器只作为 WorkOS viewer，经 Gateway/Runtime 看窗口媒体、提交授权输入。所有 viewer 离线时三个进程继续运行。无需 Xpra/VNC，也不把原始 Greenfield key、proxy WebSocket 或 child 容器端口给客户端。

原生显示进程与 Node bridge 必须放在 Runtime 管理的**每 workload 独立子容器**；可信 Go broker 留在 runtime-host。这是 runtime-host 拥有的隔离执行资源，不新增第七个服务边界。子容器 `NetworkMode=none`，Code/proxy/Chromium 只共享子容器 loopback；仅挂所授权的工作区/fixture、私有 IPC 目录及需要的 GPU render node。子容器不得挂 Docker socket、数据库密钥或 runtime-host 根目录；显式最小环境变量允许清单，不继承 runtime-host 环境；非 root、最小 capabilities、受限文件系统和资源上限由实施任务落实并验收。隔离、GPU 或必要挂载不可用时 Create/Restart 返回 `unavailable`，不得退回 host-network 同容器启动。

此边界是当前实验路径的修正前置：[`engine.go:129-133`](../../internal/runtime/nativehost/adapters/greenfield/engine.go) 用 `cmd.Env = append(os.Environ(), ...)` 启动 proxy；[`compose.yaml:141-149`](../../compose.yaml) 让 runtime-host 继承含数据库连接的环境；[`deploy/compose.workspace-runtime.yaml:12-32`](../../deploy/compose.workspace-runtime.yaml) 可选地给 runtime-host 挂 Docker socket。只清洗子进程环境不足以隔绝 host network 回环服务和容器挂载。

## 固定版本与逐窗口输出

proxy 固定 commit `6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57`；浏览器合成包另固定为实际发布的 `@gfld/compositor@1.0.0-rc1` 及锁文件完整性。二者不能被当成同一源码构建：固定 commit 的 `packages/compositor/src/UserShellApi.ts:41-43,77-78` 是 `initScene(canvasCreator)`，发布包 `types/UserShellApi.d.ts:17` / `dist/UserShellApi.js:17` 是 `initScene(sceneId, canvas)`。补丁和可重建产物必须针对发布包定版，并记录 patch 哈希。

发布包的 surface 事件可识别 top level，popup/subsurface 从属于其父树；然而 `dist/render/Renderer.js:81-115` 把全局 `viewStack` 给每个 scene，`dist/render/Scene.js:28-34` 全部绘制。因此实施必须补 scene 到单个 top level 的绑定、完整 popup/subsurface 树过滤、按 `visual_rect` 平移投影、几何变化/层次/z-order 的有序窗口事实。每个 `GreenfieldWindow.id` 是该 native top level 生命周期内稳定的 Runtime UUIDv7；窗口修订号变化后新媒体帧必须匹配该修订号。跨窗口 popup 与 XWayland transient 归属以真实 Code 测试裁决。多 canvas API 本身不算逐窗口实现证据。

## 媒体与首帧

P0 采用每窗口 PNG tile：child Chromium 对单独 canvas 的脏区编码，经私有 Unix socket 给 Go broker；Go 向授权 viewer 复用不可变 tile，`WatchGreenfieldWindowFrames` 是 Connect server stream。`WatchGreenfieldWindows` 首条消息为完整窗口快照，后续也传完整快照，`revision` 在 workload generation 内单调递增。每个 snapshot 最多 128 个窗口、序列化后最多 1 MiB，单个标题/App ID 最多 4 KiB；Runtime 对 child 输入和公开输出均校验。超限报 unavailable，不能截断窗口列表并称为完整快照。两个流均在开始时及运行中校验授权。浏览器能在没有窗口时看到真实 `RUNNING` 空列表，但不能把它标成原生 UI ready。

每块最多 512×512 物理像素、PNG payload 最多 2 MiB；每个 frame 最多 64 块、合计 PNG 最多 24 MiB，frame 宽高各最多 4096 物理像素。超上限报告 `unavailable`，不裁成看似成功的缺失画面。初订阅、resize/窗口修订变化、丢帧后的下一帧必须是覆盖整个 `frame_width × frame_height` 且无缺口/重叠的 `full_refresh`。每块带相同的 window ID、generation、window revision、frame sequence、tile count 和 rendered time；`rendered_at` 是真实常驻 compositor 生成帧时的 UTC 时刻，不是 broker 转发时间，也不证明客户端已显示。客户端只按同一 frame sequence 收齐、验证、解码并绘制全部块后提交一帧。首个**完整且已绘制的真实 Code 窗口帧**才允许 UI `ready`；流打开、窗口 snapshot 或 white canvas 均不足以标 ready。订阅已有窗口后 10 秒没有首个完整帧返回 unavailable；恢复需要新订阅和真实 full refresh。

每窗口最多 4 个活跃 viewer；每 viewer 最多一个待发送 frame（上界 24 MiB）及一个正在写的 tile。背压时丢弃该 viewer 的未发送增量 frame，标记下一个待发送 frame 为 full refresh；不得丢单个 tile 后继续声称完整。超过 5 秒不能发送完整 frame 的慢 viewer 以 `resource_exhausted` 断开。Runtime 的总显存/RAM 与活跃窗口、viewer 上限另在实现中施加，并在真实 Code A10 中报告；达到上限必须显式拒绝新订阅，不可无界排队。Connect 的消息大小配置必须显式容纳最大 tile，同时保持入口请求大小受限。

选 PNG 的依据是实现/可测性，**并非已证明性能通过**。隔离 Chromium 合成 Code 样式画布 30 帧 POC：1440×900 全帧 PNG 编码 p95 24 ms、`createImageBitmap` 解码 p95 21 ms、约 359 KiB；2880×1800 全帧分别 61/65 ms、约 889 KiB，仅编解码合计已大于 A10 的 100 ms 输入到可见反馈目标。512×512 单块编码 p95 在 DPR1/2 为 11/10 ms、解码 p95 为 6/6 ms、约 150 KiB/块。这些数字来自 synthetic headless、无真实 Code、GPU readback、网络、Go fanout 和绘制延迟；必须靠真实脏区/Code 测试裁决。若 A10 不通过，再按同一窗口和授权契约评估 WebRTC；本 ADR 不承诺 WebRTC 更快。

## 授权、输入与生命周期

新媒体 Watch 路由在 Gateway auth store 不可用时须于下一次重验（至迟 30 秒）fail closed 并终止流。现有 stream revalidation 对 store outage 可继续保留连接，不能沿用到无 TTL 的窗口媒体，否则会无限延续授权。Runtime 的 attachment 重验是另一层，仍须至少每秒执行。

Shell 激活窗口要发送有 sequence 和控制代次的 `GreenfieldWindowFocus`，把对应 native top level 激活；窗口 snapshot 的 `active` 应反映真实 Greenfield/Wayland 焦点。只改 Shell 标题栏高亮不能当作 native 焦点成功，工作区窗口关闭仅 Detach viewer。

Gateway 从已登录设备会话注入 owner/device；客户端只提供 `SurfaceAttachment.id`、session 和期望 workload generation。`SurfaceAttachment.id` 来自 `AttachSurface`，是**本次 live attach** 的 ID，不是跨浏览器稳定 ID；重连需重新 Attach。当前 [`nativeSession.ts:14-22,101-156`](../../apps/desktop-web/src/nativeSession.ts) 的 `NativeSessionHandle` 丢弃此 ID，消费者实现须保留并传给新 RPC。Runtime 新增 viewer 中性 port，按 owner、gateway device、session/workload、attachment ID、期望 generation 验证 live attachment；现有 [`AuthorizeInputGeneration`](../../internal/runtime/surface/application/continuity.go) 要求控制权，不能用于 observer。任一 Watch 在开始、每帧及无帧时至少每秒重验；Detach/Stop/Restart 或 attachment 撤销使其至迟 1 秒内终止。Gateway 对设备登录会话 stream 的现有 30 秒重验是独立外层；登录撤销至迟受该 30 秒边界约束，实施需测试。不得仅在 stream 建立时检查。

每个 pointer、key、text commit、resize、clipboard write 由 Go broker **执行前**核对当前 live attachment、owner/device、workload generation 和独立的 `control_generation`。批次最多 64 个事件，但 broker 到 child 逐事件派发、等待实际应用 ACK；同一 workload 的输入与 `RequestSurfaceControl` 接管按一个串行屏障排序，接管返回前旧代次已在途事件结束，其余排队事件丢弃。child 不保留可在接管后执行的旧事件队列。序号在 attachment + workload generation 内严格递增；重复只返回已应用的结果，gap/旧代次拒绝，不能重放已输入的文本或按键。批次首个事件之前失权或授权失败必须返回 Connect `permission_denied`/`failed_precondition`；部分事件已真实应用后才失权可返回 `STALE_CONTROL` 和实际最后应用/首个拒绝序号。无真实 native 注入/selection 写入时返回 unavailable，不能返回固定 `APPLIED`。`ReadGreenfieldClipboard` 同样要求当前控制者，读取最新真实 Wayland selection；若先要复制选中文字，控制者通过有序列号的 Ctrl+C 输入事件触发，再读取，不能把旧 broker 缓存当作当前 native 选择。纯文本 UTF-8 上限 1 MiB、单 text commit 上限 16 KiB；一个输入批次最多一次 clipboard write，总序列化大小不超过 1.5 MiB。当前 [`domain/session.go:49`](../../internal/runtime/nativehost/domain/session.go) 服务端仅为 256 KiB，而浏览器 fallback 上限为 1 MiB，实施时须统一并验证真实 selection。IME composition 只提交一次确定性 committed text；keydown/keyup 与 compositionend 不可重复写入。

零 viewer 不影响 Code、proxy、resident Chromium 或窗口内容。普通关闭只 Detach；Stop 终止子容器及 IPC 并回收私有资源；显式 Restart 生成新 workload generation。Code、proxy、Chromium 或 IPC 故障时终止窗口和媒体流并报告真实 `FAILED`/unavailable，不以现存 Code PID 或空白画布冒充已恢复同一实例。A06 必须核对 Code PID 加 `/proc/<pid>/stat` starttime、子容器身份、generation、未保存标记的像素与保存后的磁盘内容；A07 必须用两个不同设备身份同时观看并证实观察者输入被拒、接管后旧队列失效。所有错误和日志不含 user text、原始 proxy key、cookie 或密钥。

## 私有桥接协议

Go broker 与 child Node bridge 只通过每 workload 私有 `0700` Unix socket 通信；child 内的 proxy、Code、Chromium 共享 child loopback。IPC 使用 `GreenfieldChildEnvelope` Protobuf：4 字节大端无符号长度 + 序列化消息；`protocol_version=1`，单 record 最多 2,200,000 字节，超限/未知版本/错误 session 或 generation/未知 payload 均关闭连接并报告 unavailable。相同 `request_id` 关联单个 input/clipboard 请求及其结果；主动 windows/frame 消息 `request_id=0`。socket 路径只挂给相应 child，owner 为独立受限 UID；broker 仍要校验所有从 child 收到的窗口、tile 边界、PNG 类型、长度、序号及修订号。公开 Connect request 不经 Unix socket 直达 child。child 不能取得 public cookie、数据库环境或 Docker socket。

## 验收与事实状态

契约生成只证明 Go/TS 同源消息存在。实施至少覆盖：包定版及逐窗口真实 Code 像素，首帧超时和修订号，full refresh/掉帧/慢 viewer，附件撤销与零帧重验，两个设备 observer/接管竞态与 sequence 幂等，隔离容器无 DB/socket/host-network、隔离失败 unavailable，子容器/Go broker/Code 故障，A06 未保存缓冲同实例接续，A07 同时观看，以及 A10 LAN HTTPS 固定基线真实 Code **输入到浏览器像素可见** p95；只量 RPC 返回或 `rendered_at` 不算。未有端到端证据前 P0 状态最高仍是 scaffolded/partial，不因本 ADR 采纳而改为 done。
