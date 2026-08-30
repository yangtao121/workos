# Credential Vault（internal/core/credential）

Core 内的 Credential Vault 是长期 provider credential 的唯一 durable authority
（ADR-0009）。模块分层为 `domain → application → ports ← adapters`：

- `domain`：metadata 投影（绝无 secret material）、bounded grammar（secret 1–8192 bytes、
  consumer `[-a-z0-9._]{1,128}`、purpose `provider-api-key.v1`、label ≤80 code points）、
  净化 sentinel 错误。
- `application`：canonical request digest（keyed HMAC，覆盖 secret bytes）、幂等
  replay/conflict 协议、快照重验（`AsSnapshotVerifier`）、过期 lease sweep。
- `ports`：`Repository`（admin 写 + tx-scoped lease 存储）、`Cipher`（seal/open/keyed
  digest）、grant/verdict 投影。
- `adapters/cipher`：AES-256-GCM + master key 文件加载（绝对路径、非 symlink、owner-only、
  恰好 32 raw bytes）+ HMAC-SHA256 派生。认证失败 = `domain.ErrCorrupt`。
- `adapters/postgres`：`023` 迁移的三个表；admin 写以 request mapping 主键做物理仲裁
  （loser 锁内重读，same digest replay / different digest `Aborted`，失败不消费 key）。
- `transport`：`CredentialAdminService`（仅 admin Unix socket，0600，16 KiB pre-decode
  budget）与 `CredentialLeaseService`（仅 Core 私有 mTLS execution listener）。

## 边界

- lease 的签发/续期由 `internal/core/orchestration.CredentialLeaseIssuer` 在单事务内协调：
  Agent 模块 tx-scoped authority 从 active task lease 派生全部事实，Credential store 只验证
  exact revision 并物理仲裁 lease 行；secret 只在 Acquire 响应中出现一次（lease 存续内的
  response-loss replay 允许同一 worker 再次获取，否则协议死锁）。
- 本模块不 import Agent/Project/Harness 任何 package；跨模块只经由 orchestration 的窄接口。
- plaintext、master key、child env 绝不入库/日志/event/配置 dump。Go 无形式化 zeroization；
  实现做 best-effort 覆写并如实记录该限制。

## 状态

working，但范围克制：仅 provider API-key encrypted store + 本地 admin socket + task-bound
lease。OAuth/Codex login/GitHub/cloud credential、公共编辑 UI、secret reveal/export、master
key 在线轮换、多 owner overlay 均 unavailable（ADR-0009 非目标）。
