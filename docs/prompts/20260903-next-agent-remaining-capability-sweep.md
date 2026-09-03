# 下一位智能体 Prompt：剩余能力总攻——Provider 扩展、真实 Runtime 自愈链、远程 Surface、语义知识、后台推送与移动原生、桌面系统应用

> 将本文件完整交给下一位实现智能体。本批次覆盖 `docs/structure.md` 中当前全部已识别未完成缺口，
> 预计总量七十五至一百一十小时，允许且预期跨多个会话执行：每个会话开始时重读本文件、
> `docs/status.json` 与任务记录 `docs/tasks/20260903-v1-remaining-capability-sweep.md`，从任务记录标注
> 的下一个未完成阶段继续，不依赖任何聊天上下文。用户将长时间离线，期望你持续自主推进并直接完成
> 实现，不是只输出计划、审查报告或下一份 Prompt。整个总攻只有一个 branch、一个 worktree、一个
> 任务记录和一个写入智能体；所有 workstream 严格串行，禁止为并行、审核或修复再创建分支、worktree，
> 禁止让其他 Agent 修改仓库。

## 你的角色与唯一最终目标

你是 WorkOS 剩余能力的总攻实现智能体。仓库位于 `/home/aquatao/workos`；
`docs/structure.md` 是产品架构主线，`docs/architecture/implementation.md` 是当前代码边界，
`docs/status.json` 是唯一进度事实源。

当前系统已有真实链路：Project、Agent Task Router（审批/预算/配额/断路）、DeepSeek 与 Generic CLI
Adapter、Credential Vault（provider API key + task lease）、Artifact Review 与 Agent context、Knowledge
lexical search、LAN pairing、Adaptive Shell（compact/medium/expanded/fold）、App Bridge（run/watch/
notifications.create/knowledge.search）、Notification local-first 闭环、Incident read model 与
Supervised Web Service Workload 的状态机骨架。

对照 `docs/structure.md`，剩余已识别缺口收敛为六个 workstream：

```text
W1 Provider 与凭据扩展   Codex Adapter、MCP Adapter、Vault 凭据类型/轮换/多 owner/受审计揭示
W2 真实 Runtime 与自愈链 rootless Podman 真实验收、真实监督链、遥测、Repair、Deployment（L3/L4）
W3 Surface 与 Bridge 补全 Bridge 全能力、Declarative Surface、远程 Surface 栈（浏览器池/Native/WebRTC）
W4 语义知识             pgvector embedding、workspace 文件源、通用 archive、混合检索
W5 推送与移动原生       Push Relay/Web Push、Capacitor 封装、mDNS 发现、TransportProvider
W6 桌面系统应用与入口   Command Palette、Mission Control、Home/Files/Docs/Code/Browser、窗口管理
```

唯一最终目标：

```text
六个 workstream 逐项推进
  → 每一项要么取得真实端到端证据并在 docs/status.json 如实升级
  → 要么因宿主/外部账号前提缺失而记录精确、可复现的 blocker 并保持诚实状态
  → 六个进程边界、Proto 契约纪律、migration 全局序号、UI 视觉证据与安全边界全程不被破坏
  → 实现、测试、专项门禁、ADR、任务记录、implementation/status 文档完全一致
```

这是能力总攻，不是功能清单壳。成功结束时：Codex 与 MCP 成为真实可绑定的 Provider；容器
Web Service App 在真实 rootless Podman + cgroup v2 上有跨进程浏览器证据；真实
observation → Incident → restart/stop → Repair Task → Build/Test/Canary/Rollback 链路可复现；
Bridge 具备 `docs/structure.md` 10.5 定义的核心能力；Declarative Surface 与远程浏览器 Surface 可用；
知识检索升级为词法+语义混合并覆盖 workspace 文件；后台唤醒推送有 relay 模式软件链与 Web Push；
移动端有可构建的 Capacitor 封装与 mDNS 发现；桌面补齐 Command Palette、Mission Control 与核心
系统应用。任何一项拿不到真实证据，就保持 scaffolded/unavailable 并在任务记录写明阻塞，绝不伪造。

## 单分支纪律与跨会话续作协议（不可偏离）

执行时从真实本地 `main` 创建且只创建：

```text
feat/v1-remaining-capability-sweep
```

并且只建立一个总攻任务记录：

```text
docs/tasks/20260903-v1-remaining-capability-sweep.md
```

强制规则：

1. 只允许上述一个 feature branch、当前一个 worktree、当前一个写入 Agent。不得创建 review、fix、
   candidate、backup 等辅助分支，不得添加 worktree，不得让 sub-agent 或第二个 Agent 写文件。
2. 不 stash 后切分支，不 reset/rebase/squash 已有历史，不修改或删除本地 `main`，不覆盖用户未提交改动。
3. 若目标 branch 已存在，先只读核对 merge-base、任务记录与工作树；确认就是本任务后从任务记录标注的
   阶段继续，不得删除重建。无法安全确认时留下证据并停止破坏性操作。
4. 所有 workstream 在同一 branch 严格串行；每个可验证阶段完成后做聚焦提交，再继续下一阶段。
5. 跨会话续作协议：任务记录是唯一恢复点。每个阶段开始前把该阶段标记 active，完成后写明已验证命令、
   结果与下一阶段，再提交。新会话禁止重做任务记录标记 done 的阶段，禁止推翻上一会话已验证的裁决，
   除非发现真实缺陷（此时记录证据并修复）。
6. 环境阻塞切换协议：某个阶段依赖的宿主前提（rootless Podman、Xcode/Android SDK、真实 APNs/FCM
   账号、真实厂商网络）不满足时，把精确前提、探测命令与失败输出写入任务记录的 blocker 区，将该阶段
   标记 blocked-environment，立即切换到执行顺序中的下一个不依赖该前提的阶段。环境阻塞不是停工理由，
   只有本文"停止并请求用户的条件"允许等待用户。
