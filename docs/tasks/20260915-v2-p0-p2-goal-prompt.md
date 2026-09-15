# Task: V2 下一阶段 GLM-5.3 批量执行提示词

- 状态：done（提示词交付；下一阶段功能尚未实施）
- Owner/Agent：Codex
- 范围：仅编写 `docs/prompts/` 执行提示词及本任务记录
- 分支/worktree：`docs/v2-p0-p2-goal-prompt` / `/home/aquatao/workos`
- 依赖：`main@09d70cc`、`docs/structure-v2.md`、当前实现与相关 ADR/Proto/测试

## 目标

将 V2 明确的下一阶段 P0、P1、P2 转为一份可交给 GLM-5.3 在 goal 模式连续执行的
完整任务书。细化依赖顺序、代码入口、默认技术选择、阶段验收、恢复记录和环境阻塞处理。
不实施产品功能，不扩大到 P3/P4 或旧版剩余能力总攻，不修改进度事实源。

## 验收

- [x] 提示词覆盖持续 Harness 会话、真实共享工作区、授权工具、Web 应用与 LAN 设备接续。
- [x] 保留 Harness 复用和六进程边界，限定 Kasm 理念吸收范围。
- [x] 明确整批执行、每包交付、失败处理、上下文恢复和最终完成标准。
- [x] 标明当前事实与新增实现要求，区分 fixture、真实模型和独立设备证据。
- [x] 文档写入 `docs/prompts/`，仅输出文档，不执行下一阶段功能开发或测试。

## 交接

初始工作树干净，当前 `main@09d70cc` 已与 `origin/main` 对齐。已阅读 V2、根规则、
DeepSeek adapter 说明与执行代码、Agent/Harness/TaskExecution/Project/Surface/Workload
Proto、相关集成测试入口、ADR-0028/0029、UI 记录规范及旧 GLM 交接文档。
旧交接仅作为历史风险参考，本轮提示词采用 V2 的 P0–P2 范围。

本任务为提示词文档交付；沿用用户对文档任务不运行验证的要求，不运行 generate、
make check、产品测试或 UI 采集。下一阶段包含功能变更，提示词保留仓库对此的验收要求。

交付文件：[GLM-5.3 V2 P0–P2 Goal 任务书](../prompts/20260915-glm-5.3-v2-p0-p2-goal.md)。
包含 B00–B10 共 11 个工作包、18 项验收、当前实现入口、默认设计、唯一实现分支/任务记录、
环境阻塞与持续执行规则。GLM 是接手开发模型，产品继续以 DeepSeek Harness 为优先接入。
未修改产品代码、协议、UI、进度事实或原 V2 方案；本次仅交付提示词和任务记录。

用户后续明确要求合并到 main 并删除其他分支。按此授权提交文档、快进合并到本地 main，
随后删除已合并的文档分支，仅保留本地 main；不推送或删除远端分支。
