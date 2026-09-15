# V2 持续开发会话与 Web 应用接续（B00–B10）

- 任务提示词：[docs/prompts/20260915-glm-5.3-v2-p0-p2-goal.md](../prompts/20260915-glm-5.3-v2-p0-p2-goal.md)
- 基线：`main@a33e71f`；分支：`feat/v2-agent-workspace-web-continuity`
- 依据：[V2 架构](../structure-v2.md) §3–4
- 状态：in_progress（B00–B04 已完成，B05 待启动）

## 工作包状态

工作包 | 状态 | 依赖 | 实现入口/提交 | 验收编号 | 实际命令与结果 | 长期证据路径 | 未决问题/下一步
------ | ---- | ---- | -------------- | -------- | -------------- | ------------ | ---------------
B00 | done | — | `docs/architecture/v2-harness-workspace-baseline.md`（本分支首个提交） | A01 | 见下"B00 证据" | 基线文档 + tmp/b00 探测脚本输出 | 上游无 wire 级 session resume；B03 采用"每会话常驻官方 runtime 进程 + jsonl 持久化"
B01 | done | B00 | ADR-0030/0031；`api/proto/workos/agent/v1/session.proto`、`project/v1/workspace.proto`、`workload/v1/workspace.proto`、`surface/v1/continuity.proto`；native/pty 新增 Detach；migration 057–059；domain 状态机（core agent session、runtime surface continuity） | 契约层 | `buf lint` 通过；`go build ./...`、`go vet`、`go test ./internal/core/agent/... ./internal/runtime/surface/... ./internal/runtime/{ptyhost,nativehost}/...` 全绿 | ADR + 契约 + migration + 状态机测试 | B02 起按新契约实现持久化与业务链路
B02 | done | B00、B01 | runtime `workspacehost`（sources/Prepare + 私有 WorkspaceHostService）；PTY/Native 引擎接工作目录（幂等摘要含目录）；Core `ProjectWorkspaceService` + migration 058 + workspaceclient；`WORKOS_RUNTIME_WORKSPACE_MOUNTS` 环境覆盖；`tools/workspace-execution` 门禁 | A02、A03（PTY/files 侧；harness 侧 B03） | `sh tools/workspace-execution/gate.sh` → PASS（PWD=注册目录、git HEAD 一致、marker 双向、shell 写入落盘、host 写入 shell 可读、archive 后无 active）；`go test ./internal/runtime/ptyhost/adapters/shellexec ./internal/runtime/workspacehost/... ./internal/core/project/...` 全绿 | gate 日志 tmp/gate-ws.log + 门禁脚本 | 容器无 git 二进制（已记录，读取 .git 文件验证；开发工具链镜像留 B08）
B03 | done（软件侧；真实模型 A15 待外部凭据） | B00–B02 | Core AgentSessionService（migration 057、repository、应用、transport、gateway）；DeepSeek SessionManager（官方 base 组合常驻子进程、凭据指纹重生、工具事件映射）；worker 会话联动；终态钩子 FinishTaskRun；`tools/harness-sessions` 门禁 | A04、A05（输入幂等）、A07（部分） | `sh tools/harness-sessions/gate.sh` → PASS（官方 runtime 两轮原生续聊、真实 bash 工具写文件、has_turn1=true 上下文延续、重放/冲突/关闭语义）；单测全绿（deepseek adapter 7 项会话测试 + agent 包） | gate 日志 tmp/gate-hs-final.log；fixture 会话流 | harness-host 重启后原生上下文不可恢复（上游无 wire resume，基线记录）；凭据轮换=进程重生（记录于 README）；A15 真实模型与 A06 harness 重启恢复的完整证据留 B09
B04 | done（软件侧只读切片；写入工具/审批/预览留后续包） | B01–B03 | `deploy/harness/workos-tools.mjs`（零依赖 cordis 插件，configuration-relative 行）；deepseek sessions.go（workos-tools 行 + 按会话拷贝插件 + WORKOS_TOOL_* 子环境）；ports.SessionExecution 增 OwnerUserID/ProjectID；worker 从 task owner/target_scope 派生；harness-host 传 CoreURL+DeviceID；Dockerfile 拷贝插件；fixture SESSION_WORKOS_INFO 流 | A08（只读链路 partial；A09 未动） | `sh tools/harness-sessions/gate.sh` → PASS（三轮：bash 工具、上下文延续、workos_project_info 真实返回项目名）；`go build ./... && go test ./...` 全绿（docker golang:1.26.7-bookworm） | gate 输出 + internal/harness/adapters/deepseek README §Read-only WorkOS tools | 插件为外部文件不能 import 闭包内裸包名（schemastery 等）——零 import 原样注册；工具仅只读，审批行保持 policy: ask；写工具/成果创建/预览/审批路径留 B05+；真实模型证据仍留 A15
B05 | todo | B01、B03、B04 | — | A17（部分） | — | — | —
B06 | todo | B01 | — | A10、A11、A13 | — | — | —
B07 | todo | B01、B06 | — | A12 | — | — | —
B08 | todo | B02、B05、B06、B07 | — | A17 | — | — | —
B09 | todo | B00–B08 | — | A01–A18 | — | — | —
B10 | todo | 其余包 | — | A18 | — | — | —

