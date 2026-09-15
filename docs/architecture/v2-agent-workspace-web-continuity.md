# V2 持续开发会话与 Web 应用接续 — 交付架构

- 任务：[20260915-v2-agent-workspace-web-continuity](../tasks/20260915-v2-agent-workspace-web-continuity.md)
  （提示词 [docs/prompts/20260915-glm-5.3-v2-p0-p2-goal.md](../prompts/20260915-glm-5.3-v2-p0-p2-goal.md)）
- 决策：[ADR-0030 原生持续会话与项目执行环境](../decisions/0030-native-continuous-sessions-and-project-workspaces.md)、
  [ADR-0031 持续应用与设备接续](../decisions/0031-continuous-apps-and-device-takeover.md)
- 基线：[V2 Harness Workspace 基线](v2-harness-workspace-baseline.md)（官方 runtime `0.1.1rc1` 能力矩阵）
- 实现边界：[implementation.md](implementation.md)

本文描述本轮交付后的真实用户能力、部署配置、运行/连接策略、恢复行为、
验收命令与已知限制。状态结论以 `docs/status.json` 与任务记录中的证据为准；
本文不扩大任何能力声明。

## 1. 用户旅程

### 1.1 Agent 会话流（ADR-0030）

1. 用户创建/选择 Project，将 Harness 绑定到 DeepSeek（项目可选绑定，
   保留全局默认值）。
2. 打开 **Agent Sessions** 系统窗口（命令面板 / Home 启动台 / adaptive
   home 快捷入口；普通可关窗口，无新增常驻侧栏），New session 建立一个
   持续会话：Core 保存 project/provider/workspace 快照并映射到 harness-host
   侧一个**常驻官方 runtime 子进程**（每会话一个）。
3. 每条输入带客户端生成的 `client_input_id`（幂等键）。同一会话同一时刻
   至多一个活动执行；忙时提交的输入排队并按持久顺序提交，UI 显示排队态。
   官方 0.1.1rc1 无运行中 steer —— 排队即“下次执行使用”，不伪装已接收。
4. 每轮执行真实调用官方 runtime：原生 bash/read/write/edit/glob/grep 工具
   在会话工作区内真实读写文件、执行命令；B04 追加的只读
   `workos_project_info` / `workos_list_artifacts` 工具由 worker 从 task 事实
   派生 owner/project（模型无法提交身份扩大范围）。
5. 第二条指令在同一原生上下文继续（官方 runtime 同进程多轮，模型请求含
   第一轮历史）；页面刷新后 ListSessions + ListSessionInputs +
   WatchTaskEvents 从持久 cursor 恢复同一会话，不再次调用模型。
6. 「停止当前执行」（CancelSessionExecution，取消活跃 run 与排队输入）与
   「关闭会话」（CloseSession，禁止新输入并回收 harness 侧私有状态）是两个
   显式动作。历史 Task API 不变；Task 完成或取消不自动关会话。

### 1.2 工作区流（ADR-0030 B02）

1. 操作员用 `WORKOS_RUNTIME_WORKSPACE_MOUNTS` 在 runtime-host 注册
   owner/project/绝对路径（可选 `:ro`）的工作区来源；客户端只能引用
   source id，绝不提交宿主路径。
2. Core `ProjectWorkspaceService`（Bind/Get/List/UpdateAccess/Archive，
   revision 乐观控制）保存归属与授权；runtime `workspacehost`（私有
   WorkspaceHostService）拥有目录装配与执行事实。单 Project 一个活动绑定。
3. Terminal / Native 会话在该目录启动（幂等摘要含工作目录）；用户文件
   视图、Terminal、Harness 工具、开发应用读写**同一真实磁盘树**
   （workspace-execution 门禁以 pwd/git HEAD/双向 marker 文件核对）。
4. 只读绑定写入失败；归档/变更后旧 revision 拒绝新授权；越界路径被
   openat2 内核边界拒绝；两 Project 之间隔离。

### 1.3 应用连续性与接管流（ADR-0031）

1. Terminal / Native 窗口的「关闭窗口」与「项目切换」= **Detach**：仅释放
   本设备连接（native 另释放媒体 peer），程序按有界策略继续运行，PTY 输出
   持续累积。
