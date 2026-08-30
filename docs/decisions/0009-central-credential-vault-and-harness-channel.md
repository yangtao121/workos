# ADR-0009：Central Credential Vault 与 authenticated harness execution channel

- 状态：Accepted
- 日期：2026-08-30
- 关系：更新 `docs/architecture/implementation.md` 中"workos-core 不拥有 credential"的旧边界；
  细化 ADR-0001 模块所有权、ADR-0005 的 task lease 语义、ADR-0007 的 transport 安全基线与
  ADR-0008 的 materialization 协议模式。为 `docs/structure.md` 第一版优先级第 9 项
  （Central Credential Vault）提供第一阶段实现边界。

## 背景

harness-host 目前从长期进程环境读取 `DEEPSEEK_API_KEY`，并把它复制进每个官方 runtime 子进程
环境。`HarnessBinding.credential_ref` 字段已存在但 binder 永远注入空值；没有 credential
create/rotate/revoke、没有加密持久化、没有 task snapshot、没有短期 lease。同时 Core 的
TaskExecutionService 注册在普通 loopback HTTP mux 上，没有服务身份认证，不能承载任何 raw
secret。本 ADR 固定第一版 Credential Vault 与 private harness execution channel 的边界。

## 决策

### 1. 长期 credential 的唯一 durable authority 是 workos-core Credential Vault

- 新模块 `internal/core/credential`（`domain → application → ports ← adapters`），数据由
  forward-only migrations `023`（credential-owned：`provider_credentials`、
  `credential_admin_requests`、`task_credential_leases`）与 `024`（agent-owned：
  `agent_task_credentials` snapshot）持有。任何 Provider-specific 类型（DeepSeek/OpenAI/...）
  不进入 Core：credential 只认识 canonical `consumer_id`（当前是 provider ID）、
  `purpose = provider-api-key.v1`、opaque UUIDv7 ID、revision、status 与 bounded label。
- 每个实现文件中的旧注释"Core 不拥有 credential"由此 ADR 显式取代：不是悄悄漂移。Core 拥有
  credential 的 sealed 事实与 metadata；它仍然不拥有 Provider runtime/config/capability
  （那些在 harness adapter），也不把任何 Provider 类型引入 credential 模块。
- 旧 Project/API/AgentTask/TaskLease/event/public Catalog 永远只接触 opaque reference：
  `HarnessBinding.credential_ref`（服务端派生的 credential UUIDv7）与 task snapshot 的
  `(credential_id, revision)`。字段不是 bearer token，泄漏不等于可使用。

### 2. Encryption at rest

- master key：Core 从绝对路径、regular、非 symlink、owner-only（无 group/world 权限位）、恰好
  32 raw bytes 的文件读取；拒绝 env value / YAML / CLI argv 来源。文件由部署设施（生产 systemd
  credential / dev fixture one-shot）供给。
- AEAD：AES-256-GCM（标准库实现），每次 create/rotate 使用 CSPRNG 12-byte nonce；AAD 至少覆盖
  format version、owner、credential ID、consumer、purpose、revision，因此 row/revision 换位
  一律认证失败。authentication failure 是 stored corruption：sanitized Internal，绝不 fallback
  明文，绝不自动"修复"。
- admin idempotency 的 request digest 是由 master key 经 HMAC-SHA256 派生的 versioned keyed
  digest（`workos.credential-request.v1:<hex>`），canonical 请求（含 secret bytes）在 HMAC 内部。
  数据库整体泄漏也无法离线验证 secret 猜测。
- 诚实声明：Go 不提供形式化 zeroization。实现 best-effort 覆写可控 `[]byte`、缩短生命周期，
  但 runtime/exec/string copy 的副本无法保证清零；这里的原则是缩小暴露窗口，而不是声称保证。
- master key 在线轮换、多 key ring、跨主机 Vault HA 是明确非目标（记录在任务记录的未决风险）。

### 3. Raw secret 的唯一允许路径

```text
operator-owned 0600 文件 / 显式 stdin
  → workosctl 进程内存
  → Core credential admin Unix socket（0600，非 TCP、非 Gateway）
  → Credential application（bounded 校验、keyed digest、AEAD seal）
  → Credential-owned ciphertext row

active harness worker over mutually authenticated private mTLS channel
  → AcquireTaskCredential(task_lease_id, worker_id 仅此两项)
  → Core 在一个受控事务内从 active task lease 派生 task/provider/owner/snapshot
  → 精确验证 vault 中该 credential 仍 active 且 revision 精确匹配
  → 一次 bounded in-memory lease response（secret 只出现这一次）
  → 精确 provider adapter → 该 task 的 allowlisted child env
```

任何其他路径 fail closed。特别禁止并已由结构保证：raw credential 进 Project/AgentTask/
TaskLease/event/public Catalog/public metadata；客户端、worker 或 Provider 在 Acquire 请求中
提交 owner/project/provider/ref/revision；Core 把 secret 放 outbox；harness-host 读/解密
Credential 表；以 localhost/内网为由在未认证明文 HTTP 上返回 secret；用日志脱敏、base64、hash
或 opaque ID 冒充 secret boundary。

### 4. Core 进程内的两个私有 listener（不增加第七个进程）

- **credential admin Unix socket**：只注册 `CredentialAdminService`
  （put/rotate/revoke/list），mode 0600，pre-decode body budget 16 KiB，staleness probe +
  exact-inode 回收语义与 Gateway admin socket 相同。普通 Core HTTP、Gateway、harness 私有
  listener 都不可达。缺 master key/admin socket 时 Core 其余功能照常启动，vault capability 如实
  unavailable，credential-required provider 对新 binding/run fail closed。
