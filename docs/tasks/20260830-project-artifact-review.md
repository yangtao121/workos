# Task: Project Agent Markdown / Diff Artifact Review 纵向切片

- 状态：done
- Owner/Agent：feat/project-artifact-review 实现智能体
- 进程/模块：workos-core（Artifact、Agent、orchestration、harnesscatalog）、harness-host
  （Fake adapter、worker、broker）、workos-gateway（回归）、desktop-web（Agent Center、
  Artifact Center、Artifact Viewer）
- 依赖：LAN 设备配对与持久 Gateway 会话（已合入 main）；不依赖 Reliability/Podman 证据。
- 提交：见分支 `feat/project-artifact-review`（不 merge、不 push）。

## 目标与范围

让 Project Agent 通过 canonical、受限、lease-bound 的输出协议产出 immutable Markdown 或
Unified Diff Artifact，Desktop 从 Timeline / Artifact Center 打开只读审阅窗口。全部完成，
范围与任务开始时记录一致（含明确非目标：Object Store、PDF/image、patch apply、DeepSeek
结构化输出、App Bridge artifact.\* 等保持 unavailable）。

## 协议/数据影响（全部 additive）

- `artifact.proto`：`Artifact.source_task_id = 11`；`GetReviewArtifact` RPC（typed oneof
  `markdown | unified_diff`，server-derived media type）；`ListArtifacts(project_id)` 对
  review artifact 变为 working。
- `execution.proto`：私有 `AppendTaskArtifact` RPC——request 仅 lease/worker/output_key/
  title/typed content；response 返回 Core-minted Artifact + 已持久化 `ArtifactCreated` 事件。
- `harness.proto`：`HarnessCapabilities.supported_artifact_types = 16`（exact list，与
  `structured_artifacts` bool 一致性由 catalog 校验，漂移视为 corruption → unavailable）。
- `AgentEvent.ArtifactCreated` 字段号 16 不变，内容不进事件。
- Migration `021_project_review_artifacts.sql`：`workos_core.project_review_artifacts` +
  `project_review_artifact_outputs`（(task, output_key) PK、(task, task+type) 唯一索引、
  publication 引用列）；001–020 逐字节未动，021 checksum 已钉住
  （`tests/integration/project_review_artifact_test.go`）。

## 实现要点

- **单事务 materialization**（`internal/core/orchestration/task_artifact_materializer.go`）：
  lease 锁定 → scope/requested-type 校验 → (task, output_key) 裁决 → 纯 domain 准备
  （CRLF→LF、NUL/C0/C1 拒绝、≤512 KiB/20k 行/16 KiB 行、versioned digest、server-mint）→
  artifact+映射插入 → Core-minted 事件。模块所有权经事务级中立 port
  （`agentports.TaskStreamStore` / artifact `ReviewOutputStore`，`internal/platform/dbtx`），
  各模块只写自己的表。crash/response-loss/lease-lost/reclaim/restart 全部收敛为 replay 或
  稳定冲突（ADR-0008 §4 逐条列出）。
- **Fail closed**：generic `AppendTaskEvent` 拒绝 `ArtifactCreated`（InvalidArgument）；
  worker 在 RunCompleted 前校验全部请求 type 已 materialize；同 type 二次 emission 拒绝；
  broker 对无 sink 的执行面注入拒绝 sink；Task Router 入队前 exact capability 校验
  （零副作用、不 fallback、global scope 拒绝）。
- **公开读**：`GetReviewArtifact` typed read + `ListArtifacts` project/owner-wide（union），
  经中立 `ArtifactProjectScope`；错误矩阵固定（NotFound/Unimplemented/Internal/Unavailable），
  stored corruption 每次读重算 digest → Internal。
- **Desktop**：composer 复选框、timeline 可点击 artifact 事件、Artifact Center 普通窗口
  （generation guard、Project 切换隔离/关闭跨 Project viewer）、只读 inert Markdown/Diff
  renderer（`clients/artifact-viewer`，无 HTML parser/dangerouslySetInnerHTML/网络/存储）。

## 验收（全部真实执行）

- [x] Domain/application 单元测试：grammar 边界、digest golden、CRLF/bounds、corruption、
      replay/conflict、global/unrequested type、事件 payload canonical（`go test ./...` 全绿）
- [x] `go test -race`（artifact/agent/harness/orchestration）全绿
- [x] PostgreSQL integration（`go test -tags=integration ./tests/integration` 全套 9.3s ok）：
      021 forward + 约束、并发 8-goroutine 唯一 winner、replay/冲突/类型槽、lease/foreign、
      corruption→Internal、分页 exact last page、restart 后 replay/list、digest 覆盖面
- [x] Harness：Fake 两类输出（确定性、terminal 前、恰一次/type）、无效请求拒绝、sink 失败
      中止 run、DeepSeek/Generic 仍 false/empty、worker 缺失 type fail closed、超时/取消回归
- [x] Gateway/public API：private RPC 保持 deterministic 404（既有测试回归）、allowlist
      前缀语义不变（`GetReviewArtifact` 属已公开 ArtifactService）、CreateArtifact
      server-owned 字段拒绝含新字段
- [x] Browser E2E `make test-artifact-review`：真实 PostgreSQL + Core + harness-host +
      Gateway + Chromium 全链路（submit → materialize → timeline 点击 → 精确内容 →
      Center 重载后仍在）
- [x] `make generate` 二次无 tracked diff；`make check` 通过；`buf breaking` 对 main 通过；
      `git diff --check` 干净
- [x] UI before/after/current + notes.md（before/ 基准 d80320c 旧 UI + current/ 复制；
      after/ 四张 + current/ 同步；全部固定 fixture，judge 两轮验收通过后定稿）
- [x] 文档：ADR-0008、implementation.md（Project review artifacts 小节）、docs/status.json
      （Artifact → working，Broker/Router/Desktop 证据追加）、本任务记录

## 必跑命令结果汇总

| 命令                                        | 结果                                                             |
| ------------------------------------------- | ---------------------------------------------------------------- |
| `make bootstrap` / `make check`             | ✅（基线与本分支均通过）                                         |
| `make test-integration`                     | ✅                                                               |
| `make test-deepseek-fixture`                | ✅                                                               |
| `make test-lan-pairing`                     | ✅                                                               |
| `make test-e2e`                             | ✅（含本切片回归；基线期一次镜像构建 registry 抖动，重建后通过） |
| `make test-artifact-review`（新增）         | ✅                                                               |
| `make test-podman-fixture`                  | 非本任务门禁（宿主无 rootless Podman，未冒充）                   |
| `buf breaking --against '.git#branch=main'` | ✅ additive only                                                 |

## 交接

- 已知边界（诚实 unavailable）：review subtype 仅两个文本型；Object Store/图片/PDF/patch
  apply/审批/`context_refs` 读取/App Bridge artifact.\*/DeepSeek 结构化输出全部未实现。
- 风险：`project_review_artifact_outputs` 的 publication 引用列（event_id/sequence/occurred_at）
  是 Artifact 表中的 Agent 事实引用，仅为幂等 replay 服务；ADR-0008 §1 有说明。若未来事件
  stream 改版需同步评估该列的迁移。
- 下一步建议：Agent `context_refs` 引用 review artifact、Artifact 审批 deep link、移动端
  viewer 均可复用本切片的 typed read 与 renderer allowlist。