2. 重开窗口：Home → **Running apps**（ListProjectSurfaces 服务端事实）发现
   运行中的 workload → **Attach** 同一实例（绝不二次 Create；无运行实例或
   continuity 不可用时如实回退创建路径）。
3. 单控制端：首附者在同事务获得控制代次 1；后续 attach 仅观察者并显示
   **Take control**；显式接管（RequestSurfaceControl）原子推进代次，旧
   controller 的输入/排队输入/resize/迟到续期立即失效。数据路径逐请求复查
   当前租约（PTY Write/Resize、native 输入 apply 时刻），不是前端隐藏输入框。
4. **Stop**（StopSurfaceWorkload）是唯一显式停止：确定性回收进程组并过期
   attachments。Restart 对一次性 PTY/native 会话如实 FailedPrecondition
   （无持久 argv）。
5. 设备 B 接管路径见 [LAN 第二设备 runbook](../runbooks/lan-second-device.md)
   （A16，操作员执行）。

## 2. 部署配置

### 2.1 环境变量

| 变量                                                                                                         | 进程         | 语义                                                                                                                                                               |
| ------------------------------------------------------------------------------------------------------------ | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `WORKOS_RUNTIME_WORKSPACE_MOUNTS`                                                                            | runtime-host | 操作员工作区来源覆盖：分号分隔 `owner:project:/abs/path[:ro]`；路径含 `:` 或非绝对路径拒绝启动。默认空 = 无工作区（Terminal 用 scratch 目录）                      |
| `WORKOS_HARNESS_SESSION_STATE_ROOT`                                                                          | harness-host | 会话私有状态根（默认 `/var/lib/workos/harness-sessions`，0700）。每会话子目录含 `home`/`state`/`ws`/`persistence`（原生 jsonl）/生成 `cordis.yml`/`runtime.stderr` |
| `WORKOS_RUNTIME_NATIVE_CANDIDATES`                                                                           | runtime-host | native WebRTC 候选范围：`loopback`（fail-safe 默认，单测锁定）或 `lan`（枚举真实 LAN 网卡、需 host 网络；无 STUN/TURN）                                            |
| `WORKOS_DEEPSEEK_ENABLED` / `WORKOS_DEEPSEEK_BASE_URL` / `WORKOS_DEEPSEEK_MODEL` / `WORKOS_DEEPSEEK_TIMEOUT` | harness-host | DeepSeek 适配器开关与端点（生产必须 HTTPS；HTTP 仅限字面回环 fixture）                                                                                             |
| `WORKOS_SURFACE_SESSION_TTL`                                                                                 | runtime-host | 交互会话 TTL（默认 15m，native 上限 30m —— 见 §3）                                                                                                                 |

凭据：DeepSeek key 只存 Core Credential Vault（workosctl admin socket），
每轮执行经 task-bound lease 到达会话子进程环境；轮换/撤销 = 子进程按指纹
重生（见 §5）。

### 2.2 Compose overlays

| Overlay                                                | 用途                                                                |
| ------------------------------------------------------ | ------------------------------------------------------------------- |
| `deploy/compose.harness-sessions.yaml`                 | harness-sessions 门禁：挂载会话状态根、启用 DeepSeek + fixture 端点 |
| `tools/surface-continuity/compose.yaml`                | surface-continuity 门禁：独立 compose project、一次性数据库与端口   |
| `tools/workspace-execution/compose.yaml`               | workspace-execution 门禁：真实 git 树挂载                           |
| `tools/v2-development-journey/`（gate 自带）           | 桌面 E2E：共享 dev postgres + fake provider + 真 PTY                |
| `--profile deepseek-fixture` / `--profile lan-pairing` | 本地 API fixture / TLS 生产配对栈                                   |

生产部署参考 `deploy/systemd` 与 `deploy/config/dev.yaml`；六进程边界不变。

## 3. 运行与连接策略

