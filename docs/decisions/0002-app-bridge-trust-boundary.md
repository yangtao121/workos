# ADR-0002：App Bridge 信任边界

- 状态：Accepted
- 日期：2026-08-28

## 背景

Web Bundle Surface 已把不可信 App 文档隔离在 opaque-origin sandboxed iframe（`allow-scripts`、
CSP `sandbox allow-scripts`、`connect-src 'none'`）内。下一个纵向切片让该 iframe 在用户显式
grant 之后调用 Project-scoped Agent 任务。本 ADR 固定信任边界：谁持有凭证、谁做授权裁决、
失败如何闭合。

## 决策

### 1. opaque-origin iframe 只通过 exact-window MessageChannel 与可信 parent 通信

iframe 的 `event.origin` 是字符串 `"null"`，不能作为身份。握手安全不依赖任何 origin 字符串：

- parent（Desktop/AppHost，React 持有的 exact `iframe.contentWindow` 引用）在每次 iframe load
  时创建新 `MessageChannel` + 一次性 nonce，只向该 window postMessage versioned hello 并
  transfer `port2`（opaque origin 下必须 `targetOrigin="*"`，因此安全性来自 exact window 引用，
  不来自 target origin）。
- iframe SDK 只接受 `event.source === window.parent`、正确 protocol version/type、恰好一个
  transfer port 的 hello，用 port 回 ack 匹配 nonce；parent 在超时前只接受一次正确 ack。
- reload/navigation/project 切换/关闭都关闭旧 port、拒绝旧 pending request、旧 nonce/port 复用
  一律拒绝。授权从不在客户端终结：MessagePort 只是浏览器内 capability handle，每个请求仍由
  runtime 与 core 服务端再验证。
- 可信 host 强制执行不可信边界。iframe 是不可信方，可以完全绕过 `@workos/app-sdk` 直接
  `port.postMessage`，因此 host（`clients/app-host`）在 dispatch 前自行验证每条入站 envelope：
  协议版本与 type、UTF-8 byte size ≤ 64 KiB（不是 UTF-16 `string.length`）、canonical 有界
  request ID、允许方法、exact payload shape（未知/多余字段 fail closed）与字段边界
  （key ≤128 rune、role ≤64 rune、goal ≤16 KiB、task ID canonical UUID、cursor 为无符号十进制
  字符串）、in-flight ≤ 32、重复 request ID 拒绝。超时真正登记并清理 pending：run 超时后迟到
  结果 inert；stream 超时 abort 本地/server stream。close/reload/unmount 清空全部 timer、
  pending 与 stream。SDK 的 outbound request/cancel 使用同一个 bounded helper，两端共享
  `MAX_SINGLE_MESSAGE_BYTES` 语义。

### 2. bridge token：随机 256-bit secret，sha256 at rest，session 生命周期

- 生成：`crypto/rand` 32 字节熵，canonical base64url（43 字符），无格式化字段、不可解析。
- 存储：runtime-host 只在 `workos_runtime.surface_sessions` 保存 `sha256(token)` hex（migration
  `010`），不存明文；验证是常量时间 `subtle.ConstantTimeCompare(hash(client token), stored hash)`。
- 绑定：token 不携带 claim；绑定事实就是 session 行本身（owner_user_id、device_id、project_id、
  app_instance_id、created/expires）。验证顺序：Gateway 注入 identity → token hash 匹配 →
  session active（未关闭、未过期）→ `session.device_id == identity.device_id`（知道 token 但换
  device 仍拒绝）→ 方法级 grant 检查。
- TTL：token 有效期 = session expiry（默认 15m，启动校验），不单独续期。
- 持久与重启：hash 在 PostgreSQL 中，runtime-host 重启后 token 继续有效到同一 expiry；这与
  session 持久语义一致并有 integration/restart 测试。
