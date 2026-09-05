# Task: v1 剩余能力总攻——Provider 扩展、真实 Runtime 自愈链、远程 Surface、语义知识、后台推送与移动原生、桌面系统应用

- 状态：active（W1 进行中）
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