- **有界 30 分钟策略**：交互 PTY/native 会话沿用既有 30 分钟单上限
  （`persistent=true, keep_alive_seconds=1800`）。控制租约与 attachment 的
  过期共享同一上限；精确 controller 续期只移动到期时间，不升代次、不延长
  程序 TTL。到期后全员拒绝输入直至显式接管；程序本体由会话 TTL/空闲策略
  终结。没有无限保活。
- **控制代次（control generation）**：服务端绑定真实 device + attachment，
  事务内 FOR UPDATE 锁原子推进；attach/re-attach 永不改变控制权，只有
  RequestSurfaceControl 接管或精确续期。
- **连接续期**：native 媒体授权窗口 ≤30s，桌面每 20s 经 Gateway 重新鉴权并
  替换 peer；旧 peer 与排队输入跨续期失效。
- **输入幂等**：会话输入按 (session, client_input_id) 持久幂等；同 key 不同
  文本 = 冲突（Aborted）。任务/会话/attachment/控制操作各自带幂等键。

## 4. 真实能力 vs 不支持（诚实边界）

| 能力                                      | 状态                                                                                                                                                                                                                                                        |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 官方 runtime 原生两轮+续聊（同进程多轮）  | 可用（官方 runtime + 本地 fixture 门禁证明；真实模型待 A15）                                                                                                                                                                                                |
| 原生文件/bash 工具真实执行                | 可用（workspace-write sandbox + WorkOS 进程边界）                                                                                                                                                                                                           |
| WorkOS 只读系统工具（B04）                | 可用（`workos_project_info`、`workos_list_artifacts`；写工具/成果创建未接）                                                                                                                                                                                 |
| wire 级 session resume                    | **不支持**：0.1.1rc1 sdk-jsonrpc-server 仅 dispatch `initialize`/`session/prompt`/`shutdown`。会话延续 = 进程延续；重启语义见 §5                                                                                                                            |
| 审批响应（A09）                           | **不支持**：事件词汇表有 `approval/asked/decided`，但 wire 无审批响应方法；adapter 对审批类事件 fail closed（非重试协议错误、不发射事件），composition 保持 `approval: policy ask`，B04 只读工具不触发升级。单测 `TestSessionApprovalEventsFailClosed` 锁定 |
| 运行中 steer                              | **不支持**：官方语义为排队到下一轮（`agent/inbox`），UI 如实显示排队                                                                                                                                                                                        |
| runtime 容器内 git                        | **不可用**：镜像无 git 二进制（B02 记录；以读取 `.git` 文件验证 HEAD）。开发工具链镜像是 P3                                                                                                                                                                 |
| bash 进程边界                             | 官方 bash-local 执行器**不 confine**（dsh-bash-sandbox 未打入 runtime bin）。WorkOS 以会话私有 stateDir + 最小环境 + 官方 env 隔离（子进程 env 无 key）作进程级边界；无 cgroup 隔离声明                                                                     |
| PTY/native 会话 restart                   | 如实 FailedPrecondition（一次性受监督进程，无持久 argv）                                                                                                                                                                                                    |
| 可重启 app workload（持续策略对象为 app） | 未接（supervised workload manager 集成留后续）                                                                                                                                                                                                              |
| 双设备 LAN 真实媒体/输入（A16）           | 软件链可用（双设备身份真实输入链门禁）；物理双设备验收 BLOCKED 待操作员                                                                                                                                                                                     |
| 真实模型（A15）                           | BLOCKED 待操作员（§7 入口已备）                                                                                                                                                                                                                             |

## 5. 恢复行为

