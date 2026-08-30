# 下一位智能体 Prompt：LAN 设备配对与持久 Gateway 会话纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、UI 视觉证据、文档和聚焦提交，
> 不是只输出计划。

## 你的角色与最终结果

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。受监督的 Rootless Web Service
Workload 已合入本地 `main`，但真实 Podman 证据仍受当前宿主条件阻塞。你的任务不是继续堆叠尚无
真实 runner 证据的 Repair/Deployment 层，而是实现 Product Alpha 的下一条安全前置链路：

**让一个此前未受信任的浏览器 profile，经本机操作者或已配对设备展示的短期二维码完成 LAN 设备
配对；浏览器以非导出 P-256 私钥证明持有设备凭据，Gateway 签发只以 hash 持久化的安全会话 Cookie；
此后所有 Core、Runtime、Reliability、Surface 公开请求都由 Gateway 从持久会话动态派生 owner/device
身份，设备撤销或会话过期后立即 fail closed。**

最终链路必须闭合：

```text
operator workosctl over gateway-owned Unix admin socket
  或 authenticated Device Center
  → Gateway rotates one short-lived pairing ticket
  → URL/QR contains ticket only in fragment + actual TLS leaf fingerprint
  → unpaired Desktop Auth Gate reads then immediately scrubs fragment
  → WebCrypto generates P-256 key pair (private key non-extractable)
  → private CryptoKey is persisted only in browser IndexedDB
  → Gateway atomically claims hashed ticket and returns one bounded challenge
  → browser signs a versioned canonical proof transcript
  → Gateway verifies raw WebCrypto ECDSA signature
  → one durable Gateway-owned device credential + one active device session
  → __Host- HttpOnly/Secure/SameSite=Strict Cookie
  → every public request resolves that session before any upstream call
  → client-supplied identity headers are deleted
  → trusted owner/device identity from the session is injected upstream
  → Gateway restart preserves the session
  → logout/session expiry/device revocation immediately prevents forwarding
```

持续推进到实现、生成物、真实 PostgreSQL/TLS/browser 测试、UI 前后截图、ADR、任务记录、架构文档、
状态事实源和聚焦提交全部完成。不要 merge 或 push。只有遇到以下情况才停止并留下证据与选项：必须
破坏已有 v1 字段/编号、修改已执行 migration、改变六进程所有权、要求 Gateway 直接 SQL Core 表、
必须把 session/ticket/private key 明文持久化、必须降低现有 iframe/bridge 隔离、必须绕过 TLS，或
执行环境无法运行本任务要求的真实 TLS + PostgreSQL + browser 链路。

## 为什么现在做这个

当前主线已经闭合：

```text
Project → Harness binding → durable Agent task → canonical events
Registry → installation → Web Bundle Surface → least-privilege App Bridge
App policy → approval → quota reservation → usage circuit break
container descriptor → durable Workload → deterministic supervision scaffold
```

但 `workos-gateway` 的身份边界仍只有开发脚手架：

- `WORKOS_DEV_AUTH_BYPASS=true` 时从配置注入固定 owner/device，且只允许绑定 loopback；
- bypass 关闭后，所有公开 API 只会固定返回 `401 device session required`；
- 没有设备注册、私钥持有证明、持久 session、撤销或重新认证；
- 虽然非 loopback bind 已要求 TLS 文件，系统仍不能安全暴露到 LAN；
- Desktop 启动后直接请求业务 API，没有 unpaired/session-expired 状态；
- `docs/status.json` 因而如实把 Access Gateway 标为 `scaffolded`，Mobile Shell 也没有可依赖的设备身份。

`docs/structure.md` 第 13 节要求 LAN 连接先校验服务器、交换设备公钥，再生成 Device Session。设备身份
又是 Surface 的 device binding、App Bridge token、未来移动 Shell、通知和远程 transport 的共同前置
边界。因此本任务先完成“已知直连 HTTPS origin 上的浏览器设备配对与会话”，不把 mDNS、移动原生
证书 pinning、Push Relay 或公网访问混进同一个切片。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时本地 `main` 位于 `2aad565`，工作树在新增本文件前干净并与 `origin/main` 同步。
  执行时必须重新检查，以执行时本地历史为准；不得 reset、重建自落后远端或丢弃用户改动。
- `docs/status.json` 是唯一进度事实源：Access Gateway 为 `scaffolded`，证据只有 upstream routing、
  header replacement 与开发 identity gate；Desktop Shell 为 `working`；Mobile Shell 为
  `contract-only`。
- `internal/gateway/gateway.go` 只允许明确列出的 Core/Runtime/Reliability public route；所有私有
  Harness、Surface resolver 与 Workload control RPC 必须继续 404。
- 当前 reverse proxy Director 无条件删除客户端的 `X-WorkOS-User-ID` / `X-WorkOS-Device-ID`，再从
  `cfg.Auth` 注入固定值。生产模式必须改为从本次请求已验证的 session identity 注入，不能把 Cookie、
  ticket 或 public key 转发给任何 upstream。
- `internal/platform/identity.Middleware` 只运行于 loopback/private service listener，它继续只信任
  Gateway 注入的内部 headers；不能把生产 Cookie 校验复制进 Core/Runtime/Reliability。
- `Config.ValidateGateway` 已禁止非 loopback 的 dev bypass，并要求非 loopback TLS cert/key；生产
  session、public origin、admin socket、TTL 与 rate-limit 配置尚不存在。
