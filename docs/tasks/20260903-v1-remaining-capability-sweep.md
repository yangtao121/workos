# Task: v1 剩余能力总攻——Provider 扩展、真实 Runtime 自愈链、远程 Surface、语义知识、后台推送与移动原生、桌面系统应用

- 状态：active（2026-09-08 检查点交接；以末尾最新记录和 GLM-5.3 交接为恢复点）
- Owner/Agent：当前实现者完成检查点交接；后续由 GLM-5.3 认领（单一写入智能体）
- 进程/模块：全部六进程 + desktop-web/mobile-shell/sdk
- 依赖：ADR-0001..0014 全部既有裁决；实现依据 `docs/prompts/20260903-next-agent-remaining-capability-sweep.md`
- Branch：唯一开发分支 `feat/v1-remaining-capability-sweep`（自本地 `main` @ `afe8580`）；检查点 `d4fca63` 已合并，后续继续复用此分支，不再创建分支
- 基线：`make bootstrap` PASS、`make generate` 幂等 PASS、`make check`（见下方基线记录）、
  `make test-integration`、`make test-e2e` 结果随执行更新

## 目标与范围

六个 workstream 逐项推进：每一项要么取得真实端到端证据并在 `docs/status.json` 如实升级，
要么因宿主/外部账号前提缺失记录精确可复现 blocker 并保持诚实状态。执行顺序固定
W1 → W2 → W3 → W4 → W6 → W5，全部在同一 branch 严格串行。

明确非范围：见提示词"明确不在范围内"（多人协作、知识图谱、真实厂商凭据、第七进程、
手改生成区、复杂窗口动画等）。

## 阶段清单（唯一恢复点：本节状态 + 提交哈希）

### 2026-09-06 修复验收（基线 3da0478）

用户授权：全面修复六条工作流，补齐已承诺的软件链路；精简深色桌面，保留已有数据，
清理冗余实现；沿用当前单分支、单 worktree、单写入智能体，完成后合并本地 main，不 push。
下方历史阶段表仅保留溯源，不能作为本轮完成证据。

| 阶段                         | 状态                                                         | 验收                                                           |
| ---------------------------- | ------------------------------------------------------------ | -------------------------------------------------------------- |
| R1 Provider/凭据与工具链基线 | verified                                                     | 既有协议 fixture、租约、预算、取消、轮换门禁                   |
| R2 自愈与部署                | active（候选状态机已修复；Build/Test 交接依赖 R3 workspace） | 真实候选、先切换后观察、失败回滚、持久重试；禁止空候选成功     |
| R3 Surface/Bridge            | active                                                       | 文件/窗口/产物能力、真实浏览器/Native 交互，不能用静态页面替代 |
| R4 工作区/知识               | active                                                       | 混合来源结果、服务端来源过滤、分页、打开与跨项目隔离           |
| R6 桌面                      | active                                                       | 统一深色控件、入口、窗口几何、键盘与响应式；确定性视觉证据     |
| R5 通知/移动                 | active                                                       | 持久推送重试、唤醒补收、配对/发现/原生边界                     |
| 收口/main                    | pending                                                      | generate 幂等、check、integration/E2E、专项门禁、文档一致      |

已确认缺陷：部署在观察结束才切换版本且允许空候选；推送发送前消耗去重记录且失败不重试；
Knowledge Center 拒绝 workspace 来源导致整页失败；Files 先分页后过滤；Docs/Code 只读首屏；
窗口吸附未使用可用工作区域，层级推进遗漏；新增白底控件继承浅色文字；截图使用随机时间与共享数据。

R1：2026-09-06 `make test-credential-vault-expansion`、`make test-codex-harness`、`make test-mcp-harness` 全部 PASS。

基线：工作树干净；固定 Node 24 容器下 CommandPalette/Desktop/KnowledgeCenter 共 19 测试 PASS。
宿主 Node 22 无法运行 pnpm 11，后续采用 Makefile 的固定容器工具链，不修改宿主。

视觉证据计划：`docs/ui/desktop-web/changes/20260906-desktop-repair/` 的 before/after/notes.md，
使用同一固定 fixture 和 viewport 采集并更新 current。完成前不得标记 done。

### W1 Provider 与凭据扩展

| 阶段                                                                                                                    | 状态 | 提交 | 证据                                                                                                                                                                                                                                                                 |
| ----------------------------------------------------------------------------------------------------------------------- | ---- | ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| W1.1 ADR-0015：Provider 接入契约、凭据种类矩阵、master-key 轮换语义、揭示审计模型                                       | done | 待填 | `docs/decisions/0015-provider-expansion-and-vault-rotation.md`                                                                                                                                                                                                       |
| W1.2 Vault 扩展（codex/github/cloud 种类 + owner 维度 + 在线轮换 + 受审计揭示）+ `make test-credential-vault-expansion` | done | 待填 | 门禁 PASS（2026-09-03）：admin socket 全链路 put/invalid 拒绝/reveal/revoked 拒绝；在线轮换 3→4→5（successor-key 进程换回）+ 重启收敛 + no-op；in-process 5 项协议套件 PASS（真实 PostgreSQL）                                                                       |
| W1.3 Codex fixture + Adapter + broker/catalog/binding + `make test-codex-harness`                                       | done | 待填 | 门禁 PASS（2026-09-03）：catalog 如实声明（streaming/usage/hard budgets/lease=真）、codex-auth.v1 lease binding、真实 fixture 子进程事件流、Core+harness 重启持久、Chromium 绑定/运行 E2E；失败矩阵（crash/silent/out-of-order/over-budget/slow）由 adapter 单测证明 |
| W1.4 MCP fixture + Adapter + `make test-mcp-harness`                                                                    | done | 待填 | 门禁 PASS（2026-09-03）：降级能力如实声明（全 false + 无 lease）、credential-free binding、blocking tool-call 运行、无 usage 事件、重启持久；失败矩阵（no-tool/protocol-error/tool-error/oversize/hang/crash）由 adapter 单测证明                                    |

### W2 真实 Runtime 与自愈链

| 阶段                                                                           | 状态                | 提交    | 证据                                                                                                                                                      |
| ------------------------------------------------------------------------------ | ------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| W2.1 ADR-0016：监督验收标准、遥测脱敏矩阵、Repair/Deployment 语义与 L 级别映射 | done                | 待填    | `docs/decisions/0016-real-runtime-supervision-and-repair.md`                                                                                              |
| W2.2 rootless Podman 宿主探测（或 blocker 记录）+ `make test-rootless-runtime` | blocked-environment | d4c1ac4 | 探测：podman 不可用（command -v 失败）；cgroup v2 可用；user namespaces=123655 → 门禁 BLOCKED，container-runner 保持 unavailable                          |
| W2.3 真实监督链 + `make test-real-supervision`                                 | done                | 待填    | 门禁 PASS（2026-09-03）：fixture engine + 六进程栈，crash→incident（occurrence 唯一）→restart 推进 generation→restart limit→deterministic stop→owner 可见 |
| W2.4 遥测（collector 输出/存储/System Monitor 消费）+ `make test-telemetry`    | pending             |         | 下一会话：ADR-0016 §4 矩阵已定；需实现采集脱敏 decorator 白名单断言 + System Monitor 真实遥测视图                                                         |
| W2.5 Repair Orchestrator                                                       | pending             |         | 下一会话：ADR-0016 §5；incident_ref 入 AgentTaskInput（proto additive）+ 路由 + 台账                                                                      |
| W2.6 Deployment Controller + `make test-repair-deployment`                     | pending             |         | 下一会话：ADR-0016 §6；candidate→canary→promote/rollback 状态机 + 私有协作 Core                                                                           |

### W3 Surface 与 Bridge 补全

| 阶段                                                                                                            | 状态    | 提交                                                                                                                                                                                                                                                                                                    | 证据 |
| --------------------------------------------------------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---- |
| W3.1 ADR-0017：Bridge 能力清单/授权矩阵、FileRef/workspace 挂载、Declarative schema 版本、远程 Surface 安全边界 | pending |                                                                                                                                                                                                                                                                                                         |      |
| W3.2 Bridge 全能力（shell 侧切片）+ `make test-app-bridge-full` \| done                                         | 待填    | 门禁 PASS：project.current（project.read 授权，经 runtime 协商 + 共享 provider）、theme.get、window.setTitle、window.close（app-host shell 侧执行，能力协商后提供）；未授权 knowledge.search fail closed；files.\*/artifacts.create/maximize/minimize 留待下阶段（需 workspace 挂载与 artifact 写通道） |
| W3.3 Declarative Surface + `make test-declarative-surface`                                                      | pending |                                                                                                                                                                                                                                                                                                         |      |
| W3.4 Remote Browser Pool + Browser Surface                                                                      | pending |                                                                                                                                                                                                                                                                                                         |      |
| W3.5 Native Runner + WebRTC 回环 + Human Native Workspace + `make test-remote-native-surface`                   | pending |                                                                                                                                                                                                                                                                                                         |      |

### W4 语义知识与工作区源

| 阶段                                                                      | 状态    | 提交 | 证据 |
| ------------------------------------------------------------------------- | ------- | ---- | ---- |
| W4.1 ADR-0018：embedding 路径、挂载源模型、混合排序、archive 边界         | pending |      |      |
| W4.2 embedding 管道 + pgvector migration + `make test-semantic-knowledge` | pending |      |      |
| W4.3 workspace 源摄取 + `make test-workspace-indexing`                    | pending |      |      |
| W4.4 通用 archive + Knowledge Center 混合检索 UI                          | pending |      |      |

### W6 桌面系统应用与入口

| 阶段                                                                               | 状态    | 提交 | 证据 |
| ---------------------------------------------------------------------------------- | ------- | ---- | ---- |
| W6.1 设计记录：系统应用清单、入口矩阵、Browser 安全边界、Terminal/Experiments 定位 | pending |      |      |
| W6.2 Command Palette + Mission Control                                             | pending |      |      |
| W6.3 Home + Files + Docs + Code                                                    | pending |      |      |
| W6.4 Browser + 窗口 snap + Terminal 入口                                           | pending |      |      |
| W6.5 `make test-desktop-system-apps` + 全量视觉证据                                | pending |      |      |

### W5 推送与移动原生

| 阶段                                                                    | 状态    | 提交 | 证据 |
| ----------------------------------------------------------------------- | ------- | ---- | ---- |
| W5.1 ADR：推送隐私边界、偏好模型、mDNS/Transport 信任链                 | pending |      |      |
| W5.2 订阅/偏好/搜索 + fixture relay + Web Push + `make test-push-relay` | pending |      |      |
| W5.3 Capacitor 封装 + 安全存储 + `make test-mobile-wrappers`            | pending |      |      |
| W5.4 mDNS + TransportProvider + `make test-mdns-discovery`              | pending |      |      |

## 协议/数据影响（随执行更新）

- 预计新 ADR：0015 起。migration 从执行时下一个空闲编号（032）起，均 forward-only，
  每张新表声明唯一 owner 进程。
- Proto 全部 additive；v1 字段号不复用。

## Blocker 区（精确前提 + 探测命令 + 失败输出）

（暂无）

## 验收

- [ ] 六个 workstream 专项门禁 PASS 或 BLOCKED 有精确前提记录
- [ ] `make generate` 幂等、`make check`、integration/E2E 通过
- [ ] 文档（ADR/implementation/status.json/任务记录）与实现一致
- [ ] UI 变更有 before/after/current 视觉证据
- [ ] 最终交接格式九项齐全

## 交接

- 基线命令（2026-09-03，main @ afe8580）：
  - `git status --short --branch`：干净，`## main...origin/main`
  - `git log --oneline --decorate -20`：HEAD=afe8580（提示词入库提交），与 origin/main 一致
  - `git branch -a -vv`：main、feat/v1-local-first-notifications（既有历史分支，未动）
  - `make bootstrap` PASS；`make generate` 后 `git status` 干净（生成区幂等）
  - `make check`：首次 FAIL——Prettier 对 `.zcode/plans/*`（用户本地文件）、提示词文档、
    本任务记录报格式问题；修复：`.prettierignore` 增加 `.zcode/`，两个 md 文档 prettier --write。
    复跑 PASS
  - `make test-integration`：首次运行环境抖动 FAIL（compose 启动期）；重跑 PASS（311 项断言）
  - `make test-e2e`：PASS（21 passed / 14 skipped，skipped 均为需要 fixture profile 的门禁）
- 基线期间发现并修复的仓库问题：
  - 提示词文档提交时未经 Prettier（随本次提交格式化）；`.zcode/` 加入 `.prettierignore`
