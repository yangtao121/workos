# Task: LAN 设备配对与持久 Gateway 会话纵向切片

- 状态：done（两轮安全审查修复与全量门禁通过；用户后续明确授权合并到本地 `main`）
- Owner/Agent：implementation agent (2026-08-30)
- 进程/模块：workos-gateway（新增 internal/gateway/auth + admin socket + TLS 生产模式）、
  workosctl（device pair）、desktop-web（Auth Gate / Device Center / clients/device-auth）、
  api/proto（workos.auth.v1）、internal/platform（config/httpserver）、migration 020
- 依赖：ADR-0002/0003/0005/0006 已合入 main（2aad565）；migrations 001–019 已执行且逐字节
  不变；Prompt：docs/prompts/20260830-next-agent-lan-device-pairing.md

## 目标与范围

### 包含

- `api/proto/workos/auth/v1/device_auth.proto`：`DevicePairingService`（公开匿名 pairing/
  session proof）、`DeviceService`（session 认证的设备管理）、`DeviceAuthAdminService`（仅
  Unix socket 的 operator rotation）。
- migration `020_gateway_device_auth.sql`（owner: workos-gateway）：`workos_gateway` schema，
  `device_credentials` / `pairing_tickets` / `device_auth_challenges` / `device_sessions` /
  `device_revocation_requests`；状态机 CHECK、hash grammar、partial unique index（每 owner 一个
  pending ticket、每 device 一个 active key/session）、owner-scoped 撤销幂等快照。`001`–`019`
  逐字节不变。
- `internal/gateway/auth`：domain（ticket/challenge/session 状态机、canonical P-256 SPKI 解析、
  raw `r||s` ECDSA 验证、版本化 proof transcript）/ ports / application（flow 编排）/
  adapters/postgres（sqlc `gatewayauthdb`，全部事务型 guarded
  UPDATE）/ transport（Connect handlers、`__Host-` Cookie、净化错误矩阵、no-store）。
- Gateway 生产模式：每请求 Cookie→session→identity 解析（无进程内缓存）、动态 owner/device
  header 注入、Cookie/伪造 header/bridge token 剥离、Host 精确匹配、unsafe 方法 exact Origin +
  Fetch-Metadata 校验、auth 端点 16 KiB body 预算、admin socket 4 KiB、`/pair` 静态 shell
  `Referrer-Policy: no-referrer`；TLS min 1.3 由启动配置注入；readiness 含 auth store ping。
- admin Unix socket：path/parent ownership/permission/symlink 启动校验、仅 `ECONNREFUSED` + inode
  不变时精确清理 stale socket、mode 0600、与 public listener 共生命周期且异常非零退出；TCP 侧
  确定性 404（有测试）。
- `workosctl device pair`：Connect over Unix socket，输出 expiry/origin/fingerprint/URL/终端
  QR；CLI 不开数据库、错误不含 ticket。
- Desktop：`AuthGate` 状态机（checking-session/paired-session-proof/unpaired/pairing/
  authenticated/unavailable）、fragment 严格解析即擦除、WebCrypto non-extractable key +
  IndexedDB 回读与 private/SPKI 实际签验自检、显式 challenge version/purpose 拒绝降级；`Device Center`
  普通窗口（设备列表/会话到期/自动清除过期 Pair another device QR/retry-stable revoke/logout）；
  mid-operation 401 提示显式重试，不自动重放业务 mutation。
- `make test-lan-pairing`：临时 TLS leaf + admin socket ticket + 真实 Chromium 四阶段门禁。

### 不包含（非目标）

按 Prompt 与 ADR-0007 第 7 节：无 mDNS、native pinning、公网访问、WebAuthn、多用户、
push relay、WebSocket auth、Credential Vault、Podman acceptance 等。

## 协议/数据影响

- Proto：`api/proto/workos/auth/v1/device_auth.proto`（新增，additive）。
- Migration：`020_gateway_device_auth.sql`（新增；001–019 checksum 不变，由
  tests/integration 钉住）。
