# V2 Harness 工作区接入基线（B00）

- 日期：2026-09-15（UTC）
- 任务：[20260915-v2-agent-workspace-web-continuity](../tasks/20260915-v2-agent-workspace-web-continuity.md)
- 对象：DeepSeek Harness 官方 runtime，WorkOS 当前锁定版本
- 结论级别：每条结论都来自锁定 runtime 的内嵌源码提取或本机真实运行探测；未核实的写 unknown

## 1. 锁定版本与获取

| 项             | 值                                                                            | 证据                                                                                                             |
| -------------- | ----------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| runtime 发行物 | `deepseek_harness_runtime_bin==0.1.1rc1`（PyPI wheel，manylinux_2_28 x86_64） | `Dockerfile` deepseek-runtime stage（sha256 `8eb31e3ab2bc3ff45474fe419eb389e32553391f1a40789ea2cc3dc8d6de137b`） |
| 二进制         | `dsh-jsonrpc-agent`（pkg 单文件 Node ELF）+ `dsh-jsonrpc-agent-rg`            | Dockerfile install 步骤                                                                                          |
| 入口行为       | 仅接受 `DSH_CORDIS_CONFIG`（或 argv[2]）指向的 cordis.yml；无内置回退         | 内嵌 `runJsonrpcAgent` 源码                                                                                      |
| 上游仓库       | github.com/deepseek-ai/deepseek-harness，tag `dsh-v0.1.1-rc.1`                | 内嵌 package.json repository 字段                                                                                |
| 官方基线组合   | `packages/bundle/base/cordis.patch.yml`（tag dsh-v0.1.1-rc.1）                | 上游 tag 原文比对                                                                                                |

**版本决策：保持 0.1.1rc1。** 0.1.2rc1/0.1.5rc1 的 `sdk-jsonrpc-server` dispatch 仍只有
`initialize`/`session/prompt`/`shutdown` 三方法（0.1.5rc1 wheel 提取源码核对，tag
`dsh-v0.1.5-rc.1`），对 P1 会话能力没有增益；不为本轮引入版本 churn。

## 2. 能力矩阵