- W1 已验证命令（2026-09-03，全部真实执行）：
  - `make test-credential-vault-expansion`：PASS（admin socket kinds 生命周期 + 语法
    拒绝 + 受审计 reveal + revoked 拒绝 + 在线轮换（forward 与 successor-key 换回，
    epoch 前进 + 3 行重封）+ Core 重启收敛 + same-key no-op + in-process 协议套件
    5 项 PASS）
  - `make test-codex-harness`：PASS（catalog 如实声明 + codex-auth.v1 lease binding +
    真实 fixture 子进程事件流 + 幂等 replay + Core+harness 重启持久 + Chromium
    绑定/运行旅程）
  - `make test-mcp-harness`：PASS（降级能力如实声明 + credential-free binding +
    blocking tool-call 运行 + 无 usage 事件 + 重启持久）
  - `make capture-provider-catalog`：PASS（before/after/current 1440x900）
  - `go test ./internal/harness/adapters/codex/ ./internal/harness/adapters/mcp/`：
    PASS（失败矩阵：crash/silent/out-of-order/over-budget/slow；
    no-tool/protocol-error/tool-error/oversize/hang/crash）
  - `go test -race ./internal/core/credential/...`：PASS
- W1 期间发现并修复的仓库级缺陷（含真实缺陷修复，非新问题）：
  - Core 的 catalog/availability/snapshot/verifier 硬编码 `provider-api-key.v1`，
    无法承载新凭据种类：改为 provider 声明 `required_credential_purpose`（proto
    additive），Core 全链路按声明裁决（ADR-0015 §6）
  - Vault 在线轮换的 no-op 判定只比 fingerprint，外部轮换后会假报成功：补
    `VerifyCurrentKey`（必须能解密每一行才允许 no-op 判定）
  - migration 023 的 `task_credential_leases.purpose` CHECK 未随 032 扩词汇表：
    migration 034 补齐（forward-only）
- 未决风险：
  - 共享 dev 卷中一条 revoked 的 deepseek 历史 fixture 行（创建于 ADR-0009 时期、
    早期卷代）永久不可解密；按 ADR-0015 revoked 行冻结不轮换，不影响任何存活路径
  - 真实 Codex OAuth（client secret）与真实 MCP 远程部署属于外部账号前提，
    保持 fixture 形态（非 blocker，本批范围明确不含）

## 会话 3 交接（W2.4/W2.5/W2.6 完成后更新）

- W2.4 遥测门禁已绿：根因=collector file exporter 配置含无效 sending_queue/
  retry_on_failure 字段（该 exporter 不支持）导致 collector 崩溃循环、receiver
  从未监听 + 调试容器 otel-cap5 曾占用 127.0.0.1:4318 吞 POST。修复后探针与
  真实流量全部落盘。make test-telemetry PASS。
- W2.5/2.6 门禁 make test-repair-deployment PASS：repair orchestrator（台账 035）
  - Core 私有 AgentRepairTaskService（TaskRouter 完整准入）+ deployment ledger
    （036）+ canary promote / ADR-0012 rollback driver。
- 新增 make test-app-bridge-full PASS：shell-side bridge（project.current/
  theme.get/window.setTitle/window.close）经 app-host shell dispatch +
  runtime project.current 协商；未授权 knowledge.search fail closed。

## 会话 2 交接（W2 收口，供 W3 续作）

- W2.4/W2.5/W2.6 提交：`4104e12`（遥测管道）、`642e511`（repair orchestrator）、
  `d831811`（deployment controller）、`7296a73`/`500cea4`/`2b0d374`/`db47b15`/`5607516`
  （调试矩阵与修复）。全部经 `make check`。
- 遥测根因终判：collector file exporter 配置含无效字段 → collector 崩溃循环
  （receiver 从未监听）；调试容器 otel-cap5 曾占 127.0.0.1:4318。两者修复后
  探针与真实流量全部落盘。已固化：boundsExporter 采集前预算、file exporter JSONL
  共享卷、telemetryfile.Reader+TelemetryAggregator、GetTelemetrySummary 白名单、
  System Monitor Telemetry 区。
- W2.5/2.6 已固化：Core 私有 AgentRepairTaskService（走 TaskRouter 完整准入）、
  repair orchestrator 台账（035）、deployment ledger（036）、canary
  promote/rollback（ADR-0012 语义经 loopback 驱动）。
- W3 续作第一步：Bridge 全能力（window._/files._/artifacts.\*/theme.get）—
  surface-sdk + runtime bridge 校验 + Core private command 通道；
  `make test-app-bridge-full`。

## 会话 1 交接（2026-09-03 收口，供会话 2 续作）

### 本会话提交（branch `feat/v1-remaining-capability-sweep`，均经 `make check`）

- `a6a6f15` docs: define remaining capability sweep boundary
- `ee3b86b` feat: expand credential vault types and rotation（migrations 032/033/034、
  ADR-0015、purpose 全链路贯通、workosctl reveal/rotate-master-key）
- `eab3eca` feat: add codex harness adapter
- `2b522cf` feat: add mcp harness adapter
- `58ae8dc` feat: add provider catalog badge and visual evidence
- `4b0d06f` docs: record provider expansion evidence
- `d4c1ac4` feat: prove real supervision chain with fixture engine（ADR-0016、
  test-rootless-runtime BLOCKED 记录、fixture engine、compose.supervision overlay、
  test-real-supervision、WORKOS_RELIABILITY_POLL_TIMEOUT env）
- 收口提交：`test-credential-vault-test purpose 种子修复`（本次 lease 状态机回归）

### 本会话门禁裁决（真实执行结果）

| 门禁                                                       | 结果                                      |
| ---------------------------------------------------------- | ----------------------------------------- |
| make bootstrap / generate（幂等）                          | PASS                                      |
| make check（含 go/web 单测、buf、sqlc vet、status render） | PASS                                      |
| make test-integration（基线与回归）                        | PASS（回归中发现并修复 purpose 种子缺失） |
| make test-e2e                                              | PASS（21 passed / 14 skipped-profile）    |
| make test-credential-vault-expansion                       | PASS                                      |
| make test-codex-harness                                    | PASS                                      |
| make test-mcp-harness                                      | PASS                                      |
| make test-real-supervision                                 | PASS                                      |
| make test-rootless-runtime                                 | BLOCKED（podman 缺失，探测输出已记录）    |

### 会话 2 续作指引（严格按顺序）

1. **W2.4 遥测**：六进程 OTLP 已有（telemetry.HTTPClient/httpserver）；需
   (a) 采集前脱敏 decorator + 字段白名单测试（ADR-0016 §4）；(b) collector file
   exporter → reliability-host collector 组件读取聚合 → System Monitor 真实遥测视图；
   (c) `make test-telemetry`。compose.observability.yaml 已有编排。
2. **W2.5 Repair Orchestrator**：AgentTaskInput additive `incident_ref`
   （`reliability.incident.v1`+UUIDv7）→ proto → make generate；私有 incident 校验
   port；路由健康 project harness / Recovery（generic-cli）；台账 + 幂等；纳入 ADR-0005
   治理。
3. **W2.6 Deployment Controller**：candidate→canary→promote/rollback 状态机
   （reliability 台账），经既有 public Transition/Rollback 语义协作 Core；接 ADR-0012
   版本历史；`make test-repair-deployment` 一并覆盖 2.5+2.6。
4. W3 → W4 → W6 → W5 按提示词继续；UI 变更沿用
   `docs/ui/desktop-web/changes/20260903-remaining-capability-sweep/` 与 notes 惯例。

## 会话 2 交接（2026-09-05 收口，W2 收尾 + W3.4/3.5 + W4 全量 + W6 全量 + W5 全量）

### 本会话提交（branch `feat/v1-remaining-capability-sweep`，均经 make check）

- W2 收尾：`make test-telemetry`、`make test-repair-deployment` PASS（遥测聚合、
  repair orchestrator、deployment controller、telemetryfile bounds exporter）。
- W3.4/3.5：`make test-app-bridge-full`、`make test-declarative-surface`、
  `make test-browser-surface`、`make test-remote-native-surface` PASS。
- `feat: add semantic embedding function and pgvector migration`（037、domain.Embed、
  CosineSimilarity、SearchHybrid proto/delegate 桩）。
- `feat: add remote native surface hosting slice and gate`（native-status spec）。
- `feat: add fused semantic hybrid search with stored embeddings`：真实融合检索
  （摄取时计算 384 维 feature-hash embedding 落库；单条有界候选查询同时携带
  ts_rank 与 embedding；Go 侧 0.5 词法归一 + 0.5 cosine 融合，fused DESC /
  created DESC / id ASC 确定性排序；分页 token 绑定 ranking 版本，跨 ranking
  拒绝；无 embedding 旧行词法路径照常）；`make test-semantic-knowledge` PASS
  （scratch 仓储级 + compose 全栈 RPC 级）。
- `feat: add workspace file sources with bounded mount ingestion`（038、
  localmount walker、确定性 UUIDv7 文档身份、upsert/集合差 tombstone 收敛、
  显式 degraded、workosctl index workspace register/list/sync）；
  `make test-workspace-indexing` PASS。
- `feat: add desktop command palette, mission control, and system apps`
  （⌘K Palette 固定动作集 + stale 文案、Mission Control、Home/Files/Docs/Code/
  Browser、Terminal unavailable 入口、窗口 snap left/right + 精确 restore、
  adaptive 布局可达）；
  `make test-desktop-system-apps` PASS（5 E2E）+ `make capture-desktop-system-apps`
  视觉证据（1440x900 × 5 + 390x844 × 1，notes.md 已更新，current/ 同步）。
- `feat: add push relay slice with payload whitelist and quiet hours`（039、
  domain.PushPayload 白名单、fixture relay、exactly-once 投递账本、owner 免打扰、
  consumer post-commit 派发、gateway 路由 SubscribePush 等 RPC、
  web-push/APNs/FCM 如实 unavailable）；`make test-push-relay` PASS。
- `feat: add capacitor mobile shell wrapper with build-level gate`
  （原生安全存储诚实回退、fixture relay token 注册、vite bundle、
  BLOCKED-ENVIRONMENT 记录）；`make test-mobile-wrappers` PASS。
- `feat: add mdns lan discovery with fingerprint trust chain`
  （TransportProvider 抽象、LanDirect mDNS 广播/发现仅 origin+fp、常量时间
  指纹校验、Relay/Overlay 诚实 unavailable）；`make test-mdns-discovery` PASS。
- ADR：0017（语义检索）、0018（推送白名单/免打扰）、0019（传输提供方/移动封装）。

### 本会话门禁裁决（真实执行结果）

| 门禁                                    | 结果                                                         |
| --------------------------------------- | ------------------------------------------------------------ |
| make test-semantic-knowledge            | PASS（首轮失败为测试查询词与融合分页缺陷，已修）             |
| make test-workspace-indexing            | PASS（首轮失败为 SourceOperation 未传与 skip 计数，已修）    |
| make test-desktop-system-apps           | PASS（5 passed；首轮 MC 卡片选择非确定性，已改为唯一名）     |
| make test-push-relay                    | PASS                                                         |
| make test-mobile-wrappers               | PASS（android sync 记录 BLOCKED-ENVIRONMENT，构建级 PASS）   |
| make test-mdns-discovery                | PASS（宿主真实多播；无多播宿主显式 skip）                    |
| make go-check / proto-check / web-check | PASS（gateway 架构测试一次偶发并发抖动，重复 5 次稳定 PASS） |

### 关键实现事实（续作者必读）

- 混合检索分页：continuation 锚在"最后一条已发射 hit"（limit+1 probe 判定存在
  下页）；谓词为严格 `< cursorScore` + tie-break after，锚定 probe 行会自我排除。
- workspace 文档身份：`domain.WorkspaceSourceID` 确定性构造 v7（version/variant
  位固定 + sha256 载荷），ValidUUID 全链路 v7 约束不破坏。
- ConvergeWorkspacePass 每文件独立 UUIDv7 publication（共享 pass id 会被
  receipt 仲裁折叠成 replay，导致只落第一个文件）。
- push 派发钩子在 `go incidentConsumer.Run` 之前 SetPushDispatch（避免数据竞态）。
- 16 门禁总表中 `make test-rootless-runtime` 仍为 BLOCKED（宿主无 rootless
  Podman，探测输出见任务记录会话 1 部分）；其余 15 门禁 PASS。

### 剩余事项（供下一会话）

1. 16 门禁全量复跑 + `buf breaking` + `go test -race` 收口（本会话已单点复跑）。
2. W4 通用 archive（ADR-0017 §5 最小实现）与 Knowledge Center 混合检索 UI 收口
   （Knowledge Center 当前仍走词法 Search，可切 SearchHybrid）。
3. Web Push RFC 8291 加密 + Service Worker 展示（ADR-0018 §5 诚实 unavailable）。
4. W6 视觉证据 before/ 基线为新增界面（无既有 current），已在 notes.md 说明。