- sqlc：`internal/gateway/auth/adapters/postgres/{queries.sql,gatewayauthdb}`。
- 事件：无（未引入跨 schema event；`device.paired.v1` 等留给后续切片）。
- 配置：`auth.public_origin/admin_socket_path/pairing_ticket_ttl/proof_challenge_ttl/
session_ttl` + 环境变量 `WORKOS_AUTH_*`、`WORKOS_HTTP_TLS_CERT_FILE/KEY_FILE`。

## 验收

- [x] Domain/crypto 单测：transcript 跨语言向量（Go/TS 同一 SHA-256）、P-256 canonical SPKI
      接受 / P-384、trailing、oversize、非 canonical 拒绝、raw 64-byte 签名互通 + DER/长度/range
      拒绝、secret grammar、domain-separated hash、device name/class grammar、session expiry。
- [x] Application 状态机单测（内存 repo）：rotation 失效旧 QR、恢复需同 key+metadata、
      complete 单赢家、session 轮换、decoy challenge、撤销幂等/冲突/立即 fail closed、分页无
      phantom token、rate limiter 有界。
- [x] Gateway HTTP 单测：缺/坏 cookie 401+清 cookie、Host 不匹配 403、cross-site Origin/Fetch-
      Metadata（含 unsafe exact-Origin + cross-site）403、store 故障 503/corruption 500、per-remote +
      global 匿名预算、有效会话动态 identity 注入且 Cookie/bridge token 不上行、
      生产无 auth stack 拒绝启动、admin service TCP 404、pairing 端点匿名可达。
- [x] PostgreSQL 集成（tests/integration/device_auth_test.go）：020 从空库应用 + 二次 no-op +
      约束拒绝坏数据、并发 rotation 恰一个 pending、并发 claim 恰一个赢家、全生命周期（rotate→
      begin→complete→轮换→撤销→重放/冲突）、restart 后会话仍解析、任何 auth 表无明文 token。
- [x] `make check`（proto lint/format、go vet+test、pnpm architecture/eslint/prettier/vitest/
      vite build、status render --check）。
- [x] `make test-integration`（含 001–012 checksum 钉住与全部既有集成测试）。
- [x] `make test-e2e`（dev bypass 下既有 6 条 E2E 不回归；AuthGate 探测到无 auth 端点的部署
      时保持直接挂载行为）。
- [x] `make test-lan-pairing`（真实 TLS + PostgreSQL + 真实 Chromium：pair → HttpOnly Cookie →
      Core 动态身份业务请求 → /surfaces/ 同一 gate（匿名 401）→ gateway restart 会话存活 → 清
      Cookie 后 IndexedDB proof 重新认证 → Device Center 撤销当前设备后回到 unpaired；临时证书/
      profile 目录 exit 时删除）。
- [x] UI 视觉证据：`docs/ui/desktop-web/changes/20260830-lan-device-pairing/{before,after}/`
      与 `current/`（1440×900；Auth Gate 使用不可用 fragment fixture，Device Center 使用拦截的固定
      Connect fixture，不依赖 live ticket、持久卷历史或 wall clock）。
- [x] 文档同步：ADR-0007、`docs/architecture/implementation.md`、`deploy/README.md`、
      `deploy/systemd/workos-gateway.env.example`、`docs/status.json`（经生成器）。

## 交接

### 执行环境事实（2026-08-30）

- 宿主：Linux，docker 29.7.2；Go/buf/sqlc/Playwright 全部经 Makefile 容器工具链运行。
- **验收卷 019 元数据修复（环境预存问题，非本任务引入）**：本任务开始前的
  `make test-e2e` 重建镜像后 bootstrap 失败
  （`migration 019_reliability_incident_convergence.sql checksum changed`）。调查证据：卷内
  `workos_meta.schema_migrations` 记录的 019 checksum `226c88ca…` 在 git 全历史中不存在
  （019 唯一存在过的提交版本 3ee29c9/2aad565 字节为 `08d030c1…`）；且卷内
  `workos_reliability` 实际 schema（含 `incident_actions_outcome_check` 的 7 值枚举等）与
  提交版 019 完全一致。结论：卷由某个未提交的 019 中间稿初始化，仅元数据行陈旧。
  处置：`UPDATE workos_meta.schema_migrations SET checksum='08d030c1…' WHERE
name='019_…'`（仅修正一行元数据为提交事实；未触碰任何业务表、未清理任何历史记录）。
  修复后 bootstrap/E2E 全部通过。

