# 下一位智能体 Prompt：Credential Vault、Artifact Context 与 DeepSeek Structured Review 批次

> 将本文件完整交给下一位实现智能体。目标是按依赖顺序直接完成三个纵向任务，不是只输出计划。
> 用户明确允许一次完成多个任务，并要求每个阶段完成后立即创建聚焦 Git 提交。

## 你的角色与最终结果

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。Project Agent 的 Markdown / Unified
Diff Artifact review 已经合入本地 `main`。你的任务是连续完成三个彼此依赖、但必须独立记录和提交的
阶段：

1. **Central Credential Vault + task-bound credential lease**：长期 Provider credential 由 Core 的
   Credential Vault 加密持有；Project/App/普通 Core API 只接触 opaque reference；harness-host 只能在
   已认证的 active task lease 下取得短期、可续期、可撤销的内存 lease；DeepSeek 不再读取
   `DEEPSEEK_API_KEY`。
2. **Review Artifact as Agent Context**：Desktop 可把当前 Project 的 immutable Markdown / Unified Diff
   Artifact 选为 Agent context；Core 在入队前校验 owner/project/digest/provider capability，在执行时只按
   active task lease 解析 canonical bounded content；Provider 不能自行读取 Artifact 或选择 scope。
3. **DeepSeek Structured Review Output**：DeepSeek adapter 在已有官方 runtime + 本地 API fixture 链路上，
   消费受控 Artifact context，并把严格解析、完整校验的 Markdown / Unified Diff 输出通过 Core-owned
   Artifact materialization 协议原子发布；不把模型生成的伪引用当成事实。

最终链路必须闭合：

```text
local operator
  → workosctl credential put/rotate/revoke over Core-owned admin Unix socket
  → Core Credential Vault encrypts long-lived material at rest
  → owner selects DeepSeek through the existing public Harness binding command
  → Core injects an owner/provider-bound opaque credential_ref
  → SubmitTask snapshots provider + credential id/revision with zero secret bytes
  → harness-host claims the task over an authenticated encrypted private channel
  → AcquireTaskCredential derives credential solely from active task lease + worker identity
  → short-lived credential lease supplies the DeepSeek adapter in memory

current Project review Artifact
  → “Use as Agent context”
  → ContextRef(type + artifact UUIDv7 + exact digest)
  → Core verifies immutable owner/project artifact and provider capability before enqueue
  → private ResolveTaskContext derives refs from the active task lease
  → canonical bounded context documents reach the selected Provider only

DeepSeek official runtime + local deterministic API fixture
  → strict structured response envelope
  → adapter validates exact requested output set and derives stable output keys/titles
  → private atomic artifact batch materialization
  → Core mints Artifact IDs/digests/times and durable ArtifactCreated events
  → Desktop timeline / Artifact Center opens inert Markdown or diff content
  → process and PostgreSQL restart preserve snapshots, leases, context refs and artifacts
```

持续推进到三个阶段各自的实现、生成物、测试、文档、状态、任务记录和 Git 提交均完成。不得 merge 到
`main` 或 push，除非用户在执行时另行明确授权。不得因为后续阶段失败而修改、压扁或删除已经完成阶段的
提交。

只有遇到以下情况才停止并留下完整仓库证据与可选方案：必须破坏已有 v1 字段/编号、修改已执行
migration、增加第七个常驻进程、让 App/Gateway/公开 Core API 获得 raw credential、让 Provider 直接查
Core SQL、无法为 secret-bearing RPC 建立已认证加密通道、必须降低现有 Gateway/Surface/Artifact 隔离，
或环境无法运行本批次要求的 PostgreSQL + Core + harness-host + Gateway + Chromium + 本地 DeepSeek
fixture。单纯实现困难、测试耗时或代码量较大不是停止理由。

## 阶段、分支与提交纪律

本文件是一个顺序批次，不是允许多个智能体同时改同一 Proto/migration 的许可。严格按以下顺序执行：

| 阶段 | 独立任务记录                                        | 建议 branch/worktree              | 阶段完成提交                                             |
| ---- | --------------------------------------------------- | --------------------------------- | -------------------------------------------------------- |
| A    | `docs/tasks/20260830-central-credential-vault.md`   | `feat/central-credential-vault`   | `feat: add central provider credential vault and leases` |
| B    | `docs/tasks/20260830-artifact-agent-context.md`     | `feat/artifact-agent-context`     | `feat: resolve review artifacts as agent context`        |
| C    | `docs/tasks/20260830-deepseek-structured-review.md` | `feat/deepseek-structured-review` | `feat: add DeepSeek structured review outputs`           |

执行规则：

1. 每个阶段只认领一个任务记录，并在自己的 branch/worktree 中完成；不要让两个工作树同时改同一个
   Proto package、migration 或生成物。
2. A 从执行时的本地 `main` 创建。B 必须基于 A 的完成提交建立 stacked branch；C 必须基于 B 的完成
   提交建立。未经用户授权不把它们 merge 回 `main`。
3. 一个阶段的协议、实现、测试、模块文档、task、`docs/status.json`、生成 README 状态与必要 UI 证据
   必须一起闭合后，才可标记 done 并提交。禁止先提交虚假的 working 状态。
4. 每个阶段提交后，把 commit hash、完整验证命令、真实结果和未决风险写回该阶段任务记录；如这一步
   导致任务记录变化，再用同阶段的一个小型 `docs:` 收尾提交，不得 amend 已交接的实现提交。
5. 阶段内可先做独立 contract commit 以便审查，但阶段结束时仍必须有明确的 phase-complete 提交；不得
   把三个阶段 squash 成一个提交。
6. 每阶段开始前重新执行 `git status --short --branch`、`git log --oneline --decorate -15`、
   `git diff --check`，确认 stacked base 正确且没有覆盖用户或其他智能体改动。
7. 若某阶段未达完成定义，保持 task 为 active/blocked 并提交诚实证据（若适合），不得开始依赖它的
   后续阶段，也不得把状态写成 working。

## 为什么现在做这三个阶段

`docs/structure.md` 的第一版优先级中，Project、Desktop、Harness、DeepSeek、Web Surface、App Agent API、
LAN pairing 与 Artifact review 已有真实证据；第 9 项 **Central Credential Vault** 仍没有实现。当前代码的
真实缺口是：

- harness-host 从长期进程环境读取 `DEEPSEEK_API_KEY`，并把它复制到每个官方 runtime 子进程环境；
- `HarnessBinding.credential_ref` 已存在，但 public binding orchestration 始终注入空值；
- 没有 credential create/rotate/revoke、加密持久化、task snapshot 或短期 lease；
- `ContextRef` 已进入 canonical AgentTaskInput，但 Core 没有语义验证或 materialization，DeepSeek 明确
  拒绝所有 `context_refs`；