| 事件                            | 行为                                                                                                                                                                                                                            | 证据                                                                                                          |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| 页面刷新 / Gateway 重启         | ListSessions/Inputs + WatchTaskEvents/WatchSessionEvents 从持久 cursor 补收；无重复模型调用                                                                                                                                     | harness-sessions 门禁 phase1 replay 断言 + v2-development-journey 刷新恢复                                    |
| workos-core + harness-host 重启 | 已关闭会话的输入历史与生命周期日志完整可读；每个输入保持原 task id 与终态（无重复执行）；close 判定仍生效；新会话在重启后的栈上执行全新原生轮（fixture 答 `has_turn1=false total=1`）                                           | harness-sessions 门禁 A05 restart phase（TestHarnessSessionRestartRecovery）                                  |
| 凭据轮换/撤销                   | 会话不缓存 key：Ensure 对比 lease secret SHA-256 指纹，不匹配即杀进程组并按新 secret 重生（旧原生上下文有意丢弃，绝不换 key 续用）                                                                                              | deepseek adapter 单测（respawn on fingerprint change）+ README                                                |
| 会话子进程崩溃/轮错误           | 该轮失败、子进程丢弃（流位置不可信）、下一轮 Ensure 重生；结果不明的写/命令不会因重领 Task 自动重放                                                                                                                             | adapter 单测（turn error classification + respawn）                                                           |
| runtime-host 死亡/重启          | 所有 PTY 子进程随容器死亡（setsid+Pdeathsig）；启动时 reconcile 将非终态行终结为 failed —— ListProjectSurfaces 不再把死程序列为 running；attach 如实 FailedPrecondition；死会话 IO = NotFound。native 侧沿用既有启动 sweep 语义 | surface-continuity 门禁 A13 restart phase（TestSurfaceContinuityRestartReconcile）+ nativehost 既有 reconcile |
| 结果不明输入                    | 会话输入幂等：同 key 重发返回已记录输入；断线重发不会产生第二次执行                                                                                                                                                             | harness-sessions 门禁 replay/conflict 断言                                                                    |

## 6. 安全与授权

- owner/project 由服务端派生（网关身份 → task 事实 → 会话子环境）；模型
  无法提交身份选择权限。凭据只在 vault→lease→单轮子进程环境链上出现。
- 每轮执行复用 Task lease、有效凭据、取消与 deadline；预算语义按 ADR-0005
  执行（会话轮的子环境 max_tokens 固定 8192，超限/超时杀进程组）。
- 审批缺响应路径时 fail closed（§4）；WorkOS 自身 pre-run approval 与 provider
  工具审批不互相映射（既有语义）。
- 日志不含 secret、raw credential、用户内容全文；runtime stderr 落私有
  stateDir 不进日志。

## 7. 验收命令

软件侧（本轮全部 PASS，证据见任务记录）：

```sh
make test-workspace-execution    # A02/A03 工作区一致性/边界
make test-harness-sessions       # A04 三轮原生续聊 + A05 重启恢复 + A07 幂等/冲突/关闭
make test-surface-continuity     # A10-A12 detach/单控制端/sweep/stop + A13 重启 reconcile
make test-terminal-sessions      # A14 终端回归
make test-v2-development-journey # A17 桌面会话窗口/恢复/busy 契约（+capture 变体）
make test-native-surface         # A14 native 回归（detach/attach/stop 契约）
```

操作员门（BLOCKED 行，前置条件不满足时**响亮失败**、绝不静默通过）：

```sh
make test-real-model-acceptance  # A15：WORKOS_REAL_DEEPSEEK=1 + vault 真实 key + 新建无敏感测试项目
# A16：docs/runbooks/lan-second-device.md（第二台物理 LAN 设备）
```

## 8. 已知限制与 P3+ 后续

1. **构建产物发布/回滚（P3）**：本轮只有“预览当前工作区”；Build/Test 候选
   不产生可部署产物身份（ADR-0026 既有边界），UI/文档不得显示发布链路已通。
2. **开发工具链镜像（P3）**：runtime 容器无 git 二进制；含完整开发工具链的
   镜像与 preview 工具链未建。
3. **更多 Provider**：持续会话仅 DeepSeek（官方 runtime）；其他 Provider 不被
   迫伪造会话支持（capability 声明诚实）。
4. **wire resume 上游缺失**：harness-host 重启丢失未完成轮的原生上下文
   （已完成轮的持久事实保留）。升级 runtime 版本时按 B00 基线方法重测。
5. **可重启持续 app workload**：RestartSurfaceWorkload 对 PTY/native 如实
   拒绝；supervised workload manager 的持续策略集成未接。
6. **审批接入**：等官方 wire 提供审批响应方法后按 ADR-0030 方向接入；在
   此之前所有审批路径 fail closed。
7. **多主机/公网/TURN（P4）**：本轮明确不做。