### 已验证命令（真实结果，2026-08-30 最终门禁轮）

```text
make bootstrap                                   # PASS
make generate（两次）                             # PASS，第二次无生成差异
make check                                       # PASS（proto lint/format、sqlc vet、
                                                 #  go vet+test、architecture、eslint、
                                                 #  prettier、device-auth 7 tests、
                                                 #  desktop-web 78 tests、vite build、
                                                 #  status render --check）
buf breaking api/proto --against '.git#branch=main'   # PASS（exit 0，纯 additive）
go test -race ./internal/gateway/... ./internal/platform/identity/...  # PASS
go test ./cmd/...                                # PASS（workosctl）
go test -tags=integration -run TestGatewayDeviceAuth ./tests/integration  # PASS（5 项）
go test -tags=integration -count=20 -run '^TestGatewayDeviceAuthConcurrency$' ./tests/integration  # PASS
make test-integration                            # PASS
make test-e2e                                    # PASS（6 passed）
make test-deepseek-fixture                       # PASS
make test-lan-pairing                            # PASS（pair/persist/reauth/revoke 四阶段）
make capture-lan-pairing-visual                  # PASS（2 deterministic visual tests）
git diff --check                                 # PASS
```

调试过程中发现并修复的真实缺陷（均有测试或门禁覆盖）：

1. sqlc 未标注类型的 timestamptz/jsonb 参数被推断为 nullable——补显式 cast。
2. `LockOwnerTicketRotation` 用 `:one` 读取 void 列导致 pgx scan 失败——改 `:exec`。
3. pairing ticket/challenge 的 device 复合外键指向尚不存在的 credential 行——改为无 FK，
   绑定完整性由完成事务 + unique 索引裁决（migration 注释说明）。
4. 状态机 CHECK 假设 revoked 必有 claim facts——rotation 会 revoke pending ticket，改为
   单向约束（pending 无 facts；claimed/completed 必有 facts）。
5. 撤销时间戳取自锁等待前的时钟快照，可能早于并发提交行的 created_at——三类 revoke 查询
   改用数据库事务时间 `now()`（集成测试抓出）。
6. 浏览器 keygen：`generateKey(..., true, ["verify"])` / `(..., false, ["sign"])` 会给配对
   另一半生成空 usages，Chrome 直接拒绝——改为单次 `generateKey(alg, false,
["sign","verify"])`（私钥 non-extractable/sign，公钥可导出/verify），并加
   `privateKey.extractable` fail-closed 校验与陈旧身份记录的健壮重生成。

### 关键实现边界（复核入口）

- transcript 唯一事实源：`internal/gateway/auth/domain/proof.go` 与
  `clients/device-auth/src/transcript.ts`，两侧同一 fixture SHA-256
  `c857b751ae958ac27c6a0de976d8beb51808d59b8878ec6f85abc56215347713`。
- Cookie 形状：`internal/gateway/auth/transport/connect.go`（Set/Clear 同属性）。
- 动态 identity 注入：`internal/gateway/gateway.go`（Director 从 context 注入，缺 identity
  不上行；Cookie 恒剥离）。
- 撤销时间戳用数据库事务时间（`RevokePendingTickets`/`RevokeActiveSessions` 用 `now()`），
  规避锁等待导致的时钟倒挂——这是集成测试抓出的真实 bug。

### 审查修复（第一轮，2026-08-30）

第一轮审查指出 8 项阻塞问题，全部修复并补测试后全量门禁重跑：

1. **P0 rotation 只撤销 pending**：`RevokeOutstandingTickets` 现撤销 `pending` 与
   `claimed`（操作者 rotation 即刻杀死所有已展示/已领取的 QR）。新增集成测试
   `TestGatewayDeviceAuthRotationKillsClaimedTickets` 钉住"claimed 后 rotation → 完成
   必须失败"；内存 repo 同步语义。
