# Task: WorkOS V2 中期架构修订

- 状态：done（文档交付；V2 功能待实施）
- Owner/Agent：Codex
- 进程/模块：架构与进度文档；无功能代码变更
- 分支/worktree：`docs/architecture-v2-midterm-review` / `/home/aquatao/workos`
- 依赖：`main@f2ab5d4`、原结构方案、当前实现说明、进度事实与既有验收记录

## 目标与范围

将本次中期讨论写入独立的 [V2 结构方案](../structure-v2.md)，保留原方案正文。
固定 Harness 复用、DeepSeek 优先、软件开发首个场景，以及 Agent 持续工作和
Web 直接使用应用两条主线。吸收 Kasm 的浏览器应用交付理念，保留后续集成选择；
近期先验收局域网跨设备接续，跨网络直接访问留到后续，不扩展公网部署专项。

补充原方案与实现文档的 V2 导航，纠正构建产物与实际运行版本之间的证据边界，
同步 `docs/status.json` 的事实说明，模块状态不升级。README 状态区由工具生成。
不实施 V2 功能，不修改 Proto、Schema、migration、应用代码或 UI。

## 协议/数据影响

none。V2 是待实施设计，未修改现行契约或取代已接受 ADR。后续功能任务先完成专项
ADR 和 Proto，再实现与验证。本次没有可见 UI 变化，无需新截图。

## 验收

- [x] V2 包含现状、设计调整、职责与接口方向、P0–P4 路线和验收场景
- [x] 仓库相对链接与官方来源可追溯，原方案正文保留
- [x] 实现文档与 `docs/status.json` 同步说明证据边界，模块状态不变
- [x] `make generate` 后无协议/SQL 生成差异，README 与状态事实一致
- [x] 按用户明确指示停止 `make check`，不继续验证，不记为通过
- [x] 交付仅含本任务文档及工具生成的 README 状态区

## 交接

初始工作树干净，位于 `main@f2ab5d4`。在唯一文档分支完成修改。
已核对 Agent/Surface Proto、DeepSeek 配置与测试、构建器、候选版本生成和
相关验收测试；产品测试历史来自既有任务记录，不作为本次重新执行的结果。

已完成：V2 正文、原方案与实现说明导航、构建交付证据澄清、四个模块的进度事实补充，
以及工具生成的 README。没有修改功能代码、协议、migration 或 UI，模块状态保持原值。

用户最后明确要求“不需要验证，改完文档合并到 main”。据此停止正在运行的完整检查，
本次文档任务不以 `make check` 通过为完成前提；本地提交并快进合入 main，不推送远端。

在该指示之前已经执行：

- 文档 Prettier 格式化完成；15 个相对链接、原方案正文保留和模块状态不变已检查。
- 主机缺少 make，改由 `golang:1.26.7-bookworm` 容器运行仓库原有 `make generate`，
  挂载同路径工作树、Docker CLI 和 socket；结果通过，日志：
  `tmp/architecture-v2-generate.log`。
- `git diff --exit-code -- gen sdk internal api schemas` 无差异；README 由状态生成器更新。
- `make check` 使用相同容器入口启动，后按用户指示停止其父容器及 Go 检查子容器。
  日志 `tmp/architecture-v2-check.log` 是未完成记录，不能作为 PASS 证据。

未决事项属于 V2 后续功能路线：Harness 原生能力与版本接入、持续开发会话、局域网应用
接续、真实构建产物发布和跨网络扩展。下一项开发建议见 V2 的 P0/P1，本次不执行。