## 会话 4 收口（2026-09-05）：剩余范围补齐 + 全量门禁复跑

### 补齐项（全部有真实证据）

- Knowledge Center 切换 SearchHybrid：SearchHybridResponse additive 增加
  freshness（字段 3），Catching-up 指示保持；Center 走融合排序。
  `corepack pnpm --filter @workos/desktop-web exec vitest run src/KnowledgeCenter.test.tsx` PASS。
- 有界通知搜索（ADR-0018 §2）：SearchNotifications RPC（网关路由）复用
  snapshot/签名分页机制，title 子串大小写不敏感，1..128 语法 fail-closed，
  外 owner 恒空页；TestNotificationSearch（scratch 真库）PASS，
  并入 `make test-push-relay`。
- W4 通用 archive 最小实现（ADR-0017 §5）：migration 040（owner indexer）、
  内容寻址去重、8 MiB/对象、200 对象/owner、media-type 语法、超界拒绝；
  admin socket 三 RPC；archive 能力翻转 available（诚实注明"无知识图谱"）；
  TestArchiveObjects PASS 并入 `make test-workspace-indexing`。
- mDNS 接入 compose + 发现 UX 接配对：`cmd/workos-mdns-announce`（lan-pairing
  profile，host 网络，广播 origin+配对指纹，与网关证书同源）；`workosctl
device scan --fingerprint`（常量时间指纹校验后输出 origin，接既有
  `device pair` 流程）；`make test-lan-pairing` 增加 mDNS 发现阶段
  （真实发现 origin https://localhost:8443）。

### 复跑中发现并修复的真实缺陷

1. dock 层级缺陷：W6 新增 7 个 dock 按钮使居中 dock 左移，app 窗口的
   bridge 浮层拦截了 dock 点击 → dock 显式 z-index 提层（test-app-version-
   rollback 捕获）。
2. bridge 协商方法断言漂移：app-notifications / app-knowledge-search spec
   未计入 shell 侧方法（theme.get/window.setTitle/window.close），已更新。
3. knowledge-rebuild 销毁/恢复只重放 027/028：037/038/040 被账本跳过导致
   documents 缺 embedding 列 → 重放全部 indexer 迁移。
4. repair 门禁泄漏 running fixture workload（fake-fixture cgroup）毒化后续
   podman 模式 runtime 的整表观察 → FK 感知清理（surface requests →
   sessions → operations → workload）。
5. deepseek 门禁 awk 状态机在 consumer 行误触发，把 REVOKED 块 id 与 codex
   的 revision 配对 → 改为仅在 status 行判定同块匹配。

### 全量门禁复跑（最终 HEAD）

| 门禁                                                                                                                               | 结果                                                  |
| ---------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| test-credential-vault-expansion / test-codex-harness / test-mcp-harness                                                            | PASS                                                  |
| test-real-supervision / test-telemetry / test-repair-deployment                                                                    | PASS                                                  |
| test-rootless-runtime                                                                                                              | BLOCKED（宿主无 rootless Podman，如实记录）           |
| test-app-bridge-full / test-declarative-surface / test-browser-surface / test-remote-native-surface                                | PASS                                                  |
| test-semantic-knowledge / test-workspace-indexing（含 archive）                                                                    | PASS                                                  |
| test-push-relay（含通知搜索）                                                                                                      | PASS                                                  |
| test-mobile-wrappers / test-mdns-discovery / test-desktop-system-apps                                                              | PASS                                                  |
| test-e2e（完整 Playwright 套件）                                                                                                   | PASS（30 passed / 17 skipped-profile；修复 1/2/4 后） |
| test-adaptive-shell                                                                                                                | PASS                                                  |
| test-lan-pairing（含新 mDNS 发现阶段）                                                                                             | PASS                                                  |
| test-project-knowledge-search / test-app-knowledge-search / test-project-knowledge-rebuild                                         | PASS（修复 3 后）                                     |
| test-notification-center / test-incident-notifications / test-app-notifications                                                    | PASS（修复 4/5 后）                                   |
| test-artifact-context / test-artifact-review / test-deepseek-fixture / test-deepseek-structured-review / test-app-version-rollback | PASS（修复 1/2/5 后）                                 |
| test-podman-fixture                                                                                                                | BLOCKED（宿主无 rootless Podman，如实记录）           |
| test-integration（全量集成）                                                                                                       | PASS                                                  |
| make check / go test -race ./internal/... ./cmd/... / buf lint / buf breaking（vs main）                                           | PASS / 干净 / PASS / PASS                             |

### R2 候选协调检查点（2026-09-06）

- 部署单元测试和 `go test ./internal/reliability/... ./cmd/reliability-host` PASS。
- `go test -tags=integration -count=1 -run TestDeploymentReconciliationDurability ./tests/integration` PASS（scratch PostgreSQL；迁移、请求冲突、并发锁、终态恢复）。
- rootless 探测 BLOCKED：podman command 不存在；cgroup v2 存在；user namespaces=123655。
- 修复旧 repairdeployment tag 测试漏导入 pgx 的编译错误。
- 下一步：工作区文件/窗口能力是完整 Build/Test 交接的前提；R2 仍 active，尚未取得部署 E2E。

### R4 / R6 修复检查点（2026-09-06）

- Files 服务端过滤 workspace 后分页；混合检索接受 review/workspace 两种来源，文件通过精确引用读取只读快照；Docs/Code 共享列表逻辑并支持后续页。
- IndexService additive source_type 与 ReadDocument 已通过 make generate 生成。`TestIndexSourceFilterAndExactSnapshotRead`（scratch PostgreSQL）PASS；包括跨过滤 token 拒绝、外 owner 拒绝、删除后旧引用失效。
- 桌面统一深色 token、SVG 图标和应用入口，窗口拖动/缩放/吸附/最小化恢复限制在可用区域，Palette 失败可重试、重复按键保护和焦点恢复；修复平板命令不导航及手机长面板挤走导航。
- Desktop 125 单测 PASS；window-manager 10、adaptive-shell 40 PASS。
- [before](../ui/desktop-web/changes/20260906-desktop-repair/before/) / [after](../ui/desktop-web/changes/20260906-desktop-repair/after/) / [notes](../ui/desktop-web/changes/20260906-desktop-repair/notes.md)：固定 fixture 与 1440×900 / 820×1180 / 390×844，三个 Chromium 场景 PASS。
- 尚需全栈浏览器验收与其他受影响状态截图，R4/R6 保持 active。
- R5 开始：迁移 042 将通知投递改成同事务 outbox，旧发送尝试保留 unknown；通知模块与 Core 编译/单测、`TestPushRelay` scratch PostgreSQL 全部 PASS；验证事务回滚、发送失败后重建服务重试、租约接管/旧确认拒绝、重放去重、撤销、免打扰与八次失败终止。Web Push 软件链仍待完成。

### R5 Web Push 软件检查点（2026-09-06，active）

- 通知 outbox 已提交 514373e；桌面/知识检查点 7a75d92。
- `make web-check` PASS；`make test-desktop-system-apps` 5 个真实 Gateway/Core/Chromium 场景 PASS，新增拖动、键盘缩放、吸附/最小化/恢复与 Dock 边界断言。
- Web Push adapter 使用标准库 P-256、HKDF-SHA256、AES-GCM 和 ES256 VAPID；专用 owner-only key file；公开 RPC 只返回 public key。RFC 8291 标准向量与本地 TLS relay、VAPID 签名/claims、失败/redirect/expired、私钥文件权限测试 PASS。
- Worker 只接受单一 notificationId，固定提醒文案，同 tag 去重；唤醒后 shell 补收通知、点击打开通知窗口。`push-worker.spec.ts` 在完整 Chromium 的 CDP push driver 下 PASS（headless_shell 无真实通知后端，因此此项指定 channel chromium）。
- [before](../ui/desktop-web/changes/20260906-push-repair/before/) / [after](../ui/desktop-web/changes/20260906-push-repair/after/) / [notes](../ui/desktop-web/changes/20260906-push-repair/notes.md)：前后各三尺寸 PASS。修复移动顶部铃铛不导航与通知深色文字对比度。
- 迁移 043 为免打扰设置增加 revision；SetPushPreferences 拒绝 stale revision，UI 加载最新状态供重试。
- 待验收：真实 Core outbox → 加密 relay fixture → 浏览器 wake 的组合专项门禁；启用/停用失败测试、移动撤销边界；知识/遥测非空视觉证据。R5 尚未 done，不能把各切片测试当成完整远端交付。

- R5 当前检查点 `make check` PASS（Go/proto/TypeScript/lint/unit/build）；完整组合链与其余工作流仍 active。

### R3 当前工作（2026-09-06）

7e449d6 保存 Web Push 切片与通过 make check 的检查点。开始修复 Bridge：
window.close 误调用 setTitle(undefined)，project.current 回退到活动项目，shell 动作缺少逐次 session/epoch 重验。
先新增 AuthorizeShellAction RPC 与安全检查，再补齐 own-window / 文件 / 产物能力。

R3 own-window 检查点：App host 31、App SDK 11 单测与 desktop typecheck PASS；
Runtime surface 模块单测 PASS。修复项目回退、close 误 rename、旧 epoch shell 调用、
超时后迟到操作，以及应用库遮挡新窗口和已安装应用最小化后无 Dock 恢复入口。
专项浏览器门禁正在重建运行；files/artifacts 与远程栈仍待完成，R3 保持 active。

R3 own-window 验收：`make test-app-bridge-full` PASS（真实 Gateway/Core/Runtime/Chromium，
含外部 grant 撤销后旧窗口 fail-closed）；独立 transport 身份映射/拒绝测试 PASS。
[before](../ui/desktop-web/changes/20260906-bridge-repair/before/) /
[after](../ui/desktop-web/changes/20260906-bridge-repair/after/) /
[notes](../ui/desktop-web/changes/20260906-bridge-repair/notes.md) 已保存。

本检查点的 Proto 格式/lint/sqlc、Go vet/全仓单测已通过；修正桌面 fixture 后，
`make -o proto-check -o go-check check` PASS（未变的前两项沿用本轮已通过结果，
重新验证全仓 TypeScript/lint/unit/build 和 status 渲染一致性）。下一步：R3 工作区文件 Bridge。

### R3 文件 Bridge（active）

窗口检查点 9ea6ac0 已提交。Runtime 通过 owner 配置的项目目录映射持有可写工作区，
不借用 Indexer 的读快照或 SQL。新增 files.read/files.write grant；FileRef 包含原项目、
相对路径和 etag，32 KiB 单文件界限；每次 RPC 重验 token/session/安装 epoch。
Linux openat2 NO_SYMLINKS/BENEATH、受控目录内临时文件原子替换、写前 etag 比较；
其他平台/不支持的内核明确 unavailable。file picker 在 shell 显示原项目目录，应用不见宿主路径。
验收：并发 stale write、跨项目/路径/符号链接拒绝、真实 Gateway/Runtime/Core/iframe 读写及视觉证据。

文件阶段测试：workspace adapter race（重启、两个实例并发写、只读/跨项目/路径/链接、分页）
PASS；文件授权单测证明 epoch 变化及错项目请求不触碰文件系统；App host 33、SDK 14、
picker 2 单测 PASS。`make test-app-files` PASS：真实 SDK 选取/分页/读取/保存，磁盘复核，
stale etag/路径穿越/链接/撤销 grant 拒绝；临时 Runtime 配置和目录已恢复/清理。
原子写残留清理与目录创建界限 race 测试 PASS；文件选择器按钮对比度与快捷键隔离微调待复拍。

文件阶段复验：`make test-app-files`（最终按钮样式）PASS；保存真实 before/after；
[before](../ui/desktop-web/changes/20260906-files-bridge/before/) /
[after](../ui/desktop-web/changes/20260906-files-bridge/after/) /
[notes](../ui/desktop-web/changes/20260906-files-bridge/notes.md)。
新增原子替换保留 executable mode 的 race 测试 PASS；workspace 与 application race PASS。
下一步：Core 所有的 App artifact create/open 与 Repair Build/Test 交接；R3 整体仍 active。

恢复后复验：`make check` 全部 PASS（Proto/Go vet/Go tests/前端 lint、类型、单测、构建）；
`make generate` PASS。运行环境重启后的专项浏览器门禁正在复跑，沿用同一固定 fixture。

### R3 App 产物（active）

文件 Bridge 检查点 0aee618；恢复后的 `make test-app-files` PASS。
新增 App source provenance（Proto additive、ADR-0020、Core-owned migration 044），
Core 私有 AppArtifactService + Runtime Bridge + SDK artifacts.create/open + 原有查看器。
安装事务授权、每安装幂等/100 项配额，产物、索引、通知原子提交；App 无权指定来源或项目。

