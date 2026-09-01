# Task: Project Knowledge Search overnight prompt

- 状态：done
- Owner/Agent：Codex
- 进程/模块：docs/prompts；仓库级实现交接设计
- 依赖：`main` at `0360ce5`；`docs/structure.md`、`docs/architecture/implementation.md`、
  `docs/status.json` 与现有 Indexer scaffold

## 目标与范围

为下一位实现智能体编写一个可无人值守执行至少七小时、预期九至十二小时的单分支 Prompt。Prompt
必须围绕一个明确的平台闭环，把当前仅有 health scaffold 的 Indexer 收敛为 Project Knowledge Search
working slice，并覆盖契约、Core 安全来源、Indexer 持久消费与检索、Gateway、Desktop、Agent context、
授权 Web Bundle App 的 `knowledge.read` Bridge/SDK、安全本机运维面、shadow-generation 全量重建、
视觉证据、集成/E2E、灾难恢复、重启与状态裁决。

Prompt 本身只定义任务，不实现功能；必须明确只创建一个 branch、一个 worktree、一个任务记录，
所有阶段串行，不 merge、不 push，不因普通澄清或独立环境波动提前停止。

## 协议/数据影响

none（本任务只新增 Prompt/任务记录；未来实现任务必须先 additive 修改 Proto，并新增 forward-only、
单进程所有权 migration）。

## 验收

- [x] 最终目标与 owner/Agent/App/operator 端到端链路唯一、可判定
- [x] 工作量分阶段覆盖预期九至十二小时，且不依赖 rootless Podman 或真实 Provider
- [x] 单分支/单 worktree/单任务记录纪律明确
- [x] 六进程、分层、数据所有权、identity、幂等、cursor、隐私边界明确
- [x] App grant-revision/revoke、opaque iframe、SDK/app-host 边界与专项门禁完整
- [x] Indexer local-only admin、online/full rebuild、generation promote/crash-resume 与灾难恢复门禁完整
- [x] UI 视觉证据、三个专项门禁、失败矩阵、状态升级条件与交接格式完整
- [x] 文档格式与仓库门禁通过，工作树在提交后可保持干净

## 交接

- Prompt：`docs/prompts/20260901-next-agent-project-knowledge-search.md`
- 唯一实现主线：Project review Artifact durable lexical index/search → Knowledge Center/Agent context →
  granted opaque App `knowledge.read` → local-only shadow-generation rebuild/recovery。
- 实现约束：只使用 `feat/v1-project-knowledge-search`、当前 worktree、
  `docs/tasks/20260901-v1-project-knowledge-search.md` 和一个写入 Agent；不 merge、不 push。
- 新增工作量：Runtime/Core grant-revision 重验、Bridge/SDK/app-host、App 浏览器门禁、Indexer Unix admin、
  `workosctl` status/rebuild/job、online generation promote、临时 projection 丢失后的 authoritative rebuild。
- Prompt 要求实现批次新增三个真实门禁：`make test-project-knowledge-search`、
  `make test-app-knowledge-search`、`make test-project-knowledge-rebuild`。
- 编写验证：`make generate` 连续两次 PASS；`make check` PASS；`git diff --check` PASS；
  `docker compose config --quiet` PASS。
- 未决风险：实际实现工作量取决于 private Core publication/reconciliation contract 与现有 App Bridge
  authorizer 的复用程度；Prompt 已要求采用 additive Proto、单进程 migration ownership 和阶段化提交，不得
  通过跨模块 SQL、固定成功 adapter 或扩大进程数规避。
- 本任务只提交 Prompt 与本记录；实现、migration、UI evidence 与状态升级全部留给下一位 Agent 在唯一
  feature branch 完成。
