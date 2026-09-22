# 移动浏览器与 Android APK 验收（2026-09-22）

2026-09-22 清理更新：旧工作树已删除；下文引用的现有 `tmp/*.log` 等根目录日志已按原字节
归档至主工作目录的 `tmp/archived-worktree-evidence/20260922/workos-v2-mobile/tmp/`。
完整清单、SHA256 与范围见 [清理记录](../../20260922-v2-merged-worktree-cleanup.md)。
临时测试运行目录已清理，已提交的结果、截图与原日志摘要保留。

基线 `49227c9`，在独立六进程/PostgreSQL/生产设备认证/受信 HTTPS fixture 上验证。
Android 证据来自 KVM 模拟器；手机和平板浏览器来自两个独立触控 Chromium 容器。
不把这些结果扩大为物理手机、物理 LAN 或公网验证。所有模型响应均为确定性 fixture。

## 通过范围

| 范围               | 结果与断言                                                                                                                       |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| 手机／平板浏览器   | PASS；390×844 与 820×1180，touch + DPR 1；真实配对后访问同一项目，同一 Agent session 将 double 续写为 triple，并验证执行结果     |
| Native 应用        | PASS；真实 VP8 像素、SCTP 触屏文本与常用按键、20 秒媒体续约、显式接管、重开保留内存和 Workload；旧控制代次拒绝，设备撤权关闭连接 |
| APK 构建／安装     | PASS；普通 debug、独立 acceptance 与 app instrumentation 三包构建安装，Android 15/API 35 x86_64 实际启动                         |
| TLS                | PASS；普通包拒绝 fixture CA；验收包仅额外信任该 CA，错误主机名仍拒绝。没有忽略证书错误；DNS 错误不充当 TLS 拒绝证据              |
| 身份／会话         | PASS；真实平台插件配对，按 origin 保存 AES-GCM 密文，强制结束进程后恢复同一设备；清除 Cookie 后重新证明原私钥                    |
| 通知／Forget／撤权 | PASS；真实读写已读并检查服务端时间；网关停止时 Forget 失败且身份保留，恢复后成功删除；重新配对产生新设备，撤权后 401/unpaired    |
| 密文损坏           | PASS；修改真实 SharedPreferences 后启动变为 unavailable，不悄悄重置身份或退回不安全存储                                          |
| Android 平台测试   | PASS，2 tests；随机密文、重建读取、AES key 不可导出、删除幂等、跨 origin 替换/损坏/丢 key 失败关闭                               |
| 单元／浏览器       | PASS；mobile 12 tests，Native 组件 8 tests，移动壳 Chromium 4 tests，首页/Dock before 与 after 各 1 test                         |
| 网络回归           | PASS；复用 topology 的完整 LAN + NAT/TURN 门禁，LAN 56.9s、relay 63s，各 1 expected，无 skipped/flaky；coturn 四项探针通过       |
| 全仓检查           | PASS；`make check` 的 Proto/sqlc、Go vet/tests、架构、ESLint、Prettier、TypeScript/unit/build、状态渲染检查                      |

完整命令：`WORKOS_V2_SKIP_BUILD=1 sh tools/android-acceptance/gate.sh`。
`SKIP_BUILD` 复用同一 Go 源码基线的六进程二进制；移动任务没有修改 Go/Proto，
移动 Web assets、Capacitor sync 和三个 APK 都在门禁中重新构建。
本地完整日志 `tmp/android-final-full.log`，运行目录 `tmp/v2-completion.WWVw3Z`；
触控业务链 1 passed，真实 APK 1 passed（27.9s），仪器测试 2 passed。
最后一次确定性截图重跑已通过（`tmp/android-final-deterministic.log` /
`tmp/v2-completion.XWsdqN`，浏览器 56.2s、APK 29.5s，均无 skipped/flaky）；额外等待键盘收起，仅在截图时将随机分配的公开端口
归一为 `https://workos.fixture`，不修改真实网络、TLS、配对或服务端断言。

全仓检查：通过 `workos-make:local` 挂载工作树和 Docker socket 执行 `make check`，
`tmp/mobile-final-make-check.log`；desktop-web 173 tests，26 files。
网络回归 `tmp/mobile-network-regression-retry.log` / `tmp/v2-completion.EcDTuJ`。
移动视觉 `tmp/mobile-final-browser-visual.log`，4 passed；
自适应 `tmp/adaptive-visual-before.log`、`tmp/adaptive-visual-after.log`，各 1 passed。