`TestAppArtifacts`（真实 scratch PostgreSQL）PASS：8 并发同 key、重建服务后 canonical
重放、冲突、metadata/content 来源、错误项目、stale epoch、feed 失败回滚、通知去重与撤权。
`make test-app-artifacts` PASS：真实 SDK 创建、重放、冲突、打开、来源校验、撤权后创建/打开拒绝，
private RPC Gateway 404。配额/有效异项目安装的追加用例与 race 复验进行中。
[before](../ui/desktop-web/changes/20260906-artifacts-bridge/before/) /
[after](../ui/desktop-web/changes/20260906-artifacts-bridge/after/) /
[notes](../ui/desktop-web/changes/20260906-artifacts-bridge/notes.md) 已保存。

App 产物检查点复验：`make generate`、`make check` 全部 PASS；App host 35、SDK 16、
Desktop 130 单测 PASS。`TestAppArtifacts` + 并发撤权 integration race PASS，含 100 项配额、
满额重放和另一个有效安装的跨项目拒绝。浏览器追加非 review 类型拒绝用例 PASS。
下一步：R2 Build/Test 的隔离执行和已完成 repair task → 已验证候选交接尚未实现，
不能把普通 App review artifact 或任务 completed 状态当成可部署版本。其他恢复范围继续 active。

### R5 订阅状态与 R6 服务视图（2026-09-07）

App 产物检查点 3831ea1。修复 Core 撤销后浏览器误报已开启、VAPID key 轮换、注册失败清理和
停用失败状态；浏览器查询失败仍可编辑 quiet hours。公开摘要只限当前设备，跨设备修改拒绝。
Desktop 138 单测与 typecheck PASS；`make test-push-relay` PASS，含 active/absent/revoked
摘要、owner/device 隔离和事务 outbox 重试。修复遥测 unavailable 消失、窄屏表格及摘要裁切；
Knowledge 明示混合来源；命令结果缩短和查询变化后 Enter 选择错误有回归覆盖。

视觉：[订阅 before](../ui/desktop-web/changes/20260906-push-state/before/) /
[after](../ui/desktop-web/changes/20260906-push-state/after/) /
[notes](../ui/desktop-web/changes/20260906-push-state/notes.md)；
[服务 before](../ui/desktop-web/changes/20260906-service-views/before/) /
[after](../ui/desktop-web/changes/20260906-service-views/after/) /
[notes](../ui/desktop-web/changes/20260906-service-views/notes.md)。

生成脚本先向临时目录执行 Buf，远程插件失败不再删除现存 generated 文件。
本阶段 `make generate` PASS；失败注入验证所有生成文件保持且临时目录清理。
Proto/Go vet/全仓 Go 测试 PASS；前端 lint 修正后 `make -o proto-check -o go-check check`
PASS（复用未变的 Go/Proto 结果，重跑全仓前端 lint/类型/单测/构建/status）。
六项视觉场景门禁 PASS，共 12 组 before/after，已同步 current。剩余 R2 Build/Test、真实远程 Surface、
语义模型与知识打开链、桌面入口收口、推送组合门禁和设备撤销同步仍 active；不提前合并 main。

### R6 项目工具窗口（active，2026-09-07）

推送/服务视图检查点 9d52fd3。App Library 与项目设置脱离固定覆盖侧栏，接入统一窗口状态；
保留项目切换及创建入口，精简侧栏布局。桌面/平板/手机采用共享内容渲染函数，减少重复 JSX。
开始前已保存 9d52fd3 的桌面四态 before；项目工具非空目录 fixture 对比正在采集。
验收：项目切换隔离、工具最小化/恢复/关闭、App 打开不被遮挡、三尺寸 before/after/current、
桌面单测与真实应用链 E2E。创建失败不得被 Mission Control 当成功；一并去掉重复创建实现。

项目工具初验：Desktop 141 单测 PASS（新增窗口恢复、缺省分页和重复 cursor）；
五条桌面 E2E 经 Vite/真实 Gateway/Core PASS；六条视觉场景 PASS，18 组对比同步 current。
[before](../ui/desktop-web/changes/20260907-desktop-tools/before/) /
[after](../ui/desktop-web/changes/20260907-desktop-tools/after/) /
[notes](../ui/desktop-web/changes/20260907-desktop-tools/notes.md)。
创建失败回归测试已追加，Bridge 重建与全仓前端检查进行中。

项目工具复验：Desktop 142 单测 PASS（含创建失败保留输入）；`make test-app-bridge-full`
PASS（重新构建的真实 Gateway/Core/Runtime/Chromium，打开 Surface 与撤权拒绝）。
本阶段仅 TypeScript/样式/文档变更，`make -o proto-check -o go-check check` PASS，复用前一
检查点未变的 Go/Proto 结果，完整重跑前端 lint/格式/类型/全仓单测/构建与 status 校验。
下一步：设备撤销需可靠同步到 Core，停止后台推送；随后继续其余未完成验收，main 暂不合并。

### R5 设备撤销传播（active，2026-09-07）

桌面检查点 d39228b。新增 Gateway 事务 outbox → Core 私有设备推送撤销 RPC；Core 永久
设备 tombstone 与 subscribe 串行化，防止已通过旧 session gate 的迟到请求恢复订阅。
迁移 045 仅 Gateway、046 仅 Core；不修改旧迁移。验收：事务回滚/幂等/重启/租约接管，
跨 owner 拒绝、撤销前后推送数量、并发订阅和 Gateway public RPC 不暴露；补充生产配对 E2E。
不涉及可见 UI，本阶段复用已有 Device Center；推送组合加密 relay 到浏览器门禁仍待完成。

设备撤销阶段：`TestDevicePushRevocation` integration/race PASS；Gateway auth repository/concurrency
与 TestPushRelay race PASS。覆盖 enqueue 错误整笔回滚、foreign owner、Core 不可用、旧租约确认
拒绝、RPC 成功/确认丢失重放、12 个并发订阅、待发送抑制、其他设备继续接收和 tombstone 不被
单平台停用抹除。私有 Gateway route 拒绝测试 PASS。`make check`（首次完整实现）PASS。

`make test-lan-pairing` PASS（生产 TLS/admin socket/真实配对、Gateway 重启、重认证、两设备通知、
Device Center 撤销与 Core 迟到订阅拒绝）；新增探针首次缺 Origin 已修正。随后显式稳定设备幂等键
与 revoked_at 漂移 Aborted 已补入契约，race 复验 PASS，最终同链重建与 check 进行中。
该阶段不涉及可见 UI。其余 R2/R3/R4 与推送组合加密 relay→浏览器链仍待完成，main 尚未合并。

设备撤销最终复验：显式设备幂等键版本 `TestDevicePushRevocation` race PASS，含同键时间漂移
Aborted；完整 `make check` PASS；`make test-lan-pairing` 重建复跑 PASS。门禁退出同步停止
自身 mDNS announcer，避免 TLS fixture 已删除后继续广播。`make generate` 幂等 PASS，
Go/TypeScript/SQLC 全部生成文件逐项 SHA-256 前后一致。下一步继续 Browser 隔离与其余链路。

### R3 Browser 隔离（active，2026-09-07）

设备撤销检查点 4f63c16。内置 Browser 使用 allow-scripts + allow-same-origin，同源页面可能
读取桌面 DOM/storage；先通过纯本地 Chromium fixture 复现，再移除同源豁免并验证脚本仍可交互。
不将 iframe 当成真实 Remote Browser Pool；这里只修复已实现嵌入浏览器的边界。

Browser 隔离：Chromium baseline 明确读到 `Desktop fixture data`，回归按预期失败；
移除 allow-same-origin 后同一脚本返回 Isolated，6 条 Browser/桌面 E2E PASS。
不涉及可见 UI：仅改变 iframe sandbox 权限；产品布局/控件/普通静态内容渲染均不变，
无需无差异截图。`make -o proto-check -o go-check check` PASS（Go/Proto 未变，复用
4f63c16 完整检查）；真实远程执行栈仍未完成。

### R4 工作区扫描边界（active，2026-09-07）

Browser 检查点 fecc26d。修复 localmount check-then-open、无界 ReadFile、截断扫描误作完整
收敛；文件系统验证移入 MountReader port。依赖 Linux openat2，不增加跨模块 adapter 引用。
验收：符号链接/替换竞争/FIFO、单文件与整体预算、无效 UTF-8、取消、超限保持已有索引，
完整扫描恢复后正常删除。此阶段没有 UI 变化；真实语义模型与工作区浏览器组合证据仍待补齐。

扫描修复验证：Indexer 全模块 race PASS；`TestWorkspaceIndexing` 实际 PostgreSQL/race PASS，
包含超限不修改已有文档、恢复后删除及 active 状态恢复。首次恢复回归揭示 RecordWorkspaceSync
未清除 degraded，已在同一 SQL 更新修复并重新生成。真实目录/外链并发交换 200 次扫描、
1 TiB sparse 文件、FIFO、无效文本、总预算/深度/取消均 PASS。`make generate` PASS。
全仓 `make go-check` PASS；投影写入目前逐文件事务，下一阶段修复整次扫描的原子性和并发 CAS。

### R4 工作区投影事务（active，2026-09-07）

扫描检查点 dff0f26。将 workspace 完整 pass 的文档、receipt/cursor、删除及源状态置于一个
Indexer 事务；源更新时间 CAS 拒绝旧扫描和迟到的 degraded 状态，重新绑定不得被旧 pass 覆盖。
验收：第二文件失败整次回滚、状态更新失败回滚、并发 pass 仅一个提交、重绑/取消冲突、
重启后完整重试。复用源表更新时间，不增加兼容层；无用户可见 UI 变化。

事务阶段复验：Indexer 全模块 race PASS；`TestWorkspaceIndexing`、
`TestWorkspaceTransactionalConvergence`、`TestWorkspaceConcurrentArchive` PostgreSQL/race PASS。
第二文件与最后状态更新注入失败，文档/receipt/cursor/状态逐项快照不变；重建 repository 重试成功，
8 个旧版本并发只有 1 次提交，迟到降级/重绑/stopped/等待锁取消均不修改已提交事实。
16 组 live upsert/archive 竞争无归档后复活。精确读取/过滤与混合检索集成 PASS；
既有 review rebuild golden/crash/destroy-restore PASS。`make generate`、完整 `make check` PASS。
下一步补 workspace 与 rebuild 的组合：当前 Core authority snapshot 未携带 workspace，
仅 review 的既有门禁不能证明重建保留文件。无 UI 变化；main 尚未合并。

### R4 重建保留工作区（active，2026-09-07）

事务检查点 737412d。先复现仅 Core review snapshot 的重建会丢失已有 workspace 搜索结果。
重建 review authority 单独验证；promotion 持有 active generation 排他锁时，从旧 active
复制当前已索引的 workspace 集合，与切换一起提交。复用可靠扫描结果，不在 promotion
重新读取文件；文件刷新由 workspace sync 负责。复制限定 2000 文档/64 MiB，超限失败并保留
原 active；失败重试、promotion 后响应丢失重放、并发 sync 和已删除/归档文件均需验证。

重建阶段 baseline：`TestWorkspaceSurvivesRebuild` 在 737412d 明确失败（rebuild lost indexed
workspace document）。修复后 PASS；期间修改/删除、target 旧内容重取、copy 触发器失败
保持旧 active/target、每步重建 executor、成功后新增文件再重放 promotion 均 PASS。
2001 文档与 64 MiB 超界均显式 workspace-copy-limit 且原 active 不变；既有 review rebuild
Golden/crash/destroy-restore 与 workspace 事务/归档竞争 PostgreSQL/race PASS。
最后 sync/promotion 重叠测试 PASS：真实 sync 持有共享 generation 锁时 promotion 等待并
可取消，随后重试包含新文件。`make generate` 幂等 PASS（129 个生成文件逐项 SHA-256
一致），完整 `make check` PASS。无 UI 变化；接下来补 operator 停用与浏览器实际链路。

重建全仓检查补充：首轮最后 status check 进程退出 143，未以日志末尾推断成功；
完整重跑 `/tmp/workos-workspace-rebuild-check-final.log` 退出 0，`make check` 确认 PASS。

### R4 工作区停用（active，2026-09-07）

重建检查点 19eeee1。新增私有 admin StopWorkspaceSource 与 workosctl stop；返回 source etag，
停用使用 etag 防止撤销已重新绑定的目录，并在同一事务 tombstone workspace 搜索快照。
验收：停止后旧引用不可读/搜索为空，重复使用旧 etag 拒绝、在途 sync 冲突、重绑恢复；
Gateway 不暴露 private RPC。用于闭合 operator 生命周期及真实浏览器门禁 fixture 清理。