- review Artifact 已有 immutable content、digest、owner/project/task provenance 和安全 Desktop viewer，
  正好可以成为第一种真实 context source；
- DeepSeek 仍诚实报告 structured artifacts unsupported，因而不能完成“读一份项目 Artifact，再生成
  Markdown/Diff 审阅结果”的真实链路。

这三个阶段共享同一条 Task lease、provider capability 和 Artifact trust boundary，按顺序完成能避免
重复协议。它们不依赖 rootless Podman/cgroup acceptance host，也不应顺手扩张到尚无真实 runner 证据的
Reliability/Repair。

## 当前仓库事实（执行时必须重新核对）

- 本 Prompt 编写时本地 `main` 为 `7f6efa5`，领先 `origin/main` 5 个提交，工作树在新增本文件前干净。
  执行时以本地真实历史为准；不得 reset 到落后的 remote、丢弃本地 merge 或覆盖用户改动。
- 六个常驻进程固定为 `workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- `docs/status.json`：Project、Harness Catalog/Broker、Agent Task Router、DeepSeek、Desktop、Artifact、LAN
  pairing 为 working；Runtime container slice 与 Reliability 仍因无真实 rootless Podman 链路而受限；
  Indexer scaffolded、Mobile contract-only。
- `docs/architecture/implementation.md` 当前写明 workos-core“不拥有 credential”，这是现状而不是绕过
  `docs/structure.md` Credential Vault 目标的理由。阶段 A 必须用 ADR 明确更新所有权：长期 credential
  归 Core Credential Vault；harness-host 只持有 task-bound 短期 lease，不拥有 durable secret fact。
- `api/proto/workos/project/v1/project.proto` 已有 `HarnessBinding.credential_ref = 4`；
  `SetProjectHarnessBindingRequest` 不允许客户端提交 credential ref，必须继续由服务端派生。
- `api/proto/workos/agent/v1/agent.proto` 已有字符串型 `ContextRef { type, id, revision }`；本批次为现有
  字段定义第一种严格语义，不另造同义 DTO。
- `TaskExecutionService` 当前在 Core 普通 loopback HTTP mux 上提供 Claim/Renew/Append/Finish；Gateway
  虽然不转发它，但该通道没有服务身份认证，不能直接承载 raw credential。
- `internal/harness/adapters/deepseek` 当前把 API key 放在 adapter config 和 child env；它只支持 goal、
  `general` role、streaming text、usage、token/runtime hard budget，拒绝 context 与 structured artifact。
- Artifact review 的 `document.markdown.v1` / `code.unified-diff.v1`、private
  `AppendTaskArtifact`、Core-minted ArtifactCreated、typed public read、safe viewer 均已完成；历史语义由
  ADR-0008 固定。
- App Bridge 仍只允许 `role + goal`，不得借本批次让 iframe 提交 context refs、选择 provider 或读取
  credential。Artifact context 首版只供可信 Desktop 的 owner Project task。
- migrations `001`–`022` 已执行并被 checksum/forward tests 钉住，禁止修改。预计阶段 A 从 `023`
  顺延；若执行时编号已占用，使用下一个空闲编号，绝不复用。
- `gen/`、`src/gen/` 与 README 状态表均由工具生成，禁止手改。
- 验收 PostgreSQL volume 可能含现有用户数据。禁止 `docker compose down -v`、TRUNCATE、broad DELETE、
  wildcard DROP 或通过清库让测试通过。

## 最高优先级安全与隐私边界

### 真实凭据一律禁用

- 本批次的所有自动化测试只使用明显虚构、由 fixture 进程接受的合成 credential；不得访问真实
  DeepSeek/OpenAI/Codex/GitHub/Anthropic 或任何收费外部 API。
- 聊天、日志或历史上下文中曾出现的任何真实 Provider key 都应视为已经泄露。禁止复制、保存、验证、
  调用、写入任务记录或放进测试；操作者应在对应 Provider 控制台另行吊销并生成新值。
- 不得搜索 shell history、用户环境、home、credential store 或私有文件来“找一个可用 key”。
- secret 不得出现在 argv、YAML、Compose source、systemd env example、Git object、Proto text dump、URL、
  query、event/outbox、trace、metrics、panic、SQL error、日志、截图、DOM、Playwright artifact 或任务记录。

### Raw credential 的唯一允许路径

```text
operator-owned 0600 input file / explicit stdin
  → workosctl process memory
  → Core admin Unix socket (not TCP, not Gateway)
  → Credential application
  → authenticated encryption
  → Credential-owned ciphertext row

active harness worker over mutually authenticated private channel
  → AcquireTaskCredential(task_lease_id, worker_id only)
  → Core derives task/provider/owner/credential snapshot
  → one bounded in-memory lease response
  → exact provider adapter
  → allowlisted child process environment for that task only
