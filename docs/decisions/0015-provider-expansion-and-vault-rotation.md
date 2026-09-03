# ADR-0015：Provider 接入扩展（Codex/MCP）与 Credential Vault 种类、在线轮换与受审计揭示

- 状态：Accepted
- 日期：2026-09-03
- 关系：局部取代 ADR-0009 §2 的"master key 在线轮换是明确非目标"裁决（其余 ADR-0009 边界
  全部不变）；沿用 ADR-0005 的 lease/预算契约、ADR-0008/0011 的 artifact 纪律、ADR-0013 的
  capability 诚实声明模式。为剩余能力总攻 W1 提供实现边界。

## 背景

当前 harness 只有 Fake、DeepSeek（HTTP、credential lease）与 Generic CLI（stdio envelope）三个
Provider。`docs/structure.md` §5-7 要求 Codex App Server 与 MCP Server 作为真实可绑定的
Provider；Credential Vault 只支持 `provider-api-key.v1` 单一 purpose，没有 master-key 在线
轮换、没有揭示/导出路径、没有管理审计事实。本 ADR 固定这四件事的边界。

## 决策

### 1. Codex Adapter：versioned fixture 优先，能力逐项如实

- Provider 类型（Codex App Server 协议细节）只出现在
  `internal/harness/adapters/codex`。Core 只认识 canonical protocol 与
  `HarnessProviderInfo`；consumer id 固定 `codex`。
- 契约是 versioned 的本地 stdio JSON-RPC 2.0（`workos.codex.app-server/v1`）：initialize →
  session/run → 流式 canonical 事件映射 → terminal。生产实现与测试共用同一协议；测试使用
  `cmd/codex-app-server-fixture`（版本化、确定性、无网络）。
- 凭据：`RequiresTaskCredentialLease=true`，purpose 固定 `codex-auth.v1`（API-key 形态）。
  真实 OAuth 流程（client secret）属于停止条件，本批不实现。
- 能力声明（有测试证据后才为 true）：`Streaming=true`（fixture 增量事件）、
  `UsageReporting=true`（fixture 末尾 usage）、`HardTokenBudget=true` +
  `HardRuntimeDeadline=true`（fixture 强制 max_tokens 截断与进程 deadline，测试证明）、
  声明 enforced maxima；`StructuredArtifacts=false`、context refs 为空——请求即
  fail closed（沿用 Generic CLI 的降级语义）。

### 2. MCP Adapter：stdio 子集，降级路径与 Generic CLI 对齐

- `internal/harness/adapters/mcp` 把一个本地 stdio MCP server（versioned fixture
  `cmd/mcp-server-fixture`：initialize / tools/list / tools/call）作为一个 Provider。
- 能力如实：无 streaming、无 usage、无 hard budget（其上的 App run 在 Task Router 处
  FailedPrecondition）、无 structured artifacts、无 context、无 credential lease。任务 =
  goal 作为 tool input，tool 输出聚合为 AssistantMessage；stdout/stderr 有界、净化。
- 恶意/超时/无响应 fixture：有界超时 + 有界输出 + 确定性终态，不无限重试。

### 3. Credential kinds：finite purpose 词汇表

purpose 即凭据种类（finite enum，不存自由字符串类型），owner 维度沿用每行
`owner_user_id` 严格隔离（两 owner 同 consumer/purpose 互不可见、互不影响）：

| purpose（种类）       | secret 语法（boundary 校验）                                                                   | 首个 consumer           |
| --------------------- | ---------------------------------------------------------------------------------------------- | ----------------------- |
| `provider-api-key.v1` | 1..8192 bytes，拒 NUL/CR/LF（不变）                                                            | deepseek                |
| `codex-auth.v1`       | 同上字节规则（API-key 形态）                                                                   | codex                   |
| `github-token.v1`     | 可见 ASCII token，20..255 bytes，无空白/控制字符                                               | github                  |
| `cloud-credential.v1` | ≤8192 bytes 的有效 UTF-8 JSON object，仅一层 string 字段（≤32 个键、每值 ≤4096 bytes、无 NUL） | 任意 canonical consumer |

- lease 契约不变：`(owner, consumer, purpose)` 至多一个 active；adapter 声明要求的
  purpose，Acquire 按任务 snapshot 的 purpose 精确交付。种类不匹配 = fail closed。
- 无第四种"通用"用途；新增种类必须改本 ADR + domain 校验 + migration CHECK。