- 轮换：每次 `CreateSurface` 成功响应（fresh create 或 open+unexpired replay）都铸造新 token 并
  覆盖旧 hash——旧 token 立即失效；closed/expired session 的 replay 不铸造 token（返回空
  `bridge_token`）；`CloseSurface` 清空 hash。单实例假设：同一 session 只有一个 runtime 写 hash；
  未来多实例共享需要迁移到签名 token + 共享密钥轮换，本任务不假装支持。
- 并发首次 Create 的线性化：仓库事务内的 mapping PK 裁决可能让一个并发 loser 返回赢家的
  session。此时 loser 本地铸造的 token 从未落库——按本 ADR 它必须原样丢弃，loser 对返回的
  open session 执行一次真实轮换并返回轮换后的 token；响应中的 session snapshot 在轮换后重读，
  保证每个成功响应都是“凭证 ↔ 持久 hash”配对。最终库内 hash 恒等于最后一次线性化成功的
  Create 所返回 token 的 hash；旧 token 的失效始终来自这条已记录的轮换，不存在“从未存储”的
  有效响应凭证。该语义被 fake 与真实 PostgreSQL 并发测试共同钉死。
- 关闭的原子性：首次 close 的 tombstone 与 `bridge_token_hash = NULL` 是同一条 UPDATE 的两个
  SET 分支，不存在“先关闭再清除”的窗口；repeated close 幂等保留首次 `closed_at`。
- 披露面：token 只出现在 `CreateSurface` Connect response（可信 Desktop 内存）与 AppBridge 专用
  header `x-workos-bridge-token`；绝不进入 URL、cookie、storage、DOM、MessageChannel payload、
  日志、错误、trace。Gateway 只把它转发到 runtime Connect 路由，Core 路由与 `/surfaces/` asset
  剥除。

### 3. grant 的唯一事实源：安装级不可变快照

- manifest `permissions` 永远只是 requested；Registry 不铸造 grant。
- `project_app_installations.granted_permissions`（migration `008`，默认空）是安装级 grant 的
  唯一持久事实：canonical 排序、无重复、严格 ⊆ 该 pinned version manifest 的 requested set，
  由 Core 在 install 事务内校验后写入；之后不可变，改 grant 只能 uninstall + reinstall。
- 安装幂等 digest 版本化：空 grant 沿用 004/005 的 legacy digest（保护历史 replay 升级兼容），
  非空 grant 使用显式 `v2` canonical digest（覆盖排序后的 grant），同 key 不同 grant 稳定
  `Aborted`，同 version 同 grant 才是 no-op。
- effective capability = requested ∩ granted ∩ runtime 已实现，在 `CreateSurface` 时由 runtime
  从 Core resolver 返回的 grant snapshot 计算；列表里不出现未实现能力，未实现能力永远不因
  “已 grant” 而 working。

### 4. Runtime → Core：private RPC + 每次再验证

- public `workos.bridge.v1.AppBridgeService`（runtime-host）：body 只有 bounded 输入
  （`agent.run`: idempotency key/role/goal；`agent.watch`: task id/after_sequence），owner/device/
  project/app_instance/provider 全部从 Gateway identity、token、stored session 派生。
- private `workos.agent.v1.AppAgentService`（core，不进 Gateway allowlist，仅 runtime-host 经
  identity middleware 调用）：每次调用都重新 ResolveActiveInstallation（active、同 owner、同
  project、Project 未归档）并重读 grant snapshot 做 method 级检查；不信任 runtime 传来的任何
  “授权已检查” 状态；digest 漂移/损坏 grant 是净化 `Internal`，绝不降级为无权限继续。
- Core 强制 `target_scope.project_id` = installation Project；`requested_capabilities`、
  `output_artifact_types`、`parent_task_id`、`incident_id`、global scope、budget 不接受 iframe
  输入；Provider 选择留在 Task Router/Harness binding 既有语义。

### 5. App task provenance 与幂等：Core Agent 持久事实