```

任何其他路径都必须 fail closed。特别禁止：

- raw credential 进入 Project/AgentTask/TaskLease/context/event/public Catalog/public Credential metadata；
- 客户端、worker 或 Provider 在 Acquire 请求中提交 owner/project/provider/credential ref/revision；
- Core 把 secret 放入 outbox 或让 harness-host 从数据库读取/解密 Credential 表；
- 以“localhost/private Docker network”为理由在未认证明文 HTTP 上返回 secret；
- 用日志脱敏、base64、hash、Kubernetes Secret 名称或 opaque ID 冒充真正的 secret boundary；
- 声称 Go 内存可以被绝对清零。实现应使用 `[]byte`、缩短生命周期并 best-effort 覆写可控 buffer，但文档
  必须承认 runtime/exec/string copy 无法提供形式化 zeroization。

### Artifact context 与模型输出

- Artifact content 是不可信用户/模型内容，不是 system instruction。Adapter 必须以版本化结构和明确
  `untrusted_context` 语义编码，不能用可碰撞的自由文本 delimiter 把它提升为 trusted prompt。
- 不得把 resolved context bytes 放进 task row、outbox、event、日志或错误。Task 只持久化 immutable ref。
- 模型返回的 JSON/Markdown/Diff 都是不可信 bytes；必须先做 wire/aggregate bound，再严格解析和 domain
  校验，最后才能调用 Artifact sink。
- 模型无权选择 owner、project、task、artifact ID、digest、time、event sequence、credential 或数据库
  状态；这些事实继续由 Core 从 active lease 派生。

## 开始整个批次前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`deploy/README.md`、
     `docs/ui/README.md`；
   - `docs/structure.md` 的 0、1.3、2–4、6–7、10–11、14–19，尤其 7.3 与第一版第 9 项；
   - `docs/architecture/implementation.md`、`docs/status.json`；
   - ADR `0001`–`0008`，尤其 task lease/budget、Gateway session 与 Artifact materialization；
   - `docs/tasks/20260823-deepseek-harness.md`、
     `docs/tasks/20260823-harness-catalog-binding-ux.md`、
     `docs/tasks/20260829-app-agent-approval-budget-policy.md`、
     `docs/tasks/20260830-lan-device-pairing.md`、
     `docs/tasks/20260830-project-artifact-review.md`；
   - Project binding、Task Router、Agent repository/approval、TaskExecution transport/materializer；
   - Harness catalog/broker/worker、Fake/Generic/DeepSeek adapters 与官方 runtime fixture；
   - Artifact domain/application/PostgreSQL/typed read、Desktop Artifact Center/Viewer/Agent Center；
   - Gateway public allowlist、identity header cleaning、private-service 404 tests；
   - config/httpserver/database/dbtx/migration/checksum/sqlc/UUIDv7/error mapping；
   - `cmd/{workos-core,harness-host,workosctl}`、Compose、Dockerfile、systemd template/env examples、Makefile。

2. 运行并记录整批基线：

   ```sh
   git status --short --branch
   git log --oneline --decorate -15
   git branch -vv
   git diff --check
   make bootstrap
   make check
   make test-integration
   make test-deepseek-fixture
   make test-artifact-review
   make test-lan-pairing
   make test-e2e
   ```

   基线失败必须判断归属并写入阶段 A 任务记录。禁止删数据、弱化断言、跳过测试或固定成功响应绕过。

3. 只读检查当前私有监听、证书支持、systemd/Compose 用户与文件权限。不要假设 loopback、同 UID 或
   container name 等于服务身份；在 ADR 中写清实际 threat model。

4. 查看 `docs/ui/desktop-web/current/`。阶段 A 不改变像素时在任务记录中明确写“不涉及可见 UI”；阶段 B
   开始前必须建立对应 `before/`，不能拿 B 完成后的截图伪装 before。

## 必须保持分离的事实

| 事实                                            | 唯一 owner                              | 说明                                                                  |
| ----------------------------------------------- | --------------------------------------- | --------------------------------------------------------------------- |
| credential plaintext/ciphertext/revision/status | workos-core Credential Vault            | secret material 的唯一 durable authority                              |
| encryption master key                           | host system secret/file facility        | 只挂给 Core；不进 DB/env/repo/harness-host                            |
| provider runtime/config/capability              | harness-host adapter                    | Provider-specific 代码只在 adapter                                    |
| Project provider + credential ref               | workos-core Project                     | ref 是服务端派生、非 bearer 的绑定事实                                |
| task provider + credential revision snapshot    | workos-core Agent                       | fresh task/approval 的 durable execution snapshot，无 secret          |
| task execution lease                            | workos-core Agent                       | worker 不能自报 scope；Acquire 从这里派生                             |
| short credential lease                          | workos-core Credential Vault            | 绑定 task lease/worker/credential revision/expiry，不持久化 plaintext |
| review Artifact metadata/content/digest         | workos-core Artifact                    | immutable context source 与 output authority                          |
| ContextRef list                                 | workos-core Agent task input            | 只存 type/id/digest；顺序与幂等 replay 固定                           |
| resolved context bytes                          | transient Core → harness execution path | 不持久化到 Agent/event，不缓存跨 task                                 |
| model output candidate                          | harness-host DeepSeek adapter           | 仍不是真实 Artifact，直到 Core materialize                            |
| ArtifactCreated timeline event                  | workos-core Agent/Artifact coordinator  | 只能引用 Core 已持久化并验证的 Artifact                               |

模块之间只通过 application/neutral ports 协作。Credential 不直接查 Agent/Project SQL；Agent/Project 不
解密 Credential row；harness-host 不连 Core schema；Artifact 不导入 Agent adapter。需要同事务协调时，
沿用 `dbtx` + transaction-scoped neutral ports 的既有模式，不通过跨模块 repository import 偷偷耦合。

# 阶段 A：Central Credential Vault 与 task-bound lease

## A1. 先写任务记录与 ADR

创建 `docs/tasks/20260830-central-credential-vault.md`，状态 active，写清范围、data owner、Proto、migration、
crypto、admin/lease transport、task/approval snapshot、rotation/revocation、错误矩阵、测试与非目标。

新增 `docs/decisions/0009-central-credential-vault-and-harness-channel.md`（若编号占用则顺延），至少固定：

- 为什么 `docs/structure.md` 的长期 Credential Vault 归 Core，而 Provider-specific use 仍归 harness adapter；
- 为什么当前 implementation 文档中的“Core 不拥有 credential”必须显式更新，而不是悄悄漂移；
- 为什么 raw secret 不进入普通 Core listener/TaskLease，为什么 Gateway 和 App 永远不可达；
- private harness execution channel 的服务身份、加密、证书生命周期与开发/生产差异；
- encryption-at-rest 算法、AAD、nonce、master-key file、credential revision 与 ciphertext corruption 语义；
- task snapshot、short lease、renew/release、Core/harness crash、response loss、rotation/revocation race；
- 为什么“短期 lease”只限制 WorkOS 内的暴露窗口，不能把第三方长期 API key 虚构成 Provider 端短期 token；
- 第一版单 owner 限制，以及未来多 owner 不能复用全局 provider health 的注意事项。

## A2. Additive canonical protocol

先修改 `api/proto`，再运行 `make generate`。所有字段使用当前下一个空闲编号；不得改名、复用或删除已有
v1 字段。

建议新增 `workos/credential/v1/credential.proto`，保持 provider-neutral：

- `CredentialMetadata`：server-minted UUIDv7、opaque `consumer_id`（当前为 canonical provider ID）、
  canonical purpose/type、revision、active/revoked 状态、UTC timestamps；绝无 secret/ciphertext/nonce/key ID；
- Core-admin-only create/rotate/revoke/list metadata RPC；secret 只存在于 create/rotate request 的 bounded
  `bytes`；所有外部写带 idempotency key，rotate/revoke 还带 expected revision；
- harness-only `AcquireTaskCredential` request **只含** task lease ID 与 worker ID；response 包含 lease ID、
  expiry 与一次 bounded secret material；
- `RenewTaskCredentialLease` / `ReleaseTaskCredentialLease` 只接受 credential lease ID + task lease ID +
  worker ID，不接受 owner/project/provider/ref/revision；renew 永不再次返回 secret；
- service 注释明确 Admin 只在 Unix socket，Lease 只在 authenticated private harness listener，均不进入
  Gateway allowlist。

在 `HarnessCapabilities` 增加一个 canonical “requires task credential lease” capability。Core 只认识这项
布尔/枚举能力，不出现 DeepSeek 专用类型。Fake/Generic 为 false，DeepSeek 为 true。Catalog domain 对未知、
矛盾或缺少配套上限的 capability fail closed。

不要把 raw credential 加到 `AgentTask`、`TaskLease`、Project、Catalog、event 或任何 public response。

## A3. Credential domain、加密 store 与 migration

建立 `internal/core/credential/{domain,application,ports,adapters,transport}`（名称与仓库约定一致即可），依赖
方向必须为 `domain → application → ports ← adapters`。

固定第一版约束（改变需写 ADR 理由）：

- credential material：1–8192 bytes，不 trim，不允许 NUL/CR/LF；只在对应 adapter 内做更具体校验；
- consumer/provider ID：trim 后 1–128 ASCII lower-case `[-a-z0-9._]`，不把厂商 enum 写进 Core；
- purpose：只开放 canonical `provider-api-key.v1`，以后 additive 扩展；
- label：可选，trim 后 ≤80 Unicode code points，valid UTF-8，无 C0/C1；
- idempotency key：1–128 bounded text；revision 从 1 严格递增；时间 UTC 微秒；ID UUIDv7；
- 每个 `(owner, consumer, purpose)` 同时最多一个 active credential；rotate 保持 logical ID、revision +1；
  revoke 不可逆且 revision +1；重新 create 得新 ID，旧 Project binding 不自动漂移；
- master key：Core 从绝对路径 regular non-symlink file 读取恰好 32 raw bytes；拒绝 group/world writable、
  非预期 owner、空/多 key；不得从 env value/YAML/CLI argv 读取 key material；
- 使用标准 AEAD（AES-256-GCM 或同等级标准库实现），每次 create/rotate 使用 CSPRNG unique nonce；AAD 至少
  覆盖 format version、owner、credential ID、consumer、purpose、revision；authentication failure 是 stored
  corruption，返回 sanitized Internal，绝不 fallback 明文；
- idempotency request digest 必须是由 master key 派生的 versioned keyed digest，避免数据库泄露后可离线
  验证猜测 secret；首次 metadata response snapshot 持久化，same key/same request 精确 replay，different
  request 稳定 Aborted，失败事务不消费 key。

使用新的 forward-only migrations，建议：

- Credential-owned provider credential、admin request、short lease tables；
- Agent-owned task credential snapshot table，与本模块 task 有 FK，但不向 Credential 表建跨模块 FK；
- Credential lease 表不向 Agent task/lease 表建跨模块 FK，application coordinator 通过 ports 验证；
- CHECK 固定 finite UTC timestamp、revision/status/nonce/ciphertext/keyed digest grammar；
- PostgreSQL 中绝不出现 plaintext、master key、child env 或可逆“调试列”。

对同一 synthetic secret 连续 rotate 必须产生不同 ciphertext/nonce；数据库 byte scan 不能找到 plaintext
marker。错误分类使用 pgx type/SQLSTATE，不依赖 constraint 名或错误文本，不向外泄漏 SQL/schema/ciphertext。

## A4. Core admin Unix socket 与 workosctl

Core 同一进程增加独立 credential admin Unix socket，不增加第七个进程。要求：

- socket path 绝对、父目录受控、拒绝 symlink/非 socket 冲突，mode 最多 `0600`；正常退出只移除自己精确
  创建且 identity 匹配的 socket；
- admin mux 只注册 CredentialAdminService，有独立 pre-decode body budget（建议 16 KiB）；普通 Core HTTP、
  Gateway、harness private listener 都不能到达 admin RPC；
- `workosctl credential put|rotate|revoke|list` 经该 socket 调用，不直接 SQL；
- secret 只能从显式 stdin 或 owner-only regular non-symlink file 读取，绝不接受 `--secret=<value>`、位置参数
  或环境变量；CLI stdout 只输出 metadata，不回显长度/fingerprint/partial value；错误固定净化；
- put/rotate 需显式 idempotency key（CLI 可安全生成 UUIDv7 并显示非 secret request ID）；revoke 使用 expected
  revision；并发只允许一个 winner，response loss 后可重放。

缺 master key/admin socket 时 Core 的 Project、Agent、Artifact 等非 credential 功能必须仍可启动；Vault
capability 明确 unavailable，credential-required Provider 对新 binding/run fail closed。不得用空 adapter 或
固定 metadata 冒充已配置。

## A5. Authenticated private harness execution channel

Raw credential 不能经过当前普通 loopback TaskExecution listener。把 harness execution surface 移到 Core
同一进程的独立、mutual-authenticated TLS listener，并只注册 TaskExecution + CredentialLease RPC：

- TLS 最低 1.3；Core 与 harness-host 使用独立 leaf identity，由明确的 private CA 验证；双端校验 exact
  WorkOS process identity（推荐固定 URI SAN），禁止 `InsecureSkipVerify`、任意 CA、CN-only 或“有证书即可”；
- server key 只挂给 Core，client key 只挂给 harness-host，CA private key不进入任一常驻进程；key file
  absolute/non-symlink/严格权限；证书过期、wrong SAN、wrong CA、missing client cert 全部 fail closed；
- 普通 Core HTTP mux 删除 TaskExecution 注册，旧直连路径与所有 Credential RPC 对 Gateway 都是
  deterministic 404；Gateway public prefix/identity header/Surface route 不变；
- harness worker 的 Claim/Renew/Append/Artifact/Finish 全部使用同一 authenticated client，避免一半安全、
  一半可被冒领；不得把 client cert/key 传给 provider child；
- 开发/测试通过临时目录生成 ephemeral CA/leaf，结束后删除，不提交 key/cert；生产文档使用 systemd
  credential/file provisioning。默认开发与 CI 必须可复现，不能要求手工粘贴私钥。

如果现有通用 `httpserver` 不适合 mTLS，新增小而专用的 platform adapter；不要把 credential/TLS 逻辑塞进
domain。证书路径/内部地址属于配置，secret key bytes 不得进入 config dump/log。

## A6. Task credential snapshot 与 lease 状态机

Core orchestration 使用中立 ports 串起 Project、Catalog、Credential、Agent：

1. owner 选择 provider 时，Catalog 先验证 adapter runtime health；若 capability 要求 credential，Core Vault
   必须存在同 owner/consumer/purpose 的 active credential，binder 才把其 opaque ID 注入
   `HarnessBinding.credential_ref`。客户端仍不能提交 ref。
2. public Catalog 变为 owner-aware projection：adapter healthy 但 owner 缺 credential 时，provider 对该 owner
   为 unavailable，reason 固定为安全文案；不得泄漏是否存在 foreign credential、ID、revision 或 key 状态。
3. fresh user task、App allow task与 waiting-approval task都在任何 queue/outbox/reservation 前解析 exact
   credential ID+revision，并与 provider snapshot 一起持久化。provider 不需要 credential 时 snapshot absent；
   required/absent、wrong owner/provider、revoked/corrupt 均零副作用 fail closed。
4. 用户/App idempotency replay 先返回第一次 task/snapshot，不因 rotate/revoke/rebind 偷换 credential。approval
   决策时重验 snapshot 仍 active/exact；不一致则 fail closed，不 queue、不 reserve。
5. `AcquireTaskCredential` 在一个受控事务中由 `(active task lease, worker)` 派生 task/provider/snapshot，再由
   Credential port 创建短 lease。请求不得覆盖派生事实。expiry 不得晚于 task lease expiry，默认跟随现有
   30s execution lease；same active task lease 的 response-loss replay 返回同一 lease metadata 与同一
   credential revision，不新建多行。
6. 初次 Acquire 可返回一次 decrypted bytes；Renew 只延长到新的 active task lease expiry且不返回 secret；
   Release 幂等。Core restart 后 durable lease 可验证/replay；expired/released/foreign worker 统一拒绝。
7. rotate/revoke 立即阻止新 Acquire；既有 worker 下一次 heartbeat/Renew 最迟在有界间隔内收到失效 verdict，
   cancel 并 kill provider process。Core/credential store unavailable 时 fail closed，不让 child 无 lease 继续。
8. harness-host 不跨 task 缓存 plaintext。worker defer release；DeepSeek adapter 只接受匹配 provider/purpose、
   未过期的 neutral lease，在构造单个 child env 后缩短引用生命周期。Fake/Generic 不申请 lease。

不要把 credential revision 写入 public AgentTask；必要的 private snapshot 通过 Agent ports 供 coordinator 读。

## A7. DeepSeek 移除长期环境凭据

- 删除 DeepSeek `Config.APIKey` 与 `DEEPSEEK_API_KEY` 正常读取路径；Compose/systemd env example/README 不再
  指导放长期 key 到 harness-host 环境。
- 若 legacy env 仍被设置，必须安全地拒绝或明确报告迁移到 Vault，绝不静默继续使用；错误只说明变量路径
  已废弃，不显示值、长度或片段。
- DeepSeek provider startup health只描述 non-secret runtime/config；owner credential readiness由 Core
  Catalog overlay 决定。Run 缺少/错误/过期 lease 时为非重试配置/授权失败，不启动 child。
- 更新 `make test-deepseek-fixture`：自动生成 ephemeral master key和mTLS materials，通过 admin API存入
  local fixture 接受的 synthetic credential，再跑官方 runtime + loopback API + browser；不得走外网。
- credential rotate 后新 task 用新 revision；旧 queued/waiting snapshot fail closed；Project rebind/new task
  行为有真实 PostgreSQL 与跨进程测试。

## A8. 阶段 A 验收与提交

至少覆盖：

- crypto golden/tamper/wrong AAD/wrong key/nonce uniqueness、master file path/mode/symlink；
- input bounds、UUIDv7、revision、idempotency replay/conflict、并发 create/rotate/revoke、rollback；
- real PostgreSQL plaintext absence、corrupt ciphertext→Internal、restart replay、expired lease cleanup；
- catalog owner projection、server-derived binding ref、global/project/App/approval task snapshot；
- Acquire request 不能选择 owner/provider/ref，lease loss/foreign worker/rotation/revocation/response loss；
- mTLS valid path与 missing cert/wrong CA/wrong SAN/expired cert/ordinary HTTP/Gateway 404；
- DeepSeek child 只在 valid lease 下启动，cancel/revoke/deadline kill，日志/event/DB/config 无 secret；
- existing Fake/Generic、Artifact、App Bridge、LAN auth全部回归。

新增专用 `make test-credential-vault`，必须跑真实 PostgreSQL、Core private mTLS listener、harness-host、
workosctl admin socket 与 local DeepSeek fixture。阶段结束运行本文件“每阶段共同门禁”，同步 ADR/task/status/
implementation/generated README，然后创建阶段 A 聚焦提交并记录 hash。提交完成前不得开始阶段 B。

# 阶段 B：Review Artifact 作为 Agent Context

## B1. 任务、ADR 与固定语义

基于 A 的完成提交创建 stacked branch/worktree 与
`docs/tasks/20260830-artifact-agent-context.md`。新增
`docs/decisions/0010-review-artifact-agent-context.md`（编号占用则顺延）。

第一版只支持一种 canonical ref type：

```text
type     = "artifact.review.v1"
id       = canonical lowercase UUIDv7 Artifact ID
revision = exact immutable Artifact digest: "sha256:" + 64 lowercase hex
```

固定上限：每 task 最多 4 个 ref；保持请求顺序；拒绝 duplicate `(type,id,revision)` 与同 ID 不同 revision；
每个 Artifact 仍受已有 512 KiB/20k lines/16 KiB line 限制；resolved context aggregate 最多 1 MiB。global
task 不接受 Project Artifact context；App Bridge 首版仍完全不接受 context。

ADR 至少固定 submission-time authorization、execution-time lease binding、immutable digest pin、TOCTOU、
project archive、provider capability、prompt-injection边界、content不进event/log与UI Project切换隔离。

## B2. Provider capability 与入队前裁决

在 `HarnessCapabilities` 增加 exact `supported_context_ref_types` repeated field，不另造“支持全部”的魔法 bool：

- Fake 与 DeepSeek 只有在阶段 B 的真实 materialized-context tests 通过后才声明
  `artifact.review.v1`；Generic CLI 保持空；
- Core catalog 验证列表 canonical、有限、无重复；未知类型或不一致视 capability corruption并把 provider
  投影为 unavailable；
- Task Router 对 fresh task 在 task/outbox/approval/reservation 前验证 resolved provider exact support；
  unsupported → sanitized FailedPrecondition、零副作用、不 fallback；
- idempotency replay不重新按新 capability裁决，返回第一次 snapshot。

## B3. Submission-time Artifact 验证

在 Agent transport/application/orchestration 中完整校验 ContextRef grammar。通过 neutral Artifact port：

- derive owner from Gateway identity，project from TargetScope；客户端不能在 ref 中携带 scope；
- 读取 exact review Artifact metadata/content，验证 same owner/project、review subtype、ID、digest、media/count
  与 stored canonical content；Web Bundle、unknown/foreign/wrong Project统一安全拒绝；
- digest mismatch 是 FailedPrecondition/NotFound 中经 ADR 固定的一种，不形成 foreign existence oracle；stored
  corruption→sanitized Internal，transient→Unavailable；
- 成功后 task仍只保存三元 ref，不复制 title/content/media；immutable ref order进入既有 task payload digest/
  replay事实；失败不创建 task/outbox/approval/reservation。

不要让 Agent repository 直接查询 Artifact SQL，也不要把 public `GetReviewArtifact` handler 当内部 port。

## B4. Lease-bound context resolution

向 authenticated private TaskExecution 协议 additive 增加 `ResolveTaskContext`：

- request 只含 task lease ID + worker ID；不接受 refs/owner/project/provider；
- Core 从 active lease读取 task input，重新验证 canonical refs，通过 Artifact transaction-scoped read port取得
  content，并在返回前确认 lease仍由该 worker持有；
- response按 ref 顺序给出 canonical type、artifact type、ID、digest、server-stored title、media type与 bytes；
  wire aggregate有明确 pre-decode/encode 上限；没有 storage path/content_ref/owner或其他 Project信息；
- same lease重复解析得到逐字节相同结果；lease lost/terminal/foreign worker拒绝；Core restart后仍可解析；
- worker在 provider启动前解析一次，将中立 `ContextDocument` 传给 broker/provider；不落本地文件、不缓存
  跨 task、不让 provider反向调用 ArtifactService；失败则不启动 provider并由 worker写唯一 terminal failure。

若为了同一 PostgreSQL snapshot需要 coordinator，沿用 ADR-0008 transaction-scoped ports；禁止在
orchestration 中手写 Artifact/Agent SQL。

## B5. Adapter 编码与不可信上下文

- neutral provider execution input 明确区分 task command、resolved context documents、credential lease与
  artifact output sink；不要继续给 `Provider.Run` 追加无结构位置参数。
- DeepSeek 将 goal/context/output contract编码成 versioned canonical JSON task envelope，再作为官方 runtime
  的 user content block；Artifact bytes放在 `untrusted_contexts` 数组，使用 JSON length/escaping，禁止手写
  sentinel delimiter、字符串拼接伪 system prompt或让 context覆盖固定 persona。
- Fake provider以 deterministic方式证明收到了 exact count/order/digest；不得把完整 context bytes回显进
  event。Generic CLI 对非空 resolved context fail closed。
- resolved content 超限、duplicate/mismatch、invalid UTF-8或 provider拒绝都在 child启动/任何 provider side
  effect前失败。

## B6. Desktop “Use as Agent context”

在现有 Artifact Center/Viewer/Agent Center 上增加最小、普通窗口内的交互：

- 当前 Project review Artifact可执行 “Use as Agent context”；选择后打开/focus Agent Center并显示可移除
  chip（title + Markdown/Diff 类型），不显示 digest/内部 ID/content预览；
- duplicate选择幂等，最多 4 个，达到上限有固定可访问提示；提交后 request包含 exact id+digest；
- Project切换、archive/remount和窗口迟到响应必须 abort + generation-invalidate并清空旧 Project context；
  旧 Artifact或迟到 list/view response不能进入新 Project composer；
- submit成功后按既有 UX决定保留/清空，但行为必须测试并一致；失败不静默丢 ref；
- ContextRef 只在可信 Desktop主界面构造，不进入 iframe App Bridge SDK、DOM data attribute、URL、storage或
  screenshot中的 raw content/digest。

按 `docs/ui/README.md` 建立：

```text
docs/ui/desktop-web/changes/20260830-artifact-agent-context/
├── before/
├── after/
└── notes.md
```

固定 Chromium 1440×900、deviceScaleFactor 1，至少覆盖 Artifact “Use as context” 与 Agent Center selected
context 状态；`after/` 同名更新 `current/`。全部使用 synthetic fixture。

## B7. 阶段 B 验收与提交

新增 `make test-artifact-context`，真实链路至少证明：

1. Fake task生成 source review Artifact；
2. Desktop从 Artifact Center/Viewer选择它；
3. 第二个 task携带 exact ref，Core preflight验证；
4. harness-host经 private mTLS + active lease解析 exact content；
5. provider只收到同 owner/project/digest的 canonical bytes并完成；
6. PostgreSQL/Core/harness restart后 queued task仍解析同一 context；
7. foreign/wrong-project/digest mismatch/duplicate/oversize/unsupported provider/lease lost均零越权、固定错误；
8. Project切换与迟到响应不能串 context。

完成 unit/race/integration/browser/visual/回归、同步 ADR/task/status/implementation/generated README，创建阶段 B
聚焦提交并记录 hash。提交完成前不得开始阶段 C。

# 阶段 C：DeepSeek Structured Markdown / Diff Review

## C1. 任务与 ADR

基于 B 的完成提交创建 stacked branch/worktree 与
`docs/tasks/20260830-deepseek-structured-review.md`。新增
`docs/decisions/0011-deepseek-structured-review-output.md`（编号占用则顺延）。

ADR 至少固定：为什么 vendor envelope/parser只在 DeepSeek adapter；为什么 structured run不把 raw JSON delta
发布为 AssistantDelta；exact requested output contract；adapter-derived key/title；batch atomicity；partial/crash/
response-loss replay；usage/terminal顺序；malformed model output是 deterministic protocol failure而非 Artifact。

## C2. Strict structured response envelope

仅当 `output_artifact_types` 非空时启用 versioned structured mode。传给模型的 contract 必须要求一个完整、
唯一的 JSON document；建议 envelope：

```json
{
  "version": "workos.deepseek.review-output.v1",
  "summary": "bounded plain-text summary",
  "artifacts": {
    "document.markdown.v1": "canonical candidate text",
    "code.unified-diff.v1": "canonical candidate text"
  }
}
```

实际 schema可微调，但必须满足：

- strict decoder、unknown fields拒绝、exact one JSON value、无 prefix/suffix/code fence/prose；
- artifacts key set恰好等于 request set，无缺失/额外/重复/alias，顺序由 request决定；
- summary bounded（建议 ≤64 KiB）、valid UTF-8、无 C0/C1（允许明确规定的 LF/TAB）；
- 在 parse前限制 runtime aggregate bytes，在构造 ArtifactOutput前执行与 Core相同或更严格的 content上限；
- adapter固定 `output_key`（例如每 canonical type一个固定 key）与安全 title；模型不能选 key/title；
- 普通无 artifact run继续现有 streaming text语义；structured run可以保留 RunStarted，但 raw JSON/content
  fragments不得作为 AssistantDelta/AssistantMessage泄漏，只有验证后的 bounded summary可成为 message；
- parse、set/content validation或 sink任一失败时不发 RunCompleted；worker最终只写一个 sanitized RunFailed。

Prompt compliance不是安全边界。即使模型忽略 contract，严格 parser也只能 fail closed，不能从自由文本中
正则“尽量提取”Markdown/Diff或伪造成功。

## C3. Atomic Artifact batch materialization

DeepSeek可能一次请求两种 output，且模型重跑不保证逐字节确定。不能用两个独立 RPC留下“第一个提交、
第二个失败、reclaim后模型变化冲突”的无说明窗口。Additive扩展 private execution协议与 neutral sink：

- 新增 bounded `AppendTaskArtifactBatch`（或等价 batch command），最多 2 个 output；保留既有单项 RPC兼容
  Fake与历史调用；
- Core在一个 transaction内锁定 active task stream，验证 requested exact types/key slots，先准备/验证所有
  domain artifacts，再裁决全部 mappings，最后写入全部 Artifact rows/mappings与连续 Core-minted
  ArtifactCreated events；任何一项失败整批零新增；
- request order决定 event sequence；Provider仍不能提交 owner/project/task/artifact ID/digest/time/sequence；
- all absent→atomic insert；all present exact→exact replay；mixed legacy/retry state必须逐项验证，已有 exact可
  replay、缺失项在同 transaction补齐；任一 conflict/corruption→零新写入；
- response loss、Core restart、worker reclaim与 concurrent duplicate batch有真实 PostgreSQL证据；
- worker只在 batch成功后标记 requested types emitted；terminal前仍检查 exact completeness；generic
  AppendTaskEvent仍拒绝 Provider-built ArtifactCreated。

若审查证明现有 materializer可在不新增 RPC的情况下提供同等“多 output 单事务”事实，可以复用，但必须在
ADR和测试中证明，不得只依赖 adapter先做内存校验来假装数据库原子性。

## C4. DeepSeek capability、context 与 lease 联合

- 只有上述 strict envelope、batch sink、failure matrix和本地官方 runtime fixture全部通过后，DeepSeek才将
  `structured_artifacts=true` 并声明 exact
  `document.markdown.v1`、`code.unified-diff.v1`；不得提前改 capability。
- 同一 run同时支持阶段 A credential lease、阶段 B resolved Artifact context、existing token/runtime budget和
  structured output；任何一项失效都会 cancel/kill child并停止后续 publication。
- context/task/output contract使用同一个 versioned canonical envelope；goal/context始终是 untrusted payload，
  output schema是 adapter-owned contract。不得把 Provider credential写进 prompt/config/fixture response。
- usage从官方 runtime实际事件汇总；无 hard price table则 cost继续 unavailable。artifact content长度不当成
  token usage。RunCompleted只能在 batch materialization与 UsageRecorded成功后出现。
- auth/rate-limit/provider/transport错误继续使用现有安全分类；模型 protocol violation为非重试 failure；错误
  不包含 raw response/context/credential。

## C5. Local fixture 与真实浏览器链路

扩展本地 DeepSeek API fixture，使其根据 synthetic request返回 deterministic structured envelope，并增加
malformed/extra/missing/oversize/invalid-content/auth/rate-limit模式。fixture必须验证：

- official runtime确实收到了阶段 B 的 synthetic Artifact context与 exact output contract；
- Authorization使用阶段 A Vault lease中的 synthetic value，但 fixture/log/test failure绝不打印 header；
- 请求目标只为literal loopback，production仍要求HTTPS；任何测试都不能fallback真实网络。

新增 `make test-deepseek-structured-review`，通过真实 PostgreSQL + Core private mTLS + harness-host + local API
fixture + Gateway + Chromium完成：

```text
create/select Project
  → bind DeepSeek with server-derived credential ref
  → create or reuse a synthetic review Artifact
  → choose it as Agent context
  → request Markdown and Unified Diff outputs
  → official runtime/local API returns strict envelope
  → atomic batch materialization + ordered timeline events
  → open both inert viewers
  → restart Core/harness-host
  → list/read exact artifacts and replay task facts