### 4. master-key 在线轮换：单事务原子重加密，崩溃安全

- `workosctl credential rotate-master-key --new-key-file <abs path>`（admin socket）。
  新 key 文件与 ADR-0009 master key 文件同一物理语法（绝对路径、非 symlink、owner-only、
  恰 32 raw bytes）；Core 进程内加载，不落盘、不进日志。
- 每条 credential 增加 `key_epoch`（migration 032，回填 1）；singleton
  `credential_vault_state.current_epoch` 是权威 epoch。派生 key 的 domain-separation
  label 混入 epoch，因此不同 epoch 的 AEAD/digest key 互不冲突。
- 轮换 = 一个 PostgreSQL 事务：锁全部 credential 行 → 逐条用当前 epoch key 验证打开
  （任何一条 active 行失败整体回滚，绝不半加密）→ 用新 epoch key 重密封并写回
  `nonce/ciphertext/key_epoch` → 推进 singleton epoch → 写审计行。
- revoked 行冻结不轮换：吊销凭证永远不再被任何存活路径解密（reveal/lease/snapshot
  全部拒绝 revoked），重封它们只会让一次历史 key 损失永久阻塞轮换。revoked 行保留
  原 `key_epoch` 与原密文，成为只读历史事实。
- 崩溃/重启安全：事务原子，中断后旧 epoch 事实完整，重试收敛。响应丢失后重试同
  key 文件：派生 key 与当前 epoch key 相同 → 确定性 no-op 成功（幂等收敛）。
  轮换成功后旧 master 文件由部署设施退役；Core 不保存旧 epoch key（除轮换事务
  进行中的双 key 窗口）。
- 轮换不是 owner-scoped 客户端写，不走 idempotency-key 协议；审计记录每次尝试与结果。

### 5. 受审计揭示/导出：仅 admin socket，逐次落审计

- additive admin RPC `RevealCredential(credential_id, expected_revision)`：socket 侧已鉴权
  （既有 0600 Unix socket + 16 KiB pre-decode 预算）；同事务写审计行后再返回 secret；
  secret 只出现在该响应。Gateway/public 路由确定性 404，审计不含 secret bytes。
- `workosctl credential reveal --credential <id> [--expected-revision N]` 输出到 stdout
  （`--output <file>` 由 workosctl 客户端写文件）；进程日志零 secret。
- 审计事实：`credential_admin_audit`（owner：workos-core Credential Vault）：每次
  put/rotate/revoke/reveal/rotate-master-key 一行（action、credential、owner、revision、
  epoch、occurred_at、结果），append-only，不提供删除/查询 secret 的任何路径。
- 揭示的预期用例是 operator 恢复/迁移；App、浏览器、worker 永远没有揭示能力
  （worker 只经 mTLS task lease）。

### 6. 数据与契约

- migration `032_credential_vault_expansion.sql`（owner：workos-core Credential Vault，
  forward-only）：purpose CHECK 扩展为 finite 集合、`key_epoch` 列、
  `credential_vault_state`、`credential_admin_audit`。
- migration `033_agent_task_credential_purpose.sql`（owner：workos-core Agent）： admission
  快照 `agent_task_credentials.purpose` 回填 `provider-api-key.v1` 并钉住 finite CHECK，
  使 lease 派生按快照种类打开凭据。
- migration `034_task_credential_lease_purpose_vocabulary.sql`（owner：workos-core
  Credential Vault）：`task_credential_leases.purpose` CHECK 与 032 对齐。
- proto additive：`CredentialAdminService.RevealCredential` / `RotateMasterKey`；
  `HarnessCapabilities.required_credential_purpose`（lease-requiring adapter 必须声明
  其 finite kind，空声明 = capability corruption → provider 不可选）；
  `CredentialMetadata` 不变（零 secret 不变）。无新表 owner 之外的跨模块访问。

## 后果

- ADR-0009 的"在线轮换非目标"由此取代；"多 key ring/跨主机 HA"仍是非目标。
- Codex/MCP 的 capability 升级（structured artifacts、context）必须先有 fixture 证据，
  在新 ADR 下声明，不得顺手翻转。
- 状态纪律：新 Provider 在 `test-codex-harness` / `test-mcp-harness` 全链路证据前保持
  fixture 级描述；vault 扩展在 `test-credential-vault-expansion` 证据前不升级 status。
