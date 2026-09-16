# Task: V2 P3 GLM-5.3 详细执行任务书

- 状态：done（任务书交付；P3 功能尚未实施）
- Owner/Agent：Codex
- 进程/模块：仅提示词与任务文档
- 分支/worktree：`docs/v2-p3-goal-prompt` / `/home/aquatao/workos`
- 依赖：`main@3866e92`、V2 设计、P0–P2 最终交付、现有 Build/Test、Registry、Workload、Reliability 契约与测试

## 目标与范围

编写可直接交给 GLM-5.3 连续执行的 P3 任务书，详细规定不可变构建产物、真实运行、
发布与回滚的实施方案、工作包、失败恢复、验收及交接。保留 P2 A16 的物理设备验收缺口，
不混入 P4，也不将现有 Docker 开发预览当成正式 App 运行器。

本轮仅交付文档，不实现产品功能，不运行产品、生成器、构建或测试，不修改功能进度。

## 协议/数据影响

本次 none。任务书会说明下一轮需要修改的 Proto、Schema、数据所有权与迁移纪律。

## 验收

- [x] 提示词给出固定范围、默认技术选择、依赖顺序和明确完成条件。
- [x] 静态核对实际代码/协议/测试入口，区分已有能力与待实现行为。
- [x] 说明真实构建、运行行为、回滚、故障矩阵和 UI 证据要求，防止模拟验收冒充完成。
- [x] 仅修改提示词和本任务记录；记录静态检查结果与未运行项目检查的原因。

## 交接

起始工作树干净，`main` 与 `origin/main` 均为 `3866e92`。
已阅读根规则、V2、上轮任务和任务书、ADR-0006/0024/0026/0032、Build/Test 与修复协议、
manifest Schema、Workload 引擎/监督端口、Reliability 编排、现有 repair-buildtest 门禁及 UI 规范。
实际代码确认现有修复门禁的 Workload engine 为 fake-fixture；P3 必须增加真实运行验收。

本任务遵循用户本轮写提示词的授权，以及前轮不改代码、不运行程序的限制。
只做文件阅读、文档编辑与 Git 工作树检查；`make generate`、`make check`、产品测试和截图
均留给下一轮功能实现，不在提示词任务中执行。

交付：[GLM-5.3 V2 P3 真实交付 Goal 任务书](../prompts/20260916-glm-5.3-v2-p3-real-delivery-goal.md)。
它规定 C00–C10 的实施顺序、A01–A20 的验收证据、默认的固定镜像+不可变应用包方案、
正式 Docker Workload 能力探测、Core/Runtime/Reliability 对账、真实 A→B→A HTTP
验证以及崩溃/授权/回滚矩阵。P2 A16 和 P4 明确保留为独立缺口。

静态核对记录：`git status --short --branch`、`git branch -vv`、`git worktree list`、
`git log -5 --oneline` 已读；上轮总任务 A15 PASS、A16 BLOCKED；现有 build result 无
持久可部署包，process engine 清理临时目录，staged manifest 仍保留 runtime.image；
现有 `tools/repair-buildtest/compose.yaml` 对 Workload 使用 `fake-fixture`，所以旧
`test-repair-buildtest` 不能证明真实新版服务。当前分支只新增本任务记录与提示词。