```

如果阶段 C 的 UI只让已有 checkbox/context路径从 unavailable变为 working而像素不变，在 task中说明并复用
阶段 B current，不制造无差异截图；若新增 capability提示、状态或错误文案，则按 UI规则建立独立
`20260830-deepseek-structured-review` before/after/current。

## C6. 阶段 C 验收与提交

至少覆盖：

- strict JSON golden、unknown/duplicate/missing/extra/trailing/malformed/oversize/control/UTF-8；
- raw structured stream不进入 AssistantDelta/message/log，validated summary有界；
- one/two output exact set、adapter-derived key/title、sink failure、terminal/usage/event顺序；
- batch atomic all-or-none、exact replay、different response conflict、mixed legacy state、concurrency、restart；
- combined credential revoke/lease loss/context mismatch/deadline/cancel行为；
- DeepSeek capability only after evidence，Generic仍unsupported，Fake existing behavior不回归；
- browser context→DeepSeek→two review artifacts→safe viewer真实链路。

同步 ADR/task/status/implementation/DeepSeek module README/generated root README，完成共同门禁后创建阶段 C 聚焦
提交并记录 hash。不得 merge/push。

## 每阶段共同测试门禁

每个阶段结束前执行与该阶段相关的全部命令，并在任务记录写真实结果；阶段 C 还必须执行整批完整矩阵：

```sh
make generate
git status --short
make generate
git status --short                 # 第二次 generate 不得产生新 tracked diff