## B00 证据（2026-09-15，host probe）

对官方锁定 runtime `deepseek_harness_runtime_bin==0.1.1rc1`（x86_64 wheel sha256
`8eb31e3a…137b`，与 Dockerfile 一致）做了静态源码提取 + 动态协议探测。完整结论在
[基线文档](../architecture/v2-harness-workspace-baseline.md)。关键事实：

- `sdk-jsonrpc-server` 仅 dispatch `initialize` / `session/prompt` / `shutdown`；
  0.1.5rc1 相同 → 保持 0.1.1rc1 不升级。
- 同进程同 sessionId 多轮续聊：第二轮模型请求包含第一轮 user/assistant（
  `COUNT total=6 markers=TURN1`）→ 原生上下文累积成立。
- 运行中追加输入：官方语义为排队到下一轮（`agent/inbox/spliced target=next-turn`）。
- 官方 base 组合（非 agent-spine-demo）注册 `bash/read/write/edit/glob/grep`；
  bash 真实执行 `pwd` 返回工作区路径；参数校验、[exit code] 标记真实存在。
- `write` 工具越界写被 `workspace-write` sandbox 拒绝；工作区内成功。
- bash 子进程 env 中 `DEEPSEEK_API_KEY`/`DSH_CORDIS_CONFIG` 计数为 0（官方 env 隔离）。
- bash-local 执行器不 confine（`dsh-bash-sandbox` 未打入 runtime bin）→ bash 可越界写，
  WorkOS 必须自己实施进程级边界（B02/B03 设计约束）。
- `session-persistence-jsonl` 追加式持久化每会话事件日志；跨进程 wire 级 resume 在
  0.1.1rc1/0.1.5rc1 均不存在（服务层 `agents.resume` 未暴露）。

## 恢复入口

- 当前包：B04 已完成（Harness 调用 WorkOS 的最小只读授权工具集）；下一包 B05。
- 已完成动作：B00（f9cffa8）、B01（c0e28bc）、B02（workspacehost + binding + PTY/Native
  工作目录 + 门禁 PASS）、B03（342bcc9 + 8b377de：会话契约/常驻官方 runtime/门禁 PASS）、
  B04（未提交：workos-tools 插件 + 会话子环境授权事实 + 三轮门禁）。
- B04 关键事实：官方 runtime 的 packaged-bin 只解析 closure 内的裸包名，外部
  configuration-relative 插件不能 import `@deepseek-ai/schemastery` 等——插件零 import，
  以原生对象调用 `ctx.tools.register`（行 `name: './workos-tools.mjs'`，spawn 时拷入
  会话 stateDir）。owner/project 由 worker 从 task 事实派生并注入子环境
  （WORKOS_TOOL_OWNER_ID/PROJECT_ID/CORE_URL/DEVICE_ID），模型无法提交。
- 正在运行的进程/日志：无（harness-sessions 门禁已清理，fixture profile 已停）。
- 已验证命令（B04，docker golang:1.26.7-bookworm）：
  `go build ./...`、`go test ./...` 全绿；`sh tools/harness-sessions/gate.sh` →
  `harness-sessions: PASS (official runtime, three native turns, real bash + WorkOS tools)`；
  另做过本地真实 runtime 探针（tmp/wt-probe 驱动脚本 + 假 Core）验证插件加载/工具执行/
  身份头，属一次性探测证据未入库。
- 下一条具体动作：B05（Agent 入口做成可继续工作的会话窗口；UI 变更需 before/after 截图）。
- 阻塞：无。注意：make/node/go 不在宿主机上，全部走 docker（buf 1.55.1 +
  golang:1.26.7-bookworm，模块缓存卷 workos-go-cache）。

## 验收矩阵（A01–A18）

按提示词 §6 逐行补齐实际命令、结果与证据路径；当前仅登记 B00 已覆盖行。

| 编号 | 状态 | 证据 |
| ---- | ---- | ---- |
| A01 | pass（软件侧） | 基线文档能力矩阵 + 动态探测记录（tmp/b00 摘要已写入基线文档） |
| A02 | pass（软件侧，PTY+files+真实磁盘） | tools/workspace-execution/gate.sh PASS |
| A03 | pass（软件侧） | store_linux_test（openat2 越界/只读）+ workspacehost 单测 + binding 归档/revision 测试 |
| A04 | partial（单元级：deepseek sessions_test 进程复用/重生/cordis/工具事件/错误分类；真实 runtime E2E 留 B04/B09） | internal/harness/adapters/deepseek/sessions_test.go |
| A08 | partial（B04 只读链路：Harness → workos_project_info → Core GetProject → 真实项目名返回，门禁三轮 PASS；撤权/跨项目引用为 owner-scoped 服务端校验+env 派生，专项断言留 B09） | tools/harness-sessions gate + internal/harness/adapters/deepseek |
| A05–A07、A09–A18 | todo | — |
