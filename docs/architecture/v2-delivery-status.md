# V2 交付与证据索引（2026-09-22）

[structure-v2.md](../structure-v2.md)是 2026-09-15 的设计时点；本页汇总后续已接受 ADR
和用户确认的交付范围。原始差距表不作为当前缺陷清单。进度事实仍由 [status.json](../status.json)
及各任务的可复现证据支撑，最终集成见 [任务](../tasks/20260922-v2-final-integration.md)。

## 已完成的用户链路

用户从浏览器在项目中持续与 DeepSeek 原生会话开发；Agent 修改真实工作区并执行测试，
用户直接打开服务端 PTY、Native 或 HTTP 预览。关闭窗口只断开界面，另一个设备可以进入
同一项目、继续同一会话并显式接管同一程序。发布绑定真实产物，新版本呈现新行为；
回滚恢复旧行为。手机和平板触控浏览器完成相同接续，Android APK 提供部署入口、设备身份、
项目及通知投影。客户端不读取模型凭据。

| 交付范围                   | 最终行为与证据                                                                                                                                                                                                                                                       |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| P0/P1/P2 基础链            | 锁定官方 DeepSeek Harness、原生持续会话、真实 Docker 工作区、代码修改/测试、桌面恢复、PTY/Native/HTTP 预览与设备控制权；[架构](v2-agent-workspace-web-continuity.md)、[六进程任务](../tasks/20260915-v2-agent-workspace-web-continuity.md)                           |
| P3 发布／修复／回滚        | 真实 Docker bundle 产物与 provenance、A→B→A 行为、重启恢复、重复请求/版本变更保护；F01–F27 全矩阵及崩溃/漂移/清理通过；[P3 证据](../tasks/evidence/20260920-v2-p3-final-matrix/results.md)                                                                           |
| 原生目标／Skills／子 Agent | 持久目标暂停恢复、项目技能、最多两名深度一子 Agent、隔离工作树和独立 diff 成果、共享父预算与授权；故障后 needs_review、不盲目重放；真实 DeepSeek 自动化与持续 double→triple 两条验收；[证据](../tasks/evidence/20260920-v2-native-goals-skills-subagents/results.md) |
| 跨网络接续                 | 生产 HTTPS 配对、实际 CA 信任、独立 LAN 与两侧 NAT、短期 TURN capability、真实 coturn relay/relay、VP8/SCTP、媒体续约、接管/故障恢复/撤权；[网络证据](../tasks/evidence/20260921-v2-network-continuity/results.md)、[部署配置](native-network-connectivity.md)       |
| 移动浏览器／Android        | 手机和平板触控真实项目/Agent/Native 接续；Android debug APK 构建安装、Keystore 加密、严格 TLS、配对/重启/重新证明、已读、Forget、撤权和损坏失败关闭；[移动证据](../tasks/evidence/20260921-v2-mobile-android/results.md)、[使用与构建](android-shell.md)             |

## 能力与证据边界

- 本轮用户接受 Docker 网络与 KVM Android 模拟器；物理手机、物理 LAN、真实公网/IPv6、
  TURN TCP/TLS 和高可用没有通过声明。90 秒 TURN 凭证限制新分配，实际 allocation 最短
  600 秒；媒体授权与每条输入的控制代次由 WorkOS 独立验证。
- 原生目标与子任务保留明确范围：最多两名子 Agent、深度一、干净 Git 基线、子 diff 不自动
  合并；工作区撤权/失租约/未知执行结果停止自动推进。任意后台 Bash 仍 unavailable。
- Docker daemon 属于受信 Runtime；未将 Docker 测试表述为 rootless Podman 或 memory.high
  验收。六进程及各表所有权、canonical protocol、App capability 边界保留。
- Android APK 当前是部署、设备、项目与通知客户端；完整工作台使用移动浏览器。
  iOS/Keychain、硬件安全元件、扫码/深链、商店签名发布与 APNs/FCM/后台推送不在此次范围。
  Android 不可导出指 AES 包装 key；JS 内仍使用签名 JWK。
- Kasm/VPN、更多 Provider/Harness 扩展未纳入本轮确认计划。现有不可用能力保持诚实声明，
  不以模块 working 状态推断所有未来扩展已实现。

## 交付入口

- 主工作目录 `/home/aquatao/workos`，当前分支 `main`；全部功能已从集成分支快进合入。
  旧功能分支和三个工作树已清理，见 [清理记录](../tasks/20260922-v2-merged-worktree-cleanup.md)。
- 普通 Android debug APK 本机副本 `tmp/deliverables/workos-android-debug.apk`；
  源构建命令见 [Android 壳](android-shell.md)，摘要见移动证据。它不信任验收专用 CA，
  需要设备信任部署的有效 HTTPS 证书；首次启动输入 HTTPS 地址或粘贴配对链接。
- API key 只在仓库外 owner-only 文件；无 Git 跟踪、截图或日志明文。累计 38 次真实请求，
  保守预算预留人民币 6.055478 元，小于授权 20 元；模型结果与固定 fixture UI 证据分开。
- 最终生成一致性、全仓检查及集成 E2E 结果由 [最终集成任务](../tasks/20260922-v2-final-integration.md)
  记录；UI after/current 已随各功能任务提交。
