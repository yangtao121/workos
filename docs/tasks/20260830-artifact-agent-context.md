# Task: Review Artifact 作为 Agent Context（阶段 B）

- 状态：done
- Owner/Agent：WorkOS 实现智能体
- 进程/模块：workos-core（Agent transport/router、Artifact、orchestration resolver）、
  harness-host（worker + Fake/DeepSeek/Generic adapters）、desktop-web（Use as Agent context）
- 依赖：阶段 A（feat/central-credential-vault 完成提交）；ADR-0008 artifact 事实

## 目标与范围

按批次 prompt 阶段 B：Desktop 可把当前 Project 的 immutable Markdown/Unified Diff Artifact
选为 Agent context；Core 在入队前校验 owner/project/digest/provider capability；执行时只按
active task lease 解析 canonical bounded content；Provider 不能自行读取 Artifact 或选择 scope。

第一版唯一 canonical ref：`artifact.review.v1` + UUIDv7 + `sha256:` digest；≤4 refs、保持
顺序、拒绝重复与同 ID 不同 digest；resolved 聚合 ≤ 1 MiB；global task 与 App Bridge 不接受
context（ADR-0010）。

不包含：workspace/URL/RAG context、App Bridge context API、多 modal、structured output
（阶段 C）。

## 协议/数据影响

- `HarnessCapabilities.supported_context_ref_types = 18`（additive，exact list）。
- TaskExecutionService additive `ResolveTaskContext`（request 仅 lease+worker；response 按序
  返回 ref_type/artifact_type/id/digest/title/media_type/content）。
- 无新 migration、无新表：refs 存既有 task payload 快照。

## 验收

- [x] 语法/集合校验、provider capability exact-match 裁决、digest/foreign/project 校验、
      lease-bound 解析（happy/replay/foreign/digest-mismatch/foreign-artifact/wrong-type）
      —— 单元 + 进程内 PostgreSQL 测试
- [x] Desktop chip/submit/上限/切 Project 隔离 —— ArtifactWindows/Desktop 单元测试 + 浏览器
      spec（artifact-context.spec.ts）
- [x] `make test-artifact-context`（stack 级：Go 进程内 PostgreSQL 协议测试 + 真实跨进程
      Chromium 链路 2 条）
- [x] `make check` + 既有全部门禁
- [x] 文档与 `docs/status.json`

## 执行记录

- 基于 `feat/central-credential-vault` 完成提交（87a0621 + e3f69ec）建立 stacked branch
  `feat/artifact-agent-context`。
- 门禁（真实执行，退出码逐项核对）：
  - `make check`：PASS；
  - `make test-integration`：PASS；
  - `make test-deepseek-fixture`：PASS；
  - `make test-artifact-review`：PASS；
  - `make test-artifact-context`：PASS（TestResolveTaskContext happy/replay/foreign +
    digest-mismatch/foreign-artifact/wrong-type 三条 fail-closed 子用例 + 浏览器链路 2 条：
    pin→chip→context-bound task（fake receipt 精确 digest）→提交后 chips 消费→切 Project
    清空；以及 5 artifacts/4-chip 上限与固定提示）；
  - `make test-lan-pairing`：PASS；`make test-e2e`：PASS；
  - `go test -race ./internal/core/agent/... ./internal/core/artifact/... ./internal/core/orchestration/... ./internal/harness/...`：全绿；
  - `buf lint` 通过；`buf breaking api/proto --against .git#branch=main,ref=aa560bb` 无破坏。
- UI 证据：`docs/ui/desktop-web/changes/20260830-artifact-agent-context/{before,after,notes.md}`，
  `after/` 两张已同步 `current/`（1440x900 dsf1，synthetic fixtures，无 digest/ID 入图）。
- 2026-08-31 复核修复：原两张 after 截图内容相同，context chip 被 Artifact Center 覆盖且
  chip 样式缺失；现已补齐 chip/type/remove 的确定性样式，关闭 covering window 后重采，
  before/after 文件名逐张对应，并隐藏与本视觉契约无关的随机 task UUID/历史 Project 列表。
- commit：`ad3747a`（`feat: resolve review artifacts as agent context`）。

## 交接

- ContextRef 语义与执行协议已固定（ADR-0010）；fake/deepseek 已声明
  `supported_context_ref_types=["artifact.review.v1"]`（证据先行），generic 保持空并 fail closed。
- 未决风险：暂无阻塞；structured output（阶段 C）基于本提交 stacked。
