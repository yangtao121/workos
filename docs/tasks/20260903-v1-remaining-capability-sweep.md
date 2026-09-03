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

| 阶段                                                                                                                    | 状态    | 提交 | 证据 |
| ----------------------------------------------------------------------------------------------------------------------- | ------- | ---- | ---- |
| W1.1 ADR-0015：Provider 接入契约、凭据种类矩阵、master-key 轮换语义、揭示审计模型                                       | done    | 待填 | `docs/decisions/0015-provider-expansion-and-vault-rotation.md` |
| W1.2 Vault 扩展（codex/github/cloud 种类 + owner 维度 + 在线轮换 + 受审计揭示）+ `make test-credential-vault-expansion` | done    | 待填 | 门禁 PASS（2026-09-03）：admin socket 全链路 put/invalid 拒绝/reveal/revoked 拒绝；在线轮换 3→4→5（successor-key 进程换回）+ 重启收敛 + no-op；in-process 5 项协议套件 PASS（真实 PostgreSQL） |
| W1.3 Codex fixture + Adapter + broker/catalog/binding + `make test-codex-harness`                                       | done    | 待填 | 门禁 PASS（2026-09-03）：catalog 如实声明（streaming/usage/hard budgets/lease=真）、codex-auth.v1 lease binding、真实 fixture 子进程事件流、Core+harness 重启持久、Chromium 绑定/运行 E2E；失败矩阵（crash/silent/out-of-order/over-budget/slow）由 adapter 单测证明 |
| W1.4 MCP fixture + Adapter + `make test-mcp-harness`                                                                    | done    | 待填 | 门禁 PASS（2026-09-03）：降级能力如实声明（全 false + 无 lease）、credential-free binding、blocking tool-call 运行、无 usage 事件、重启持久；失败矩阵（no-tool/protocol-error/tool-error/oversize/hang/crash）由 adapter 单测证明 |

### W2 真实 Runtime 与自愈链

| 阶段                                                                           | 状态    | 提交 | 证据 |
| ------------------------------------------------------------------------------ | ------- | ---- | ---- |
| W2.1 ADR-0016：监督验收标准、遥测脱敏矩阵、Repair/Deployment 语义与 L 级别映射 | pending |      |      |
| W2.2 rootless Podman 宿主探测（或 blocker 记录）+ `make test-rootless-runtime` | pending |      |      |
| W2.3 真实监督链 + `make test-real-supervision`                                 | pending |      |      |
| W2.4 遥测（collector 输出/存储/System Monitor 消费）+ `make test-telemetry`    | pending |      |      |
| W2.5 Repair Orchestrator                                                       | pending |      |      |
| W2.6 Deployment Controller + `make test-repair-deployment`                     | pending |      |      |

### W3 Surface 与 Bridge 补全

| 阶段                                                                                                            | 状态    | 提交 | 证据 |
| --------------------------------------------------------------------------------------------------------------- | ------- | ---- | ---- |
| W3.1 ADR-0017：Bridge 能力清单/授权矩阵、FileRef/workspace 挂载、Declarative schema 版本、远程 Surface 安全边界 | pending |      |      |
| W3.2 Bridge 全能力 + `make test-app-bridge-full`                                                                | pending |      |      |
| W3.3 Declarative Surface + `make test-declarative-surface`                                                      | pending |      |      |
| W3.4 Remote Browser Pool + Browser Surface                                                                      | pending |      |      |
| W3.5 Native Runner + WebRTC 回环 + Human Native Workspace + `make test-remote-native-surface`                   | pending |      |      |

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
