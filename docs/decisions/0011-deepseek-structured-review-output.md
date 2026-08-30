# ADR-0011：DeepSeek Structured Markdown / Diff Review 输出

- 状态：Accepted
- 日期：2026-08-30
- 关系：建立于 ADR-0008 materialization 协议、ADR-0009 credential lease/私有通道、ADR-0010
  context envelope 之上；细化 `docs/structure.md` 7.3 的 DeepSeek review 输出阶段。

## 背景

DeepSeek 此前诚实报告 structured artifacts unsupported。本 ADR 固定其 structured review
输出：仅当 task 的 `output_artifact_types` 非空时启用 versioned structured mode，把严格解析、
完整校验的 Markdown/Unified Diff 输出通过 Core-owned Artifact materialization 协议原子发布。

## 决策

### 1. Vendor envelope/parser 只在 DeepSeek adapter

模型的最终回答必须是一个完整、唯一的 JSON document：

```json
{
  "version": "workos.deepseek.review-output.v1",
  "summary": "bounded plain-text summary",
  "artifacts": {
    "document.markdown.v1": "canonical candidate text",
    "code.unified-diff.v1": "canonical candidate text"
  }
}
```

- strict decoder：exact one JSON value、unknown fields 拒绝、无 prefix/suffix/code fence/
  prose；artifacts key set 恰好等于 request set（无缺失/额外/重复/alias，顺序由 request 决定）；
  summary ≤ 64 KiB、valid UTF-8、无 C0/C1（允许 LF/TAB）；
- parse 前限制 runtime aggregate bytes；构造 ArtifactOutput 前执行与 Core 相同或更严格的
  content 上限（512 KiB/20k 行/16 KiB 行、UTF-8、C0/C1 规则——adapter 内镜像实现，Core 端
  AppendTaskArtifact 仍独立验证）；
- adapter 固定 `output_key`（document/patch）与安全 title；模型不能选 key/title；
- structured run 保留 RunStarted，但 raw JSON/content fragments 一律不成为 AssistantDelta/
  AssistantMessage；只有验证后的 bounded summary 可成为 message；
- parse、set/content validation 或 sink 任一失败 → 不发 RunCompleted；worker 只写一个 sanitized
  RunFailed（非重试 protocol failure，不回显 response）。
- Prompt compliance 不是安全边界：模型忽略 contract 时 strict parser 只能 fail closed，绝不
  从自由文本正则"尽量提取"。

### 2. Atomic Artifact batch materialization

- 私有协议 additive 增加 bounded `AppendTaskArtifactBatch`（≤2 outputs）；保留单项 RPC 兼容
  Fake 与历史调用；
- Core 在一个事务内：锁定 active task stream → 验证 requested exact types/key slots →
  逐项 prepare/verify → 裁决全部 mappings → 写入全部 Artifact rows/mappings 与连续
  Core-minted ArtifactCreated events；任何一项失败整批零新增；
- request order 决定 event sequence；Provider 仍不能提交 owner/project/task/artifact ID/
  digest/time/sequence；
- all absent → atomic insert；all present exact → exact replay；mixed legacy/retry state 逐项
  验证（已有 exact 可 replay、缺失项同事务补齐）；任一 conflict/corruption → 零新写入；
- worker 只在 batch 成功后标记 requested types emitted；terminal 前仍检查 exact completeness；
  generic `AppendTaskEvent` 仍拒绝 Provider-built ArtifactCreated。

### 3. Capability 只在证据之后

只有 strict envelope、batch sink、failure matrix 和本地官方 runtime fixture 全部通过后，
DeepSeek 才声明 `structured_artifacts=true` 与 exact `document.markdown.v1`、
`code.unified-diff.v1`。同一 run 同时支持 ADR-0009 credential lease、ADR-0010 resolved
context、既有 token/runtime budget 与 structured output；任一失效都 cancel/kill child 并停止
后续 publication。usage 从官方 runtime 实际事件汇总；无价格表则 cost 继续 unavailable。

### 4. 模型输出是不可信 bytes

模型返回的 JSON 是不可信 bytes：wire/aggregate bound → 严格解析 → domain 校验 → 才能调用
Artifact sink；Core 仍从 active lease 派生 owner/project/task 并独立验证内容。本地 fixture
验证 official runtime 确实收到了 output contract 与 synthetic context，Authorization 使用
Vault lease 的 synthetic value，fixture/log/test failure 绝不打印 header；请求目标只为
literal loopback。

## 非目标（保持 unavailable）

image/PDF/binary artifact、patch apply/edit/download、HTML 渲染、tool calls、模型自选
key/title/identity、真实价格表。
