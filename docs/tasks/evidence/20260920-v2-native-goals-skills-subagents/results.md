# 原生目标、技能与子 Agent 验收（2026-09-21）

契约基线 `71e7c5d`，P3 收口证据 `8d507cc`；当前任务源码的六进程隔离栈，
真实 PostgreSQL、Docker worktree、锁定官方 Harness `0.1.1rc1`、Chromium。

| 范围                                            | 结果                                                                             | 原始证据                                                                       |
| ----------------------------------------------- | -------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| `sh tools/native-automation/gate.sh`            | PASS，两个原生子 session、独立 diff 成果、技能、目标暂停/恢复、重启接续          | `tmp/v2-completion.N4kvoP`                                                     |
| 浏览器                                          | 4 PASS，无 skipped/flaky；三尺寸确定性视觉及真实会话审阅/恢复                    | 上述目录 `native-browser-results.json`                                         |
| 运行中取消、Harness SIGKILL/restart、工作区撤权 | PASS，两个子命令容器清空，未知结果持久化 needs_review                            | `tmp/native-full-gate-fixed.log`                                               |
| PostgreSQL 事务                                 | PASS，独立成果名额、幂等/容量竞争、未知 grant 拒绝、取消及失租约                 | `tmp/native-expired-lease-regression.log`、`tmp/native-review-regressions.log` |
| 原生二进制与 race                               | PASS，目标轮数/暂停恢复、共享预算、缺失 usage 拒绝继续、技能隔离、子工具边界     | `tmp/native-final-probes.log`                                                  |
| Docker 工作树                                   | PASS，两个树隔离、dirty 拒绝、hook/fsmonitor 隔离、新文件 diff                   | `tmp/native-worktree-docker.log`                                               |
| 真实 DeepSeek 原生自动化                        | PASS，两名子 Agent 各自产生独立准确文件、Skill 工具与私有 marker、持久化目标完成 | `tmp/native-live-deepseek-final.log`、`tmp/v2-completion.vTQzkZ`               |
| 真实持续开发                                    | PASS，同一原生会话先实现 double，再改为 triple，独立执行测试通过                 | `tmp/native-live-continuity.log`、`tmp/v2-completion.KX05lJ`                   |
| 生成一致性                                      | PASS，`make generate` 前后 211 个生成文件哈希一致                                | `tmp/native-generate-consistency.log`                                          |
| 全仓检查                                        | PASS，Go/Proto/sqlc、架构、前端 lint/typecheck/tests/build                       | `tmp/native-make-check-complete.log`                                           |

真实模型重现：设置 `WORKOS_REAL_DEEPSEEK=1`、仓库外 owner-only 的
`WORKOS_REAL_DEEPSEEK_KEY_FILE`、共享 `WORKOS_REAL_BUDGET_LEDGER`，执行
`sh tools/real-model-acceptance/gate.sh`。加 `WORKOS_REAL_NATIVE_AUTOMATION=1`
会创建干净项目技能 fixture 并运行原生自动化用例。每次执行结束撤销测试 Vault 凭据。
预算代理仅转发并记用量，不参与 Agent 循环。并发线程/进程互斥和重启累计回归通过。

累计 38 请求，保守预留人民币 6.055478 元，
观察到 173008 input / 6608 output tokens，按 2026-09-21
[官方无折扣高峰价](https://api-docs.deepseek.com/zh-cn/quick_start/pricing/)
估算上界 0.398880 元。代理预留总上限 19 元，用户授权总上限 20 元。
凭据仅从仓库外文件经标准输入导入隔离 Vault；不在 Git、日志或截图中保存。

## 修复与失败记录

- 来源 ID 为 opaque `ws_…`，migration 073/074 使用 text；UUID 校验只用于 canonical 资源 ID。
- Artifact-owned migration 075 将每个 delegation 的成果名额与普通 Task 的单类型名额分开，
  publication 同事务验证 Core grant，父任务限制不放宽。
- 首轮连续开发提示允许具名导出，独立函数 oracle 拒绝；明确直接函数导出后重跑成功。
  首次混合测试的整体失败不计为完整通过，上表列出最终独立 PASS。
- 强杀 Harness 暴露 expired-lease Claim 未更新子任务投影；现在父失败、目标 disarm、
  未结算子任务 needs_review 在同事务中完成。新增回归保留已完成的子成果，并证明不重放执行。
- 首轮全仓组件检查使用过期手写 session fixture，已改用生成的 Proto 默认值；11 个组件用例通过。
  后续格式检查与删除 Python 临时缓存发生竞争；缓存现排除 Git，最后检查从清理后的工作树执行。

[三尺寸 before/after/current](../../../ui/desktop-web/changes/20260920-v2-native-goals-skills-subagents/notes.md)
均使用确定性 fixture，无真实模型内容、凭据或私人数据。所有测试栈只清理本次 namespace。

## 能力边界

最多两名并行子 Agent、深度一；共用父任务授权、输出预算和截止时间。仅接受干净 Git 基线，
子工作树不自动合并。中断的工作树保留供核对，没有 diff 的中断结果由操作者检查 Runtime 存储。
Skills 只读取绑定项目的 `.dsh/skills` 与 `.agents/skills`；任意后台任务仍不可用。
本任务不提供 rootless、TURN、物理 LAN、Android/iOS 或推送的新证据。

## 原始记录 SHA-256

临时日志不提交 Git；本机原始记录摘要如下。

- `tmp/native-full-gate-fixed.log`：`99e95ad5087c4f10c30113d6fba673ea953861d7fc40b0b275fe7426da2b97e7`
- `tmp/native-live-deepseek-final.log`：`6e6a5eb9ecf52a7ccedbbe1c5070631fc3621085251f7f0e533909d0a7ab8551`
- `tmp/native-live-continuity.log`：`f6d87d84d3cc9ae6ddba3c986e6a03df76ea775fc1b445585981750d5a2d9f99`
- `tmp/native-expired-lease-regression.log`：`ce391e0f9acd206a7927b0ec6c2bfac76183c83fc5b19653d52491c459b5cf55`
- `tmp/native-final-probes.log`：`4ebc28bb7e84fe6304d6acf26a8a7219b4e62de6e435c9759dbe8a54e0bc6d4b`
- `tmp/native-review-regressions.log`：`36a21ee80c4fc2369d4aa21980189978fdd72a1ec36c4a516ab50cdcb20c9f75`
- `tmp/native-worktree-docker.log`：`32a6fb2d400fb6d0e874bd3297912892e05247ccb7d46dc0ef64cfa2e1de5c45`
- `tmp/native-generate-consistency.log`：`30bae6213ebbc0b993189336bdb8489b64820b35d8798698b4be45adaa6efdc4`
- `tmp/native-make-check-complete.log`：`731f4b8ea6bc3c8091f91a24d5b4dce5831e3008e33d57f998fb447f3393f966`
- `tmp/native-budget-guard-tests-final.log`：`42623b4e53fa38db5ed2822bb46b3512980b03f72ca7ea7a9ba47c92dff24010`