make check
make test-integration
make test-deepseek-fixture
make test-artifact-review
make test-lan-pairing
make test-e2e

# 随阶段加入并从该阶段起持续回归
make test-credential-vault
make test-artifact-context
make test-deepseek-structured-review

go test -race ./internal/core/credential/... ./internal/core/agent/... \
  ./internal/core/artifact/... ./internal/core/orchestration/... \
  ./internal/core/harnesscatalog/... ./internal/harness/...

buf lint api/proto
buf breaking api/proto --against '.git#branch=main'
git diff --check
```

若当前 Makefile 的 target 名称略有不同，可使用实际等价命令，但必须新增三个清晰的跨进程 acceptance target。
`buf breaking` 应分别对阶段 base 与本地 main 检查 additive compatibility。不要用 `|| true`、skip、删测试、
扩大 timeout掩盖 race，或因共享数据库污染而清 volume；应使用独立 fixture identity/scratch database。

对 secret/content 安全还要做聚焦扫描，但扫描命令与输出不得回显真实 material。只扫描仓库受控内容，禁止
扫描 home、环境或 shell history。检查 Git staged diff、tracked fixture、日志与截图没有 PEM private key、
bearer token、provider key、raw context全文或临时证书。

## 错误矩阵（所有阶段统一净化）

至少固定并测试：

| 场景                                                | 外部 verdict                        | 约束                                                     |
| --------------------------------------------------- | ----------------------------------- | -------------------------------------------------------- |
| missing identity/session                            | Unauthenticated                     | public owner API only；不形成 credential/artifact oracle |
| malformed/bounds/unknown enum                       | InvalidArgument / ResourceExhausted | decode前 body budget；零业务副作用                       |
| unknown/foreign Project/Artifact                    | NotFound 或既有安全 verdict         | foreign与missing不可区分                                 |
| provider lacks credential/context/output capability | FailedPrecondition                  | fresh run零 task/outbox/reservation                      |
| stale revision/idempotency conflict                 | Aborted                             | 首次结果保持，绝不覆盖                                   |
| wrong mTLS identity/cert                            | TLS handshake failure               | RPC handler零调用，不返回业务细节                        |
| lost/expired/foreign task or credential lease       | Aborted / FailedPrecondition        | 无 secret/content，child被取消                           |
| revoked/rotated snapshot                            | FailedPrecondition                  | 旧 task不偷换新 credential                               |
| transient PostgreSQL/Core/harness                   | Unavailable                         | retryable但不泄漏 DSN/SQL/endpoint                       |
| ciphertext/artifact/task snapshot corruption        | Internal                            | fail closed，不自动修复/覆盖                             |
| malformed DeepSeek structured response              | terminal RunFailed                  | 非重试 protocol failure，不回显 response                 |

内部/private transport可使用更精确 Connect code，但 public UI文案必须固定、安全、可重试性准确。

## 文档与状态同步

每阶段都必须更新：

- 对应 `docs/tasks/...`：范围、事实 owner、Proto/migration、命令结果、commit、风险、下一步；
- 对应 ADR；
- `docs/architecture/implementation.md`：实际调用图、public/private/admin listener、事务与 unavailable边界；
- 受影响模块 README（Credential、Agent context、DeepSeek）；
- `docs/status.json`：只按真实证据写状态；
- 工具生成的 root `README.md` 状态区块；
- 有可见 UI的阶段按 `docs/ui/README.md` 更新 before/after/current/notes。

状态必须克制：

- 阶段 A 可新增 `Credential Vault` working，仅限 provider API-key encrypted store + local admin + task lease；
  OAuth/Codex login/GitHub/cloud/search credential仍 unavailable。
- 阶段 B 只能声称 review Artifact context working；workspace/RAG/URL/file/foreign Project context仍 unavailable。
- 阶段 C 只能声称 DeepSeek支持两种 review text output；图片/PDF/binary/tool-call/patch apply仍 unavailable。
- mTLS只证明 Core↔harness execution identity，不得冒充全系统 service mesh或完整 Workload Identity。
- 不得改变 Runtime/Reliability/Indexer/Mobile 的状态来“顺便推进路线图”。

## 明确非目标

- 真实 Provider smoke、价格表或真实 monetary budget；
- OpenAI/Codex/Anthropic/GitHub/cloud credential adapter、OAuth refresh、HSM/TPM/remote KMS；
- 公共/browser credential编辑 UI、App访问 credential metadata、secret reveal/export/recovery；
- master-key在线轮换、多 key ring、跨主机 Vault HA（必须诚实记录后续）；
- arbitrary file/workspace/URL/RAG/index context、App Bridge context API；
- tool calls、MCP、approvals during provider run、subagents、persistent DeepSeek sessions；
- image/PDF/binary Artifact、patch apply/edit/download、HTML/active-link rendering；
- rootless Podman真实验收、cgroup/Reliability repair、mobile shell、mDNS/public internet。

上述能力必须保持明确 unavailable；不得留 TODO、空 adapter或固定成功响应并标 working。

## 整批完成定义

只有同时满足以下条件，阶段 C 才可标记 done并完成本批次：

1. 长期 Provider credential 只有 Core Vault encrypted-at-rest authority，master key不进数据库/repo/env；
2. harness执行面有真实双向身份与加密，Acquire完全从 active task lease派生，Gateway/App不可达；
3. credential create/rotate/revoke、task snapshot、lease renew/release对并发/restart/response loss收敛；
4. DeepSeek不再读取长期 API-key环境变量，只在单 task有效lease内启动child；
5. review Artifact ContextRef在submission与execution双重owner/project/digest/lease校验，content不进event/log；
6. Desktop context选择对Project切换/迟到响应隔离并有确定性视觉证据；
7. DeepSeek strict structured envelope与atomic batch只发布Core-minted Markdown/Diff Artifact事实；
8. malformed/partial/oversize/unsupported/revoked/corrupt路径全部fail closed且错误净化；
9. 三个新增real-stack acceptance target及既有integration/DeepSeek/Artifact/LAN/E2E/race/breaking全绿；
10. `make generate`二次无差异，所有 task/ADR/implementation/status/generated README/UI证据同步；
11. 三阶段各有独立任务记录、stacked branch与聚焦Git提交，hash和门禁写入仓库；
12. 工作树/Git提交无真实credential、私钥、临时证书、raw context、trace、video、report或构建产物；
13. 未经用户授权没有merge到`main`、没有push。

## 每阶段提交前自查

```text
[ ] 当前只认领一个阶段任务，base/branch/worktree 与 stacked 依赖正确
[ ] 未修改 migrations 001–022，未手改 gen/src-gen/README 状态块
[ ] 没有读取、复制、验证或调用聊天中出现过的真实 Provider key
[ ] raw credential 不在 public Proto/TaskLease/Project/event/outbox/log/DB plaintext
[ ] secret-bearing RPC 不在普通 HTTP/Gateway，mTLS exact identity failure 已测试
[ ] Core/Project/Agent/Harness/Artifact 模块没有跨 SQL 或 internal adapter import
[ ] Provider 没有自选 owner/project/credential/context/artifact identity
[ ] App Bridge 仍只有既有最小能力，iframe不能提交 context或读取 credential
[ ] context/model output均作为不可信bounded content，错误不回显原文
[ ] provider capability与真实测试一致，unsupported功能仍明确 unavailable
[ ] UI before/after/current 是同 fixture/viewport且不含credential/raw content
[ ] make generate二次无差异；check/integration/E2E/race/breaking与本阶段target全绿
[ ] task/ADR/implementation/status/generated docs记录的是实际证据
[ ] 已创建本阶段聚焦Git提交并把hash/命令结果写入任务记录
[ ] 未merge、未push、未清理用户数据库或其他人的改动
```

## 最终交付格式

完成阶段 C 后，在最终回复中只汇报：

1. A/B/C 各自完成的用户链路与边界；
2. 三个 task 文件、ADR、关键实现和 UI 证据的仓库链接；
3. 每阶段 commit hash 与 stacked branch 名；
4. 实际执行的门禁及结果；
5. 仍明确 unavailable 的能力和真实风险；
6. 明确声明未使用真实 Provider credential、未访问外部 Provider、未 merge、未 push。

不要用聊天结论替代任务记录，也不要只留下“下一步建议”而没有完成三个阶段的仓库事实。
