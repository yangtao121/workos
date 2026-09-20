# V2 P3 剩余验收矩阵

- 状态：in_progress
- 基线：`eaf6c4c`
- Branch：`feat/v2-p3-final-matrix`
- 设计：[V2](../structure-v2.md)
- 前序：[P3](20260916-v2-p3-real-artifact-delivery.md)

## 范围与依赖

沿用 ADR-0033 与真实 Docker bundle profile，补齐 F04、F07、F09、F10、
F14、F16、F17、F21、F23、F24、F25、F26 的未覆盖组合，修复发现的缺陷，
恢复固定 viewport 视觉验收。不得将原有 PASS 扩大解释为完整矩阵通过。

本任务是用户已确认 V2 全部剩余交付的第一包。后续顺序为 DeepSeek 原生目标、
项目 Skills 与原生子 Agent；Docker 模拟 LAN/NAT 和 WebRTC/TURN；移动浏览器
与 Android；综合验收与文档收口。每包独立任务与 branch/worktree，契约先行。
不增加 KasmVNC、Codex/MCP、iOS、FCM 或任意后台 Bash；现有保护能力声明不降低。

用户接受 Docker 多网络模拟替代本轮物理设备验收；模拟网络、模拟器、真实公网和
物理设备证据分别标注。DeepSeek 真实验收总费用上限人民币 20 元；凭据仅在仓库外
`/home/aquatao/.config/workos-secrets/deepseek-v2-acceptance.key`，目录 0700、文件
0600，通过标准输入导入 Vault，禁止复制到日志、仓库或截图。

## 验收

- F01–F27 每行明确所执行的子场景、结果和证据位置。
- bundle、faults、legacy、V2 completion、受影响 race 和 namespace 清理检查通过。
- 固定三尺寸视觉用例必须执行；按 UI 规范保存 before/after/current。
- `make generate` 无生成差异，`make check` 通过。
- 同步模块文档、前序任务矩阵和 `docs/status.json`；未验证内容保持原状态。

## 进展

- 工作树起始干净，凭据已保存到仓库外并核对权限；未调用真实模型。
- Core 独立复核 ready bundle 的 incident/project/installation/source bundle 来源；Docker inspect 复核完整 Entrypoint+Cmd、非 root 用户和工作目录。
- 新增 `TestP3FinalMatrix`、`TestP3FinalAuthority`、`TestP3FinalRuntime`，覆盖完整失败零发布、结果查询丢回复、跨 owner 去重、注册前及台账提交前崩溃、生命周期配额、来源冒用、陈旧 stop 和实际容器配置漂移。
- `TestP3FinalMatrix` 九个真实栈子场景通过；authority 的配额/来源场景通过，F25 断言已修正为重新打开产生新 Workload，等待重跑。
- `make generate` 无生成代码差异；`make check` 与受影响 Go race 通过（通过 `workos-make:local` 调用宿主 Docker，宿主没有 make/Go/Node）。
- `sh tools/v2-p3-delivery/gate.sh legacy` 通过，`tmp/v2-p3-delivery.R0mUGY`；命名空间清空。
- `sh tools/v2-completion/gate.sh` 全通过，`tmp/v2-completion.D2WoWE`；四个浏览器用例含三尺寸视觉全部执行，无 skipped。
- [视觉 before](../ui/desktop-web/changes/20260920-v2-p3-final-matrix/before/)、[after](../ui/desktop-web/changes/20260920-v2-p3-final-matrix/after/)、[采集说明](../ui/desktop-web/changes/20260920-v2-p3-final-matrix/notes.md)；不涉及可见 UI 改动。
- 首轮 runtime 矩阵暴露测试清理竞争：卸载前受理的修复可在一次性查询之后提交构建，占用后续队列。清理现跨多个 coordinator tick 排空，屏障等待预算为三分钟；完整门禁正在重跑。
- 待执行：最终 bundle 全矩阵、INT/TERM 清理结果复核、同步事实状态和最终审查。
- 三个首次超时的 runtime 变体（restart-policy/user/image）在排空修复后定向重跑全部通过。其余八个漂移变体与不相关容器保留检查首轮通过；新增真实替换 Workload ID 场景等待验收。
- SIGINT/SIGTERM 分别退出 130/143；Compose/runtime/acceptance 标签的自有容器及 runtime 网络、卷归零，无关 sentinel 保留。证据：`tmp/v2-p3-final-signals/results.json`。
- 排查发现 Core 对注册前卸载/归档返回 Internal，导致已完成构建持续轮询。现在稳定返回目标已变更，并新增两个真实栈退役回归。
- 首次完整 bundle 运行已通过 `TestP3Closeout`（含 F07 重启后全部漂移），因补入退役缺陷修复，在 F12 期间主动中止并清理（退出 143）；不算完整通过。已重新从源码构建最终完整门禁，输出 `tmp/v2-p3-final-bundle-v2.log`。