7. 禁止把整段工作压成一个巨型提交。建议提交序列（按实际阶段裁剪）：

   ```text
   docs: define remaining capability sweep boundary
   feat: add codex harness adapter
   feat: add mcp harness adapter
   feat: expand credential vault types and rotation
   feat: prove rootless container runtime acceptance
   feat: prove real supervision chain
   feat: add telemetry collection
   feat: add repair orchestrator
   feat: add deployment controller
   feat: complete app bridge capabilities
   feat: add declarative surface
   feat: add remote browser and native surface stack
   feat: add semantic knowledge search
   feat: add workspace knowledge sources
   feat: add push relay and web push
   feat: add mobile native wrappers
   feat: add mdns discovery and transport providers
   feat: add desktop command palette and mission control
   feat: add desktop system apps
   test: prove remaining capability gates
   docs: record remaining capability evidence
   ```

8. 每次提交前运行 `git diff --check` 并审查 staged diff。不得提交 secret、私钥、真实用户内容、数据库、
   容器归档、构建二进制、trace/video、Playwright 临时目录、浏览器 profile、宿主绝对路径快照或测试
   报告 dump。
9. 未经用户新授权，不 merge 到 `main`、不 push、不删除其他分支。最终停在唯一 feature branch 的干净
   HEAD，供用户审查。

## 无人值守与工作量安排

用户离线期间不要等待普通澄清。优先从架构、Proto、现有实现和测试推导最保守的正确方案，然后持续推进。
执行顺序固定为 W1 → W2 → W3 → W4 → W6 → W5：契约与纯软件链路先行，宿主与外部账号强依赖的收口
后置；任一阶段触发环境阻塞切换协议时按顺序后移。下列时间按有经验的实现智能体估算，只用于确保
任务量和依赖顺序充足，不是到点停工条件：

| Workstream | 主要内容 | 建议投入 | 宿主/外部依赖 |
| ---------- | -------- | -------: | ------------- |
| W1 | Codex/MCP Adapter、Vault 扩展 | 12–18 小时 | 无（全部本地 fixture） |
| W2 | rootless 验收、真实监督、遥测、Repair、Deployment | 16–24 小时 | rootless Podman（阻塞可切换） |
| W3 | Bridge 全能力、Declarative、远程 Surface 栈 | 14–20 小时 | WebRTC 可本地回环验证 |
| W4 | 语义 RAG、workspace 源、通用 archive | 10–16 小时 | 无 |
| W6 | Palette、Mission Control、系统应用、snap | 10–16 小时 | 无（Browser/Terminal 依赖 W3） |
| W5 | Push Relay/Web Push、Capacitor、mDNS/Transport | 12–18 小时 | APNs/FCM 账号、Xcode/Android SDK（阻塞可切换） |

执行规则：

- 不要只写计划；完成必读、基线与首个 ADR 裁决后立即实现。
- 一个测试慢、镜像构建慢、偶发下载失败或某个独立门禁需修复都不是停止理由。网络瞬时失败可有界重试；
  Buf 中途失败导致生成目录变化时，先恢复到可再生状态，禁止手改 `gen/` / `src/gen/`。
- 每 60 秒以内给用户一条简短进度更新，但持续推进，不因更新打断测试。
- 不访问真实 DeepSeek/OpenAI/Codex/Anthropic、APNs/FCM 或收费服务；所有 Provider 链路使用本地
  versioned fixture。
- 不搜索 shell history、用户 home、环境变量或 credential store 获取 key。
- 不安装宿主软件、不用 `sudo`、不改内核/systemd/防火墙。rootless Podman、Xcode、Android SDK 一律
  走环境阻塞切换协议，不得自行安装，不得伪造 PASS。
- 独立门禁遇到环境波动时记录并继续其他阶段，收尾时复试。

## 本 Prompt 编写时的仓库事实（执行时必须重新核对）

- 编写前本地 `main` 为 `6853b83`，与 `origin/main` 一致。执行时以真实本地 `main` 为准，不 reset 到
  本文哈希或更旧远端。
- 六个进程固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、`reliability-host`、
  `indexer`。本总攻不得增加第七个常驻 daemon；otel-collector 作为 compose/systemd 编排的外部
  依赖服务部署，不是 WorkOS 进程。
- migration 是 `internal/platform/migrations/files/` 下的全局 forward-only 序列，编写时最新为 `031`
  （Core notifications snapshot revision）。禁止修改已存在文件；每个新 migration 取执行时下一个空闲
  编号并写明唯一 owner 进程。migration checksum 有 pin 测试。
- ADR 编写时最新为 `0014`（local-first notifications）。本总攻预计需要多个新 ADR，从 `0015` 起按执行
  时编号；每个 workstream 的关键裁决先 ADR 后代码。
- `agent/adapters/` 下 `codex/` 目录存在但为空；`runtime/browser-runner/`、`runtime/native-runner/`、
  `reliability/repair-orchestrator/`、`reliability/deployment-controller/` 同样为空。Go 实际代码位于
  `internal/`（如 `internal/core/...`、`internal/harness/...`），目录边界以 `docs/architecture/implementation.md`
  为准，执行时重新确认模块落位。
- App Registry capability vocabulary 当前为 `agent.task.run`、`agent.event.watch`、`artifact.read`、
  `artifact.write`、`knowledge.read`、`project.read`、`notifications.create`。Bridge 已协商
  agent run/watch、knowledge.search、notifications.create。
- 既有专项门禁（新增门禁不得重名）：`test-adaptive-shell`、`test-app-knowledge-search`、
  `test-app-notifications`、`test-app-version-rollback`、`test-artifact-context`、`test-artifact-review`、
  `test-credential-vault`、`test-deepseek-fixture`、`test-deepseek-structured-review`、
  `test-incident-notifications`、`test-integration`、`test-lan-pairing`、`test-notification-center`、
  `test-podman-fixture`、`test-project-knowledge-rebuild`、`test-project-knowledge-search`。
- `test-podman-fixture` 在编写时仍是已知 blocker：宿主不满足 rootless Podman 前提。本总攻 W2 把
  "满足前提并取得真实证据"作为目标之一，但探测失败时维持 blocker 记录，不安装、不伪造。
