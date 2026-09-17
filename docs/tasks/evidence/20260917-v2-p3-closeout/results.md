# 2026-09-17 P3 closeout 结果与命令记录

规则：只记录实际执行并观察到退出码的命令；失败同样记录。`tmp/v2-p3-delivery.*`
为当次运行的原始日志目录（成功后容器按 namespace 清理，日志保留至下次清理）。

## 前序主门禁历史（bundle profile，六进程 + 真实构建/容器）

以下表格保留接续前的运行记录。第 5/10 轮「非代码缺陷、仅资源争抢」是前序推断，
本轮已发现 Runtime 整批共享单作业 timeout 的产品缺陷，不能继续据旧归因排除代码问题。
F20 的前序“新候选可在旧候选回滚后自动晋升”也受不可变目标约束：修复 B 的任务不能
重新绑定到已恢复的 A。当前修复及最终验证以 [repair-validation.md](repair-validation.md) 为准。

命令：`sh tools/v2-p3-delivery/gate.sh`（等价 `make test-v2-p3-delivery`）

| 日期时间                           | 结果         | 说明                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ---------------------------------- | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2026-09-17 ~13:59–14:50            | 中断 ×7      | 前序运行被中断，输出不完整，不作为证据（任务记录已声明）                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| 2026-09-17 15:26（第 2 次）        | FAIL         | `TestP3Closeout/F02` 90s 窗口未收敛（tmp/v2-p3-delivery.PJnPev）；该轮不含 Windows/Matrix（文件名 `_windows` 被 GOOS 排除）。同轮 F01/F03–F09/F24 子测试通过。修复：容器收敛 + driveLaunch 串行化                                                                                                                                                                                                                                                                                                                                                                    |
| 2026-09-17 16:0x（第 3 次）        | FAIL         | 同 F02 失败；新增错误日志暴露根因：测试循环直连 runtime 未鉴权（`unauthenticated: workos identity is missing`），从未真正发起 relaunch（tmp/v2-p3-delivery.5n82n7 的前一目录）。同轮其余子测试通过                                                                                                                                                                                                                                                                                                                                                                   |
| 2026-09-17 16:4x（第 4 次）        | FAIL         | F02 通过；`TestP3CloseoutWindows` 首次真实执行：F13/F15/F16×2/F17×2 通过，F11/F12 失败（跨子测试同 digest 内容寻址去重）、F14 失败（断言 SQL 列名错误）（tmp/v2-p3-delivery.iO0Nm5）                                                                                                                                                                                                                                                                                                                                                                                 |
| 2026-09-17 17:1x（第 5 次）        | FAIL         | 仅 F03 失败：job 排队 120s 未被认领。归因：与 v2-completion 全量门禁并发运行，F02 的 supervisor 二级修复构建在资源争抢下拉长，单飞构建引擎被占用（F04 随后正常通过佐证引擎已释放）。非产品缺陷，改为独占运行（tmp/v2-p3-delivery.FuReQ3）                                                                                                                                                                                                                                                                                                                            |
| 2026-09-17 17:4x（第 6 次）        | FAIL         | 独占重跑。`TestP3Closeout` 10/10 全过（含 F02）；Windows：F13–F17 全过，F11/F12 在新断言点失败——F11 否定了 reconcile 在持久 verdict 后的合法晋升，F12 把 preparing 工件的 `failed_precondition`（产品正确的未就绪信号）当致命错误；F14 SQL 修复生效（tmp/v2-p3-delivery.FuDf4i）                                                                                                                                                                                                                                                                                     |
| 2026-09-17 19:0x（第 7 次）        | FAIL         | 独占重跑。legacy `TestP3RealDelivery` 2/2、`TestP3Closeout` 10/10、`TestP3CloseoutWindows` 9/9（F11–F17 全过，断言修复生效）全绿；`TestP3CloseoutMatrix` 10/12：F18_user_upgrade 失败——变体 manifest 缺 `build` 段（schema allOf 规定 runtime 带 artifact 时必须给出完整 build recipe 与真实 sourceBundleId），测试基建缺陷；F20_second_incident 失败——用例假设"旧先发布"，与 ADR-0016 §6 newest-wins 抢占语义（新 incident 使旧行有界 rolled_back，新 offer 在旧行终态后被接纳）相反，用例语义缺陷。e2e 因 gate 在矩阵失败后中止未运行（tmp/v2-p3-delivery.HrZHwO） |
| 2026-09-17 20:0x（第 8 次）        | FAIL         | 独占重跑。F18 修复生效（1.2.0 变体注册并上线 P3-VALUE-7）；矩阵 11/12，唯一失败 F20_second_incident——重写后的用例把两个 incident 背靠背创建，但 `new_incident` 判定要求更新 incident 的 created_at 晚于 ledger 行（旧行先建 → 不触发抢占 → #1 正常发布）。修正为"先等 #1 进入 canary 再插入 #2"（第 7 次运行已实证该时序确定性触发抢占）。legacy/Closeout/Windows 同 #7 全绿；e2e 仍因矩阵失败中止（tmp/v2-p3-delivery.abLDs4）                                                                                                                                      |
| 2026-09-17 20:3x（第 9 次）        | FAIL         | 独占重跑。抢占语义生效（#1 有界 rolled_back），但 #2 的 ledger 行始终未出现。DB 实证：#2 repair 目标快照取到 #1 的 staged 候选 pin（源码已含修复），generic-harness-fixture 对无 `return 0` 的目标 `!changed → fail()`，agent run 60ms 失败、repair 行 terminal——第二 incident 永久卡死。用新增 PREPARE_ONLY/KEEP 隔离栈 + 事件转储定位；修复 fixture 为幂等修复后在保留栈复测 F20 通过。其余同 #8 全绿（tmp/v2-p3-delivery.YJw3OZ）                                                                                                                                 |
| 2026-09-17 19:41–20:11（第 10 次） | FAIL（环境） | 门禁启动与上一诊断栈 `compose down -v`（多容器/卷拆除）几乎同时，docker daemon 争抢：legacy 段速度正常，随后 F01 变慢（199s）、F03/F04/F05/F08 构建 job 持续 queued，runtime 报 `build job store is temporarily unavailable`（负载下 DB 超时）；postgres 自身无错误。与第 5 次失败同类（并发资源争抢），非代码缺陷。F20 的 fixture 修复与本轮失败无路径交集（F03/F04/F05 不经 harness）。改为完全空闲窗口重跑（tmp/v2-p3-delivery.AXFTOm）                                                                                                                           |

## 当前结果

最终 bundle 门禁（`tmp/v2-p3-delivery.DZU1Lw`）退出 0：原主链 2/2、Closeout 10/10、
Windows 9/9、Matrix 13/13、两项 Chromium、重启 replay 全部通过。
Docker engine 矩阵补强后另跑 9/9 通过。

每个 F 编号的完整要求是否齐备，以[单一总任务](../../20260916-v2-p3-real-artifact-delivery.md)
F01–F27 表为准；不能把测试方法通过扩大成全部子矩阵通过。
详细命令、修复、身份链及兼容回归见 [repair-validation.md](repair-validation.md)。

## 身份与隔离声明

- 每个子测试独立 project/installation/idempotency key（`p3SeedNamed` 每次新建）。
- 字节篡改用例（F21/F22）篡改前读取原字节并在 `t.Cleanup` 恢复；artifact 按
  content digest 去重，恢复保证不影响其他用例与后续运行。
- 故障屏障在 `p3ResetFaults` 清空；门禁容器挂载 `WORKOS_FAULT_DIR`。