- `cmd/workosctl` 当前只为 foundation bootstrap 直接初始化 Core-owned owner/development device。
  新增 Gateway auth 数据不能让 CLI 直接写 SQL；CLI 必须经 Gateway 自己的 private admin port。
- migration `001` 中有历史 `workos_core.devices` scaffold，当前除 `workosctl owner init` 外无人读取。
  它不是生产认证权威，也不能被无公钥 backfill 成“已配对设备”。`001` 必须逐字节不变。
- Desktop 当前在 `main.tsx` 直接挂载 `Desktop`；现有 Connect clients、Surface iframe、App Bridge、
  Project/sessionStorage 行为都必须回归不变。
- migrations `001`–`019` 已被 checksum/forward tests 钉住，禁止修改。预计本任务使用新的 `020`；
  若执行时编号已占用，必须顺延。
- 当前验收 PostgreSQL volume 可能含用户已有数据。禁止 `docker compose down -v`、TRUNCATE、broad
  DELETE、wildcard DROP 或清理历史记录。
- 本任务不需要 DeepSeek、OpenAI、Codex、GitHub、外部 CA 或其他真实凭据。不得使用、保存、转述或
  验证聊天中曾出现的真实 Key；所有 cryptographic test material 必须在临时目录中生成。

## 官方安全基线与明确上限

实现前阅读并在 ADR 中记录采用的具体约束：

