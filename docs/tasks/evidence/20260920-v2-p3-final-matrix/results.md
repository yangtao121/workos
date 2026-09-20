# P3 最终验收（2026-09-20）

源码基线 `3f51fa4`，隔离 Docker 六进程、真实 PostgreSQL、当前源码二进制和 Chromium。
没有调用真实模型或访问私人项目。完整 F01–F27 映射见 [任务矩阵](../../20260916-v2-p3-real-artifact-delivery.md)。

| 命令 / 范围                              | 结果                                                  | 临时证据目录                |
| ---------------------------------------- | ----------------------------------------------------- | --------------------------- |
| `sh tools/v2-p3-delivery/gate.sh` bundle | PASS，全部集成测试、两条浏览器链、重启 replay         | `tmp/v2-p3-delivery.nU7EES` |
| `sh tools/v2-p3-delivery/gate.sh legacy` | PASS                                                  | `tmp/v2-p3-delivery.R0mUGY` |
| `sh tools/v2-completion/gate.sh`         | PASS，业务用例及三尺寸视觉共四例，无 skipped          | `tmp/v2-completion.D2WoWE`  |
| `make generate`                          | 无生成代码差异                                        | generation log              |
| `make check`                             | PASS                                                  | check log                   |
| 受影响 Go race                           | PASS                                                  | 任务记录及本地日志          |
| SIGINT / SIGTERM                         | 130 / 143，自有容器/网络/卷均为 0，无关 sentinel 保留 | signals results             |

## 最终轮次新增覆盖

- F04 完整失败零发布；F07 全字段漂移跨重启；F09 查询丢回复跨重启；F10 跨 owner 去重和损坏隔离。
- F14/F17 在注册前和 starting/canary/promoted/rolled_back 台账提交前实际终止 Reliability，再恢复收敛。
- F16/F21 实际容器的 12 类身份/配置漂移，每个场景重启 Runtime；旧版本自己的包恢复 A。
- F23 ready/preparing/staging/orphan 配额，F24 全来源复核，F19 注册前卸载/归档稳定拒绝，F25 陈旧 stop 与无关容器保留。
- 修复 Core 将退役目标误判 Internal 的无限重试；修复测试清理跨 coordinator tick 的遗漏。

[视觉记录](../../../ui/desktop-web/changes/20260920-v2-p3-final-matrix/notes.md)保留固定三尺寸 before/after/current。
初次中断及定向重跑不算最终完整通过；上表 bundle 是修复后的完整运行。

## 证据摘要校验

临时日志不入 Git；以下 SHA-256 对应本机保留的原始执行记录。

- `tmp/v2-p3-final-bundle-v2.log`：`f746b58e8caaa8642d18f4411a407cd22b9cd0e9bd94ac3dfa96a00f35956e64`
- `tmp/v2-p3-final-check-v2.log`：`e8497c96cd62184a20e2bc7d59b096886c8961e4188d3e45c298725f10a2215b`
- `tmp/v2-p3-final-generation.log`：`c93a468354d2c0633aa17a437a80a265758156a5afcc6065d8979dc9ddd106d2`
- `tmp/v2-completion-final.log`：`2e69519effb207da5901433bf78fc6fc6ed891f41549964cec1e18c1cf5e82e5`
- `tmp/v2-p3-final-signals/results.json`：`43e07e5d909862bbc139c47f0735c50e8382b793475c3a76dee305eb7965c9dc`

## 边界

本结论是 P3 软件交付和 Docker 故障矩阵证据。rootless/memory.high 继续 unavailable，
不替代物理 LAN、真实公网、原生设备或真实模型的验收。后续任务分别保存这些证据。
