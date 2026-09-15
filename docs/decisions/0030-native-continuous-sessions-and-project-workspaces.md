# ADR-0030：原生持续会话与项目执行环境

- 状态：Accepted
- 日期：2026-09-15
- 关系：structure-v2 §3.3/§3.6；ADR-0005/0009/0010/0011（任务、凭据租约、上下文信封）；
  [B00 基线](../architecture/v2-harness-workspace-baseline.md)。
- 调整：ADR-0028/0029 的会话在本 ADR 只改"关闭即回收"的单一生命周期（见 ADR-0031）；
  单次 Task 契约保持原义。

## 裁决

### 持续会话

1. WorkOS 新增 **AgentSession**（`workos.agent.v1.AgentSessionService`，migration
   `workos_core.agent_sessions`）：一个会话绑定一个 Project、一个 workspace binding
   revision、一个 Provider/Profile 快照和一个原生会话引用。Core 拥有关联、授权与
   输入事实；harness-host adapter 拥有原生会话接入与私有状态。
2. 每条被接受的输入（`agent_session_inputs`）以 `(session_id, client_input_id)` 幂等，
   以严格递增 `sequence` 排序，并关联一个既有 Task/run。Task 的完成或取消不关闭会话。
   一会话同时最多一个活动执行（`active_task_id` 唯一约束于执行状态）；活动执行存在时
   新输入持久为 `accepted`（排队），按序在上一个执行结束后 dispatch。重复提交同 key
   返回已记录输入；同 key 不同文本以摘要冲突拒绝。
3. 官方 DeepSeek runtime `0.1.1rc1` 的 wire 只有 `initialize/session/prompt/shutdown`
   （B00 证据），因此原生续聊由 harness-host 的**每会话常驻 runtime 子进程**承接：
   同 sessionId 的后续 `session/prompt` 在同一原生上下文继续（B00 动态证明）。会话
   事件经 `session-persistence-jsonl` 追加持久化到 harness-host 私有目录。跨进程
   wire 级 resume 上游不存在：harness-host 重启后已完成轮次的事件与投影可回放，
   继续对话需要新原生轮次，如实呈现，不以重放聊天文本冒充原生恢复。
4. 运行中追加输入的官方语义是排队下一轮（`agent/inbox` next-turn splice，B00 证据），
   不注入当前轮；UI 显示"已排队，下次执行使用"。
5. 取消走现有 Task 取消链并终止该输入的执行；会话历史保留。`CloseSession` 才禁止新
   输入并按策略清理原生私有状态。租约与凭据按执行粒度：每次 dispatch 重新领取
   Task lease 与凭据租约，租约到期/撤销杀掉该会话的 runtime 子进程（持久化日志保留）。
6. Core 只持久化关联与有界投影（`agent_session_events`：input 生命周期 + 会话状态，
   摘要 ≤2048 字符）；assistant/tool/usage 投影仍在 Task 事件流上，不新增第二套
   上下文压缩或"把聊天拼回 prompt"的实现。

### 项目执行环境

7. 新增 **WorkspaceBinding**（`workos.project.v1.ProjectWorkspaceService`，migration
   `workos_core.project_workspace_bindings`）：Core 管归属与授权（source 引用、读写
   模式、revision、归档）；runtime-host 管实际目录装配（`workos.workload.v1.
   WorkspaceHostService`，私有，不经 Gateway）。浏览器/Provider 不能自选宿主路径；
   workspace source 由操作员在 runtime-host 部署配置注册，`BindWorkspace` 只能引用
   已注册 source。首期一个 Project 一个 active binding。
8. 每次执行解析固定 workspace revision 与读写模式；只读 binding 拒绝写；归档/变更
   后旧 revision 拒绝新授权，重试不悄悄指向另一个目录。
9. 官方工具（bash/read/write/edit/glob/grep）经 B00 验证的官方 base 组合在
   runtime 管理的同一环境执行；`sandbox-policy mode: workspace-write` 约束文件工具。
   bash-local 不 confine（B00 证据），因此启用工具的部署必须由 WorkOS 施加进程级
   边界（受限身份、仅工作区可写、无 Docker socket/数据库连接），无法实施时该组合
   声明 unavailable。用户文件视图、Terminal、原生开发应用与 Harness 指向同一受控
   目录（B02 验收）。

## 后果

- `AgentTaskService` 单次任务语义不变；会话输入产生的 Task 与普通 Task 同池调度。
- 会话数与常驻子进程受 harness-host 有界策略约束（上限、空闲回收），不无限保活。
- 上游未来暴露 wire 级 resume 时，可在不改变本契约的情况下替换常驻进程实现。

## 验收

- 契约/状态机：`go test ./internal/core/agent/...`（会话与输入状态转换、幂等、
  冲突、跨项目引用拒绝）。
- 组合链（B03 后）：`make test-harness-sessions`——官方 runtime + 本地模型 fixture
  两轮代码修改/测试与重连恢复。