2. **P0 CompletePairing 未重核当前快照**：完成前现在比对 ticket snapshot 与进程当前
   `PublicOrigin`/`TLSFingerprint`（与 BeginPairing 同一检查）；证书/origin 变更后旧
   challenge 一律 fail closed。单测
   `TestCompletePairingRejectsRotatedSnapshot` 钉住。
3. **P1 outage 被降级为 401/404**：application 所有 lookup（ticket/challenge/device/
   session）现在透传 `ErrStoreUnavailable`（→ 503），包括 BeginDeviceSession 的 decoy
   分支；单测 `TestStoreOutageStaysUnavailable` 钉住。
4. **P1 代理未剥 Forwarded 系**：Director 删除 `Forwarded` 与全部 `X-Forwarded-*`；
   新增 `TestProxyStripsClientForwardingHeaders`（dev gate）并在生产 gate 测试中断言
   客户端 forwarding 值不达上游（httputil 自身断言的直连 peer XFF 除外）。
5. **P1 IndexedDB 只等 request success + 载入 key 未重验**：`withStore` 现在等待
   `transaction.oncomplete`（提交后才算成功）；`isWellFormedIdentity` 重新校验载入
   私钥 `extractable=false`、ECDSA/P-256、usage 恰为 `sign`，不合格记录先重生成再
   claim。
6. **P1 并发 RevokeDevice 竞态**：revoke 事务先取
   `pg_advisory_xact_lock(owner|key)`，同 key 重复请求等待首笔提交后走 replay
   快照路径，不再出现 NotFound。
7. **P1 admin socket 清理/生命周期**：绑定前对现存 socket 先拨号探测——仍应答即
   判定另一进程持有，启动失败；无应答（stale）才删除。单测
   `TestListenAdminSocketRejectsLiveSocket` 钉住"live 不动、stale 回收"。admin
   socket 运行期失败现在会停止整个 Gateway（`ctx` 贯穿 server 与 admin 监听）。
8. **必需门禁真实性与测试缺陷**：`TestGatewayDeviceAuthConcurrency` 重写——并发
   rotation 后从 DB 读取幸存 pending ticket（原实现误取首个 rotation 返回值，即已
   被 revoke 的 ticket），随后真实并发 4 路 claim（恰 1 胜）与 6 路 CompletePairing
   （恰 1 胜、恰 1 device、恰 1 active session）；并发套件下瞬时连接耗尽以有界
   backoff 重试 `Unavailable`（隔离运行 5/5 通过）。配套修复集成期间的
   `app_registry` padding 断言竞态归因记录（预存的验收卷敏感测试，非本任务引入；
   本轮全量重跑通过）。

重跑结果（真实）：`make check` PASS、`make test-integration` PASS（0 失败）、
`make test-e2e` PASS、`make test-deepseek-fixture` PASS、`make test-lan-pairing`
PASS（四阶段）。`docs/status.json` 的 Gateway `working` 状态在上述真实证据之上
维持。未 merge、未 push。

### 审查修复（第二轮，2026-08-30）

用户要求继续审核并直接修复全部发现项；本轮完成以下 hardening，并为每项增加定向证据：

1. **unsafe Origin 提前返回绕过 Fetch Metadata**：exact Origin 通过后仍检查
   `Sec-Fetch-Site`；新增 unsafe POST + `cross-site` 403 测试。
2. **stored corruption 被伪装为 400/401/404**：transport 让 `ErrAuthCorrupt` 优先于嵌套
   parser verdict；application 的 ticket/challenge/device/session lookup 与 Gateway cookie gate
   保留 corruption→Internal；PostgreSQL row mapper 在出口重验 UUIDv7、状态/结果、时间、name/class、
   digest、canonical SPKI/key hash，revocation JSON 拒绝未知字段和非 canonical snapshot；幂等快照补齐
   created/last-authenticated 时间并复核 device/revision/request-time binding，重试逐字段返回首次公开投影。
   新增 adapter、domain、application、transport、PostgreSQL replay 与 HTTP 测试。