- 新表 `workos_core.agent_app_task_requests`（migration `009`，owner：workos-core Agent）：
  PK `(owner_user_id, app_instance_id, client_idempotency_key)`，含 canonical request digest、
  task_id、project_id；composite FK `(owner_user_id, task_id) → agent_tasks(owner_user_id, id)`
  把 mapping 钉在同 owner 的 task 上（009 同时补 `agent_tasks` 的 `UNIQUE (owner_user_id, id)`）。
  对 Project/Registry 表无跨模块 FK；app_instance_id 只存稳定 snapshot ID，安装有效性靠每次
  port/RPC 再验证。
- task + mapping + outbox 在一个 Core-owned 事务内提交；并发 loser 回滚后无 orphan task/outbox。
  same key + same digest 精确 replay 首次 provider snapshot；same key + different digest 稳定
  `Aborted`；两个 App 用相同 client key 互不冲突（namespace 含 app_instance_id）。
- watch 要求 task 同时满足：同 owner、mapping 指向该 app_instance、project 一致；拥有任意
  task ID 字符串不构成读取能力。task 行的 `agent_tasks.idempotency_key` 对 App 任务存
  非 namespace 语义的独立 UUID（App 幂等裁决完全由 mapping PK 承担），不用拼接 key 冒充
  provenance。

### 6. 当前单 runtime 实例限制

token at rest（hash in session row）假设同一 session 的 Create/Close/verify 都落在拥有该表的
单一 runtime 部署上。多副本方案（per-instance HMAC key + `kid` 轮换，或独立 token 服务）留待
真正的多 runtime 需求出现时另立 ADR；本任务不引入未批准的生产信任根（无 Credential Vault、
无静态仓库密钥）。

### 7. 公共 Bridge 错误映射的稳定性选择

- runtime 侧凭证链（token 缺失/畸形/过期/篡改/换 device）统一 `Unauthenticated`，不区分
  具体失败原因。
- Core 侧的二次裁决失败（installation 不 active、grant 缺失、provenance 不符）在公共
  Bridge 上统一净化为 `PermissionDenied`；`Aborted`（same key/different request 的幂等
  裁决）单独透传，因为它是客户端可以安全重试决策的输入。closed/expired session 已被
  runtime 的 token 解析挡住，不会形成 foreign 资源的存在性 oracle。

## 理由

- token 不进 iframe：iframe 是不可信渲染环境；MessagePort + 服务端验证已足够表达 capability，
  bearer secret 进入 iframe 只会把信任边界退化回客户端。
- 携带 token 的 Gateway 路由集合是显式 allowlist（`/workos.bridge.v1.AppBridgeService/`）而非
  “所有 Runtime 上游”：以 upstream 名称决定会 SurfaceService/asset 一起放行。默认剥除、按路径
  精确恢复，恶意客户端无法把 bearer 侧向带入其他 WorkOS 路径。
- 稳定错误语义：Desktop Connect adapter 把 transport 失败投影为有限 `BridgeErrorCode`
  （InvalidArgument/Unauthenticated/PermissionDenied/NotFound/Aborted/Unavailable → 同名码，
  DeadlineExceeded/Canceled 归入 unavailable，其余 → internal，本地解析失败 → invalid_argument），
  host 只接受该类型并回固定短消息；run 与 stream 共用同一映射，同一服务端 verdict 不因 RPC
  形态改变类别。raw Connect message、SQL、DSN、token、goal/event 全文、stack 与内部地址永远
  不跨过 MessagePort。
- grant 做成安装级不可变快照：让“用户批准过什么”成为可审计的持久事实，避免 requested/grent
  混淆，也避免为最小切片引入 mutable policy engine。
- 每次调用双侧再验证（runtime 查 token/session，core 查 installation/grant）：任何一侧的缓存
  或 claim 都不能成为长期授权真相；uninstall/archive/Close 立即 fail closed。
