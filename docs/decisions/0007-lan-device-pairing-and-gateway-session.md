# ADR-0007：LAN 设备配对与持久 Gateway 会话

- 状态：Accepted
- 日期：2026-08-30
- 关系：细化 ADR-0001 的身份边界与 ADR-0002 的 device 绑定规则；`docs/structure.md` 第 13 节
  的第一階段落地。本 ADR 只覆盖"已知直连 HTTPS origin 上的浏览器设备配对与会话"。

## 背景

Access Gateway 的身份边界此前只有开发脚手架：`WORKOS_DEV_AUTH_BYPASS=true` 时从配置注入固定
owner/device，bypass 关闭后所有公开 API 固定返回 401。设备注册、私钥持有证明、持久会话、撤销
均不存在，系统因此不能安全暴露到局域网。设备身份同时是 Surface device binding、App Bridge
token、未来移动 Shell 与远程 transport 的共同前置边界（ADR-0002 的验证链第一环）。

## 决策

### 1. Device credential / pairing / session 属于 workos-gateway

- 生产设备凭据（server-minted UUIDv7、canonical P-256 SPKI、digest、revision、revocation）、
  pairing ticket、proof challenge、device session 全部是 Gateway 拥有的 durable facts，存储在
  migration `020` 建立的 `workos_gateway` schema。
- Gateway 不查询 `workos_core.devices`、projects、surface、incident 表；Core/Runtime/Reliability
  不查询 Gateway auth 表、不校验 Cookie。跨进程身份只经 Gateway 清洗后注入的 private headers。
- 不复用 legacy `workos_core.devices`：它是 foundation bootstrap 的开发脚手架（`001`，逐字节
  不变），没有公钥、hash、状态机或撤销语义，把它 backfill 成"已配对设备"等于伪造凭据。两表并存，
  legacy 表继续只被 `workosctl owner init` 写入。

### 2. Operator bootstrap 走私有 Unix socket

- `workosctl device pair` 经 Gateway 进程拥有的 admin Unix domain socket 调用
  `DeviceAuthAdminService.RotatePairingTicket`；socket path 启动时验证（绝对、cleaned、无
  symlink、parent 为真实目录、已有文件必须是 stale socket 才可精确清理），文件 mode ≤ 0600。
- admin service 只注册在 socket mux 上；同一 RPC 的 TCP 请求确定性 404，并有测试钉住。
- CLI 不 import Gateway postgres adapter、不写 `workos_gateway` SQL。
- `RotatePairingTicket` 是显式 rotation：每次成功调用在 owner 级 advisory lock 内 revoke 旧
  outstanding ticket 后插入新 ticket，旧 QR 全部失效；响应丢失时操作者重新执行命令，raw ticket
  永不明文落库。

### 3. Pairing secret 位于 URL fragment

- QR URL 固定为 `https://<origin>/pair#v=1&t=<base64url 32B>&fp=sha256:<64hex>`。secret 在
  fragment 是因为 fragment 不会被浏览器发往服务器（不进 access log / proxy log / upstream），
  也不进入 referrer。Pairing UI 严格校验 grammar 后立即 `history.replaceState` 清除地址；
  ticket 只留在本次内存流程。
- `fp` 是 Gateway 实际服务的 leaf certificate DER 的 SHA-256（启动时从 TLS keypair 计算，不信
  配置另填值）。ticket snapshot 固定 origin+fingerprint；证书或 origin 变更使 outstanding
  ticket 立即失效（BeginPairing/CompletePairing 都比对 snapshot 与当前事实）。浏览器 UI 显示
  fingerprint 供人工比对，但普通 Web 页面无法绕过平台 trust store 自证 TLS peer——本切片明确
  不声称 native certificate pinning；已配对设备的 session proof 不钉死旧 fingerprint。

### 4. P-256 raw WebCrypto proof + opaque Cookie

- 浏览器生成两把 `ECDSA P-256` key：exportable pair 只出 SPKI（usage verify），non-extractable
  private key（usage sign，`extractable=false`）结构化存入 IndexedDB。IndexedDB 写失败必须停在
  claim 之前；不允许 localStorage/sessionStorage fallback、不允许导出 PKCS#8/JWK。
- 签名是版本化 canonical transcript 上的 64-byte raw `r||s`（WebCrypto 原生输出），domain
  separator `workos.device-proof/v1`、purpose byte、uint32-BE-length-prefixed 字段
  （origin、purpose、challenge、nonce、device、key digest；pairing 另加 ticket、fingerprint）。
  Go/TypeScript 共用同一测试向量（SHA-256 pinned）。服务端把 r/s 各 32 bytes 解析为正整数后
  `ecdsa.Verify`；DER、长度、zero、out-of-range、noncanonical SPKI、unknown version 一律
  fail closed，无算法协商。