- [TLS 1.3（RFC 8446）](https://www.rfc-editor.org/rfc/rfc8446.html)：生产 Gateway 自己终止 TLS，
  本切片不信任 `X-Forwarded-Proto`，也不把外部 TLS termination 冒充已支持。
- [Cookies: HTTP State Management Mechanism（RFC 10025）](https://www.rfc-editor.org/rfc/rfc10025.html)：
  session Cookie 必须使用 `__Host-` prefix、`Secure`、`HttpOnly`、`Path=/`、无 `Domain`，并显式
  `SameSite=Strict`。
- [Web Cryptography Level 2](https://www.w3.org/TR/webcrypto/)：P-256 public key 可导出，private
  `CryptoKey` 必须 `extractable=false`；CryptoKey 可结构化序列化到 IndexedDB；WebCrypto ECDSA
  signature 是固定宽度 `r || s`，P-256 恰好 64 bytes，不是 ASN.1 DER。
- [Go `crypto/ecdsa`](https://pkg.go.dev/crypto/ecdsa)：服务端必须把 64-byte signature 拆成 `r`/`s`
  后使用 `ecdsa.Verify`；不得误用 `VerifyASN1` 接受另一种编码，也不得写宽松的双编码 fallback。
- [RFC 5480](https://www.rfc-editor.org/rfc/rfc5480.html)：public key wire/storage 使用 canonical P-256
  SubjectPublicKeyInfo DER；解析后必须确认 algorithm/curve，并重新 marshal 做 canonical equality。

必须诚实区分：浏览器 profile 中的 non-extractable WebCrypto key 是“浏览器 profile credential”，
不是硬件 attestation；本任务的 session Cookie 是 TLS 下的短期 bearer session，不是每请求 DPoP；
二维码中的 certificate fingerprint 供用户确认并为未来 native pinning 保留，但普通 Web 页面无法绕过
浏览器/OS trust store 自行证明当前 TLS peer certificate。只有受信任证书的 HTTPS origin 才可称为
生产可用；测试中的临时 CA/证书不等于自动证书管理。

固定安全上限（若需改变先写 ADR 理由）：

- pairing ticket：`crypto/rand` 32 bytes，base64url 无 padding，默认 TTL 5 分钟，范围 1–15 分钟；
- proof challenge：`crypto/rand` 32 bytes，默认 TTL 2 分钟，范围 30 秒–5 分钟；
- device session：`crypto/rand` 32 bytes，默认绝对 TTL 24 小时，范围 5 分钟–30 天；
- device name：trim 后 1–80 Unicode code points，valid UTF-8，无 C0/C1 控制字符；
- public key：只接受 canonical P-256 SPKI DER，wire 最大 256 bytes；
- signature：恰好 64 bytes raw `r || s`；
- auth/public Connect request：解码前最大 16 KiB；admin request：最大 4 KiB；
- page size：默认 50、最大 100，cursor 为 canonical lowercase UUIDv7；
- ticket/challenge 每个对象的失败尝试有有限上限，并叠加不信任 `X-Forwarded-For` 的 bounded
  RemoteAddr/global rate limiter；rate limiter 的 key map 必须有容量与淘汰上限。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`deploy/README.md`、
     `docs/ui/README.md`；
   - `docs/structure.md` 中 0、1.3、2、3、4.4、10、11、12、13、14–18；
   - `docs/architecture/implementation.md` 与 `docs/status.json`；
   - ADR `0001`–`0006`，尤其 Surface/Bridge 的 owner/device 信任边界；
   - `docs/tasks/20260825-minimal-web-bundle-surface.md`、
     `docs/tasks/20260828-minimal-project-agent-app-bridge.md`、
     `docs/tasks/20260829-supervised-web-service-workload.md`；
   - `internal/gateway/gateway.go` 及全部测试；
   - `cmd/workos-gateway`、`cmd/workosctl`、`internal/platform/{config,identity,httpserver,database}`；
   - `api/proto/workos/common/v1` 与全部 public/private service allowlist；
   - Surface session、App Bridge token、Gateway header cleaning 和 device-bound tests；
   - `apps/desktop-web` bootstrap、clients、window state、styles 与全部 Playwright E2E；
   - migration runner、`001`、`007`、`010`、`012`、`015`、checksum tests 与 `sqlc.yaml`；
   - `compose.yaml`、Dockerfile、Makefile、systemd units/env examples。

2. 运行并记录基线：

   ```sh
   git status --short --branch
   git log --oneline --decorate -15
   git branch -vv
   git diff --check
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败必须判断归属并写入任务记录。禁止删 volume、历史测试、断言或固定返回来绕过失败。

3. 从当前本地 `main` 创建独立 branch/worktree，建议分支：

   ```text
   feat/lan-device-pairing
   ```

   如果分支或 worktree 已存在，先检查并认领，不得覆盖另一位智能体的工作。

4. 从 `docs/tasks/TEMPLATE.md` 创建：

   ```text
   docs/tasks/20260830-lan-device-pairing.md
   ```

   初始状态 `active`，写清 Gateway auth facts、ticket/challenge/session 状态机、TLS/cookie/key 边界、
   admin socket、migration、错误、测试、UI 和非目标。

5. 新增聚焦 ADR，建议：

   ```text
   docs/decisions/0007-lan-device-pairing-and-gateway-session.md
   ```

   ADR 至少固定：为什么 device credential/session 属于 Gateway；为什么不复用 legacy Core devices
   table；为什么 operator bootstrap 使用 private Unix socket；为什么 pairing secret 位于 URL fragment；
   为什么采用 P-256 raw WebCrypto proof + opaque Cookie；session response 丢失如何恢复；为什么每请求
   持久重验而不缓存 revocation；为什么本切片不声称 mDNS、native pinning、WebAuthn 或公网访问。

6. 按 `docs/ui/README.md` 建立任务级 before：

   ```text
   docs/ui/desktop-web/changes/20260830-lan-device-pairing/before/
   ```

   使用固定 Chromium 1440×900、deviceScaleFactor 1 和确定性 fixture。开始改 UI 前保存当前 Desktop
   基线；不得在截图中出现真实 ticket、Cookie、device key、证书私钥、真实设备名或真实用户数据。

## 必须保持分离的事实

| 事实                             | 唯一 owner                          | 语义                                                                  |
| -------------------------------- | ----------------------------------- | --------------------------------------------------------------------- |
| 单 owner bootstrap identity      | 既有 Core/config foundation         | 当前单用户 owner；本任务不创建多用户账号系统                          |
| production device credential     | workos-gateway Device Auth          | server-minted device ID、public key、name/class、revision、revocation |
| pairing ticket / proof challenge | workos-gateway Device Auth          | 短期、单用途、可恢复但不可复制的认证状态机                            |
| device session                   | workos-gateway Device Auth          | token hash、device/owner binding、absolute expiry、revocation         |
| browser private key              | trusted Desktop browser profile     | non-extractable CryptoKey，只在 IndexedDB，不进入 protobuf/日志/截图  |
| trusted request identity         | workos-gateway request context      | 从 validated session 派生，再注入 private upstream header             |
| Surface/App Bridge session       | runtime-host Surface                | 继续绑定 Gateway 提供的 device ID；不成为登录 session                 |
| Project/App/Task/Incident facts  | 现有 Core/Runtime/Reliability owner | 不因配对而迁移或被 Gateway 查询                                       |

Gateway 不得查询 `workos_core.devices`、projects、surface 或 incident 表。Core/Runtime/Reliability 不得查询
Gateway auth 表或校验 Cookie。跨进程身份仍只通过 Gateway 清洗后注入的 private headers；浏览器提交的
同名 headers 永远没有权威性。

## 必须完成的目标链路

### 1. 明确区分 development 与 production auth mode

- `WORKOS_DEV_AUTH_BYPASS=true` 的现有 loopback 开发模式必须原样可用：固定 owner/device 只来自配置，
  不创建 Cookie、不依赖 production session 表、不允许非 loopback bind。
- bypass 关闭时，无论 bind 是否 loopback，都必须要求：Gateway 自己终止 TLS、configured public origin
  为无 userinfo/query/fragment/path 的 canonical `https://` origin、Database 可用、owner ID 为 canonical
  UUIDv7、admin Unix socket 为安全 absolute path、所有 TTL/limit 合法。
- production mode 不再要求或使用 `WORKOS_DEVICE_ID`；device ID 只能由 Gateway 的 UUIDv7 generator
  生成。dev mode 则继续要求 canonical configured device ID。
- production TLS listener minimum version 固定 TLS 1.3。不得信任 `Forwarded`、
  `X-Forwarded-Proto`、`X-Forwarded-Host` 或 request Host 来生成 public URL。
- 所有非 health public 请求校验 request Host 与 configured public origin host 完全一致；auth 浏览器
  POST 还必须校验 exact `Origin`。不启用 CORS，不接受 wildcard origin。
- production Gateway readiness 同时依赖 Core 与 Gateway auth store；store outage 返回 unavailable，
  绝不临时回退到 configured identity 或 stale cache。

### 2. Gateway-owned domain、ports 与 PostgreSQL authority

在 `internal/gateway/auth` 下建立标准：

```text
domain → application → ports ← adapters/postgres
                              ← transport/connect
```

domain 不得 import pgx、Connect、HTTP、crypto vendor、文件系统或其他模块 adapter。时间、entropy、UUID、
public-key verifier、Cookie writer 和 repository 都从 application 边界注入或置于 adapter/transport。

新增 forward-only migration（预计 `020_gateway_device_auth.sql`，owner 注释写明 workos-gateway），创建
`workos_gateway` schema 及至少以下 durable facts：

- `device_credentials`：canonical UUIDv7、owner snapshot、bounded name/class、canonical P-256 SPKI、
  public-key SHA-256 thumbprint、revision、created/authenticated/revoked UTC timestamps；
- `pairing_tickets`：ticket ID、owner、secret hash、public origin + TLS leaf fingerprint snapshot、expiry、
  claimed device/key facts、state timestamps、bounded attempt count；
- `device_auth_challenges`：purpose (`pairing`/`session`)、device/ticket/key binding、32-byte nonce、expiry、
  consumed/result facts、bounded attempt count；
- `device_sessions`：session ID、owner/device binding、token hash、created/expires/last-seen/revoked timestamps；
- `device_revocation_requests`：owner-scoped idempotency digest + immutable first result snapshot（若公开 revoke
  contract 采用同等可证明的已有通用模式，也要保留相同语义）。

数据库必须下沉：合法 state CHECK、positive revision/attempt bounds、coherent timestamps、same-owner
composite FK、token/key hash 长度、一个 device 至多一个 active session、一个 owner 至多一个 active
pairing ticket。创建新 ticket 时在 owner 级数据库锁内 revoke 旧 outstanding ticket，再创建新 ticket；
并发 issuance 最终只有一个可用票据。新 session 在 device row lock 内 revoke 旧 active session 后创建，
因此 lost response 后重新认证不会留下多个可用 bearer token。

不得：

- 修改或 backfill `001` 的 `workos_core.devices`；
- 建立跨 schema FK 到 Core；
- 保存 raw pairing secret、raw session token 或 private key；
- 把 public key 当 secret 加密后便声称 Credential Vault 已实现；
- 在 service 启动时自动 migration；
- 用内存 map 冒充 restart-persistent authority。

更新 `sqlc.yaml` 并生成 Gateway-owned query package。把 migration checksum/empty-database/forward tests 扩展
到新文件；`001`–`019` 字节必须保持不变。

### 3. 私有 operator bootstrap 与 ticket issuance

新增 private admin service（命名可调整但职责不可漂移），只监听 Gateway 进程拥有的 Unix domain socket：

```text
workosctl device pair
  → HTTP/Connect over /run/.../gateway-admin.sock
  → Gateway DeviceAuthAdminService.RotatePairingTicket
  → raw ticket returned exactly once to the CLI
  → CLI prints expiry, public origin, TLS fingerprint, URL and terminal QR
```

- `workosctl` 不得 import Gateway postgres adapter 或写 `workos_gateway` SQL。
- admin service 不得注册到 public TCP mux、Gateway proxy allowlist或 SPA fallback；对同一 RPC 的 TCP 请求
  必须确定性 404，并有测试。
- socket parent/path/permissions 启动即验证；文件 mode 最多 `0600`。只可清理 exact configured stale
  socket；若 path 是 symlink、regular file、目录或超出受控 runtime directory，启动失败，禁止 broad rm。
- systemd 只为 `workos-gateway` 创建受限 `RuntimeDirectory`/socket；不放宽其他五个进程。dev compose 可
  在 bypass mode 不启用 admin socket。
- `RotatePairingTicket` 是明确的 credential rotation：每次成功调用使所有旧 outstanding QR 失效。
  响应丢失时操作者重新运行命令即可；不能为了“幂等重放”把 raw ticket 明文保存在数据库。
- 已认证 Desktop 的 Device Center 也可调用同一个 application command 生成“Pair another device” QR；
  它必须经过正常 session gate。重复点击/并发生成会使旧 QR 明确显示为已替换，而不会留下多个有效码。
- raw ticket 只能出现在一次性 admin/authenticated response 和用户主动显示的 QR/URL 中；不得进入
  request URL query、server access log、trace attribute、error、database、clipboard 默认值或 telemetry。

QR URL 固定使用 configured public origin，形状至少版本化为：

```text
https://workos.example/pair#v=1&t=<base64url-ticket>&fp=sha256:<64-lower-hex>
```

secret 必须在 fragment，不在 query/path。Pairing UI 读取并完成严格 grammar 校验后，第一时间用
`history.replaceState` 把地址恢复为 `/pair`；ticket 只留在本次内存流程。刷新或关闭页面后需要重新扫描，
除非同一已 claim ticket 被同一浏览器 pending credential 明确恢复。

### 4. Browser profile device key 与 canonical proof

新增仅供 trusted Shell 使用的 client package（建议 `clients/device-auth`）；不得把 device/session API、
private key handle 或 pairing helpers 导出到 `@workos/app-sdk`、`@workos/surface-sdk`、App Bridge 或 iframe。

浏览器流程固定为：

1. 在发送 raw ticket 前，用 WebCrypto 生成 `ECDSA/P-256` key pair；private key
   `extractable=false`、usage 只有 `sign`，public key usage 只有 `verify`。
2. 把 private `CryptoKey` 与本地 pending/active device metadata 结构化存入 IndexedDB。若 IndexedDB
   写入失败，必须在 claim ticket 前停下；不得 fallback localStorage/sessionStorage、导出 PKCS#8/JWK、
   把 key 放 React serializable state 或下载文件。
3. 只导出 public key 的 SPKI DER，服务端 parse 后必须确认 ECDSA P-256、无 trailing bytes，并重新
   marshal 后与提交 bytes 完全相同。
4. `BeginPairing` 以 ticket hash 锁定一条 active ticket，server-mint device UUIDv7，绑定 exact key
   thumbprint/name/class，并返回 challenge。same ticket + same pending key 可恢复并 rotate challenge；
   same ticket + different key/metadata 统一认证失败，不能泄露 claim 事实。
5. client 与 server 各自构造同一 versioned proof transcript；client 不签名服务端返回的任意 opaque
   bytes。
6. `CompletePairing` 在同一事务锁定 ticket/challenge，验证 proof，创建 credential、完成 ticket、创建
   session。challenge 只能消费一次；并发 complete 恰有一个 winner。
7. pairing completion 响应不确定时，client 使用已持久 pending device ID/key 尝试正常 session proof；
   若 server 已 commit 即可恢复，若未 commit 则重新扫描同一仍有效 QR。不得生成重复 device。

canonical transcript 不使用 map JSON、protobuf JSON、字符串拼接或 vendor JWS。建立 Go/TypeScript 共用
测试向量的单一二进制编码：

```text
ASCII domain separator: workos.device-proof/v1
purpose byte: 0x01 pairing | 0x02 session
随后每个字段：uint32 big-endian length || raw bytes
```

字段顺序固定并至少覆盖：canonical public origin、purpose、challenge UUIDv7、32-byte nonce、device
UUIDv7、public-key SHA-256 thumbprint；pairing proof 额外覆盖 ticket UUIDv7 与 ticket 中 snapshot 的 TLS
leaf fingerprint。所有 string 在编码前已有唯一 canonical grammar。

签名算法固定 `ECDSA P-256 + SHA-256`。WebCrypto 返回 64-byte `r || s`；Go 将前后 32 bytes 解析为
positive `r`/`s`，验证范围后调用 `ecdsa.Verify`。任何 DER、短/长、zero、out-of-range、unknown curve、
noncanonical SPKI 或 unknown proof version 一律 fail closed；没有 algorithm negotiation 或 fallback。

### 5. Session creation、Cookie 与每请求 Gateway gate

新增 unauthenticated-but-TLS-only session proof：

```text
BeginDeviceSession(device_id)
  → bounded single-use challenge（unknown/revoked device 返回相同外形的假 challenge）
CompleteDeviceSession(device_id, challenge_id, raw_signature)
  → verify exact stored public key
  → rotate the device's one active session
  → Set-Cookie only; response body never carries the raw session token
```

unknown、foreign、revoked、expired、consumed、wrong-signature 都返回相同的 sanitized unauthenticated
结果；不得通过状态、body、字段 presence 或明显 timing 分支提供 device existence oracle。Begin/Complete
均受 TLS、Host、Origin、body budget、attempt budget 和 rate limit 保护。

session Cookie 固定：

```text
Name: __Host-workos_session
Value: 32 random bytes, base64url without padding
Secure
HttpOnly
SameSite=Strict
Path=/
no Domain
absolute Max-Age/Expires matching the stored UTC expiry
```

- 数据库只保存 `sha256(raw token)`；raw token 只进入一次 `Set-Cookie`。
- logout/revoke/invalid-cookie clearing 必须用完全相同 name/path/security attributes 和 expired value。
- Gateway 每个受保护请求都从 Cookie hash 查询 active session + active credential + owner，核对 UTC expiry
  与 configured owner；不使用 process-local auth cache，因此 revoke commit 后的新请求立即失败。
- store 暂时不可用为 sanitized `503`/Connect `Unavailable`，不是 `401`，也不得继续用上次 identity。
- malformed/missing/expired/revoked session 为固定 `401`，并在适当时清 cookie；任何情况下 upstream
  都不得被调用。
- `last_seen_at` 如更新必须以有界时间门槛的 guarded UPDATE 执行，不能每个 asset request 写库，也
  不能参与授权或延长 absolute expiry。
- proxy Director 必须删除 inbound identity/forwarded headers，从 gate 写入 request context 的 validated
  identity 动态注入 user/device headers；Cookie、pairing headers、auth proof、public key、session token
  一律不转发 Core/Runtime/Reliability。
- 现有 App Bridge credential 仍只在精确 AppBridge route 转发；新增 auth refactor 必须保留所有现有
  header-cleaning tests。
- `/surfaces/<session>/...`、public Connect routes 与 Incident route 全部经过同一 session gate；SPA
  static assets和 `/pair` 可匿名加载，但不能返回任何业务数据。
- Cookie-authenticated unsafe requests 必须 exact-Origin 且拒绝 cross-site Fetch Metadata；不因
  `SameSite=Strict` 而省略服务端 CSRF/Host 检查。不得允许 CORS。
- Desktop 遇到 mid-operation `401` 不得自动重放业务 mutation。先完成 session proof，再让用户明确
  重试；已有业务 idempotency key 不是静默重放授权。

### 6. Device management、撤销与 Desktop Auth Gate

Additive public Gateway-local services建议拆成：

```text
DevicePairingService   BeginPairing / CompletePairing / BeginSession / CompleteSession
DeviceService          GetCurrentDevice / ListDevices / RotatePairingTicket /
                       RevokeDevice / Logout
DeviceAuthAdminService RotatePairingTicket（private Unix socket only）
```

命名可按 Proto style 调整，但 authenticated 与 unauthenticated 方法必须在 routing、transport 和测试中
显式区分。不得把 admin service 放入 public prefix allowlist，也不得把 auth services代理给 Core。

`DeviceService` 要求：

- owner scope 只来自 session；request 不接受 owner；
- List 使用 application-owned page normalization + repository limit+1，token 不产生 phantom page；
- response 只含 device ID、bounded name/class、revision、created/last-authenticated/revoked 时间和
  `is_current`；不返回 SPKI、thumbprint、session ID/hash、ticket、IP、user-agent 或 owner ID；
- Revoke 使用 owner-scoped idempotency key + expected device revision；同请求精确 replay，different
  request stable `Aborted`；device revoke、全部 active sessions revoke、result snapshot 同事务；
- current device 可被撤销，但 UI 必须明确确认，成功响应后清 Cookie 与本地 key并回到 unpaired screen；
- revoking another device makes its next request fail immediately；已有 Runtime Surface row 不跨进程
  删除，而是因 Gateway gate 不可达并由既有 TTL 收敛；不得直接 SQL Runtime。
- Logout 只撤销当前 session并保留 local device key，允许后续 proof re-auth；“Forget this browser”是
  明确的 client action，先 logout/revoke（按用户选择）再删除 IndexedDB key，不能在 transient outage
  时自动丢 key。

Desktop 启动改为显式状态机，未认证时不得先挂载会发业务请求的 `Desktop`：

```text
checking-session
paired-session-proof
unpaired
pairing
authenticated → mount Desktop
unavailable
```

- 有 active Cookie：`GetCurrentDevice` 成功后挂载 Desktop。
- Cookie 缺失/过期但 IndexedDB 有 active device key：完成 session challenge/proof 后挂载；这只是认证，
  不自动重放此前业务操作。
- 无 key：显示 bounded unpaired screen；URL 有合法 fragment 时进入 pairing。
- revoked/unknown 与 bad proof 使用一致文案；store/network unavailable 使用不同的可重试 unavailable
  文案，不能误导用户删除 key。
- 新增普通 Device Center/System Settings window，列出 paired devices、当前设备、session expiry、
  Pair another device（短期 QR）与 revoke/logout actions；它不是永久 sidebar。
- QR/ticket/private key/session token 不进入 window-manager serializable state、sessionStorage、DOM text
  debug attributes、analytics、logs 或错误。QR canvas/SVG 可在 trusted window 临时渲染；窗口关闭或
  ticket替换/过期立即清内存。Copy URL 必须是用户主动操作。
- sandboxed App Surface、opaque origin、CSP `connect-src 'none'`、bridge capability 与 token 边界保持
  不变；App 无权调用 DeviceService 或读取 Cookie/IndexedDB credential。

### 7. TLS certificate fingerprint 与部署边界

- Gateway 从实际将被 TLS listener 使用的 leaf certificate DER 计算 `sha256:<lower-hex>`，不信配置中
  另填的 fingerprint。ticket snapshot 与 admin/UI response 使用这一个事实。
- public origin 从显式配置产生；QR、proof transcript、Origin/Host validation 都用同一 canonical 值。
- 短期 ticket 在 leaf certificate/public origin 改变后必须失效；已经配对的 device credential可在
  operator 正常轮换受信任证书后继续 session proof，不把旧 fingerprint 永久钉死。
- 浏览器 UI 显示 fingerprint 并要求与 ticket issuer 展示值一致，但文档必须说明浏览器仍依赖平台
  trust store；不得声称实现 native certificate pinning。
- 更新 systemd env/example 与 deployment guide，给出 operator-provided trusted cert、public origin、
  admin socket 和 `workosctl device pair` 步骤。不得提交真实 cert/key、个人域名、内部 IP、CA private
  key 或自动弱化浏览器证书错误的生产选项。
- 默认 compose 继续是 loopback HTTP + dev bypass。另建确定性的 TLS auth fixture/profile/Make target，
  每次测试在受控临时目录生成 CA/leaf keypair与 SAN，结束时清理；测试私钥不得进入 Git 或日志。

## 协议与数据要求

- 所有跨 Go/TypeScript/CLI 的契约先新增 `api/proto/workos/auth/v1/*.proto`，再 `make generate`。
- v1 字段只 additive；enum 拒绝 UNSPECIFIED/unknown；字段号不得复用；删除必须 reserved。
- Proto 表达 wire facts，不把 Cookie raw value放 message，不另手写同义 DTO。
- public pairing/session response必须 version/purpose explicit；timestamps 使用 UTC protobuf timestamp。
- private admin service 使用同一 generated contract，但只挂 Unix socket mux。
- session/ticket/device IDs 全部 server-minted canonical UUIDv7；client random UUID 只可作为 idempotency key。
- pairing ticket/session token使用独立 domain-separated hash helper，不能把一种 token放入另一种索引。
- public key thumbprint 是 canonical SPKI DER 的 SHA-256，不是任意 JWK JSON hash。
- 所有 mutation concurrency由 PostgreSQL lock/guard/unique constraint裁决；Go pre-check只能优化，不能
  成为唯一仲裁。
- 若新增事件，只允许无 secret 的 `device.paired.v1` / `device.revoked.v1` 等 Gateway-owned audit facts，
  并必须 outbox + at-least-once；本切片不要求下游消费，不能为了“看起来完整”跨 schema写 event。

## 错误与 HTTP 语义

固定 sanitized matrix，且 transport 用 `errors.Is`/typed verdict，不匹配数据库文本：

| 情况                                                                         | Connect / HTTP            | 对外信息                            |
| ---------------------------------------------------------------------------- | ------------------------- | ----------------------------------- |
| malformed/unknown enum/oversize/noncanonical key/signature                   | `InvalidArgument` / 400   | 固定 invalid request                |
| missing/expired/claimed-by-other ticket，wrong proof，unknown/revoked device | `Unauthenticated` / 401   | 同一 authentication failed          |
| missing/expired/revoked session                                              | 401                       | device session required             |
| authenticated foreign/unknown device management target                       | `NotFound`                | device not found                    |
| stale device revision / idempotency conflict                                 | `Aborted`                 | device changed / request conflict   |
| rate/attempt budget exhausted                                                | `ResourceExhausted` / 429 | retry later，不回显 key/device 状态 |
| Host/Origin/fetch-site policy failure                                        | `PermissionDenied` / 403  | request origin rejected             |
| PostgreSQL/admin dependency temporarily unavailable                          | `Unavailable` / 503       | gateway auth unavailable            |
| stored invariant/canonical key corruption                                    | `Internal` / 500          | device authentication failed        |

所有 auth response 使用 `Cache-Control: no-store`；pairing/static shell至少设置
`Referrer-Policy: no-referrer`。日志只记录 bounded operation、结果类别、trace ID和非敏感 server-owned
ID；不记录 Cookie header、Set-Cookie、request body、ticket fragment、signature、SPKI、fingerprint完整值、
device name或 SQL error文本。

## 必须补齐的测试

### Domain / crypto / cross-language vectors

- device name/class、UUIDv7、TTL/page/idempotency grammar边界；unknown enum拒绝；
- 32-byte entropy、base64url无 padding、hash domain separation、token不落明文；
- P-256 canonical SPKI accept；P-384/521、RSA、compressed/noncanonical/trailing/oversize拒绝；
- Go/TypeScript 对同一 transcript fixture产生逐字节相同 bytes和 SHA-256；任一字段变化签名失效；
- WebCrypto 64-byte raw `r || s` 与 Go `ecdsa.Verify`互通；DER/长度/range异常拒绝；
- CryptoKey private non-extractable并可在 IndexedDB round-trip后签名；storage失败不 claim ticket。

### PostgreSQL / concurrency / restart facts

- migration 020 从空库和 019 前向应用，第二次 migrate no-op；001–019 checksum不变；
- raw ticket/session token/private key不存在于任何 auth表；hash长度与 unique约束生效；
- rotate ticket 后只有一个 active ticket；并发两次 issuance最终仅一个可 claim；
- same ticket/same pending key可恢复 challenge，不重复 device；different key统一失败；
- CompletePairing并发恰一个 device/session，challenge只能消费一次；失败事务不产生半个 device；
- CompleteSession并发/重复 proof不产生两个 active session；新 session原子 revoke旧 session；
- lost pairing response后正常 session proof恢复；gateway restart后 Cookie仍有效；
- device revoke + all sessions + idempotency result同事务；same key replay、different request conflict；
- List分页 exact last page无 phantom token且 owner-scoped；cursor boundary不跨 owner；
- transient pgx outage分类为 Unavailable；corrupt SPKI/state/result snapshot为 Internal且不静默修复。

### Gateway / HTTP / admin socket

- dev bypass仍只允许 loopback，注入配置 identity，不发 Cookie；
- production auth无 TLS/public origin/store/admin socket时启动失败；TLS min version与 leaf fingerprint正确；
- Host/Origin/cross-site拒绝，request body gzip/oversize在业务代码前拒绝；
- missing/bad/expired/revoked Cookie绝不调用 Core/Runtime/Reliability upstream；DB outage返回503；
- valid session向所有 allowlisted upstream和 `/surfaces/` 注入 exact dynamic owner/device；spoof headers、
  Cookie、auth proof全部被剥除；App Bridge token仍只走原精确 route；
- public TCP访问 admin service/private services继续404；Unix socket mode/path/symlink/stale socket测试；
- `workosctl device pair`只调用 Unix RPC，不开数据库；错误输出不含 ticket，成功输出 QR/URL只一次；
- Set-Cookie精确包含 `__Host-`/Secure/HttpOnly/SameSite=Strict/Path=/、无Domain；response body和
  `document.cookie`取不到 token；logout用同属性清除；
- auth endpoints no-store，pair shell no-referrer，unknown/revoked/wrong proof响应外形一致；
- rate limiter有容量/淘汰测试，不信 `X-Forwarded-For`，restart不会把 durable attempt count清零。

### Desktop / browser / E2E

- Auth Gate在 session判定前不挂载 Desktop、不发 Project/App请求；unavailable与unpaired文案区分；
- URL fragment严格解析后立即 scrub，HTTP server/access log从未收到 fragment；ticket不进 storage；
- real Chromium WebCrypto生成/persist/sign，pairing后进入现有 Desktop；reload保留，gateway restart后
  session继续；Cookie过期后用IndexedDB key重新 proof登录；
- pairing response lost fault injection经 session proof恢复且只有一个 device；
- wrong key/signature、expired/replaced QR、claim race、revoked device均fail closed；
- Device Center列出current/other device，生成新QR使旧QR失效，revoke other后其browser下一请求401；
- revoke current/logout/forget行为与local key保留/删除语义精确，transient outage不删key；
- 生产 auth E2E经真实 TLS Gateway、真实 PostgreSQL、真实 Cookie和真实 browser运行；证书/私钥每次临时
  生成，fixture不访问外网。若Playwright为测试CA启用ignoreHTTPSErrors，任务证据必须明确它不证明
  browser trust-store或native pinning；
- 所有现有Project、Harness、App install、mutable grants、approval、Web Bundle/App Bridge、System
  Monitor和DeepSeek local fixture E2E继续通过。

### UI 视觉证据

至少保存：

```text
auth-gate--unpaired--1440x900.png
auth-gate--pairing--1440x900.png
device-center--paired-devices--1440x900.png
```

`before/`、`after/`、`current/` 使用相同 viewport/fixture。Pairing截图只能编码明确无效的 deterministic
fixture token与fixture fingerprint；不得截取测试运行中仍有效的QR、Cookie、真实key/device/用户数据。
`notes.md`写清这是不可用fixture，不能被扫描后授权。

## 非目标

本任务明确不实现：

- mDNS/Bonjour discovery、自动 DNS、DHCP、NAT traversal；
- ACME、自签CA分发、证书自动续期、浏览器绕过证书错误、外部 reverse proxy trust；
- iPad/Android/Capacitor wrapper、Keychain/Keystore/Secure Enclave、native certificate pinning；
- WebAuthn/passkey、hardware attestation、TPM、每请求 DPoP/mTLS；
- 多用户、密码登录、SSO/OAuth、角色/管理员授权、账号恢复；
- 公网 Gateway、VPN/overlay/relay、APNs/FCM、后台 Push；
- WebSocket realtime auth或session迁移；
- App可见device API、App Bridge新capability、iframe Cookie访问；
- Credential Vault、provider credential、DeepSeek live smoke；
- 修改/删除legacy `workos_core.devices`，或把它backfill为production credential；
- Podman acceptance、Repair Orchestrator、candidate deploy/rollback、Indexer/RAG、Artifact review；
- silent business mutation retry、unlimited session、remember-me bearer token、localStorage secret fallback。

不要用 TODO、固定成功response、内存-only session、明文token表、client-supplied identity、仅检查Cookie
存在、弱random、HTTP-only fixture或dev bypass E2E冒充working链路。

## 文档与状态同步

完成实现后同步：

- 新 ADR 与任务记录；
- `docs/architecture/implementation.md`：Gateway auth ownership、pairing/session flow、dynamic identity
  injection、legacy Core device边界、TLS/Unix socket与明确unavailable；
- `deploy/README.md` 与 systemd/env examples：受信任TLS证书、public origin、admin socket、初次配对、
  revoke/recovery步骤；
- `README.md` 的非生成部署说明中删除“device enrollment未实现”的陈述，并准确保留mDNS/mobile/public
  internet限制；状态表只能由生成器更新；
- `docs/status.json`：只有真实TLS + PostgreSQL + browser + restart/revoke证据闭合后，Access Gateway才可
  从 `scaffolded` 升为 `working`，证据必须限定“direct configured HTTPS origin上的browser profile
  pairing/session”；不得声称mDNS、native pinning、mobile或public internet已完成；
- Desktop Shell evidence补充Auth Gate/Device Center E2E；Mobile Shell继续`contract-only`；
- `docs/ui/desktop-web/changes/20260830-lan-device-pairing/{before,after}/`、`notes.md`和对应`current/`。

`README`状态区块、`gen/`、`src/gen/`只能由工具生成，不得手改。

## 完成门禁

至少执行并在任务记录中写真实结果：

```sh
make bootstrap
make generate
make check
buf breaking --against '.git#branch=main'
go test -race ./internal/gateway/... ./internal/platform/identity/...
make test-integration
make test-e2e
make test-deepseek-fixture
make test-lan-pairing
make generate
git diff --check
git status --short
```

其中 `make test-lan-pairing` 是本任务新增的确定性验收门禁，必须至少证明：

```text
temporary TLS leaf actually served
→ operator/admin ticket issuance
→ real browser P-256 pairing proof
→ HttpOnly Cookie session
→ public Core request with dynamic identity
→ Surface route protected by same session
→ Gateway restart persistence
→ session proof re-authentication
→ device revocation immediately blocks forwarding
→ all temporary cert/key/ticket/test processes cleaned
```

第二次 `make generate` 后工作树不得产生新生成差异。检查 Git 中不存在 private key、certificate fixture
key、session/ticket值、Cookie dump、Playwright trace/video、数据库dump、临时socket或浏览器profile。

如果真实 TLS/browser链路因环境被阻塞，可以完成domain/unit/PostgreSQL代码并留下门禁，但任务不得标
`done`，Access Gateway不得升`working`，UI after/current不得伪造为成功状态。记录确切blocker和可复现
命令，不用dev bypass替代。

## 最终交付格式

最终回复必须简洁报告：

1. 完成的production auth链路与关键安全边界；
2. Proto、migration、Gateway/CLI/Desktop/部署文档和视觉证据位置；
3. 实际通过的命令，特别是TLS/browser/restart/revoke结果；
4. 未完成或仍unavailable的能力（mDNS、native pinning、mobile、public internet等）；
5. branch与commit hash；明确说明未merge、未push；
6. 明确说明没有使用或保存任何真实Provider/API credential。