## 平台与交付边界

- SDK command line tools 15859902；platform-tools 37.0.1；compile/target SDK 36；
  build-tools 36.0.0 与 AGP 所需 35.0.0；JDK 21；Gradle 8.14.3，wrapper 校验分发 SHA256。
- Emulator 37.1.11/build 15917651，Google APIs Android 35 x86_64 rev 9，KVM。
  系统为 Android 15，fingerprint `google/sdk_gphone64_x86_64/emu64xa:15/AE3A.240806.043/12960925:userdebug/dev-keys`。
- SDK、AVD、Gradle 和 debug 签名 key 在仓库外缓存；APK/测试运行文件被 Git 忽略。
  普通 debug APK 路径 `apps/mobile-shell/android/app/build/outputs/apk/debug/app-debug.apk`。
  `.acceptance` 包仅用于一次性测试 CA；不作为生产发布包。
- AndroidKeyStore 不可导出保证指 AES 包装 key；签名 JWK 在受控 JS 中使用。
  模拟器不证明物理硬件安全元件。日志禁用插件参数，SharedPreferences 仅密文且禁备份。
- 原生 APK 是部署/身份/项目/通知客户端；完整 Agent/Native 工作区运行于手机和平板浏览器。
  iOS/Keychain、真机、商店签名、FCM/APNs、后台推送、扫码/深链不在本轮范围。

## 首轮失败与修复

- canonical 配对链接含 `/pair`；部署解析原先只接受根路径，真实 APK 发现后补齐解析和测试。
- MarkNotificationRead 原先漏传已有 Proto 规定的幂等键，真实服务返回 400；已补齐，
  浏览器 mock 现在也断言该 key，且 APK 断言服务端 readAt。
- ADB listener/client 启动竞争、临时 debug 签名变化和隔离 Wi-Fi 未选中导致 fixture 失败；
  已分别按 listener 就绪、持久化本地 debug key、实际路由/DNS 等待修复。
- 首次触控断言把 `done` 写成 `completed`；首次全仓 lint 误扫 Android build 输出；均修复。
- 首次网络回归在 Native client 启动阶段退出，尚未产生 ICE；不计为通过，也不推断根因。
  之后全新完整 LAN/NAT 回归通过；此前失败日志保留 `tmp/mobile-network-regression.log`。

长期截图与说明：
[触屏桌面](../../../ui/desktop-web/changes/20260921-v2-mobile-android/notes.md)、
[Web 壳](../../../ui/mobile-shell/changes/20260921-v2-mobile-android/notes.md)、
[Android WebView](../../../ui/android-shell/changes/20260921-v2-mobile-android/notes.md)。

## 生成、清理与摘要

`make docs` 先渲染状态，随后 `make generate` 前后 212 个 Proto/sqlc/README 文件
SHA256 一致，`tmp/mobile-generate-final.log`。Capacitor Android 文件由 sync 生成。
两个最终 Android gate 与网络回归自己的容器/网络均清空，共享开发栈未操作。
仓库外真实 DeepSeek key 权限 0600、父目录 0700；精确字节扫描全部 Git 候选文件无匹配。

最终 [平台](android-platform.json)、[APK SHA256](android-apk-sha256.json)、
[公开结果](android-continuity.json)、[真实 Keystore 测试结果](android-keystore-tests.txt)。
APK 是本机生成产物；接受测试 CA 的包每次随一次性 CA 改变，不承诺二进制可重复哈希。

原始日志保留本机、不提交 Git；SHA256：

- `tmp/android-final-full.log`：`f90875b3bc2246b18d85554dec56a7a00d2bd3dde5cf458b62c783ba71f3e37d`
- `tmp/android-final-deterministic.log`：`ce7fce446cbeebadf471bca4404fee34b6799112490f35cfb41b8c362003e97c`
- `tmp/mobile-final-make-check.log`：`acfc7f43bd60a585f74c289a1dc1ae4976495ad2ad5099cf3601330476a7178c`
- `tmp/mobile-generate-final.log`：`2ca0d48ba09fa5b42c5c1c89ef61795f5347c56ccbf8dba81e2763170e3689c3`
- `tmp/mobile-network-regression-retry.log`：`7bd93b16032e2693a8d0c560ae230ce108b40a6d8d2114e7f98229a6843efd56`
- `tmp/mobile-final-browser-visual.log`：`5e70b882a19365cb44e3946f86da0d16f90ff31898c1739edd020fc025ea2a52`
