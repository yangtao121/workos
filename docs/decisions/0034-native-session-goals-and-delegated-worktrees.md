# ADR-0034 原生目标、项目 Skills 与隔离子 Agent

- 状态：accepted，实现与证据见[任务](../tasks/20260920-v2-native-goals-skills-subagents.md)
- 日期：2026-09-20
- 延续 ADR-0030、ADR-0032；不改变六进程边界。

## 原生执行与 Core 投影

保持 `deepseek_harness_runtime_bin==0.1.1rc1`。官方 `goal`、`goal-round-driver`、
`tool-goal` 拥有目标日志、状态转换和自动轮次；官方 `skill`/`tool-skill` 拥有技能发现与
加载接口；官方 `subagent-spawn-in-process` 拥有独立子 session、原生模型循环和结果。
不在 Core、Gateway 或 WorkOS plugin 内编写第二套 Agent 循环。

新增 canonical `SessionGoal`、`SessionDirective`、`AgentDelegation`，通过追加字段和事件
传递。原生 goal ref 是不透明关联引用，WorkOS 资源与 delegation ID 仍由服务端生成
UUIDv7。Provider 内部 session、tool 和 goal 类型只在 DeepSeek adapter 转换；App 看不到
凭据、宿主路径或完整原生日志。

Core 的目标/子 Agent 数据是带来源的展示投影。每次更新与 Task 事件持久化一起提交，
受当前 lease、Task、Session 和 delegation 的作用域限制；重放不重复增加用量或创建
工作树。原生 JSONL 在完成通知之前刷新。Core 不根据模型的一段文字推断目标完成。

## 目标控制与中断

用户显式创建、恢复或暂停目标作为 canonical SessionDirective 进入既有持久输入队列，
复用幂等键、单执行槽、Provider binding 和预算。运行中的暂停另有幂等
`RequestSessionGoalPause`，记录针对同一个 goal ref 的请求；原生 step 边界通过受租约
保护的工具后端读取请求，停止继续调度。已开始的前台操作先收敛，UI 区分请求中和已暂停。

暂停不等于 Task 取消。取消、失租约、执行崩溃或结果未知沿用 `needs_review`，不得自动
重跑文件写或命令。重新加载原生 session 只恢复日志；goal 的 armed activation 不持久化，
必须由用户显式恢复。任务结束后 Core 展示的 armed 必须为 false，不能让历史事件伪装成
当前正在执行。原生 round 上限和 Task 的输出/时间预算同时有效。

## Skills 与预算

技能 provider 只读取授权项目的 `.dsh/skills` 与 `.agents/skills`，通过 Runtime 文件接口
读取有限目录和有界 SKILL.md。保留官方名称、描述和 model/user invocation 语义；不扫描
宿主 HOME、不启用任意 watcher、不通过加载技能取得额外权限。撤权或 binding revision
改变后，后续发现/加载/工具操作拒绝。

父 Agent、自动目标轮次和全部子 Agent 共用一个 Task 的输出预算、截止时间与凭据租约。
每次原生模型请求先预留可用输出额度，收到 usage 才结算并释放未用预留；并发请求不能
各自获得完整剩余额度。缺 usage、超额或过期都拒绝后续请求。Core 用量只记录一次合计，
子 Agent 事件不能替代或提前结束父 Task。

## 子 Agent 与 Git worktree

仅开放原生 foreground spawn，最多两个并发、深度一。Core 先按当前 Task 租约创建
delegation grant，Runtime 依据持久 owner/project/binding/task 关联选择目录；工具参数
不提供宿主路径。原生子 session 的工具调用携带服务器授予的 delegation ID。

每个子 Agent 使用独立可写 Git worktree 与专属 Git 元数据；不能挂载父项目的可写
`.git` 或其他子 Agent 的工作树。初始基线是项目已提交 HEAD；项目存在未提交变更时
明确拒绝创建，避免静默遗漏当前文件。准备和 Git 操作也在 Runtime 的无网络、无
凭据、受资源限额的容器内运行，禁止在 Harness 进程中运行 Git 或宿主 Bash。

父项目和兄弟子 Agent 的实际文件不因子 Agent 的命令而改变。子 Agent 完成后保留可
审核的 worktree 身份、基线提交和有界 diff/成果引用，不自动覆盖父目录。取消、撤权和
租约丢失回收活进程；未知文件效果保持待核对，不能伪报干净回滚。无 Git/toolchain 或
不能满足目录隔离时，该次 delegation 返回 unavailable。

## 证据要求

分别记录官方原生 binary + 本地模型 fixture、六进程浏览器链和有界真实 DeepSeek 的
证据。验收必须涵盖自动多轮、暂停/恢复、重启后不自发继续、技能作用域、两个并行子
Agent、父/兄弟文件隔离、合计预算、撤权及异常中断。三尺寸视觉使用确定性 fixture；
没有端到端证据的新增能力不能标记 working。

## 成果名额（2026-09-21）

Core Artifact 的 migration 075 将结果名额细化为 `(task, delegation, type)`；普通任务的
`delegation` 为空，保留每种输出类型一个结果的原规则。子任务名额只能由 Core 专用
materializer 在父 Task 事务锁内、经 Agent 模块 port 复核运行中 grant 后选择；公开
Provider artifact RPC 不接受这个字段。`(task, output key)` 仍全局唯一，重放必须匹配
原 delegation，不能用同一个键更换来源。每份 diff 仍与事件、索引和通知原子提交。
工作区 source ID 是现有 Runtime 的不透明 `ws_…` 引用，不按 UUID 解析。