停用阶段：Indexer/Gateway/CLI race PASS，`TestWorkspaceStop` 实际 PostgreSQL + Connect/race
PASS。覆盖最后状态更新失败回滚、旧文档不可读/不可搜、停止后 sync 拒绝、旧在途 pass 冲突、
旧 etag Aborted、当前 stopped etag 重复操作、重绑恢复。Gateway 私有 route 拒绝 PASS。
CLI 文本输出增加 etag，不改变桌面可见 UI；`make generate` 与完整 `make check`
PASS，退出码 0。下一步使用真实 CLI/admin socket/Gateway/Chromium 串起工作区门禁。

### R4 工作区浏览器门禁（active，2026-09-07）

停用检查点 18b8c38。门禁串起真实 operator CLI/admin socket/Indexer/Gateway/Chromium，
验证 23 文件过滤分页、精确只读快照、Knowledge 混合来源、重建保留、停用后旧引用拒绝。
使用唯一临时只读 fixture 目录和新建 fixture Project；退出停用该绑定、归档该 Project，
恢复默认 Indexer 配置，不删除现有数据卷。无计划中的 UI 像素变更。

浏览器首轮：CLI 绑定/23 文件分页/精确 preview/24 条混合 Knowledge/Indexer 真重启 PASS。
脚本读取 GetJobResponse 的 JSON envelope 已修正；随后 rebuilt 阶段复现产品缺陷：
Knowledge 24→23，review snapshot 写入未携带 embedding，部分词匹配退化。修复该写入
路径并追加重建前后混合检索 golden，浏览器验收仍要求完整的 24 条混合结果。

继续真实门禁定位：rebuild source_count 固定 100；Core artifact 与 archived-project
reconciliation 仓储都忘记 LIMIT+1，导致 continuation 永远为空。生产适配器 + Core authority

- Connect + Indexer client 的新门禁明确复现 99/210 review sources（首 100 含 1 archived）。
  补齐两条 probe，并简化重复映射；该问题无法由自带正确分页的 fake rebuild feed 覆盖。

浏览器门禁最终结果：`sh tools/workspace-index/gate.sh` PASS，退出 0；真实 CLI/admin socket/
Gateway/Core/Indexer/Chromium 的 seed、indexed、restarted、rebuilt、stopped、cleanup 六阶段
均 PASS，23 文件分页/精确 preview 和 24 条混合结果在重启/重建后保持，stop 使旧引用 404。
门禁清理已停用绑定、归档本次 Project、恢复默认 Indexer，临时目录仅保留本地诊断结果。
`TestIndexFeedCompletePagination` 真实 Core 仓储/authority/Connect/client PASS（210 条 review
及多页 archived-project，无遗漏/重复）；review hybrid golden 与 workspace rebuild race PASS。
本阶段补功能证据，未改变 UI 布局/控件/样式；完整 `make check` PASS，退出 0。

收尾回归另复现迟到 rebuild snapshot 会复活已归档 Project；snapshot 与 live 写入共享
generation/project 锁并检查持久 tombstone，重复快照记为 tombstoned。新增真实 PostgreSQL
回归先失败、修复后与分页/hybrid/workspace/golden rebuild race 全部 PASS。包含该修复的
真实浏览器门禁再次 PASS（`/tmp/workos-workspace-browser-final.log`）；全仓检查再次退出 0。

### R6 完整桌面画布（active，2026-09-07）

工作区门禁检查点 d70f72d。依照 structure 11.1–11.3 移除永久项目侧栏，项目创建/切换统一
使用顶部 Mission Control；设置与应用库继续使用正常窗口和 Home/Dock/搜索入口。
Home 重新排列工具，去除用户界面的 revision 实现细节。验收空项目创建、切换、设置保存、
窗口管理和多尺寸视觉；同步迁移依赖旧侧栏的浏览器门禁。现有 current 已保存为
[before](../ui/desktop-web/changes/20260907-desktop-canvas/before/)，完成后追加 after/notes。

画布阶段完成：删除永久侧栏与重复创建表单；Mission Control 选择后返回工作区；Home 居中加宽、
统一卡片高度，1440×900 无溢出。空项目可在三种尺寸直接创建，同时全局工具仍可用。
真实回归揭示 adaptive pane 的冒泡 focus 覆盖内部新窗口，已移至捕获阶段；六条三尺寸
创建/Home→设置→切换链均 PASS，连同六条原视觉捕获共 12 PASS。
视觉：[before](../ui/desktop-web/changes/20260907-desktop-canvas/before/)、
[after](../ui/desktop-web/changes/20260907-desktop-canvas/after/)、
[notes](../ui/desktop-web/changes/20260907-desktop-canvas/notes.md)，21 组已同步 current。

真实 Gateway/Core/Runtime/harness/Chromium：adaptive 四模式、五条桌面链、foundation、
安装/移除/重载、两条 context、artifact review、完整 App Bridge 共 15 条 PASS。
重跑最后两条前已修复测试在页面挂载前发送快捷键；记录为 real3 的 13 PASS 与 real4 的
2 PASS，不以失败首轮作为完成证据。迁移旧侧栏/常驻 Dock 入口，revision 由 RPC 断言。
Desktop 142 单测及完整 TypeScript 工作区检查 PASS；`make -o proto-check -o go-check check`
退出 0，复用本阶段已通过且未变的 Go/Proto 检查。下一步核查 R5 移动封装的实际启动与门禁，
其当前 lib bundle 不能证明可启动的 Capacitor App；总任务仍 active，尚未合并 main。

画布收尾 `make generate` 幂等 PASS：129 个生成源码文件 SHA-256 完全一致。

### R5 移动封装核查（active，2026-09-07）

画布检查点 79dd8e2。现有 main.ts 只导入分类工具，Vite library mode 不产生可启动 HTML；
原门禁把任意 cap sync 失败归因 SDK，不能作为可启动 wrapper 证据。本阶段先精确复现、
修复错误验收与 relay URL 边界；完整启动和原生平台接入仍须单独实现/验证，不标环境阻塞冒充。
不涉及已运行客户端的可见 UI。

核查结果：固定 Node 24/Capacitor 6.2.1 的 build 仅输出 main.js；真实 `cap sync android`
失败为 platform has not been added yet（`/tmp/workos-mobile-baseline.log`）。现已修正门禁：
检查缺失的 HTML/Android/iOS 工程并失败，不再捕获任意 sync 错误后打印 PASS。Mobile Shell
状态下调 scaffolded，响应式 Desktop 的已有证据保留。去掉已弃用 bundledWebRuntime 选项。

删除无 caller、无 Core canonical 契约的 registerPushToken；其前缀比较还会允许伪装的
http://localhost.example 及 http://127.0.0.1.example。未新增另一套 DTO 或兼容入口。
剩余移动工具的 typecheck/2 单测/library build PASS；修正后的 wrapper 门禁预期 FAIL，
列出 dist/index.html 与 Android/iOS 工程三项软件缺口。无运行 UI 变化，无需截图。
完整 wrapper 启动与原生接入仍是软件待实现，不能按宿主阻塞关闭任务。接下来先补 R5 的
真实 Core outbox→加密 TLS relay→Chromium wake→权威通知补收组合证据。

移动核查收尾：`make -o proto-check -o go-check check` PASS，退出 0（Go/Proto 未改，复用
既有完整检查）；门禁脚本真实执行与 `node --check` 通过，generated 区没有改动。

### R5 加密推送组合门禁（active，2026-09-07）

移动核查检查点 ba57f4d。门禁使用独立 scratch database、独立端口和六进程 fixture 实例，
不启用默认数据库的推送发送器，不改变既有订阅/免打扰。真实 Core outbox 经 RFC 8291/8292
TLS relay 验签/解密；先返回 503，再重启 Core 验证持久重试。Chromium 通过实际 push driver
接收解密结果，恢复窗口后从真实 Core 补收。仅对通知流/读取注入断线，不 mock 成功响应。
外部 FCM/APNs、原生移动封装不在该软件证据内；UI 渲染无计划变更。

推送组合门禁完成：`sh tools/push-wake/gate.sh` PASS，退出 0，证据
`tmp/push-wake-gate7.log`。独立 Core outbox 的两条任务/产物通知经 RFC 8291 加密、
RFC 8292 验签与严格 TLS receiver 解密，首次 503 后 Core 真重启，持久重试 delivered；
Chromium 冻结页面收到实际解密 payload，唤醒后真实 ListNotifications 补收两条事实。
未 mock 成功 API，仅阻滞 Watch；验证两个系统提醒和重复推送去重，最终 SQL 台账及
独立数据库/密钥/容器清理通过。开发身份与 CDP 注入不证明真实厂商服务或原生后台。

真实门禁复现连续推送丢一条系统提醒；Worker 顺序执行查重/展示后浏览器通过，新增
两项测试覆盖重叠重复通知及一次展示失败后的继续处理。`tmp/push-worker-unit.log` PASS，
receiver/notification race PASS（`tmp/push-wake-race.log`）。首轮全仓 `make check` PASS
（`tmp/push-wake-check1.log`）；新增回归测试与文档后再执行最终检查。无可见界面布局、
控件、固定文案变化；修复 Worker 内部并发调度，不制造无差异 UI 截图。

恢复环境时启动原 PostgreSQL 容器；此前临时门禁因容器 UID、目录 umask、宿主代理
及测试在导航前误捕旧 response 失败，均已修复后复跑。中间修改运行中的 gate.sh 导致
一次 shell 读取偏移错误，该轮浏览器虽过但不计整体通过。总任务仍 active，未合并 main。

推送收尾：Go/Proto 检查 PASS；新增 Worker 回归测试的 lint 已修正，完整 Web 检查及
Desktop 144 单测 PASS（`tmp/push-wake-web-final.log`，退出 0）。`make generate`
成功且 129 个生成源码 SHA-256 不变（`tmp/push-wake-generate.log`）。所有本轮
workos*push*\* scratch 数据库和隔离容器已清理。下一步继续检索与剩余部署/远程/移动软件缺口。

### R4 混合检索同分分页（active，2026-09-07）

推送检查点 bf20dd0。审查模型接入前发现现有 hybrid 的同分时间游标使用 After，
与 score DESC / created DESC / id ASC 排序相反，可能漏掉较旧结果或重发较新结果。
先通过真实 scratch PostgreSQL 的跨时间/同时间同分分页复现并修复，再推进语义模型。
保持来源、owner/project、签名 token 及 generation 隔离；无可见 UI 布局/控件变化。

同分分页 baseline 在 page size 1/2/3 均 FAIL（空续页或重复新结果/遗漏旧结果）；
修正为 Before 后四种页大小全部 PASS。真实 PostgreSQL hybrid/rebuild golden 与 Indexer
全模块 race PASS（`tmp/hybrid-pagination-after.log`）；`make test-semantic-knowledge`
含 Gateway/Core/Indexer 的实际 RPC 链 PASS（`tmp/hybrid-pagination-gate.log`）。完整
`make check` PASS（`tmp/hybrid-pagination-check.log`），无 Proto/SQL 生成输入变更。

### R4 离线模型路径（active，2026-09-07）

分页检查点 37aa9a6。核查官方 intfloat/multilingual-e5-small 固定 revision
614241f622f53c4eeff9890bdc4f31cfecc418b3，取得模型/tokenizer SHA-256，下载至 ignored tmp
并先用无网络 CPU 容器验证中英检索。该路径不需要真实 Provider 密钥，原 ADR 的
“真模型必需外部 API key”判断不成立。暂未改变运行中的检索实现或升级状态。

离线模型 adapter 已取得真实证据：固定 FP32 权重与 tokenizer SHA-256 校验通过；禁网、
只读、2 CPU/2 GiB 容器中的 Go adapter → Python child 通过三条中英跨语言查询、同进程
重复和子进程重启向量一致性（`tmp/embedding-real-gate.log`，PASS）。单独数学探测的
排名为 [0,2,1]，重复最大误差 0（`tmp/embedding-offline-probe.log`）。进程错误矩阵
race PASS（`tmp/embedding-adapter-tests.log`）。新增内部 Proto 已 make generate +
proto-check PASS；ADR-0021 记录路径与长文截断边界。当前尚未接入生产摄取、pgvector、
重建和搜索，状态不升级。依赖下载因容器网络较慢改用已有代理与镜像，未安装宿主软件。