3. **admin socket 安全与生命周期**：runtime parent 必须由 euid 所有且 group/other 不可写；existing
   socket 只有 connect=`ECONNREFUSED` 且 inode 未变化才删除，timeout/EACCES/替换竞态保留；关闭时
   也只删除自己记录的路径节点，并以真实 replacement socket 测试钉住。admin runtime failure 通过
   caller context 优雅关闭 public listener 并
   返回非零，匹配 systemd `Restart=on-failure`。新增错误分类、路径权限和 context shutdown 测试。
4. **浏览器持久 key 只验 shape**：IndexedDB 回读后重新 import canonical SPKI、重算 SHA-256，并用
   固定消息实际 sign/verify 证明 non-extractable private key 与 public key 属于同一 pair；pairing 在
   首次网络 claim 前完成 durable round-trip，自检失败则停止；reauth 在发请求前同样拒绝损坏记录。
   新增 key mismatch/hash/SPKI 与 storage-failure-no-network 测试；真实 Chromium reauth 继续覆盖
   IndexedDB 跨重启 round-trip。
5. **地址轮换绕过匿名预算**：production auth stack 同时要求 bounded RemoteAddr limiter 与独立
   process-global limiter；malformed RemoteAddr 进入共享 bucket，不再绕过。新增 per-IP/global/
   malformed-address 与 nil wiring 测试。
6. **public challenge 缺显式版本/用途**：先 additive 修改 Proto，`Challenge` 新增
   `proof_version=4` 与 `purpose=5` enum；Gateway 对 pairing/session 明确填值，client 在签名前拒绝
   zero/unknown version 与 purpose mismatch；重新 `make generate` 并通过 buf breaking。
7. **QR expiry 与撤销 lost response**：Device Center 按 server expiry 定时清除内存 QR，并拒绝缺失、
   非有限、已过期或超过 15 分钟上限的响应，QR 生成结束时再次检查 expiry；同 device revision 重试
   复用一个 idempotency key，revision 变化才开新逻辑请求。新增组件测试。
8. **视觉证据依赖 live ticket/持久卷历史**：新增 `make capture-lan-pairing-visual`；Device Center 在
   导航前拦截固定 Connect fixture，固定 Chromium viewport/locale/timezone，隐藏无关动态业务背景；
   重新生成并目检 after/current 三张截图，均不含 live credential 或真实数据。
9. **并发验收错误地把合法恢复当成单赢家**：连续 20 次真实 PostgreSQL 门禁发现，同 key/metadata
   请求按设计可恢复 claimed ticket，因此旧 fixture 的成功数依调度变化。竞争者现使用四个不同 P-256
   key，并把实际胜者 key 贯穿 completion；修正后 `-count=20` 全通过，同时保留 lost-response 恢复语义。
10. **LAN reauth 瞬态 UI 断言竞态**：清 Cookie 后 proof 可在 Playwright 观察 Auth Gate 前完成；门禁改为
    先直接断言 session Cookie 已清空，再以 authenticated shell + 真实 Core 写证明 IndexedDB key 建立了
    新 session，不再依赖中间 render 的时序；四阶段门禁重跑通过。

本节下方“未决风险”只保留明确非目标；上述问题均已实现、测试并进入最终门禁，不再是 open risk。

### 未决风险与下一步

1. 验收卷的 019 元数据修复已记录于上；后续如再出现 "checksum changed"，应先比对卷内 schema
   与提交版声明，再考虑是否重复本修复模式。
2. mDNS、native pinning、公网访问、移动 Shell 仍为 contract-only/非目标；Access Gateway 升
   `working` 的证据限定于"direct configured HTTPS origin 上的 browser profile
   pairing/session"（本任务已闭合），不涵盖上述能力。
3. challenges 表无 TTL 清理任务；decoy 行按 expires_at 失效但行保留，长期运行需后续切片提供
   有界清理（未做以免引入跨进程清理语义）。
4. 同一浏览器 profile key 对第二张 ticket 完成配对会被 credentials 唯一索引以净化
   authentication-failed 拒绝（防重复设备，正确行为）；UI 当前对显示通用 Pairing failed
   文案。若后续需要更细的"此浏览器已配对"引导，需要新的非存在性泄漏设计。
