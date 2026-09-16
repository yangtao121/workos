# V2 持续开发会话与 Web 应用接续（B00–B10）

- 范围：[任务书](../prompts/20260915-glm-5.3-v2-p0-p2-goal.md) 的 P0–P2；六进程边界不变。
- 基线：本地 main `a33e71f`，接管任务分支 `feat/v2-agent-workspace-web-continuity@c2ff6de`。
- 当前：软件交付完成，真实模型与最终仓库门禁通过；A16 按用户确认保留验收缺口；仅本地 main 合并，不 push。
- 用户确认：使用 Runtime 专用 Docker 后端；真实模型总费用上限 ¥20；暂缺第二台物理设备，A16 保留缺口。
- 决策：[ADR-0032](../decisions/0032-runtime-isolated-development.md)；[部署和用户操作](../architecture/v2-agent-workspace-web-continuity.md)。
- 本记录替换旧的部分完成声明。历史过程可从 `c2ff6de` 读取，不将旧 PASS 推导为当前能力。

## 工作包

| 包                | 状态              | 依赖          | 实现/验收入口                                                    | 验收            | 结果与未决项                                                                                    |
| ----------------- | ----------------- | ------------- | ---------------------------------------------------------------- | --------------- | ----------------------------------------------------------------------------------------------- |
| B00 官方基线      | done              | 官方 0.1.1rc1 | Dockerfile、DeepSeek README、ADR-0032                            | A01             | 官方 agents create/resume/followup、原生持久化和 fs/shell/userQuestions 扩展点实测；无自研 loop |
| B01 契约/事实     | done              | B00           | agent/project/surface/workload Proto；迁移 060–065               | A02/A05–A07/A11 | 事务输入/事件/执行槽、未知结果 needs_review、持久 generation/动作回执和问答                     |
| B02 工作区        | done              | B01           | Project binding、Runtime workspacehost、Docker/containerprocess  | A02/A03         | 文件、命令、PTY、Native 和预览共用已授权目录；App Bridge 同步核对 Core 当前授权                 |
| B03 原生会话      | done              | B01/B02       | deploy/harness 三个薄插件、DeepSeek sessions                     | A04–A07/A15     | 每 Task 新凭据进程、已完成轮原生恢复、累计输出预算、真实两轮改码与测试                          |
| B04 系统工具/交互 | done              | B02/B03       | task-lease Tools、session_tools、interaction                     | A08/A09         | 成果、预览、项目/工作区/应用查询；原生询问可回答/拒绝，权限升级 unavailable                     |
| B05 会话窗口      | done              | B03/B04       | AgentSessions、ExecutionQuestions                                | A05/A09/A17     | 刷新补收、取消/关闭分开、needs-review、问题选择与提交                                           |
| B06 程序持续运行  | done              | B01/B02       | PTY/Native/preview services                                      | A10/A11/A13     | detach 存活、显式 stop/restart、新 generation、TTL、重启对账                                    |
| B07 接管/LAN      | blocked（仅 A16） | B06、外部设备 | Continuity、Native input/ICE、LAN runbook                        | A12/A16         | 双身份真实输入与旧代次拒绝通过；CIDR/UDP fail-closed；缺第二台物理设备                          |
| B08 桌面闭环      | done              | B02/B05/B06   | WorkspaceSettings/Files/Previews、RunningSurfaces、AdaptiveShell | A17             | 三尺寸 24 张 after/current；移动询问 radio 的 focus 时序缺陷已修复                              |
| B09 综合验收      | done（软件/模型） | B00–B08       | tools/v2-completion、real-model-acceptance                       | A02–A15/A17     | 独立六进程、真实 Docker/Chromium、真实 DeepSeek；A16 独立保留                                   |
| B10 收口          | done              | B09           | 本记录、status、架构/部署/视觉文档                               | A18             | 最终生成无差异、make check PASS；交付到本地 main                                                |

## 实际验收

证据目录：[20260916-v2-completion](evidence/20260916-v2-completion/)。日志只保留专门测试
项目的 ID、命令与结果，不包含 API key、请求头、真实用户数据或完整模型内容。