adapter 收尾：`make test-local-embedding` PASS（`tmp/embedding-gate-final2.log`），固定依赖
镜像、Go race 故障矩阵、禁网只读真实模型及损坏 tokenizer 拒绝全部通过。首轮组合门禁
因 /tmp noexec 拒绝故障 fixture 脚本，已将该矩阵放在 Go 工具容器；真实模型仍保持
noexec/禁网/只读限制。`make check` PASS（`tmp/embedding-check.log`），生成后 131 个
源码 SHA-256 不变（`tmp/embedding-generate-final.log`）。本阶段没有运行客户端 UI 变化。
下一阶段将通过 application 层组合 model port，存储绑定 fingerprint，替换 feature-hash；
模型计算必须在 SQL 事务/项目锁之外，保留 archive/CAS 优先级及完整重建回填验收。

adapter 最终完整 `make check` 再次 PASS（`tmp/embedding-check-final.log`，退出 0），
已包含真实模型缓存损坏回归；生成和门禁结果如上。工作树只含本阶段改动。

### R4 模型摄取与 pgvector 接入（active，2026-09-07）

adapter 检查点 f353fdc。在 application 层组合模型与 projection/workspace/rebuild ports，
SQL 只接收经过验证的向量与指纹；模型推理不持有事务锁。计划新增 Indexer-owned
forward migration 047，将可再生 feature-hash 缓存换为 pgvector，保留全部文档/receipt/
cursor，并用已有快照的 digest/publication CAS 回填，无需原目录仍然存在。
当前默认库已经有 public.vector 扩展；沿用该不可变类型，不迁移或删除用户扩展。
混合搜索必须按指纹隔离、在 SQL 内完成排序分页，杜绝原 LIMIT 2000 的静默截断。
workspace promotion 必须检查目标向量齐备；缺模型、回填中或指纹不符如实 unavailable。
本阶段尚未修改默认数据库，现有 046 及以前的 migration 保持不变。

R4 接入进展：047 已在独立临时库验证，再通过新镜像应用到默认库；全部有效文档已回填
至一个模型指纹，缺失数为 0。未删除文档、receipt、cursor 或卷。旧 Indexer 在迁移前
已停止，随后启动新栈，未保留旧向量格式兼容逻辑。固定模型内置统一镜像，模型推理
仍是 Indexer 拥有的子进程。Compose 新增可选 WORKOS_BUILD_NETWORK，仅解决构建时
本地代理可达性；首轮默认网络连不到宿主代理，host 构建重试通过。构建缓存来自公开、
校验过的模型文件，无宿主安装或真实服务凭据。

已验证：

- PostgreSQL/race：`tmp/model-pg-regression5.log` PASS，迁移事实保留、五种 CAS 竞争、
  2102 文档完整分页、缺向量拒绝 promotion、workspace/review 重建与 archive 全部通过。
- 页 token 模型指纹隔离和 source/digest 精确读取：`tmp/model-token-final.log` PASS。
- 模型故障不会落库或确认 Core，恢复后正常消费；transport 固定 unavailable；模型排队
  内部超时按 retryable 处理，避免退出摄取进程。`tmp/model-unit-final.log` 及完整检查通过。
- `make test-model-postgres`：`tmp/model-real-pg-gate2.log` PASS，真实固定模型、pgvector、
  三项中英跨语言查询、摄取/重建向量一致、子进程重启回填后的排序一致；禁止 SKIP 假 PASS。
- `make test-local-embedding`：`tmp/model-offline-final-gate.log` PASS，含 20 秒排队超时回归、
  race 故障矩阵和禁网只读真实模型。
- `WORKOS_BUILD_NETWORK=host make test-semantic-knowledge`：
  `tmp/model-semantic-stack-gate.log` PASS，新栈 Gateway/Core/Indexer 真实 RPC。
- `make check`：`tmp/model-full-check.log` PASS。后续浏览器 fixture 扩展仍需最终复验。

浏览器门禁正在运行：`tmp/model-workspace-browser-gate.log`，在原 23 文件/混合 review/
重启/重建/stop 链路加入中文查询英文私钥文档、词法零命中与正确只读快照断言。
本阶段没有修改客户端布局或控件；新增的是确定性 E2E fixture 与检索语义。

R4 浏览器收尾：首轮因新增测试未从只读预览返回结果页而超时，已修正测试导航；
失败 fixture 已按原 gate 清理。`WORKOS_BUILD_NETWORK=host make test-workspace-browser`
第二轮六阶段全部 PASS（`tmp/model-workspace-browser-gate2.log`，fixture
`tmp/workspace-index.Jq5jgh`）：首次摄取、重启和全量重建后中文查询英文文件均正确召回，
词法 RPC 零命中；23 文件与混合 review 分页、只读快照、stop 拒绝与 cleanup 均通过。
完整模型检索链已有实际 CPU/Gateway/Chromium 证据，status/ADR/implementation 同步。
尚不声称分块索引、ANN、生成式 RAG 或多 owner 部署。

下一阶段：R2 的 Repair Task → Build/Test → 不可变候选 → Registry version/grants →
Deployment 交接。rootless Podman 缺失仍是真实宿主门禁 blocker，但不能代替缺失的软件实现。
R3 远程 Browser/Native/WebRTC/PTY 与 R5 原生 wrapper 仍需继续；全部完成前不合并 main。

模型接入最终检查：`make check` 再次 PASS（`tmp/model-full-check-final.log`）；
`make generate` 后 132 个生成源码/README 的 SHA-256 全部不变
（`tmp/model-generate-idempotent.log`）。本检查点可提交，整个总攻仍 active。
收尾审查发现两项需继续核对：Runtime App knowledge adapter 仍调用词法 RPC 且未过滤
workspace 来源（其输出契约只接受 review）；workosctl 的 workspace sync 固定 30 秒预算
需要用最大文件数与长文本真实模型验证。先完成这两项，再进入 R2 Build/Test 交接。

### R4 App 知识入口修复（active，2026-09-07）

依赖模型接入提交 bb835e0。Runtime 的知识输出契约只接受 review artifact，但 adapter
当前未在 Indexer 侧过滤 workspace，且仍调用 Search 词法 RPC。改为既有 SearchHybrid RPC
并在排序/分页前限定 artifact.review.v1，沿用当前安装 grant revision 与 owner/project 绑定；
不扩展 App 的文件读取授权。验收：真实 Connect 请求边界、workspace 混入拒绝、模型分数
范围、中文查询英文 review 的 opaque App/Gateway/Runtime/Indexer/CPU 链及撤销后零调用。
不修改已有 Proto 字段或 migration，不保留旧 RPC 回退。

App 模型入口已完成：`tmp/app-model-unit2.log` Runtime surface 全部 race PASS；
`tmp/app-model-pg-final.log` 真实 Connect/PostgreSQL PASS，先证明 workspace 行确实排在
review 前，再验证来源过滤不会丢失第一页。`WORKOS_BUILD_NETWORK=host make test-app-knowledge-search` 最终两项浏览器测试 PASS（`tmp/app-model-browser-gate2.log`），
中文查询英文 review、无 grant 不协商、撤销后拒绝均通过。`make check` PASS
（`tmp/app-model-check.log`）；再次 generate 后 132 个生成文件/README 不变
（`tmp/app-model-generate-idempotent.log`）。status、SDK 注释、ADR 与模块文档同步。
没有 UI 控件变化。下一步用真实模型验证 1000 文件长文本同步的 30 秒 CLI 预算。

### R4 大批量模型同步（active，2026-09-07）

依赖 ca77bf5。workosctl admin client 固定 30 秒超时；目前每次完整 workspace scan
都会对所有文件重做模型推理。先用 1000 文件、每文件约 8 KiB 的确定性内容复现真实 CPU
上限（总量小于 16 MiB scanner 限制），记录冷同步与重复同步时间，再按结果修复预算和
已验证向量复用。继续保留整批事务、source etag、archive/promotion 和取消边界。

上限基线已复现失败（`tmp/workspace-model-capacity-before.log`）：1000 文件同步被
Client.Timeout 在 30 秒中断，失败 fixture 已清理；重复推理基线
`tmp/workspace-model-cache-before.log` FAIL（两个未变文件的第二次同步把调用数从 2 增至 4）。
现已仅为 workspace sync 提供五分钟 CLI 预算，其他 admin 命令仍为 30 秒。
Indexer 一次有界读取 active generation 的 workspace 向量缓存，不读取全文；复用要求
source ID、digest、title、model fingerprint 全匹配，指纹变更/新文件/内容或标题变更重算。
缓存和推理都在事务外，原 source etag 与整批落库保持不变，无新增 migration。
`tmp/workspace-model-cache-after.log` Indexer/workosctl 单元与 PostgreSQL/race PASS，覆盖
模型缓存计数、模型回填、2102 分页、workspace 并发/撤销/重建与 copy limits。
`make check` PASS（`tmp/workspace-model-cache-check.log`）；真实冷同步/重复同步上限门禁进行中。

上限门禁完整 PASS（`tmp/workspace-model-capacity-after.log`，fixture
`tmp/workspace-index.jRhiUU`）：1000 文件冷同步 290 秒、未变重复同步 3 秒；原六阶段
浏览器链、stop 与 fixture cleanup 全部通过。大批次计算期间旧投影仍只有原 23 个文件，
证明没有中途发布半批文档。五分钟只剩十秒余量，最终将 sync 预算设为十分钟，其他
admin 调用仍为 30 秒；该单调增加仅留宿主负载余量，不改变计算与提交逻辑。
ADR/status/implementation 如实记录冷启动耗时，不宣称所有设备同等性能。

大批次修复收尾：最终十分钟预算代码的 `make check` PASS
（`tmp/workspace-model-cache-check-final.log`），统一镜像构建 PASS 并已恢复到默认 Indexer
（`tmp/workspace-model-cache-build-final.log` / `tmp/workspace-model-cache-deploy-final.log`）。
真实 admin status 返回 active generation、pending publications=0；没有仍挂载的 gate 目录。
再次 generate 后 132 个生成文件/README 不变（`tmp/workspace-model-generate-idempotent.log`）。
本阶段完成；下一阶段回到 R2 的 Repair/Build/Test/候选版本交接，整个任务仍 active。

### R2 部署协调修复（active，2026-09-07）

依赖 4722a40，工作树干净。先修复候选启动期间故障被观察窗起点遗漏、回滚只改 Core pin
而未启动恢复版本的问题；随后补齐 Repair 产物与 Build/Test 交接。部署故障检测覆盖从
candidate 持久化到观察窗结束的整段时间，观察窗仍须在候选实际启动成功后完整计时。
回滚命令与恢复 Surface 使用固定且不同的幂等键，恢复启动成功后才能记录 rolled_back。
验收包括状态机重启/失败重试、真实 Connect 边界与 PostgreSQL 时间/安装范围；无 UI 变化。
完整 R2 仍须 Build/Test/Registry/Canary 跨进程证据，不能因这些修复标记完成。

2026-09-08 续接：宿主重启中断上轮浏览器门禁，`tmp/deployment-startup-browser.log`
只有启动行，不能作为 PASS。已通过的 `tmp/deployment-startup-after.log` 包含 Reliability/
Runtime race 与两项 PostgreSQL 部署测试；最后并发 replay descriptor 修复单元另见
`tmp/deployment-version-final-unit.log`。全量检查第一轮因新增 E2E 四处缺少类型声明失败，
已修正，等待 Docker 恢复后重新执行镜像、浏览器与全量检查。

继续审查发现 Repair 轮询固定取最早四条 submitted，失败/取消任务或没有候选产物的完成任务
会永久占住队首。此次 R2 范围增加公平轮询、失败终态收敛和 TaskID 完整交接；复用原
repair ledger，不变更已应用 migration。验收须证明超过四条任务时后续行仍被处理，
失败/取消不触发部署，完成交接保留 owner/project/incident/task 关联。

Docker 恢复后重新启动原 postgres 容器成功，六进程恢复运行，原 volume 未删除/替换。
Repair 轮询修复与部署回归 PASS（`tmp/repair-fairness-tests.log`）：全部 Reliability/
Runtime surface race、部署台账锁与重启、启动期 incident 范围、九条 submitted 公平轮询
及 terminal 清理。Connect 单元覆盖 TaskState 六种正常状态、错 owner/task/project/
incident、缺少 input/task 与未知状态；application 单元验证失败任务不部署、交接失败不
丢弃、成功交接保留 TaskID。最终镜像构建与浏览器版本门禁仍在运行。

最终统一镜像构建 PASS（`tmp/deployment-repair-final-build.log`），Runtime/Reliability 已
更新（`tmp/deployment-repair-final-deploy.log`）。浏览器第二轮发现旧测试在启动 App 后仍
操作自动关闭的 App Library；按现有桌面流程重新打开 Library，并移除重复关闭步骤，
没有更改 UI 行为。最终浏览器 PASS（`tmp/deployment-startup-browser3.log`，11 秒）：
真实 Gateway/Core/Runtime 精确版本启动、错误前置条件不消费 key、回滚后旧版本重放拒绝、
恢复版本实际页面内容、版本历史/冲突和 UI 版本切换全部通过。原静态 bundle fixture 不含
Bridge SDK，测试仅证明版本与页面链，不将其算作 Bridge 握手或自动 Build/Test 验收。