- `deploy/compose.observability.yaml` 与 `deploy/otel-collector.yaml` 已存在：otel-collector 编排
  与六进程 OTLP endpoint 注入已就位，但尚无真实遥测产出、存储与 System Monitor 消费。
- Desktop 已有：AdaptiveShell、Agent Center（Approvals/Usage）、Artifact Center/Viewer、System Monitor、
  Knowledge Center、App Library、Device Center、Notification Center、HarnessSettings、VersionDialog。
  没有 Command Palette、Mission Control、Home/Files/Docs/Code/Browser/Terminal/Experiments 应用。
- 通知链路现状：前台实时 + durable 补收 + 双设备已读收敛已 working；APNs/FCM/Web Push/Service
  Worker/偏好/免打扰/通知搜索未实现。
- Indexer 现状：project review-artifact 词法索引 working；语义 embedding、pgvector、workspace 文件源、
  通用 archive 未实现。
- `docs/ui/` 视觉证据体系与 `docs/ui/README.md` 流程已建立；所有 UI 变更必须产出
  before/after/current + notes。
- README 状态区块由 `docs/status.json` 生成，禁止手改。

## 开始前必须完成

完整阅读，不要只靠关键词片段：

1. `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`SECURITY.md`、`docs/ui/README.md`；
2. `docs/structure.md` 全文，尤其是 1、5、6、7、8、9、10、11、12、13、14、16、17、18 节；
3. `docs/architecture/implementation.md` 全文与 `docs/status.json`；
4. ADR `0001`–`0014` 全部；
5. `docs/tasks/` 下与本总攻相关的记录：`20260829-supervised-web-service-workload.md`、
   `20260830-central-credential-vault.md`、`20260830-lan-device-pairing.md`、
   `20260831-v1-runtime-reliability-adaptive-closeout.md`、`20260901-v1-project-knowledge-search.md`、
   `20260902-v1-local-first-notifications.md`；
6. `api/proto/workos/` 全部包（agent、app、artifact、auth、bridge、common、credential、harness、
   incident、index、internal、notification、project、surface、taskexecution、workload）与生成代码；
7. harness 侧 Provider/Adapter/Generic CLI/DeepSeek 实现、broker、catalog、task lease 与 mTLS 执行
   listener；
8. Core 的 credential、agent（router/approval/usage/outbox）、artifact、indexfeed、notification、
   appregistry、project 模块与 PostgreSQL adapter / integration / restart 测试；
9. Runtime 的 workload manager、surface broker、App Bridge authorizer、`sdk/app-sdk`、
   `sdk/surface-sdk`、`clients/app-host` 与 opaque Web Bundle E2E；
10. Reliability 的 incident、action ledger、pending action replay、Runtime private client 与
    `cmd/reliability-host` wiring；
11. Gateway 的 allowlist/proxy、device session middleware、streaming、TLS pairing；
12. Desktop 全部组件、`@workos/adaptive-shell`、Playwright fixture 与截图工具链；
13. `internal/platform/migrations`、sqlc、compose.yaml、deploy/、Makefile、CI targets、status renderer
    和 migration checksum 测试。

随后创建唯一 branch 与唯一总攻任务记录（按 TEMPLATE.md，含六个 workstream 的阶段清单、依赖、
预期 Proto/migration、失败矩阵索引与 blocker 区），写明 baseline SHA。开始改代码前实际运行并记录：

```sh
git status --short --branch
git log --oneline --decorate -20
git branch -a -vv
git diff --check
make bootstrap
make generate
make check
make test-integration
make test-e2e
```

不得为了基线清理 PostgreSQL volume。若基线失败，先分类为代码、环境或已有 blocker；记录证据并继续
所有不依赖阶段，禁止把基线失败归咎于尚未产生的改动。

## 全批次不可违反的边界

### 架构与数据所有权

- 依赖方向固定为 `domain → application → ports ← adapters`。Domain 不得导入 PostgreSQL/pgx/sqlc、
  Connect/Proto、HTTP、文件系统、浏览器 API、厂商 SDK 或其他模块 adapter。
- 六个进程边界固定。Codex/MCP 的 Provider 类型只能出现在对应 adapter；Core 只认识 canonical
  protocol。Reliability 新增的 repair/deployment 模块物理归 `reliability-host`，遥测采集归
  `reliability-host` 的 collector 组件，不新增进程。
- 每张表和 migration 只能由一个进程拥有；跨进程访问只走 versioned private RPC 或事件，按
  at-least-once 设计，consumer 幂等并持久化 cursor。禁止跨 schema SQL、共享可变 entity、引用对方
  internal package。
- 跨进程/Go/TypeScript/SDK 契约必须先 additive 修改 `api/proto` 再 `make generate`。v1 字段号不得
  复用；删除字段/枚举值必须 reserved；无法 additive 表达才新版本 + ADR。
- 已执行 migration 逐字节不变。所有资源 ID 使用 canonical UUIDv7，时间 UTC microsecond；外部写命令
  用 durable idempotency key 或 monotonic etag/revision，绝不以进程内 mutex/map 冒充裁决。
- App manifest 是版本化 JSON Schema，是唯一 manifest shape 事实源；不得新增同义 DTO。

### 身份、安全、内容与隐私

- 浏览器/App 提交的 owner/user/device/project/app header 全部不可信；Gateway 剥除后从有效 device
  session 注入。所有公开读写按 owner scope 重验；foreign/missing 同一净化语义，不形成存在性 oracle。
- App 永远不能读取模型或外部服务真实凭据，只能请求 capability。Vault 揭示/导出只经本机 admin
  Unix socket（workosctl），全部审计落库，不经 Gateway 公开路由，不写日志。
- 新增 Bridge 能力每次调用重验 bridge token、surface session、installation 与 exact current grant
  revision；grant revoke/uninstall/epoch 变化后旧 MessagePort 立即 fail closed 且零副作用。
- Push Relay 只允许携带最小唤醒信息（加密 notificationId/设备推送标识）；不得把通知正文、项目名、
  代码、Agent 输出发给 APNs/FCM/Relay。日志不包含 secret、provider raw credential 或用户内容全文。
- WebRTC Data Channel、Declarative Surface、mDNS 记录、terminal 流都按不可信输入处理：有界、
  校验、拒绝控制字符/超长/嵌套深度，inert 渲染，不解释 markup，不执行任意 URL/route。
- 截图与 fixture 只用明显假数据；不得出现真实凭据、真实用户数据、真实设备 ticket/cookie、bridge
  token 或依赖外部服务才能复现的内容。

### 真实能力与诚实状态

- 状态只能升到有真实证据的级别。fake/fixture 可证明软件链路，但不能把 rootless container、
  supervisor、真实厂商 Provider、真实 APNs/FCM 投递升为 working。`docs/status.json` 每次更新都写
  实际证明内容与 Not covered。
- transport/实现按真实名称记录（Connect server stream、WebSocket、WebRTC、mDNS）；固定成功响应、
  内存-only 状态、setInterval 假事件、route mock、直插数据库都不构成 working evidence。
- 未实现的保护必须明确报告 unavailable（例如 Terminal 在 native runner 不可用时不得提供假终端）。

## W1：Provider 与凭据扩展

### 目标链

```text
Codex App Server（本地 versioned fixture，JSON-RPC/流式事件）
  → agent/adapters/codex 实现 HarnessProvider/HarnessConnection
  → capability 诚实声明 + canonical 事件映射 + providerRawEvent 仅调试
  → task-bound credential lease（复用 ADR-0009 模式，凭据种类扩展）
  → Catalog 注册 + Project binding + Desktop 运行/审批/usage 真实链路
