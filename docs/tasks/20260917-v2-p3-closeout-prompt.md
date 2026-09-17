# Task: V2 P3 收尾验收 Cursor 详细任务书

- 状态：done（提示词文档交付；P3 功能收尾尚未实施）
- Owner/Agent：Codex
- 进程/模块：文档；`docs/prompts`、`docs/tasks`
- 开发分支/worktree：`docs/v2-p3-closeout-prompt` / `/home/aquatao/workos`
- 集成交付：按用户 2026-09-17 追加指令，提交文档并快进合并至本地 `main`，
  删除已合并任务分支；不推送远端。
- 基线：`main@6e8bf37`，开始时工作树干净。
- 依赖：`docs/structure-v2.md`、P0–P2/P3 任务与验收记录、ADR-0033、现有协议和测试。

## 目标与范围

对照 V2 设计与当前实现，确定下一步，并交付适合 Cursor 执行的详细中文 prompt。
当前建议为 P3 验收收尾：补齐既有发布链的失败、并发、崩溃、授权和兼容性证据，
修复其中发现的问题；不重复实施 P0–P3，不提前启动 P4。

本轮只编写任务书，不实施上述产品变更。物理 LAN 第二设备验收与 P4 保留原边界。

## 协议/数据影响

none。提示词须约束后续契约先行、迁移 owner、生成代码与状态更新。
本轮不变更功能进度，不修改 `docs/status.json` 或生成的 README 状态。

## 验收

- [x] 根据代码、Proto、任务记录与已有测试确认下一步及其依据。
- [x] 给出文件入口、依赖顺序、逐项断言、故障注入方式、验收矩阵和停止条件。
- [x] 明确本轮 prompt 交付与后续功能完成的区别，保留未验证环境限制。
- [x] 完成文档格式、链接和工作树检查，记录执行过及未执行的检查。

## 交接

交付：[Cursor V2 P3 收尾任务书](../prompts/20260917-cursor-v2-p3-closeout.md)。
后续执行任务沿用 `20260916-v2-p3-real-artifact-delivery.md` 作为单一功能任务事实源。

## 判断依据

- `main@6e8bf37` 已合并真实发布与回滚；现有 P3 任务 C08 为 scaffolded，
  A06/A08/A14/A15/A16/A17 为 PARTIAL，所以先收尾 P3，再独立安排 P4。
- 静态检查 `p3StartRepair` 发现测试直接插入 Incident，原 C09 要求的真实 Docker
  故障到监督修复入口尚需端到端证据；提示词 F01 明确补齐。
- 现有 `p3-delivery.spec.ts` 使用真实 RPC 和 Surface URL，但没有完整走桌面
  App Library/Versions 操作；F02 补实际桌面用户链，保留现有 HTTP 证据。
- 提示词划分 D00–D07、F01–F27，说明真实提交窗口注入、数据/运行双重断言、
  授权语义、隔离门禁、旧行为回归、视觉要求与完成条件。
- 未新增产品能力；Release 可选展示字段不扩展，P2 物理 LAN 第二设备、P4、
  rootless、Docker memory.high 继续保留现有边界。

## 验证记录

- 已读：V2 与原设计相关章节、当前实现说明、状态、P0–P2/P3 任务及原提示词、
  ADR-0033、BuildTest/Artifact/Release Proto、产物/构建/发布代码、已有集成和浏览器测试。
- Git 基线/工作树、两份文档的相对链接、D00–D07/F01–F27 引用：静态检查通过。
- 固定 Node Docker 镜像运行仓库 Prettier：两份新增文档格式化完成。
- `make generate`：通过，生成后没有 tracked 文件差异；未修改功能状态。
- `make check`：通过（退出码 0）；包括 Proto/sqlc、Go、TypeScript 架构/lint/格式/测试、
  Desktop 构建及生成状态检查；Desktop 单测 25 files / 168 tests 通过。
  宿主无 make/Go/Node，使用现有 `workos-make:local` 和 Makefile 固定工具链，
  仓库按同一绝对路径挂载，映射 Docker socket。
- `git diff --check`、生成文件与功能代码无 tracked 差异检查：通过。
  交付仅包含本任务记录和 prompt；集成前无其他工作树改动。
- 未运行 P3/Workspace 等产品专项集成/E2E，未调用真实模型，未修改 UI 或采集截图。
  本轮是提示词文档交付，这些功能验收由后续 Cursor 任务实施并留证。

实际工具链命令（均在仓库根执行）：

```sh
docker run --rm -v /home/aquatao/workos:/home/aquatao/workos -w /home/aquatao/workos -v /var/run/docker.sock:/var/run/docker.sock workos-make:local make generate
docker run --rm --user 1000:1000 --group-add 983 -v /home/aquatao/workos:/home/aquatao/workos -w /home/aquatao/workos -v /var/run/docker.sock:/var/run/docker.sock workos-make:local make check
docker run --rm --user 1000:1000 -v /home/aquatao/workos:/workspace -w /workspace node:24.19.0-bookworm-slim node node_modules/prettier/bin/prettier.cjs --check docs/prompts/20260917-cursor-v2-p3-closeout.md docs/tasks/20260917-v2-p3-closeout-prompt.md
git diff --check
git diff --exit-code -- README.md gen sdk docs/status.json internal
```

第一次生成使用工具容器默认用户，生成目录的 owner 已恢复到仓库用户 1000:1000；
后续工具检查均显式使用该 UID/GID。生成文件内容与原提交一致。