| 能力                             | 官方精确版本支持                                                                                    | 当前 adapter 接入             | 部署实际可用                | 证据                                                                                                  | 本轮动作                                                                    |
| -------------------------------- | --------------------------------------------------------------------------------------------------- | ----------------------------- | --------------------------- | ----------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| JSON-RPC 方法集                  | `initialize`、`session/prompt`、`shutdown` 仅此三个                                                 | 已接（runtime.go）            | 是                          | 内嵌 server 源码 dispatch + 动态调用（未知方法返回 `unknown … method`）                               | B03 扩展 adapter 使用既有三方法，不发明新方法                               |
| 同进程多轮续聊（原生上下文累积） | 支持：同 sessionId 重复 `session/prompt`，`agent.followup` 追加                                     | 未接（每 Task 临时进程）      | 是（探测证实）              | 第二轮模型请求包含第一轮 user/assistant 消息（`COUNT total=6 markers=TURN1`）                         | B03 实现每会话常驻 runtime 进程                                             |
| 运行中追加输入（steer）          | 官方语义：排队下一轮（`agent/inbox/spliced target=next-turn`），不注入当前轮                        | 未接                          | 是                          | 持久化日志中 steer 输入以 next-turn splice 记录                                                       | B03/B05 按"已排队，下次执行使用"呈现                                        |
| 取消当前执行                     | 无 wire 方法；杀进程即取消（README 已记载）                                                         | 已按杀进程实现                | 是                          | dispatch 源码无 cancel                                                                                | 保持现状；会话历史由持久化保留                                              |
| 会话事件持久化                   | `session-persistence-jsonl`：append-only 每会话日志（header + 事件流，可选 zstd）                   | 未接                          | 是（探测：19 事件完整落盘） | `state/sessions/--<cwd>--/<sessionId>/session.jsonl`                                                  | B03 装载该插件，root 由 WorkOS 管理                                         |
| 跨进程恢复（wire 级 resume）     | 不支持：服务层 `agents.resume` 存在但未暴露到任何 server（sdk/acp 均无）                            | 未接                          | 否                          | 动态：新进程同 id `session/prompt` 得到全新会话；config 声明恢复后 prompt 报 `session already exists` | 诚实记录；恢复策略见 §4                                                     |
| 原生文件/命令工具                | base 组合注册 `bash`、`read`、`write`、`edit`、`glob`、`grep`                                       | 未接（当前 no-tools Profile） | 是                          | 模型请求 `tools` 字段实测                                                                             | B03 采用官方 base 行组合（弃用 demo spine 作为工具组合）                    |
| bash 真实执行                    | 是：`pwd` 返回会话 cwd；参数 schema 强校验（必填 `description`）；结果带 `[exit code: N]`           | 未接                          | 是                          | 探测 TOOL RESULT                                                                                      | B03 接入                                                                    |
| 文件写边界                       | `write` 越界被 `workspace-write` 策略拒绝（`[sandbox: file access denied]`），工作区内成功          | 未接                          | 是                          | 探测                                                                                                  | B02/B03 以 `sandbox-policy mode: workspace-write + workspaceRoot` 落地      |
| bash 边界                        | **不 confine**：`dsh-bash-sandbox` 未打入 runtime bin，只有 `dsh-bash-local`                        | 未接                          | 部分（见 §5）               | 探测：bash 在工作区外创建文件成功                                                                     | WorkOS 自施进程级边界（B02）                                                |
| 凭据隔离                         | bash 子进程 env 无 `DEEPSEEK_API_KEY`、无 `DSH_CORDIS_CONFIG`（grep -c = 0）                        | —                             | 是                          | 探测                                                                                                  | 保持官方 env 白名单行为                                                     |
| 询问/审批                        | 事件 `approval/asked/decided` 与 `session/request_permission`（ACP）存在；sdk server 无审批响应方法 | 未接                          | 部分                        | 源码 + dispatch                                                                                       | B04 只在官方响应路径存在时接入；否则声明 unsupported 并拒绝执行             |
| 原生 Goals/Jobs/Skills           | 插件在 bin 闭包内（`dsh-goal`、`dsh-jobs-local`、`dsh-skill` 等）                                   | 未接                          | 可装载                      | 闭包模块映射                                                                                          | B03 按需最小装载；未装载即声明 unsupported                                  |
| usage/预算                       | 每 step `usage` chunk（input/output/cache tokens）                                                  | 已投影                        | 是                          | 探测事件流                                                                                            | B03 累计多轮预算（不用单次 max_tokens 冒充）                                |
| 会话查询                         | `session-projection`/`session-query-sqlite`（闭包内），sdk server 未暴露 list/load                  | 未接                          | 否（wire 级）               | 源码                                                                                                  | Core 自持会话投影，不依赖 harness 查询                                      |
| profile 组合                     | 官方 base 行组合与 `agent-spine-demo`（demo）并存                                                   | 当前用 demo spine             | 是                          | 上游 tag yml                                                                                          | B03 改用官方 base 行组合（工具注册在 root 作用域，sdk 创建的 agent 才可见） |

注：`agent-spine-demo` 组合内 toolBash 注册在 spine 子 fiber，sdk server 在 root 创建的
agent 看不到这些工具（动态证实 `unknown tool "bash"`）。工具必须以 base 行组合的顶层行
注册。这是组合层事实，不是 runtime 缺陷。

## 3. 执行图（V2 目标形态）

```text
浏览器(desktop-web)
  └─ Gateway（认证/路由）
       ├─ Core: HarnessSession 事实、授权、输入幂等、事件投影   [表 owner: core]
       │    └─ harness-host: 会话管理器（每 WorkOS 会话一个官方 runtime 子进程）
       │         ├─ stdin/stdout JSON-RPC: initialize / session/prompt / shutdown
       │         ├─ 官方 runtime（cordis.yml 由 WorkOS 生成: base 行组合）
       │         │    ├─ llm-deepseek → DEEPSEEK_BASE_URL（每次执行注入租约 key）
       │         │    ├─ tool-fs / fs-sandbox → workspaceRoot 内读写
       │         │    ├─ tool-bash → bash-local 子进程（env 白名单，无 key）
       │         │    └─ session-persistence-jsonl → 会话状态目录（append-only）
       │         └─ 租约到期/撤销 → 杀 runtime 进程；持久化日志保留
       └─ runtime-host: 工作区目录事实、Terminal/PTY、Native/浏览器 Surface、Workload
```

数据 owner：Core 拥有会话/授权/输入/投影表；harness-host 拥有 runtime 子进程与私有
会话状态目录；runtime-host 拥有目录装配与运行实例；indexer 只读派生索引。