| 编号 | 当前结果                | 实际证据                                                                                                                                                                                                                                                                                                 |
| ---- | ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A01  | PASS                    | 官方 runtime 0.1.1rc1 固定 wheel SHA；实际官方 runtime 完成 fixture 和真实服务两轮；[adapter README](../../internal/harness/adapters/deepseek/README.md)                                                                                                                                                 |
| A02  | PASS                    | `make test-v2-completion`：Harness 改 calculate.cjs 并用 node:test 测试，owner 文件 API 同目录读取；PTY 和 Native 写入同目录；真实预览 [六进程日志](evidence/20260916-v2-completion/isolated-stack.txt)                                                                                                  |
| A03  | PASS                    | localfs/Docker 单测与实际容器；只读、撤权、旧绑定、路径/链接、跨 scope、停止旧程序 [矩阵](evidence/20260916-v2-completion/regression.txt)                                                                                                                                                                |
| A04  | PASS                    | 同一官方原生 session：double→triple，各自真实 Bash 测试；第二轮 fixture 要求原生历史第一轮标记                                                                                                                                                                                                           |
| A05  | PASS                    | Core/Harness/Gateway 真重启后原 session/task/history；输入幂等；浏览器刷新后 Agent 历史仍在；24 并发输入与事件序号事务                                                                                                                                                                                   |
| A06  | PASS                    | 已完成轮原生 persistence 恢复；执行租约过期转 failed/needs_review，不再次领取；未关联取消 admission 被恢复流程收敛                                                                                                                                                                                       |
| A07  | PASS                    | 输入重复/冲突/取消、205 输入恢复轮转；原生每次请求递减 maxTokens；缺 usage 拒绝下一请求；每执行关闭凭据进程                                                                                                                                                                                              |
| A08  | PASS                    | 官方工具经私有 mTLS task lease：成果创建带 task provenance，真实预览启动/列表，撤权和跨项目拒绝                                                                                                                                                                                                          |
| A09  | PASS（可用能力）        | 原生 ask_user_question→Core 持久交互→用户应答→原生继续；拒绝/过期/重放/撤权矩阵；无权限升级后端，明确 unavailable；旧 App 事前审批回归通过                                                                                                                                                               |
| A10  | PASS                    | PTY detach 后 shell 内存和后台文件写仍在；Native Chromium reload 后同 workload/generation 且 shell 环境变量保留；预览两个客户端请求计数连续                                                                                                                                                              |
| A11  | PASS                    | stop 真回收、restart 增代次；旧 stop 重试不杀新代次；进程 TTL（命令 5 分钟、交互 30 分钟）和并发上限；拒绝终态 attachment                                                                                                                                                                                |
| A12  | PASS（软件）            | 两个可信设备身份 A/B 真实 PTY 输入/resize，B 接管拒绝 A；A 再接管仍拒绝自己旧 epoch；Native peer 信令及队列 gate；不等同 A16                                                                                                                                                                             |
| A13  | PASS                    | 实际 Runtime 重启，旧程序不再 running，控制事实与 attach/IO 如实拒绝；相同部署 namespace 容器清理                                                                                                                                                                                                        |
| A14  | PASS                    | 旧 App→Agent、成果、安装、grant no-op/撤销、Core 事前批准/拒绝、Task 幂等 PostgreSQL/真实 RPC 回归；[矩阵](evidence/20260916-v2-completion/regression.txt)；全 Go/Web 检查通过                                                                                                                           |
| A15  | PASS                    | [真实 DeepSeek 记录](evidence/20260916-v2-completion/live-deepseek.txt)；官方 runtime 两轮实现/测试、独立容器重跑测试和行为断言；[用量](evidence/20260916-v2-completion/live-usage.jsonl)                                                                                                                |
| A16  | BLOCKED（用户确认保留） | 2026-09-16 用户没有第二台物理设备；[LAN 操作手册](../runbooks/lan-second-device.md)；没有声称真实两台 LAN 图形链已通过                                                                                                                                                                                   |
| A17  | PASS                    | [before](../ui/desktop-web/changes/20260916-v2-completion/before/)、[after](../ui/desktop-web/changes/20260916-v2-completion/after/)、[current](../ui/desktop-web/current/)、[notes](../ui/desktop-web/changes/20260916-v2-completion/notes.md)；三尺寸真实 radio 操作/iframe/错误状态断言               |
| A18  | PASS                    | [make check](evidence/20260916-v2-completion/make-check.txt)：Proto/sqlc/Go vet/全部 Go 测试、13 个 TS workspace 架构检查、eslint/prettier、全部 Web check、desktop 165 测试、Vite 构建；[重复生成](evidence/20260916-v2-completion/generation.txt) 200 文件 byte-identical；完整门禁 4 个浏览器测试通过 |

