# Task: Review Artifact 作为 Agent Context（阶段 B）

- 状态：active
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
- [ ] `make test-artifact-context`（stack 级）
- [ ] `make check` + 既有全部门禁
- [ ] 文档与 `docs/status.json`

## 执行记录

（回填：门禁结果、commit hash、风险。）

## 交接

（回填。）