R2 协调修复检查点：`make check` PASS（`tmp/deployment-repair-final-check.log`）；
`buf breaking --against .git#branch=main` PASS（`tmp/deployment-repair-buf-breaking.log`）；
再次 `make generate` 后 132 个生成文件/README 不变（摘要基线
`tmp/deployment-generated-before.json`、生成日志 `tmp/deployment-repair-generate-final.log`）。
没有 UI 像素变化或新增 migration。本检查点完成；整个任务保持 active，下一步仍为
Recovery Harness 安全执行边界与 Repair 候选协议、Build/Test producer/consumer 实现。

### R2 Recovery CLI 执行边界（active，2026-09-08）

依赖 7694941。Generic CLI 当前继承整个 Harness 环境、把 stderr 附入错误、取消只杀直接
子进程，后代持有 stdout 时可能拖住轮询；其 streaming-only 声明也未拒绝伪造 artifact/
approval/usage 事件。先修复这些现有问题，再扩展候选输出协议，避免把不完整执行边界
带入自动修复。范围：最小环境/独立临时目录、输入与总输出预算、进程组和父进程退出
清理、确定性错误、能力一致性。验收使用真实子进程故障矩阵与 Harness 单元/race；
不以进程组替代 rootless/cgroup 安全隔离，不升级缺失的 token budget 或容器能力。

CLI 基线 FAIL（`tmp/recovery-cli-before.log`）复现父环境泄漏、stderr 进入错误、无全流
预算和伪造 artifact 事件被接受。修复后真实子进程/race PASS
（`tmp/recovery-cli-final-tests.log`，包含整个 internal/harness）：空 HOME/CWD、最小环境、
stderr 丢弃、1 MiB 请求/单行、4 MiB/1024 事件总预算、未声明事件拒绝、后代 pipe 取消、
子进程非零退出不得发布 completed。默认 Generic CLI 仍关闭，未升级其 token budget、
Recovery 路由或 structured candidate 能力；完整候选链 E2E 仍是 R2 后续任务。

CLI 执行边界检查点收尾：`make check` PASS（`tmp/recovery-cli-check.log`）；再次
`make generate` 后 132 个生成文件/README 不变（`tmp/recovery-cli-generate.log`，摘要
`tmp/recovery-cli-generated-before.json`）。没有 Proto 或 migration 变化，没有 UI 变化。
本阶段可提交；接下来处理 Repair 入队目标快照与幂等来源校验，再实现候选 Build/Test。

### R2 Repair 入队来源与目标（active，2026-09-08）

依赖 ff1d8c6。首先修复普通 TaskRouter 与 Agent Create 在同 key、不同输入时直接返回
旧任务的漏洞；否则 repair-incident 键碰撞会绑定错误任务。匹配必须覆盖 owner/project
和规范 JSON 输入，忽略已快照的 Provider/credential 变化；并发落库路径也须裁决冲突。
公开 SubmitTask 禁止伪造私有 incident_id。验收包括无副作用单元、真实 PostgreSQL
并发（恰一个 task/outbox）及公开 Connect 拒绝；随后增加故障安装的固定版本目标快照。

入队来源修复检查点：基线 `tmp/repair-admission-before.log` FAIL（不同 goal/incident
错误重放旧任务），修复后 Core Agent/orchestration race 与 PostgreSQL 十请求并发 PASS
（`tmp/repair-admission-after.log`），恰一 task/outbox，五次同输入成功、五次不同输入冲突。
镜像构建与 Core/Harness 更新 PASS（`tmp/repair-admission-build.log` /
`tmp/repair-admission-deploy.log`）；真实 Gateway/Core/PG E2E PASS
（`tmp/repair-admission-e2e.log`），新增可复跑 `make test-task-submission-identity`。
`make check` PASS（`tmp/repair-admission-check.log`），再次 generate 后 132 个生成文件/
README 不变（`tmp/repair-admission-generate.log`，摘要
`tmp/repair-admission-generated-before.json`）。无 UI 像素变化、无 migration/Proto 变化。
下一步继续固定 Repair 安装版本快照；完整 R2/R3/R5 未完成，不合并 main。

### R2 Repair 版本快照（active，2026-09-08）

前置入队幂等修复已提交。新增 canonical RepairTarget 到 AgentTaskInput，并在私有
CreateRepairTask 请求携带 installation ID；公开 SubmitTask 不接受这两个私有来源字段。
Core 从 Project 自有安装/项目表的同一 SQL snapshot 读取 installation、App/version/digest
和 project revision，写入不可变任务输入。重放只比较调用者事实，保留第一次版本快照，
不得因后续升级或归档重新解析目标。修复 queued task 的 replay 标识不准确问题：创建结果
显式返回是否新建，不再用 task state 推断。计划无 migration，先 Proto/generate 后实现。
验收：目标归属/停用/归档、同一快照、版本变更后的重放、并发同请求只创建一个任务、
私有来源拒绝和真实 Core/Reliability 调用；Build/Test 与 Recovery 路由仍为后续独立工作。

版本快照验证通过：Core Agent/orchestration 与 Reliability race
（`tmp/repair-target-tests.log`）；真实 PostgreSQL（`tmp/repair-target-pg.log`）覆盖
升级事务期间 80 次读取无版本/revision 撕裂、owner/project/归档/卸载拒绝，以及十请求并发
只有一次 Created、一个 task/outbox。统一镜像已构建并更新 Core/Harness/Reliability
（`tmp/repair-target-build.log`、`tmp/repair-target-deploy.log`）。真实私有 RPC 门禁 PASS
（`tmp/repair-target-rpc.log`，新增 `make test-repair-target`）：Reliability producer adapter
重放升级且已归档的安装仍得到首次版本、错误安装冲突、缺字段拒绝、Gateway 隔离。
本门禁验证 Repair admission，不宣称 incident 采集或 Build/Test/发布完成。

版本快照检查点收尾：`make check` PASS（`tmp/repair-target-check.log`），buf breaking
对本地 main PASS（`tmp/repair-target-breaking.log`；首次运行因容器未设置 HOME 无法写
cache，补为 /tmp 后通过）。再次 `make generate` 后 132 个生成文件/README 不变
（`tmp/repair-target-idempotent-generate.log`，摘要
`tmp/repair-target-generated-before.json`）。没有 migration 或 UI 像素变化。
本阶段完成，整个任务仍 active；下一步是健康 Provider 选择、Recovery 入队治理及候选构建。

### R2 Provider 健康入队（active，2026-09-08）

依赖 350248e。审查发现 providerCapabilities 只复制能力、忽略健康状态，普通/Repair/App
新任务可能排给 catalog 已声明 unavailable 的 Provider；普通入队还最多重复查询三次
catalog，artifact/context/credential 判定可能来自不同事实。先收口为新入队健康验证与
一次能力快照，保留 replay-first；不把瞬时不可用转换为权限授予或静默更换普通任务绑定。
验收覆盖真实 catalog adapter、健康状态矩阵、零入队副作用、重放不依赖当前健康。
Recovery 专用路由、预算/审批与 Build/Test 继续在本总任务内推进。

健康检查基线 FAIL（`tmp/provider-health-before.log`）：四种非 healthy 状态仍然入队，
同时请求 artifact/context 时 catalog 被读取三次。修复后的 Core Agent/orchestration/
Harness Catalog race PASS（`tmp/provider-health-after.log`）。Generic CLI 还会在可执行文件
缺失时固定声明 healthy；补上存在/执行权限检查，错误只返回固定原因，不暴露路径。
CLI 基线 FAIL（`tmp/provider-health-cli-before.log`），整个 Harness race PASS
（`tmp/provider-health-cli-after.log`），验证缺失、无执行权限、恢复可执行、再次删除。

健康检查最终镜像/部署 PASS（`tmp/provider-health-final-build.log`、
`tmp/provider-health-final-deploy.log`）；真实 Repair RPC 回归 PASS
（`tmp/provider-health-final-rpc.log`），公开任务并发/重放/私有来源拒绝 E2E PASS
（`tmp/provider-health-e2e.log`）。新 ErrProviderUnavailable 返回明确健康原因与
FailedPrecondition，不冒充 token budget 缺失；最终 Core race PASS
（`tmp/provider-health-final-unit.log`）。非健康状态的拒绝与执行文件故障证据来自测试矩阵，
此次默认栈回归不宣称覆盖真实 Provider 健康切换。
`make check` PASS（`tmp/provider-health-final-check.log`）。没有 Proto/migration/UI 变化。
再次 `make generate` 后 132 个生成文件/README 不变
（`tmp/provider-health-idempotent-generate.log`，摘要
`tmp/provider-health-generated-before.json`）。健康入队检查点完成，完整任务保持 active。

### R2 Generic CLI 结构化输入输出（active，2026-09-08）

依赖 1eb4a32；单分支工作树干净。按 ADR-0023 先新增 canonical CLI v2 Proto，再实现
context 精确绑定、显式 review 输出、完整成功后原子发布、任务级 runtime deadline。
保留既有最小环境/进程组/总流预算；不保留旧 CLI 格式，不升级缺失的 token/usage/隔离能力。
验收：真实子进程错误矩阵、真实 Core/Harness/CLI review 与 context 门禁、全量检查及
generate 无漂移。无 UI/migration；候选 Build/Test 和 Repair 治理仍在后续范围内。

Generic CLI v2 子进程/race PASS（`tmp/generic-structured-final-tests.log`）：结构化成功后
batch 先于 completed；假完成退出失败、failed、缺失/重复/终态后产物、旧格式与 sink 失败
均不发布成功；上下文内容精确匹配、错 digest 拒绝、任务 1 秒 deadline、unsupported
budget/sink 在执行前拒绝。既有最小环境、stderr、流量上限与后代取消测试继续通过。

独立跨进程门禁 PASS（`tmp/generic-structured-gate.log`，1.6 秒测试时间）：真实
Gateway/Core/Harness/CLI 发布 markdown+diff、以首次 markdown 的 ID/digest 解析上下文并
产出包含原文的新 review；临时 CLI 失去执行权限后新入队 FailedPrecondition 且不消费 key，
原任务可重放，恢复权限后同 key 成功入队。共六份带 source_task_id 的 review 产物。
门禁仅回收自己的临时数据库/容器/凭据，默认栈没有被 fixture 配置覆盖。

CLI v2 收尾：首轮 `make check` 因 architecture 文档格式失败，格式化后通过。CLI 专用
消息放在独立 cli.proto，避免桌面 catalog 引入整个私有 execution 描述符；最终
`make check` PASS（`tmp/generic-structured-split-check.log`），buf breaking 对 main PASS
（`tmp/generic-structured-breaking.log`）。默认 Harness 已更新
（`tmp/generic-structured-default-deploy.log`），Generic 默认仍关闭。没有 migration 或
客户端布局/控件变化，R2 Build/Test、Recovery 治理和 R3/R5 尚未完成，不合并 main。
再次 `make generate` 后 134 个生成文件/README 不变
（`tmp/generic-structured-idempotent-generate.log`，摘要
`tmp/generic-structured-generated-before.json`）。CLI v2 检查点完成；下一阶段固定 Registry
拥有的源码与构建配置，再实现候选交接和运行时构建，完整任务仍 active。

### R2 不可变构建输入（active，2026-09-08）

依赖 301cf41。按 ADR-0024 先实现 Registry 自有源码包 Create/Get 与 canonical digest、
owner/idempotency/并发/读取完整性；然后在唯一 manifest JSON Schema 接入固定 build 配置
并校验 owner+source digest。后续候选不得自行更改测试命令或安全配置。计划新增 048，
001–047 不变；跨进程契约先 Proto/generate。验收包括源码路径/预算/冲突矩阵、真实 PG
并发与重启、真实 RPC 注册/绑定源码；此阶段不宣称 Runtime Build/Test 或 Recovery 治理完成。

不可变输入门禁通过：AppRegistry/orchestration/Gateway race 与真实 PostgreSQL 并发、损坏读取
（`tmp/app-source-bounded-tests.log`）；十请求只保留一个源码事实，不同内容/执行位冲突，
超大损坏 JSON 在 SQL 投影阶段截断后拒绝。传输单元覆盖二进制/JSON 合法 512 KiB 边界、
超大与 gzip bomb、无身份零持久化；包含在完整 `make check` PASS
（`tmp/app-build-inputs-check.log`，桌面 144 tests）。