## 真实模型与费用

- 授权总上限 ¥20。实际执行仍使用更低的 ¥1.90 预留上限代理；无自动重试、未知请求不退还预留。
- 固定目标 `https://api.deepseek.com/chat/completions`，沿用 `deepseek-v4-flash` 别名。
  [2026-09-16 官方价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing/)说明其路由到 V4.1-Flash：
  峰值输入 cache miss ¥2/M、cache hit ¥0.04/M，输出 ¥8/M。
- 共 8 请求，输入 27,191 tokens、输出 999 tokens；保守请求预留共 ¥1.247284。
  按返回缓存用量和峰值价格估算 **¥0.01696472**，不是账单结算值。
- 第一次外部复核容器未指定用户，drop ALL 后 root 无权读取 uid 1000 的 0700 测试目录；
  纠正验证容器为同 uid 后重跑同 input key，第一轮没有再次调用模型或执行副作用。
- API key 通过 stdin 导入独立 Vault；Task 取得 lease，工具容器没有 key。完成后撤销凭据、
  删除临时密钥文件。仓库只保存使用量，不保存请求/响应内容或密钥。
- 默认普通检查完全离线于收费模型。复现：设置 `WORKOS_REAL_DEEPSEEK=1` 和权限 600 的
  `WORKOS_REAL_DEEPSEEK_KEY_FILE` 后 `make test-real-model-acceptance`；只新建独立测试项目/DB/Vault。

## 交付前自检

1. 官方 Harness 执行原生 agents、history、loop、tools；薄插件仅负责授权/协议/后端适配。
2. 第二轮使用同一原生持久 session，fixture 和真实服务均完成继续开发，没有拼接 UI 聊天代替恢复。
3. 文件、Terminal、Native、预览与 Harness 只挂同一授权目录；不能选任意宿主路径。
4. 每 Task 重新取 lease；累计输出预算；Core 当前授权不可确认则拒绝/终止。Docker socket 仅 Runtime 持有。
5. 关闭 UI 仅 detach；Native/PTY/预览内存续接已有实际证据；restart 是新 generation。
6. 控制 device+epoch 在服务端逐请求/事件验证；旧输入、旧 resize、旧信令不会因同设备重新接管而恢复。
7. 运行中断和未知命令不自动重放；Core 恢复队列持久游标，Runtime 终结已死亡程序。
8. 普通 App 仍走 capability，文件 Bridge 的原路径/大小/etag 规则保留；已有 App→Agent 和事前审批回归。
9. 未实现：权限升级审批、公网/TURN、WebSocket/HMR、多 owner/多机、P3 发布/回滚。预览不等于部署产物。
10. A16 尚无物理设备证据；总目标不标记全验收完成。用户已明确接受保留该项缺口后交付软件。

## 恢复入口与合并

软件与限额真实模型验收已结束；本任务创建的独立测试容器、数据库和真实凭据已清理/撤销。
日志保留于 `tmp/v2-completion-intake/`，长期脱敏证据在本记录链接目录。共享开发栈未重置；
无 sudo 操作。不要为继续阅读文档再次调用收费模型。

保留未知 `scratchprobe`（不提交），原 GLM 未提交草稿备份于 `tmp/v2-completion-intake/`。
提交本任务后快进合并本地 main，不 push；精确提交以 Git 历史为准。后续仅需在具备第二台
物理设备时按 A16 runbook 补验，不能将此处软件 PASS 写成物理 LAN PASS。

实际命令：`sh tools/v2-completion/gate.sh`（完整六进程与 4 个浏览器测试）；已准备独立
fixture 上 `sh tools/v2-completion/regression.sh`（PostgreSQL/旧链路/真实容器失败矩阵）；
`sh tools/real-model-acceptance/run.sh`（同一 native session 的真实两轮验收）；固定 Docker
工具链中 `make generate`、`make check`，再次 `make generate` 校验全部生成文件不变。
主镜像及 Runtime Dockerfile 均实际构建成功。

最后补测发现并修复 Docker daemon 自身 deadline 与 Runtime kill 同时完成的竞态：
已死亡容器的 kill 冲突经 inspect 确认为停止后返回 timeout，不能误报 unavailable。
小于 1ms、非数字或非有限 timeout 在启动容器前拒绝，避免 timeout 被舍入为零而失效。
修复后实际 Docker 矩阵与 `make check` 再次通过。
