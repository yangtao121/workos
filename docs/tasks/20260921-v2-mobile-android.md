# V2 移动浏览器与 Android APK

- 状态：done
- Branch：`feat/v2-mobile-android`
- Worktree：`/home/aquatao/workos-v2-mobile`
- 基线：`49227c9`；P3、原生目标/技能/子 Agent、LAN/NAT/TURN 已验收。
- 设计：[V2](../structure-v2.md)、[移动协议 ADR-0019](../decisions/0019-transport-providers-and-lan-discovery.md)、[当前实现](../architecture/implementation.md)。

## 范围与依赖

用户已接受模拟器和 Docker 网络证据；完成移动浏览器的项目/Agent/应用接续，以及真实 Android APK 的构建、安装、启动、平台安全存储、设备 HTTP/TLS/Cookie、配对和重启会话恢复。补齐原生壳可使用的部署地址/配对链接入口。使用现有 canonical RPC 和设备身份协议，不引入模型凭据到 App，不增加并行 DTO。

不包含 iOS、FCM/APNs、后台推送、商店签名发布或物理手机/公网验收。任何模拟器事实明确标注。SDK/JDK/系统镜像只进仓库外缓存；fixture CA 只限验收包，不向生产包加入信任绕过。

## 验收

1. 手机/平板触控浏览器通过受信 HTTPS 配对，访问同一项目、持续 Agent 会话与原生应用，显式接管保留程序状态。
2. Android SDK 36 + JDK 21 构建 debug/验收 APK；在 KVM Android 模拟器安装启动，真实 Capacitor HTTP 与 Keystore 插件，不用网页 mock 替代。
3. 严格 HTTPS、错误证书拒绝、正确 fixture CA 信任；配对/项目和通知读写、进程重启、Cookie 清除后私钥重认证、撤权/Forget 失败关闭。仅记录公开设备 ID/结果，不输出私钥/Cookie。
4. 证明落盘为密文且 AndroidKeyStore 实际持有加密 key；固定 viewport 和确定性 fixture 的 UI before/after/current，记录 APK 摘要和 Android/工具版本。
5. 模块/任务/status/生成一致性/`make check`，清理自有模拟器、namespace 和测试栈。

## 起始检查

- 工作树从网络验收提交建立，未覆盖其他任务改动。
- 已读取 Android manifest/build/Capacitor config、native vault/transport/auth/shell、相关设备 Proto、现有 unit/Chromium wrapper tests 和 UI 规范。
- 当前 MainActivity 未接入外链，壳只读构建 origin 或 localhost；无可输入的部署/配对入口，需要补齐才能形成可操作 APK。
- `/dev/kvm` 可用；官方 command line tools 15859902 SHA256 已核验，SDK/系统镜像在 `/home/aquatao/.cache/workos-android` 下载。尚未声明 APK 或模拟器验收通过。

## 实现与验证进展（2026-09-22）

- Android 使用自有失败关闭的 AES-256-GCM/AndroidKeyStore 插件，禁用旧插件的 Android 自动链接、备份和 Capacitor 参数日志。iOS 保留未验收状态。详见 [ADR-0036](../decisions/0036-android-keystore-and-deployment-entry.md)。
- 原生壳新增 HTTPS 地址/配对链接入口，公开 origin 单独持久化，ticket 仅在内存。手机/平板自适应首页补齐 Native 入口，平板 Dock 增加 Home。
- SDK 36、Java 21、Gradle 8.14.3 下 WorkOS debug、独立验收 APK 和 app AndroidTest 包构建通过。构建限定 `:app:`，不运行 Capacitor 附带库的独立测试包（其旧 Kotlin 测试依赖冲突与 WorkOS 无关）。
- Android 15/API 35 Google APIs x86_64 在 KVM 启动，`DeviceKeyVaultTest` 两项真实平台测试通过：随机密文、重建读取、Keystore 加密密钥不可导出、跨 origin 替换、损坏和丢失密钥失败关闭。
- 前端 11 项测试通过；移动壳四个 Chromium 用例、确定性自适应截图通过。完整浏览器/APK 端到端门禁仍在进行，不据此前置验证提升完成状态。
- 首轮触控浏览器配对与真实 Agent 续写成功，文件由 double 改为 triple；测试将 UI 状态 `done` 误写为 `completed`，已修正。首轮不计为完整通过。