MCP Server（本地 stdio fixture）
  → agent/adapters/mcp 以同一 Provider 接口接入，能力按实际子集声明
Credential Vault
  → 新增 codex/github/通用云凭据种类 + owner 维度
  → master-key 在线轮换（原子重加密 + 崩溃安全）
  → workosctl 受审计揭示/导出
```

### 范围内

- `internal` harness 侧新增 Codex Adapter 与 MCP Adapter，遵循 DeepSeek/Generic CLI 的既有分层与
  broker 注册模式；`agent/adapters/` 空目录按仓库实际落位惯例处理。
- Codex 凭据种类（API-key 形态先行；OAuth 流程仅在不需要用户提供真实 client secret 的范围内实现
  骨架，真实 OAuth 凭据属于停止条件）、GitHub token、通用云凭据的 Vault schema/种类校验/lease。
- master-key 在线轮换：forward migration + 单事务逐条重加密或等价原子方案，中断/重启后旧钥与新钥
  状态一致收敛，不出现半加密；轮换全程审计。
- `workosctl` 揭示/导出：admin socket 鉴权、按 credential 的审计记录、输出不落日志；Web/Gateway
  不暴露。
- Catalog/绑定 UX 对新 Provider 的展示、能力徽标与不可用能力的降级说明。

### 契约与数据

- harness catalog/binding proto 若需 additive 字段（如 provider kind、capability 列表展示）先改
  `api/proto` 再生成。
- Vault migration 声明 owner=workos-core；凭据种类用 finite enum/grammar 校验，不存自由字符串类型。
- 新 Adapter 的 `HarnessCapabilities` 必须逐项如实：Codex fixture 支持什么就声明什么；MCP 通常只有
  运行/停止/stdout 读取，就按 Generic CLI 的降级路径处理。

### 分阶段

1. ADR：Provider 接入契约、凭据种类矩阵、轮换语义、揭示审计模型。
2. Vault 扩展（种类 + owner 维度 + 轮换 + 揭示）+ `make test-credential-vault-expansion`。
3. Codex fixture（独立进程，版本化协议）+ Adapter + broker/catalog/binding + 任务链路 +
   `make test-codex-harness`。
4. MCP fixture（stdio server）+ Adapter + `make test-mcp-harness`。

### 关键失败矩阵（W1 至少覆盖）

| 场景 | 必须结果 |
| ---- | -------- |
| Codex fixture 崩溃/响应丢失/事件乱序 | 任务终态确定，无重复 usage，lease 释放 |
| Adapter 声明的能力与 fixture 实际不符 | 测试失败；能力声明与探测一致 |
| 凭据种类非法/过期/被吊销 | run 前 fail closed，无任务入队副作用 |
| master-key 轮换中途崩溃 | 重启后收敛，所有凭据仍可解密或明确失败，不半加密 |
| 揭示未鉴权/非本机 | 拒绝并审计；无secret进日志 |
| MCP server 无响应/超时/恶意输出 | 有界超时、净化事件、任务终态确定 |

## W2：真实 Runtime 与自愈链

### 目标链

```text
rootless Podman + cgroup v2（宿主满足前提时）
  → Web Service 容器 App：install → run → supervised container → surface proxy → 浏览器
  → Workload Identity 归因（哪个程序占了 CPU/内存）+ PSI 压力事实
watchd 真实采集 → Policy → Incident → L0/L1 确定性动作（限速/重启/停止）真实生效
  → otel-collector 遥测（metrics/logs/traces）→ System Monitor 展示真实数据
Incident → Repair Orchestrator → Task Envelope（incidentId）→ Project/Recovery Harness（L3）
  → Deployment Controller：Build + Test + Canary + Promote + Rollback（L4，衔接 ADR-0012）