## 4. 恢复语义（受版本事实约束）

- 页面刷新 / Gateway 重启：只补收事件（Core 投影持久化），不再次调用模型 —— B03 实现。
- harness-host 重启（runtime 进程死亡）：0.1.x 无 wire 级 resume。已完成轮次的事件日志
  保留（jsonl），Core 投影可完整回放展示；**继续对话需要新的原生轮次**（新 runtime 进程、
  同 WorkOS 会话、新原生上下文）。该缺口在任务记录与 A06 证据中如实登记，不以重放聊天
  文本冒充原生恢复。
- 执行中崩溃：结果不明的文件写/命令标记待核对；未执行的排队输入可重试（B03 输入状态机）。

## 5. 默认限制与部署要求

- bash 执行器为 `dsh-bash-local`（不 confine）。启用工具的部署必须由 WorkOS 施加进程级
  边界：runtime 子进程以受限身份运行、仅工作区目录可写（DAC/目录属主）、不挂 Docker
  socket、不继承 Core 配置与数据库连接。无法实施该边界的组合返回 unavailable。
- `permission-presets` 需要 confining executor，在 runtime bin 内不可装载（加载即失败，
  官方 fail-closed）。权限呈现按实际装载能力声明。
- 模型 key 仅以任务租约注入 runtime 进程 env；官方 env 白名单已证明 bash 不可见。
- 会话状态目录（jsonl root）由 harness-host 私有；按会话子目录组织，含未压缩事件流。

## 6. 复现命令（探测环境）

探测在仓库 `tmp/b00/`（gitignored）完成，脚本与输出保留于该目录；要点：

1. 从 `workos:dev` 镜像提取 `dsh-jsonrpc-agent`（或按 Dockerfile 校验 wheel sha256 安装）。
2. `probe_cordis.yml`：官方 base 行组合（timer/llm/session/agent/…/subprocess/sandbox/
   sandbox-policy/bash-local/shell-env/approval/tool-bash/fs-_/tool-fs_/persistence/
   llm-deepseek/sdk-jsonrpc-server），`sandbox-policy.workspaceRoot` 指向探测工作区。
3. `probe_fixture.py`：本地 DeepSeek SSE 兼容端点（记录请求、可编程工具调用响应）。
4. `probe.py`/`probe_battery.py`：JSON-RPC 驱动，执行 initialize → session/prompt(多轮) →
   shutdown，收集 `session.event`/`session.status` 通知。
5. 上述组合下实测：工具注册、真实 bash 执行、fs 写边界、env 隔离、多轮上下文、steer
   排队、jsonl 落盘。B03 将把该链路固化为 Go 集成测试（本地 fixture 只模拟模型响应，
   不替代 Harness 或工具执行）。

## 7. 缺项（blocked 行）

| 缺项                   | 影响                                            | 需要的最小外部条件                                                          |
| ---------------------- | ----------------------------------------------- | --------------------------------------------------------------------------- |
| wire 级原生会话 resume | harness-host 重启后不能在原原生上下文继续（§4） | 上游在 sdk server 暴露 resume/load，或官方 CLI 常驻服务形态                 |
| confining bash 执行器  | bash 命令不可声明内核级隔离                     | 上游把 `dsh-bash-sandbox` 打入 runtime bin，或 WorkOS 侧进程边界验收（B02） |
| 真实模型验收（A15）    | 未消耗任何真实 API 额度                         | 已授权 DeepSeek key 与限额（执行时经 Vault，不入命令行）                    |
| 第二台 LAN 设备（A16） | 无法验证真实双设备链路                          | 独立物理设备（步骤由 B09 脚本提供）                                         |

## 8. 来源

- 锁定 runtime 内嵌源码（`sdk-jsonrpc-server`、`agent-loop`、`session-persistence-jsonl`、
  `tool-bash`、`bash-local`、闭包模块映射）——版本 0.1.1-rc.1，提取于 2026-09-15。
- 上游 tag `dsh-v0.1.1-rc.1` 的 `packages/bundle/base/cordis.patch.yml` 与
  `packages/bundle/headless/cordis.patch.yml` 原文。
- PyPI 版本列表与 0.1.5rc1 wheel（sha256 `ad877237…5229`）源码比对。
- 本机动态探测日志（tmp/b00/，含 fixture 请求记录与会话 jsonl 样本）。