- 平台验收进一步修复：接受 canonical `/pair#…` 链接；无效输入也立即清空；通知已读补齐已有 Proto 要求的幂等键。Chromium fixture 现在断言通知请求中的 key，避免 mock 掩盖真实服务拒绝。
- 触控 Native 增加文本提交及 Enter/Backspace/Tab/Esc/Ctrl+C 控件，沿用原输入协议和控制权；IME 未提交文本不发送，拥塞时保留草稿。8 项 Native 组件测试通过。
- 测试环境修复：ADB 监听必须先于 adb 客户端轮询；外部缓存固定 debug 签名避免覆盖安装签名冲突；模拟器显式选择隔离 Wi-Fi，并等待实际路由/DNS。TLS 负例严格匹配证书错误，DNS 失败不计为通过。
- 网络 refactor 回归：`tmp/mobile-network-regression-retry.log` / `tmp/v2-completion.EcDTuJ` 完整 LAN + NAT/TURN 通过，各 1 passed、无 skipped/flaky，namespace 清空。此前一轮 Native client 启动退出，未计为通过；没有以重试掩盖 TURN 失败。
- 全仓库 Go/proto 检查通过，首次 web-check 被 Android 新生成的 build 中间文件误入 ESLint 阻断。已精确排除 Android build 产物，`make web-check` 通过；最终完整检查待端到端收口后再执行。

## 最终验收（2026-09-22）

完整 APK + 两个触控浏览器门禁两次通过，最终 `tmp/android-final-deterministic.log` /
`tmp/v2-completion.XWsdqN`：浏览器 1 passed、APK 1 passed（29.5s）、Keystore 2 tests。
前述首轮失败与待验收段落为历史进展，最终事实见 [结果与摘要](evidence/20260921-v2-mobile-android/results.md)。

- `WORKOS_V2_SKIP_BUILD=1 sh tools/android-acceptance/gate.sh`：PASS，包含实际构建/安装、
  HTTPS/TLS 拒绝、持久身份、Cookie-free proof、通知已读、Forget、撤权、密文损坏。
- `WORKOS_V2_SKIP_BUILD=1 sh tools/network-continuity/gate.sh`：完整 LAN/NAT/TURN 回归 PASS。
- `make check`：PASS（`tmp/mobile-final-make-check.log`）；mobile 12、desktop 173 tests。
- `make generate`：PASS，212 个生成文件前后哈希一致（`tmp/mobile-generate-final.log`）。
- 任务自有 namespace 容器与网络清空；真实模型 key 在仓库外，精确字节扫描 Git 候选通过。

视觉证据：

| Client      | before                                                                   | after                                                                  | 说明                                                                     |
| ----------- | ------------------------------------------------------------------------ | ---------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| 触屏桌面    | [before](../ui/desktop-web/changes/20260921-v2-mobile-android/before/)   | [after](../ui/desktop-web/changes/20260921-v2-mobile-android/after/)   | [notes](../ui/desktop-web/changes/20260921-v2-mobile-android/notes.md)   |
| Web 壳      | [before](../ui/mobile-shell/changes/20260921-v2-mobile-android/before/)  | [after](../ui/mobile-shell/changes/20260921-v2-mobile-android/after/)  | [notes](../ui/mobile-shell/changes/20260921-v2-mobile-android/notes.md)  |
| Android APK | [before](../ui/android-shell/changes/20260921-v2-mobile-android/before/) | [after](../ui/android-shell/changes/20260921-v2-mobile-android/after/) | [notes](../ui/android-shell/changes/20260921-v2-mobile-android/notes.md) |

所有 after 已同步对应 current。移动模块提升为 working，仅代表上述已验证范围；
iOS、物理手机、硬件安全元件、商店签名和后台推送保持未验收。后续为主目录集成任务。