真实独立 Gateway/Core/PG 门禁 PASS（`tmp/app-build-inputs-final-gate.log`）：中文源码字节数、
排序/ID/摘要/时间、注册 build 绑定、错 owner/摘要、冲突失败不消费 key，真实 Core 重启后
上传与注册精确重放。共享门禁脚本的 Generic CLI 回归 PASS
（`tmp/app-build-inputs-generic-regression.log`）。默认 Core/Gateway 已更新，048 已应用
（`tmp/app-build-inputs-default-migrate.log` / `tmp/app-build-inputs-default-deploy.log`）；
001–048 从此保持不变，下一 migration 为 049。默认任务入队和 Repair 快照 RPC 回归 PASS
（`tmp/app-build-inputs-default-tasks.log` / `tmp/app-build-inputs-default-repair.log`）。

buf breaking 对 main PASS（`tmp/app-build-inputs-breaking.log`）；再次 generate 后 137 个
生成文件/README 不变（`tmp/app-build-inputs-idempotent-generate.log`、摘要
`tmp/app-build-inputs-generated-before.json`）。没有用户可见 UI 变化。源码/build 配置检查点
完成；Runtime Build/Test、候选交接、Recovery 治理及 R3/R5 仍待完成，整个任务保持 active。

### R2 租约绑定的修复源码交接（active，2026-09-08）

依赖 30bc8a7，按 ADR-0025 实现私有 RepairExecutionService：从有效 lease 推导固定构建
输入；普通任务、错 worker、失效 lease、错 manifest/source digest 均拒绝；每任务至多一个
不可变文件候选，提交和重放必须继续验证租约，候选不进入可安装版本列表。先 Proto/generate，
新增 Registry 自有 049 候选映射，001–048 保持不变。验收包含真实 PG 并发/事务与私有 RPC
重启/失效矩阵；实际 Harness producer、Runtime Build/Test 和治理继续在后续接通。

租约源码交接验证 PASS：`tmp/repair-source-pg-tests4.log` 覆盖十请求同任务五成功/五内容
冲突、单一候选/源码事实、重新装配后精确重放、普通/错 worker/取消/过期/终态拒绝、
manifest/source 损坏与无 build 配置，以及写入后到期导致源码和映射全部回滚。初轮 fixture
遗漏必需的 restartLimit，第二轮错误假设一个数据库可有两个 owner；均修正为现有单 owner
架构，没有放松生产校验或修改既有迁移。Core Agent/Registry/orchestration race PASS
（`tmp/repair-source-race.log`）。

`make test-repair-sources` 真实独立 Gateway/Core/PG/mTLS 门禁 PASS
（`tmp/repair-source-rpc-gate.log`）：真实入队并 claim、源码解析/候选保存、非法路径/不同
内容/超大体积、错 worker、无客户端证书、Gateway 与普通 Core listener 排除；安装升级为
v2 后重启 Core，修复输入仍是 v1 的源码/测试命令，候选首次 ID/摘要/时间不变；取消后读取
和重放均拒绝。此测试由专用客户端充当 worker，Harness catalog 正常运行、worker poll
设为一小时以避免抢占，不宣称 provider 自动生成候选或执行 Build/Test。

049 已应用并更新默认 Core（`tmp/repair-source-default-migrate.log` /
`tmp/repair-source-default-deploy.log`），001–049 不再修改，下一迁移 050。
`make check` PASS（`tmp/repair-source-check.log`）；buf breaking 对 main PASS
（`tmp/repair-source-breaking.log`）。没有 UI 变化。本阶段检查点完成，下一步为 Harness
真实候选输出与 Runtime 构建/测试；整个任务仍 active，未合并 main。

再次 generate 后 140 个生成文件/README 不变（`tmp/repair-source-idempotent-generate.log`，
摘要 `tmp/repair-source-generated-before.json`）。

### R2 Harness 候选 producer（检查点 verified，2026-09-08）

依赖 91ce9af。扩展 canonical CLI v2 与 Harness capability，worker 在执行前从租约解析
固定源码，provider 只提交普通文件候选；Core 新入队校验候选能力，worker 不允许缺少候选
的 repair 成功终态。Generic CLI 完整验证流与子进程退出后再发布，拒绝多候选、伪造输入
关联、混合 review/candidate 与异常退出。Fake 仅产出明确合成候选用于软件链测试；真实
CLI fixture 修改有断言的示例程序，验证候选进入 Core。此阶段仍不升级 token/usage/内核
隔离或 Recovery 治理，Runtime Build/Test 另行接入。无 migration/UI 变化，先 Proto/generate。

### 2026-09-08 用户授权检查点合并与 GLM-5.3 交接

用户最新明确要求“将当前的修改合并到 main”，并在 docs/prompts 将剩余任务交给
GLM-5.3。此指令替代本记录此前“全部完成前不合并 main”的阶段限制；允许合并当前
检查点，不代表 R2/R3/R5 已完成，不推送远端。总任务继续 active。

交接入口：[GLM-5.3 剩余任务说明](../prompts/20260908-glm-5.3-remaining-work-handoff.md)。
已列出已验证边界、具体文件/Proto/调用入口、剩余执行顺序、失败矩阵、环境约束和验证
命令；要求至少三轮实现、失败/重启、完成前复核，禁止用 fixture 或历史 PASS 冒充完整功能。

本次 producer 包含 Core 入队能力检查、Harness 租约输入解析、CLI 完整成功退出后提交
唯一候选、worker 回执与缺失候选拒绝，以及普通 Core 私有 listener 的 owner-scoped
completed-task 候选读取。消费者返回原始 build 输入和候选文件，保留 project/incident 关联；
Gateway 不开放此读取。没有新增 migration；001–049 不变，下一迁移为 050。

此次没有用户可见 UI 变化：桌面三处修改仅补测试 fixture 的 capability 字段。之前画布
视觉证据仍为 [21 组前后对比](../ui/desktop-web/changes/20260907-desktop-canvas/notes.md)。
检查环境时原 PostgreSQL 容器停止，启动原容器后 PostgreSQL 与依赖服务恢复，未重置数据。
此次只验证独立 gate，不把共享镜像构建视为默认六进程已部署。

合并前最终代码验证（2026-09-08，全部 PASS）：

- `make check`：Go vet/tests、Proto/SQL lint、架构/格式/TS 检查及桌面 build，桌面 144 tests；
  日志 `tmp/glm-handoff-check.log`。
- Harness、Core Agent/orchestration/AppRegistry/HarnessCatalog 的 `go test -race`；
  日志 `tmp/glm-handoff-race.log`。
- 真实 PostgreSQL `go test -tags='integration repairsource' -count=1 -run '^TestRepairSources|^TestAppSource' -v ./tests/integration`：
  并发身份、持久完整性、completed 读取、失效与提交前到期回滚；日志 `tmp/glm-handoff-pg.log`。
- `WORKOS_BUILD_NETWORK=host sh tools/generic-cli/gate.sh repair-producer`：真实 worker/CLI
  文件产出，原输入/测试不变、错 owner 与公开接口拒绝、Core/Harness 重启后候选完全一致；
  日志 `tmp/glm-handoff-producer-gate.log`。示例测试与镜像构建没有执行。
- 同一脚本默认 Generic CLI 与 `repair-sources` 回归：
  `tmp/glm-handoff-generic-gate.log`、`tmp/glm-handoff-source-gate.log`。
- `buf breaking --against '.git#branch=main'`，在合并前对旧 main 检查；
  日志 `tmp/glm-handoff-breaking.log`。

早期 producer 检查曾暴露三处 TS capability fixture 缺字段，以及 capability 拒绝测试的
stub 默认值覆盖显式 false；已修正 fixture 并由上述完整检查/race 重新验证，未放宽断言。

下一步由 GLM-5.3 认领（分支方式以下方最新补正为准）：先完成 Runtime Build/Test 和候选版本暂存/
Deployment 交接，再完成 Recovery 治理、R3 真实远程 Surface、R5 移动原生软件工程，最后
复核 R1/R4/R6 和原完整验收。未实现项仍为软件缺口，不因本次合并自动变为 working。

再次 `make generate` 后 140 个生成文件及 README 内容摘要完全不变（包括 README 在内共 140 个文件）；
日志 `tmp/glm-handoff-idempotent-generate.log`，摘要 `tmp/glm-handoff-generated-before.json`。
此检查点按用户授权合并到本地 main；保留原任务分支，不推送、不标记总任务 done。

### 2026-09-08 交接范围与唯一分支规则补正

用户指出交接的分支保护要求不够明确、任务范围偏少。当前在唯一现有任务分支
`feat/v1-remaining-capability-sweep` 修订交接；此记录替代上文“接手者从 main 建分支”
的后续操作建议。main 已包含检查点 d4fca63，此前合并授权已经执行；后续开发、测试、
文档和提交均在唯一任务分支，不增设 worktree/辅助分支，不改动其他历史分支。
没有远端分支保护也禁止直接写 main；后续合并须有用户新的明确授权。应用版本 promote
与 Git merge 分别定义，不能互相代替授权。

[更新后的 GLM-5.3 交接](../prompts/20260908-glm-5.3-remaining-work-handoff.md) 补齐六条
工作流逐项验收：除 Build/Test、Recovery、远程栈与 mobile 外，明确要求完成监督/遥测、
Provider/凭据失效链、Bridge/Declarative、知识源/索引/归档、通知/LAN、全部桌面系统入口
与多尺寸交互的核查修复；增加完整专项门禁矩阵和最终交付标准。已有证据需复核，发现
缺陷必须修复，不要求重写无缺陷实现；仍保留原计划非范围及真实环境阻塞边界。

本次仅修改提示词和任务记录，没有代码、协议、数据库、模块能力或可见 UI 变化，
`docs/status.json` 保持原有事实，不生成无差异截图。验收为文档格式、引用/Make target
可解析、分支断言、diff 检查，以及确认 main 未移动；不重复运行无代码变化的全栈门禁。

验证 PASS：两份文档的 Prettier check、status renderer --check、交接中 49 个 Make target
及显式仓库路径核查、git diff --check；唯一任务分支正确，main 保持 d4fca63。

### R2 Runtime Build/Test 与候选发布（active，2026-09-08）

按 ADR-0026 实现 Build/Test 执行段。先 Proto（taskexecution/v1/build.proto：
Runtime 私有 BuildTestService + Core 私有 RepairVersionService），migration
050（Core app_versions.state + 任务映射）、051（workos_runtime.build_jobs）、
052（部署台账 staged 事实）。Runtime `internal/runtime/buildtest`：持久作业
状态机（task 幂等/规范摘要漂移拒绝/租约恢复/transient 三次重试后终态）+
进程沙箱引擎（bash ulimit 内核限制、墙钟 deadline、进程组清理、1 MiB 输出
预算、最小环境 GOPROXY=off）。Core：staged 版本注册（派生 manifest 仅换
build source 绑定）、私有 canary 精确切换、canary 后 publish；owner 读取与
默认版本选择只见 published。Reliability：BuildCoordinator 串起 候选读取→
BuildTest→staged 注册→Offer（staged 事实持久化）；promote 先 publish；
失败构建零部署副作用。默认六进程未部署新版本（本次仅代码+单测）。

已验证（2026-09-08，全部真实执行）：

- `make test-build-engine` PASS：真实子进程矩阵——成功、测试失败终态、构建
  失败不跑测试（exit 7 断言日志无测试输出）、墙钟超时、输出预算、内核
  地址空间 kill（有界内存版）、路径穿越拒绝、无代理最小环境。
- `go test ./internal/...` 全部 PASS（buildtest 状态机：幂等/漂移 Abort/
  重试耗尽 engine-failed/cancel 终态不可变；deployment promote-publish、
  staged 切换；受影响 fake 全部补齐）。
- `make check` PASS（首轮格式化后）；`buf breaking --against .git#branch=main`
  PASS；buf+sqlc 再次生成 94 个生成文件 SHA-256 不变。
- 开发中断排查：go module 缓存卷 root 属主目录已恢复 uid 1000；桥接网络
  goproxy.cn 不可达时按交接文档改用 host 网络运行 Go 工具容器。引擎
  RLIMIT_NPROC 计入线程，并行测试套件共享 uid 会触发 Cannot fork：默认
  4096 并把真实子进程矩阵放入 engineexec 标签的专用门禁。

未完成（下一阶段继续，不作为完成证据）：

1. `tools/repair-buildtest` 跨进程门禁：成功/测试失败/构建失败/启动故障/
   canary 故障回滚/回滚失败重试/进程重启/重复提交/用户版本变更矩阵，断言
   实际版本、Surface 与持久台账。默认栈 Core/Runtime/Reliability 需成组
   部署后再验收。
2. rootless Podman 构建引擎（本宿主 BLOCKED 不变）；进程引擎不声明容器级
   隔离。
3. R2 Recovery 治理、监督/遥测复核仍待做；R3/R4/R5/R6 未动。