```

### 范围内

- ADR-0006 收口：真实 rootless Podman 验收。宿主探测（用户级 podman 可用、cgroup v2、slirp4netns/
  pasta 等）不满足时走环境阻塞切换协议：记录精确前提与探测输出，容器 runner 维持 unavailable，
  但用既有 fake engine 完成所有不依赖宿主的软件阶段。
- 真实监督链：对真实 workload（容器或宿主进程）观察资源 → 违反 policy → 创建 Incident → restart/
  stop 真实发生且幂等。L0/L1 不依赖任何 Agent；Global Harness 不进入安全链路（structure 9.4）。
- 遥测：`deploy/compose.observability.yaml` 与 `deploy/otel-collector.yaml` 已有 otel-collector
  编排与六进程 OTLP endpoint 注入，但没有真实指标输出、存储与消费。本阶段补齐：六进程与 workload
  的结构化 metrics/logs/traces 真实产出（经 collector，不新增进程）、collector 配置与 telemetry
  store、System Monitor 真实遥测视图；日志脱敏规则落实。
- Repair Orchestrator：Incident 证据包（有界、脱敏）作为 context refs 生成修复任务，路由到健康
  Project Harness，不可用时路由到 Recovery（Generic CLI）Harness；修复任务纳入既有审批/预算/断路体系。
- Deployment Controller：候选版本 build → 测试门禁 → canary 观察窗 → promote 或回滚到上一 pinned
  版本；与 App Registry 版本历史（ADR-0012）和 incident-scoped rollback 入口衔接；L5（数据迁移/
  凭据/权限提升）永远保持人工确认。

### 契约与数据

- repair/deployment 的跨进程契约（incident → repair task、deployment verdict）先 proto 后实现；
  Repair 归 reliability-host，Deployment 的版本事实归 workos-core（registry/installation），通过
  versioned private RPC 协作，禁止 Reliability 直写 Core 表。
- 遥测采集配置与 systemd/compose 变更归 deploy/；不改变六进程拓扑。
- 新增 workload kind（如 remote-browser 在 W3）如需扩展 `WorkloadIdentity.kind`，只能 additive enum。

### 分阶段

1. ADR：真实监督验收标准、遥测边界（脱敏矩阵）、Repair/Deployment 语义与 L 级别映射。
2. 宿主探测与 rootless 验收（或 blocker 记录）+ `make test-rootless-runtime`。
3. 真实监督链 + `make test-real-supervision`（宿主不满足时以 fake engine 证明软件链并保持
   supervisor unavailable）。
4. 遥测 + `make test-telemetry`。
5. Repair Orchestrator + fixture 驱动跨进程证据。
6. Deployment Controller + `make test-repair-deployment`。

### 关键失败矩阵（W2 至少覆盖）

| 场景 | 必须结果 |
| ---- | -------- |
| 宿主无 rootless Podman | blocker 记录 + 状态诚实 unavailable + 软件链用 fake 证明 |
| 容器 OOM/busy loop/崩溃循环 | cgroup 限制生效、Incident 唯一、restart 上限后停止 |
| watchd 重启 | 观察不重置裁决、pending action 幂等重放 |
| 遥测含 secret/用户内容 | 采集前剥离，测试断言日志字段白名单 |
| Project Harness 不可用时发生 Incident | 路由 Recovery；两级都失败则通知用户等待处理 |
| canary 失败 | 自动回滚上一 pinned 版本，版本历史一致 |
| 修复任务超预算/断路 | 终态确定，不无限重试同一候选版本 |

## W3：Surface 与 App Bridge 补全

### 目标链

```text
App Bridge（structure 10.5）
  window.*（setTitle/setBadge/maximize/minimize/close）+ project.current
  + files.*（有界 FileRef 读/写/pick，workspace 逻辑路径）
  + artifacts.open/create + theme.get
  → capability vocabulary additive 扩展 + 安装/管理 consent + 每调用 epoch 重验
Declarative Surface
  → App/任务输出返回有界结构化 UI（Table/Form/Chart/Markdown/Button/Progress/ArtifactViewer）
  → WorkOS 原生 inert 渲染，manifest 新 renderer 类型，版本化 schema
远程 Surface 栈
  → Remote Browser Pool（chromium worker workload + Browser Surface）
  → Native Runner + WebRTC Remote Native Surface（虚拟 Wayland 显示 + 视频编码 + Data Channel 输入）
  → Human Native Workspace（宿主原生进程 + 终端/网页控制）
```

### 范围内

- Bridge 能力逐项实现并保持既有信任模式：Runtime 验证 → Core private command / 本地 shell 动作；
  `window.*` 由 runtime-host 转发给 shell 宿主（`clients/app-host`）执行，越权窗口操作被拒。
- `files.*`：FileRef 语法（workspace 相对逻辑路径）、大小/数量上限、路径穿越与符号链接拒绝、
  project scope 校验；workspace 挂载模型的最小实现（本地目录映射到
  `/workspaces/<project-id>/` 逻辑视图），owner 显式绑定挂载源。
- Declarative Surface：JSON schema 版本化（有限组件集、有限嵌套深度、有限条目数、无脚本/HTML/任意
  URL）；Desktop 在 expanded/medium/compact 下自适应渲染；manifest schema additive。
- Remote Browser Pool：browser-runner 作为 runtime 的 workload 管理者（复用 W2 workload/cgroup 纪律），
  浏览器会话生命周期与 Agent 可用性；Browser Surface 在 Desktop 窗口内渲染。
- Native Runner + WebRTC：本机回环（loopback）即可验收：虚拟显示 → 编码 → WebRTC → 窗口渲染 →
  Data Channel 输入回传；Gateway 负责 signaling 路由与鉴权。真实远程 NAT 穿透不在本批。
- Human Native Workspace：最小实现 = owner 显式启动的受监督宿主进程 + 终端控制页（PTY 流经
  runtime-host，有界、审计、cgroup 归因）。
- Browser dock 应用（W6）与 Terminal 窗口消费本栈产出；本栈完成前它们保持明确 unavailable 入口。

### 契约与数据

- Bridge proto、surface proto、manifest schema 全部 additive；capability vocabulary 新增（如
  `files.read`/`files.write`/`window.control`/`artifacts.create` 等，命名在 ADR 定稿）不得与现有
  七个冲突。
- 浏览器池/native workload 的状态表 owner=runtime-host；WebRTC 会话信令经 Gateway private 路由，
  offer/ICE 不含 owner/token。
- Declarative 渲染的输入在服务端与客户端双重校验；未知组件/版本降级为安全占位。

### 分阶段

1. ADR：Bridge 能力清单与授权矩阵、FileRef/workspace 挂载语义、Declarative schema 版本策略、
   远程 Surface 安全边界（输入回传、剪贴板、分辨率、审计）。
2. Bridge 全能力 + `make test-app-bridge-full`（opaque Web Bundle 真实链路）。
3. Declarative Surface + `make test-declarative-surface`。
4. Remote Browser Pool + Browser Surface。
5. Native Runner + WebRTC Remote Native Surface + Human Native Workspace +
   `make test-remote-native-surface`。

### 关键失败矩阵（W3 至少覆盖）

| 场景 | 必须结果 |
| ---- | -------- |
| App 调用未授权/被撤销的 bridge 能力 | fail closed，零 Core/shell 副作用 |
| FileRef 越出 project workspace / 穿越符号链接 | 拒绝，净化错误 |
| 超大文件读写/超深 declarative 嵌套/未知组件 | 有界拒绝或安全降级 |
| 浏览器 worker OOM/崩溃 | workload 限制生效、会话终态确定、可重启 |
| WebRTC 输入通道恶意洪泛 | 有界速率、会话可被 owner 终止 |
| 剪贴板/文件选择器未声明能力 | 不协商、不转发 |
| surface 会话过期后旧端口调用 | 立即失败，不复活旧窗口 |

## W4：语义知识与工作区源

### 目标链

```text
Indexer 拥有 embedding 管道（pgvector）
  → review-artifact + workspace 文件（code/notes/papers/data，经 project 挂载源注册）
  → 增量摄取 + 游标 + tombstone（卸载/归档永久生效）
  → 词法 + 语义混合检索（确定性排序融合，沿用 HMAC 分页与 generation/snapshot 语义）
  → 通用 archive/object store（超越 review-artifact 的有界归档事实）