- **authenticated private harness execution listener**：mutual TLS 1.3，只注册
  `TaskExecutionService` + `CredentialLeaseService`。Core 与 harness-host 各持独立 leaf
  identity，由一条显式 private CA 签发；双端校验 exact URI SAN
  （`urn:workos:core` / `urn:workos:harness-host`），禁止 InsecureSkipVerify、任意 CA、CN-only。
  CA private key 不进入任何常驻进程。普通 Core HTTP mux 不再注册 TaskExecution——旧直连路径与
  全部 Credential RPC 对 Gateway 都是确定性 404。TaskExecution 从"无认证 loopback HTTP"迁出是
  本 ADR 的前提：这个通道现在要承载 raw secret，必须先有真实的服务身份。
- 这不是完整 service mesh 或 Workload Identity：它只证明 Core↔harness execution 的进程身份。
  其余进程间通道维持现状，由后续 ADR 处理。
- 开发/CI：dev-fixture one-shot 在共享 runtime volume 内生成 ephemeral CA/leaf + dev master key
  （UID 匹配容器用户），全程无手工粘贴；生产由 systemd credential/file provisioning 提供。

### 5. Task credential snapshot 与 short lease 状态机

- fresh user task、App allow task、waiting-approval task 都在任何 queue/outbox/reservation
  之前解析 exact `(credential_id, revision)`，与 provider snapshot 一起持久化（agent-owned
  `agent_task_credentials`，无跨模块 FK）。provider 不需要 credential 时 snapshot absent。
  required/absent、wrong owner/provider、revoked/corrupt 全部零副作用 fail closed。
- idempotency replay 返回第一次 task/snapshot，不因 rotate/revoke/rebind 偷换 credential。
  approval decide 时重验 snapshot 仍 active/exact，不一致保持 pending（FailedPrecondition）。
- `AcquireTaskCredential` 在一个受控事务内：Agent tx-scoped authority 按
  `(task_lease_id, worker_id)` 锁定并验证 active execution lease，返回 task 的 durable
  snapshot；Credential tx-scoped store 验证该 credential 仍 active 且 revision 精确，再以
  `task_lease_id` 唯一索引物理仲裁插入 short lease。expiry 不晚于 task lease 当前 expiry
  （跟随 30s execution lease）。response-loss replay 返回同一 lease 行与同一 credential
  revision（secret 在 lease 存续内可再次交付给同一 worker——否则第一次响应丢失后 worker 永远
  无法拿 secret，协议死锁）；换 worker/换 revision/非 active 一律 ErrLeaseLost。
- Renew 只延长到新的 active task lease expiry，永不再返回 secret；同时重验 credential revision
  精确匹配。rotate/revoke 后下一次 heartbeat（≤10s 有界间隔）收到 `valid=false`，worker cancel
  run 并 kill provider child。Release 幂等。Core/credential store unavailable 时 renew 出错
  → worker 同样 cancel（fail closed，child 不在无 lease 下继续）。expired leases 由有界 sweep
  - 读路径双重收敛。
- worker 不跨 task 缓存 plaintext；defer release；DeepSeek adapter 只接受 consumer/purpose
  匹配、未过期的 neutral lease，在构造该 task 的 child env 后 best-effort 缩短引用生命周期。
  Fake/Generic 不申请 lease（TaskLease.requires_task_credential 为 false），Generic 对非预期
  credential 附加 fail closed。

### 6. DeepSeek 移除长期环境凭据

- `Config.APIKey` 与 `DEEPSEEK_API_KEY` 正常读取路径删除。若 legacy env 仍被设置，只产生
  sanitized configuration issue 指引迁移到 Vault，绝不静默继续使用，也不显示值/长度/片段。
- provider startup health 只描述 non-secret runtime/config；owner credential readiness 由 Core
  Catalog 的 owner-aware projection 决定：adapter healthy 但 owner 缺 active credential 时，
  该 provider 对该 owner 投影为 unavailable，reason 固定安全文案，不泄漏 foreign credential
  的存在/ID/revision/key 状态。
- "短期 lease"只限制 WorkOS 内的暴露窗口；第三方长期 API key 在 Provider 侧仍是长期凭据，不把
  它虚构成 Provider 端短期 token。第一版单 owner（admin RPC 不携带 owner，Core 使用部署配置的
  owner）；未来多 owner 不能复用全局 provider health，catalog projection 需按 owner 维度缓存
  与失效（记录为后续风险）。

## 错误矩阵（本模块）

| 场景                                        | verdict                                  |
| ------------------------------------------- | ---------------------------------------- |
| malformed/bounds/未知 purpose               | InvalidArgument（零副作用）              |
| unknown/foreign credential                  | NotFound（不可区分）                     |
| stale expected revision / revoked rotate    | Aborted                                  |
| active credential 已存在（不同 key 的 put） | FailedPrecondition（AlreadyExists 语义） |
| key consumed by different canonical request | Aborted                                  |
| lost/expired/released/foreign lease         | FailedPrecondition / invalid verdict     |
| transient store                             | Unavailable                              |
| ciphertext/metadata corruption              | Internal（fail closed）                  |

## 后果与既定非目标

- 实现：`internal/core/credential/*`、`internal/platform/privatetls`、agent snapshot 表与
  tx-scoped authority、orchestration lease issuer/catalog overlay/binder 注入、Core 双私有
  listener、workosctl credential 命令、DeepSeek lease-only 运行。
- 非目标（保持 unavailable，不得顺手实现）：OAuth/refresh token/Codex login/GitHub/cloud
  credential；公共/浏览器 credential 编辑 UI；secret reveal/export/recovery；master key 在线
  轮换与多 key ring；HSM/TPM/remote KMS；多 owner catalog overlay；App 访问 credential
  metadata。
