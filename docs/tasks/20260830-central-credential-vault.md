# Task: Central Credential Vault 与 task-bound credential lease（阶段 A）

- 状态：active
- Owner/Agent：WorkOS 实现智能体
- 进程/模块：workos-core（Credential Vault + 双私有 listener）、harness-host（lease 客户端 +
  DeepSeek lease-only）、workosctl（credential 命令）
- 依赖：ADR-0008 的 lease-bound materialization 模式与 ADR-0005 的 task lease 事实

## 目标与范围

按 `docs/prompts/20260830-next-agent-credential-context-deepseek-batch.md` 阶段 A：

1. 长期 provider credential 由 Core Credential Vault 加密持有（AES-256-GCM + master key 文件），
   Project/App/普通 Core API 只接触 opaque reference。
2. `workosctl credential put|rotate|revoke|list` 经 Core credential admin Unix socket（0600，
   16 KiB pre-decode budget）操作；secret 只从 stdin 或 owner-only 文件读取。
3. harness 执行面迁移到 mutual TLS 1.3 私有 listener（TaskExecution + CredentialLease，URI SAN
   精确身份），普通 HTTP mux 不再注册 TaskExecution，Gateway 确定性 404。
4. fresh/App/waiting-approval task 在入队前解析 exact credential snapshot；approval decide 时
   重验；`AcquireTaskCredential` 完全从 active task lease + worker 派生；renew/release/
   revoke/rotate/expire 状态机收敛。
5. DeepSeek 删除 `DEEPSEEK_API_KEY` 读取路径，legacy env 只产生 sanitized migration issue；
   只在有效 lease 下启动 child；Catalog owner-aware projection 对缺 credential 的 owner
   投影 unavailable。

不包含：OAuth/Codex/GitHub/cloud credential、公共 UI、secret reveal/export、master key 在线
轮换、多 owner overlay（见 ADR-0009 非目标）。阶段 A 不改变任何可见 UI（HarnessSettings 的
能力列表渲染不含新布尔能力，像素不变）。

## 协议/数据影响

- 新 proto `workos.credential.v1`（CredentialMetadata、CredentialAdminService、
  CredentialLeaseService）；`HarnessCapabilities.requires_task_credential_lease = 17`；
  `TaskLease.requires_task_credential = 5`。全部 additive。
- migration `023_credential_vault.sql`（Credential-owned）与
  `024_agent_task_credential_snapshot.sql`（Agent-owned，FK 只指向 agent_tasks）。
- Core SystemService 新增 `credential-vault` capability（configured 时 available）。

## 验收

- [x] 行为测试：cipher（roundtrip/tamper/AAD/wrong-key/nonce 唯一性/key file grammar）、domain
      grammar、catalog owner overlay、binder credential_ref、task router snapshot fail-closed、
      gateway 404、真实 PostgreSQL（lifecycle/idempotency/plaintext 缺席/lease 状态机）、
      mTLS 三方拒绝、stack 三阶段门禁
- [x] `make check`
- [x] 文档与 `docs/status.json`

## 执行记录

- 基线（批次开始，本地 main `aa560bb`）：工作树干净（仅新增本批次 prompt 文件）；`make
bootstrap`、`make check` 全绿。
- 门禁（全部真实执行，记录于仓库日志与本次执行）：
  - `make check`：PASS（gofmt/go vet/go test、buf format/lint/vet、TS 架构/eslint/prettier、
    desktop build、README 状态一致性）。
  - `make test-integration`：PASS（83 个 integration 断言组 + restart seed/verify 全套）。
  - `make test-credential-vault`：PASS（五段：missing fail-closed → workosctl put 经真实
    admin socket → granted 全链路 lease-bound DeepSeek 任务 → core+harness restart 持久化 →
    revoke 后 fail closed；含 3 个进程内协议测试对真实 PostgreSQL）。
  - `make test-deepseek-fixture`：PASS（官方 runtime + loopback fixture + 浏览器 E2E）。
  - `make test-artifact-review`：PASS（真实跨进程 Chromium 门禁）。
  - `make test-lan-pairing`：PASS（production-auth TLS + pairing 全流程）。
  - `make test-e2e`：PASS（7 passed / 6 skipped——skipped 为既有 opt-in 用例）。
  - `go test -race ./internal/core/credential/... ./internal/core/agent/... ./internal/core/artifact/...
./internal/core/orchestration/... ./internal/core/harnesscatalog/... ./internal/harness/...`：全绿。
  - `buf lint`：通过；`buf breaking api/proto --against ".git#branch=main,ref=aa560bb"`：无破坏。
  - secret 扫描：staged diff 无 PEM/bearer/provider key/raw context；`tmp/` 下临时证书与
    master key 未入库（.gitignore 覆盖）。
- UI：本阶段不涉及可见 UI（新 capability 未渲染，像素不变；无需 before/after/current）。
- commit：`87a0621`，分支 `feat/central-credential-vault`。

## 交接

- 已验证命令如上；stack 依赖 dev 一次性 `workos-dev-fixture`（DEV-only）在共享 runtime volume
  内生成执行通道身份与 dev master key；生产必须改用 systemd credential/file provisioning，
  `workos-dev-fixture` 不得在生产运行。
- 未决风险（诚实记录）：master key 在线轮换/多 key ring/跨主机 HA 未做；多 owner catalog
  overlay 未做（admin 为单 owner）；Go 无形式化 zeroization，secret 以暴露窗口最小化而非保证
  清零；mTLS 只覆盖 Core↔harness execution，不是全系统 mesh。
- 下一步：阶段 B（review artifact 作为 agent context）基于本阶段完成提交 stacked。