```

### 范围内

- embedding 执行路径先 ADR 裁决：候选包括本地确定性 embedding（零外部依赖、可离线复现）与经
  Agent API/Model Gateway 的模型 embedding（计入预算与断路）。默认实现本地确定性方案保证可验收，
  模型路径留契约接口且不访问真实 Provider。
- workspace 挂载源注册（owner 显式绑定本地目录/git 仓库）、有界文件过滤（大小、类型、数量、
  忽略规则）、增量游标、project archive 时 tombstone 永久优先（复用既有 upsert/tombstone 语义）。
- 语义索引 migration owner=indexer；检索输入/输出保持既有 Unicode 有界、确定性排序、签名分页，
  混合模式的 score 融合规则可解释且测试固定。
- 通用 archive：有界归档事实与对象存储的最小实现（文件系统或 PostgreSQL 大对象，ADR 定稿），
  不做全文知识图谱。

### 分阶段

1. ADR：embedding 路径、挂载源模型、混合排序、archive 边界。
2. embedding 管道 + pgvector migration + `make test-semantic-knowledge`。
3. workspace 源摄取 + `make test-workspace-indexing`。
4. 通用 archive + Knowledge Center 混合检索 UI 收口。

### 关键失败矩阵（W4 至少覆盖）

| 场景 | 必须结果 |
| ---- | -------- |
| 挂载目录越权/消失/符号链接逃逸 | 停止摄取、tombstone 或显式 degraded，不静默 |
| 超大/二进制/敏感文件 | 过滤规则生效，跳过有记录 |
| 索引重建与实时摄取并发 | generation 隔离，最终一致 |
| 语义与词法结果冲突 | 融合排序确定，测试固定，无随机漂移 |
| 未授权 App 语义检索 | 沿用 grant-revision 授权，拒绝零副作用 |

## W5：后台推送、移动原生封装与局域网发现

### 目标链

```text
Push Relay 模式（structure 13.4）
  → 服务端只发送加密 notificationId（不含正文/项目名/代码/Agent 输出）
  → APNs/FCM adapter（fixture relay 先行）+ 设备订阅注册（owner/device 绑定）
  → Web Push/Service Worker（PWA，本地 VAPID，后台唤醒）
  → 偏好/免打扰时段（owner 级服务端裁决）+ 有界通知搜索
移动原生
  → Capacitor iPad/Android 封装（@workos/adaptive-shell 共享壳）
  → 原生安全存储设备密钥 + 推送注册 + 折叠屏 posture + 安全区/键盘/手写笔
局域网
  → mDNS 广播与发现（workos.local）+ 配对流程集成 + 证书指纹校验/固定
  → TransportProvider 接口 + LanDirectTransport 验收；Relay/Overlay 如实 unavailable
```

### 范围内

- 推送订阅/偏好/免打扰/通知搜索的持久事实 owner=workos-core（沿用 notification 模块模式）；
  推送发送器（relay adapter）作为 Core 的可插拔出口，fixture relay 全链路可验收；真实 APNs/FCM
  凭据缺失时发送能力如实 unavailable，订阅/偏好/软件链全部 working。
- Web Push：VAPID 密钥本地生成与轮换、Service Worker 订阅/展示（只显示最小标题+ID，正文回源读取）、
  权限被拒不影响 durable Notification Center。
- Capacitor 封装：`apps/mobile-shell` 增加 wrapper 工程；设备密钥迁入原生安全存储（不可用时回退并
  如实报告）；推送 token 注册对接 fixture relay；构建级门禁在无 SDK 环境下优雅降级为
  blocked-environment 记录。
- mDNS：服务端广播（compose/systemd 配置或内嵌 responder，ADR 定稿）+ 客户端发现 UX（扫描 →
  指纹校验 → 既有配对流程）；TransportProvider 抽象落位，LanDirect 真实验收，Relay/Overlay 只留
  接口与诚实状态。

### 分阶段

1. ADR：推送隐私边界（Relay 可见字段白名单）、偏好模型、mDNS/Transport 信任链。
2. 订阅/偏好/搜索 + fixture relay + Web Push + `make test-push-relay`。
3. Capacitor 封装 + 安全存储 + `make test-mobile-wrappers`（构建级）。
4. mDNS + TransportProvider + `make test-mdns-discovery`。

### 关键失败矩阵（W5 至少覆盖）

| 场景 | 必须结果 |
| ---- | -------- |
| Relay payload 含正文/项目名/代码 | 测试断言白名单，违规即失败 |
| 设备 token 过期/被撤销 | 订阅清理幂等，不再投递 |
| 免打扰时段内的事件 | 不发后台唤醒；durable 事实不受影响 |
| Web Push 权限拒绝/Service Worker 不可用 | 前台与补收链路不受影响 |
| mDNS 冒名服务/指纹不符 | 配对拒绝，净化错误，无降级提示 |
| 原生安全存储不可用 | 回退方案 + 状态如实，不静默用明文 |
| 通知搜索越 owner/超时 | 有界、净化、无存在性 oracle |

## W6：桌面系统应用与入口

### 目标链

```text
Command Palette（⌘K）：有限动作集（ask project/global agent、run task with provider、
  open recent artifact/app/window、switch project）
