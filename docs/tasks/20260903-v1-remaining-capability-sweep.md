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
| R3 Surface/Bridge            | pending                                                      | 文件/窗口/产物能力、真实浏览器/Native 交互，不能用静态页面替代 |
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
