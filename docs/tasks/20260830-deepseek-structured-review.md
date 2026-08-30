# Task: DeepSeek Structured Markdown / Diff Review（阶段 C）

- 状态：active
- Owner/Agent：WorkOS 实现智能体
- 进程/模块：harness-host（DeepSeek adapter structured mode + batch sink）、workos-core
  （AppendTaskArtifactBatch materialization）、本地 DeepSeek API fixture、desktop-web（既有
  checkbox/context 路径）
- 依赖：阶段 B 完成提交；ADR-0008/0009/0010

## 目标与范围

按批次 prompt 阶段 C：仅当 `output_artifact_types` 非空时启用 versioned structured mode；
DeepSeek 消费受控 Artifact context，把严格解析、完整校验的 Markdown/Unified Diff 输出经
Core-owned 原子 batch materialization 发布；malformed/oversize/unsupported/revoked 全部
fail closed；DeepSeek capability 只在全部证据之后翻转。

不包含：image/PDF/binary、patch apply、tool calls、真实价格表。

## 协议/数据影响

- TaskExecutionService additive `AppendTaskArtifactBatch`（≤2 outputs；保留单项 RPC）。
- 无新 migration；复用 Artifact materialization 表与映射。

## 验收

- [ ] strict JSON goldens（unknown/duplicate/missing/extra/trailing/malformed/oversize/
      control/UTF-8）
- [ ] raw structured stream 不进 AssistantDelta/message；validated summary 有界
- [ ] batch 原子性（all-or-none/exact replay/conflict/restart，真实 PostgreSQL）
- [ ] combined credential revoke / lease loss / context mismatch / cancel 行为
- [ ] `make test-deepseek-structured-review`（PostgreSQL + mTLS + fixture + Gateway + Chromium）
- [ ] `make check` + 既有全部门禁
- [ ] 文档与 `docs/status.json`

## 执行记录

（回填。）

## 交接

（回填。）