Project Mission Control（expanded）：项目卡片（Agent 状态/运行任务/未读/告警）+ 新建 Project
Dock 系统应用：Home（总览/启动台）、Files（workspace 浏览，消费 W3 files/W4 源）、
  Docs（markdown 笔记 + artifact 集成）、Code（代码/diff 只读查看）、
  Browser（WorkOS 窗口内网页，_blank 拦截规则 per 11.6）
Terminal：依赖 W3 Native Runner；不可用时明确 unavailable 入口，不做假终端
Experiments：定位为 App Library 的 project app 模板（manifest 示例），不是系统应用
窗口管理：snap left/right、fullscreen 收口（无复杂动画）
```

### 范围内

- 所有新窗口走既有 system-window/window-manager 与 Adaptive Shell 投影；compact/medium 经
  bottom nav/sheet/dock 可达；fold-separated 不跨铰链。
- Command Palette：键盘导航、有界结果、无任意命令执行；动作目标全部走既有 public service 重验。
- Browser 窗口：外部网页在 WorkOS 窗口内呈现（先以受沙箱 iframe/内置页面实现并明确安全边界；
  远程浏览器池就绪后升级为 Browser Surface）；`_blank` 拦截按 11.6 三分规则。
- Files/Docs/Code 全部只读优先、有界加载、project 切换 generation 隔离，复用既有净化/错误语义。
- 视觉证据：每个新应用/入口在 1440×900、820×1180、390×844 固定 viewport 采集
  before/after/current + notes，目录 `docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/`。

### 分阶段

1. ADR/设计记录：系统应用清单、入口矩阵、Browser 安全边界、Terminal/Experiments 定位。
2. Command Palette + Mission Control。
3. Home + Files + Docs + Code。
4. Browser + 窗口 snap + Terminal 入口（依赖 W3 完成情况如实标注）。
5. `make test-desktop-system-apps` + 全量视觉证据。

### 关键失败矩阵（W6 至少覆盖）

| 场景 | 必须结果 |
| ---- | -------- |
| Palette 动作目标已失效 | 重验后固定 stale 文案，不 fallback 任意 URL |
| 外部网页尝试 top navigation/弹窗 | 被窗口拦截规则接住，不打开浏览器标签页 |
| Files 越权路径/超大目录 | 有界拒绝，净化错误 |
| Project 快速切换 | 迟到响应隔离，窗口状态不串台 |
| compact 布局新增入口 | 不遮 Agent composer/safe area，可达性达标 |

## 专项门禁总表与全局失败矩阵

新增门禁（全部按既有专项门禁工程规范：全新唯一 fixture namespace、可重复执行、不删共享
PostgreSQL volume、timeout/condition 等待、失败清理、输出脱敏）：

```text
make test-credential-vault-expansion   W1：凭据种类/owner/轮换/揭示审计
make test-codex-harness                W1：Codex fixture → Core mTLS → harness-host → 浏览器
make test-mcp-harness                  W1：MCP stdio fixture → Provider 链路
make test-rootless-runtime             W2：真实 rootless Podman 容器 App E2E（宿主前提）
make test-real-supervision             W2：真实观察 → Incident → restart/stop（宿主前提）
make test-telemetry                    W2：otel-collector 采集与 System Monitor 视图
make test-repair-deployment            W2：Incident → Repair Task → Build/Test/Canary/Rollback
make test-app-bridge-full              W3：Bridge 全能力 opaque App 真实链路
make test-declarative-surface          W3：Declarative 渲染与降级
make test-remote-native-surface        W3：浏览器池 + WebRTC 回环验收
make test-semantic-knowledge           W4：embedding + 混合检索
make test-workspace-indexing           W4：挂载源摄取 + tombstone
make test-push-relay                   W5：fixture relay + Web Push + 偏好
make test-mobile-wrappers              W5：Capacitor 构建级门禁
make test-mdns-discovery               W5：mDNS 发现 + 指纹校验 + 配对
make test-desktop-system-apps          W6：系统应用 + Palette + Mission Control E2E
```

全局失败矩阵（各 workstream 矩阵之外，跨链路至少覆盖）：

| 场景 | 必须结果 |
| ---- | -------- |
| 任一门禁在环境缺失宿主上运行 | blocker 记录 + 其余断言仍执行，不整批 FAIL 也不伪造 PASS |
| Core/Reliability/Runtime/Indexer 任一重启 | 所有新增 durable 状态收敛，无重复副作用 |
| 并发同类外部写（idempotency key 竞争） | 恰一次裁决，`Aborted` 语义稳定 |
| 存储不变量损坏 | 净化 `Internal`，不静默修复/覆盖 |
| 依赖进程不可达 | `Unavailable` + 有界降级，恢复后追平 |
| 任何新公开 RPC | pre-decode wire budget（含 gzip）、身份注入、固定错误矩阵、private 路由 404 |

## 明确不在范围内

- 多人协作、多 owner 共享、跨 owner 通知/项目协作；
- 复杂知识图谱、自动语义推理、自动摘要推荐流；
- 完全自动语义修复（L5 数据迁移/权限提升/凭据修改永远人工确认）；
- 多个 Harness 同时协作一个 Project；
- 公网商业信任根（付费中继、公网 CA/域名的对外暴露）、VPN/Tailscale 集成生产化；
- 真实 Codex/OpenAI/Anthropic/DeepSeek 付费调用、真实 APNs/FCM 凭据申请、真实应用商店发布；
- 第七个常驻 WorkOS 进程、跨 schema SQL、第二套 manifest DTO、手改 `gen/`/`src/gen/`/README 状态区；
- 复杂自由窗口动画、多显示器编排。

## 停止并请求用户的条件

仅在以下情况停止整批并请求用户决定；其余一切选择按最保守方案记录依据后继续：

1. 必须复用/删除/改变已有 v1 字段号，无法 additive 表达；
2. 必须修改已执行 migration（`internal/platform/migrations/files/` 001 至执行时最新），而非新增
   forward migration；
3. 必须增加第七个常驻进程或新的外部信任根/付费服务；
4. 需要真实厂商凭据（Codex OAuth client secret、APNs/FCM 账号、真实推送证书）才能继续的阶段——
   先完成全部 fixture 链路再停下；
5. 需要安装宿主软件（rootless Podman、Xcode、Android SDK）、提权或修改内核/systemd/防火墙；
6. 必须删除用户数据/持久卷，或工作树出现无法归属且与本任务直接冲突的用户改动；
7. 仓库证据证明某个模块 authority 归属必须改变，且两种方案会造成实质不同的产品/安全结果。

普通实现选择、命名、测试失败、依赖暂时不可达、工作量大、无 Podman、无 SDK、无真机都不是停止
理由。

## 必须执行的最终验证

每个 workstream 完成时先做本阶段聚焦提交，再从干净 HEAD 执行该 workstream 的专项门禁与
`make check`。全部 workstream 收口时执行完整清单并把真实结果写入任务记录：

```sh
git diff --check
make generate
git diff --exit-code -- gen sdk/protocol/src/gen
make generate
git diff --exit-code -- gen sdk/protocol/src/gen
make check
make test-integration
make test-e2e
make test-credential-vault-expansion
make test-codex-harness
make test-mcp-harness
make test-rootless-runtime
make test-real-supervision
make test-telemetry
make test-repair-deployment
make test-app-bridge-full
make test-declarative-surface
make test-remote-native-surface
make test-semantic-knowledge
make test-workspace-indexing
make test-push-relay
make test-mobile-wrappers
make test-mdns-discovery
make test-desktop-system-apps
make test-adaptive-shell
make test-lan-pairing
make test-notification-center
docker compose config --quiet
```

另外执行：

- `buf lint` 与相对执行时 `main` 的 `buf breaking`；
- `go test -race` 覆盖全部新增 Go package；
- 新增前端模块的 Vitest 单元测试；
- migration checksum pin 与真实 PostgreSQL forward/restart；
- private RPC Gateway 404、identity header stripping、pre-decode gzip bomb 抽查；
- staged diff secret/credential/私钥/大二进制/trace/video/绝对路径扫描；
- 视觉 PNG 尺寸、before/after/current 与 notes 链接核验。

环境缺失导致 BLOCKED 的门禁必须逐条列出精确前提与探测输出，不得省略也不得标 PASS。

## 文档、状态与完成定义

每个 workstream 收口时同步：

- 对应新 ADR（从 0015 起按执行时编号）；
- `docs/architecture/implementation.md`：新增模块边界、表 owner、私有 RPC、能力边界；
- `docs/status.json`：只按真实证据升级；fixture 证明的软件链与宿主/外部账号证明的能力分开写；
  Reliability/Runtime 的 capability 升级必须有真实宿主链路证据；
- 任务记录：六个 workstream 的阶段状态、blocker 区、已验证命令、提交哈希、截图链接、未决风险；
- `docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/` 与 `docs/ui/desktop-web/current/`；
- Makefile 门禁、compose/systemd/env 示例、README 生成结果（只经工具）。

可以标 done 的必要条件：

- 该 workstream 的专项门禁 PASS（或 BLOCKED 有精确前提记录）；
- `make generate` 幂等、`make check`、相关 integration/E2E 通过；
- 新公共行为有单元/集成测试，跨进程用户链路有 E2E 或明确的测试任务；
- UI 变更有 before/after/current 视觉证据；
- 无端到端证据的能力保持 scaffolded/unavailable，任务记录给出可复现阻塞与下一步。

若某 workstream 部分完成，任务记录按子能力逐项标注，不用"代码已写"替代端到端证据；跨会话交接只
依赖任务记录与仓库事实，不依赖聊天记录。

## 最终交接格式

最终回复和任务记录都必须包含：

1. 唯一最终目标的总体达成情况；六个 workstream 逐项的真实状态（working/scaffolded/unavailable/
   blocked-environment）；
2. branch 名、base SHA、按 workstream/阶段的 commit hash，确认只有一个 branch/worktree/writer；
3. 新增 Proto/ADR/migration 清单与每张表的 owner；
4. 每个专项门禁与全量命令的真实 PASS/FAIL/BLOCKED 及证据路径；
5. 视觉证据路径与 viewport 清单；
6. 环境阻塞清单（精确前提、探测命令、失败输出）与建议的用户决策点（真实凭据、SDK 安装、公网
   暴露）；
7. `docs/status.json` 最终裁决，明确哪些能力因宿主/外部账号证据缺失而保持诚实低状态；
8. 未决风险和下一步，不用聊天记录替代仓库事实；
9. 最终 `git status --short --branch`、HEAD，并明确未 merge、未 push、未删除 volume、未使用真实
   secret、未安装宿主软件、未访问真实 Provider/推送服务。

从现在开始：先核对仓库与基线，创建唯一 branch 与总攻任务记录，完成首个 ADR 裁决，然后按
W1 → W2 → W3 → W4 → W6 → W5 的顺序持续实现到完整验收。
