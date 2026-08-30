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

- [x] strict JSON goldens（unknown/duplicate/missing/extra/trailing/malformed/oversize/
      control/UTF-8）
- [x] raw structured stream 不进 AssistantDelta/message；validated summary 有界
- [x] batch 原子性（all-or-none/exact replay/conflict，真实 PostgreSQL；restart 由
      test-credential-vault 与 deepseek fixture 门禁覆盖）
- [x] combined credential revoke / lease loss / context mismatch / cancel 行为
- [x] `make test-deepseek-structured-review`（PostgreSQL + mTLS + fixture + Gateway + Chromium）
- [x] `make check` + 既有全部门禁
- [x] 文档与 `docs/status.json`

## 执行记录

- 基于 `feat/artifact-agent-context` 完成提交（ad3747a + 24625b7）建立 stacked branch
  `feat/deepseek-structured-review`。
- 门禁（真实执行，逐项退出码核对，阶段 C 末整批矩阵全部执行）：
  - `make check` / `make test-integration` / `make test-deepseek-fixture` /
    `make test-artifact-review` / `make test-artifact-context` /
    `make test-deepseek-structured-review` / `make test-lan-pairing` / `make test-e2e`：
    全部 EXIT=0；
  - `go test -race ./internal/core/credential/... agent/... artifact/... orchestration/...
harnesscatalog/... harness/...`：全绿；
  - `buf lint`：通过；`buf breaking api/proto --against .git#branch=main,ref=aa560bb`：无破坏；
  - `make generate` × 2：无 tracked 差异；
  - secret 扫描：无 PEM/bearer/provider key 入库；tmp/ 临时材料未跟踪。
- 实现要点：strict parser（`structured.go`）含 duplicate-key / trailing-content / unknown-field
  拒绝与 bounds 镜像；batch 协议经 `BatchMaterializerAdapter` 在 composition root 适配
  （两模块零交叉 import）；fixture 支持 structured 响应与 malformed/extra/missing/oversize/
  invalid 五种失败模式并验证 output contract/Authorization（不打印 header）。
- 修复记录：materializer 重构曾遗漏单条路径的 `tx.Commit`，被
  `TestReviewArtifactConcurrentMaterialization`/`TestResolveTaskContext*` 及时捕获并在本阶段
  修复（原子 batch 测试此后全绿）。
- UI：本阶段仅使既有 checkbox/context 路径从 unavailable 变为 working，像素不变，复用阶段 B
  current 证据（无差异截图不制造）。
- commit：`af87aa6`（`feat: add DeepSeek structured review outputs`）。

## 交接

- 三个 stacked branch：`feat/central-credential-vault`（87a0621+e3f69ec）→
  `feat/artifact-agent-context`（ad3747a+24625b7）→ `feat/deepseek-structured-review`
  （af87aa6+docs）。未 merge、未 push。
- 未决风险：模型 structured 输出依赖 prompt compliance + strict parser 的 fail-closed（非安全
  边界）；cost 仍 unavailable（无价格表）；本地 fixture 只覆盖 deterministic 路径，真实
  DeepSeek smoke 明确不在本批范围。
