# Task: v1 剩余能力总攻——Provider 扩展、真实 Runtime 自愈链、远程 Surface、语义知识、后台推送与移动原生、桌面系统应用

- 状态：active（2026-09-06 全面修复；以下修复验收表为当前恢复点）
- Owner/Agent：overnight capability-sweep implementation agent（单一写入智能体，单分支单 worktree 串行）
- 进程/模块：全部六进程 + desktop-web/mobile-shell/sdk
- 依赖：ADR-0001..0014 全部既有裁决；实现依据 `docs/prompts/20260903-next-agent-remaining-capability-sweep.md`
- Branch：`feat/v1-remaining-capability-sweep`（自本地 `main` @ `afe8580`）
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