- 会话 Cookie 固定 `__Host-workos_session`：`Secure`、`HttpOnly`、`SameSite=Strict`、`Path=/`、
  无 Domain、绝对 Max-Age/Expires 等于存储的 UTC expiry。数据库只存 domain-separated
  `sha256(raw)`；raw token 只出现在一次 `Set-Cookie`；清除用完全相同属性 + 过期值。本切片的
  session 是 TLS 下的短期 bearer session，不是每请求 DPoP，也不是硬件 attestation。
- 频繁资产请求不做每请求写库：`last_seen_at` 由有界时间门槛的 guarded UPDATE 维护，不参与授权、
  不延长 absolute expiry。

### 5. 每请求持久重验，不做进程内 revocation 缓存

- Gateway 每个受保护请求从 Cookie hash 查询 active session + active credential + owner，核对
  UTC expiry 与 configured owner；无进程本地 auth cache。撤销 commit 后的第一个请求即失败，
  不需要失效广播，也不存在 stale cache 的安全窗口。
- store 故障是 sanitized `503`/`Unavailable`，不是 `401`——绝不把暂时故障伪装成"请重新配对"，
  也绝不回退到 configured identity 或 stale cache。Desktop 的 unavailable 文案与 unpaired
  文案严格区分，瞬时故障不诱导用户删除本地 key。

### 6. Lost-response 恢复与并发裁决

- pairing 完成响应不确定时，client 用已持久化的 pending device/key 走正常 session proof 恢复；
  server 已 commit 即成功，未 commit 则同一仍有效 ticket 可恢复 challenge（same ticket + same
  key/name/class），never 生成重复 device。不同 key/metadata 统一认证失败。
- 并发裁决全部下沉 PostgreSQL：owner 级 advisory lock 序列化 rotation；guarded UPDATE 行数裁决
  claim/consume/revoke 唯一赢家；partial unique index 保证每 owner 一个 pending ticket、每
  device 一个 active credential key、每 device 一个 active session；撤销的 `revoked_at` 用
  数据库事务时间，杜绝锁等待导致的时钟倒挂。
- RevokeDevice 是 owner-scoped idempotency：同 key + 同 digest 精确重放第一结果快照，同 key
  不同请求稳定 `Aborted`；device revoke + 全部 active session revoke + result snapshot 同事务。
  撤销 current device 后 UI 清 Cookie 并删除本地 key 回到 unpaired；撤销其他设备立即使其下一
  请求 fail closed（Runtime Surface 行不跨进程删除，由 Gateway gate 不可达 + 既有 TTL 收敛）。

### 7. 本切片不声称的能力

- 无 mDNS/Bonjour/DHCP/NAT traversal、无公网 Gateway、无 VPN/overlay/relay；
- 无 ACME/证书自动管理/浏览器绕过证书错误/外部 reverse proxy trust；
- 无 WebAuthn/passkey、硬件 attestation、TPM、每请求 DPoP/mTLS、native pinning；
- 无多用户、密码登录、SSO/OAuth、角色授权、账号恢复；
- 无移动原生 wrapper、Keychain/Keystore、APNs/FCM 推送、WebSocket realtime auth；
- App 不获得任何 device API、新 bridge capability 或 iframe Cookie 访问。

## 兼容性（v1）

- Proto 只 additive：`workos.auth.v1` 新增三个 service 与消息；v1 字段号不复用；删除必须
  reserved；enum 拒绝 UNSPECIFIED/unknown。
- Migration `020_gateway_device_auth.sql`（owner: workos-gateway）forward-only；`001`–`019`
  逐字节不变（checksum 钉住）。
- `WORKOS_DEV_AUTH_BYPASS=true` 的 loopback 开发模式原样保留：固定 identity 来自配置、不发
  Cookie、不建 auth 依赖；Desktop 探测到部署未提供 auth 端点时保持既有直接挂载行为。
- bypass 关闭即 production：Gateway 自终止 TLS（min TLS 1.3）、canonical `https` public
  origin、canonical UUIDv7 owner、postgres URL、admin socket path 全部启动即验证；生产模式
  不要求也不使用 `WORKOS_DEVICE_ID`。

## 后果

- Access Gateway 的 production auth 链路可用真实 TLS + PostgreSQL + 浏览器证据闭合后，
  `docs/status.json` 才从 `scaffolded` 升 `working`，证据限定"direct configured HTTPS origin
  上的 browser profile pairing/session"。
- 后续移动 Shell、远程 transport、通知系统以此为共同前置边界；mDNS/native pinning/公网访问
  需要新 ADR。
