# Android 壳与移动浏览器

Android 壳沿用 canonical device-auth 和 Connect JSON unary RPC；设备配对、私钥证明、
Cookie 会话、项目和通知投影不另设协议。手机/平板浏览器运行完整自适应客户端，提供
持续 Agent 会话与 Native 应用的显式控制权接管。Android APK 当前提供部署入口、
设备配对、项目列表、通知已读与 Forget；并不在原生壳中宣称拥有完整桌面编辑器。

实现决策见 [ADR-0036](../decisions/0036-android-keystore-and-deployment-entry.md)，
实际通过范围见 [任务记录](../tasks/20260921-v2-mobile-android.md)。本轮不验收 iOS、
商店签名、APNs/FCM、后台推送、扫码或深链唤起，也不把模拟器称为物理手机。

首次启动可输入 HTTPS origin 或粘贴配对链接。普通 Web Storage 仅保存公开 origin，
ticket 提交后清空输入且不写盘；设备身份按 origin 隔离。Android 原生 HTTP 保留 TLS
证书和主机名验证，固定 Origin，拒绝重定向。失败不会回退 HTTP 或不安全 Web 存储。

`WorkOSSecureStorage` 使用 AndroidKeyStore 中不可导出的 AES-256 密钥与随机 GCM IV
加密身份记录，slot 作为 AAD 防止密文跨 origin 替换。SharedPreferences 只保存版本化
密文。密钥丢失且仍有密文时拒绝重建；损坏、写入失败与删除失败必须传递失败。
设备签名 JWK 会在受控 JS 中使用，因此这里的不可导出保证指加密密钥，不是说签名
私钥永不离开 Keystore。模拟器证据不保证硬件安全元件。关闭 Android 备份与插件参数
日志；不记录 JWK、Cookie、配对链接或模型凭据。

## 可复现门禁

`make test-android-acceptance` 或 `sh tools/android-acceptance/gate.sh` 创建独立六进程、
PostgreSQL、受信测试 HTTPS、两个触控 Chromium 容器及 KVM 模拟器。所有测试模型响应
来自 fixture，不调用付费 Provider。DNS 只回答测试主机，不修改宿主 DNS、路由或共享栈。
门禁使用手机 390×844、平板 820×1180 与实际 APK WebView；Android 屏幕设为
390×844/density 160，截图文件名使用真实 WebView 尺寸（系统栏占用不伪装成网页尺寸）。

前置条件是 Docker、Python 3、可访问的 `/dev/kvm`、已安装依赖与外部 Android SDK。
`WORKOS_ANDROID_CACHE` 默认 `$HOME/.cache/workos-android`，含 `sdk/` 与 `gradle/`；
SDK、AVD、Gradle 缓存与 APK 构建产物不进入 Git。使用的包为 platform-tools、
platforms;android-36、build-tools;36.0.0、AGP 所需的 build-tools;35.0.0、emulator、
system-images;android-35;google_apis;x86_64。Java 21 来自验收 Docker 镜像。
Gradle wrapper 锁定 8.14.3 bin 分发并检查 SHA256；仅构建应用及其仪器测试包。
构建的 Java user.home 固定到缓存的 `home/`，其中保存本地 debug 签名 key，避免每次
短命容器生成新签名而导致 APK 无法覆盖安装；它不属于生产发布签名。

普通 debug/release 不信任 fixture CA。只有独立 applicationId 后缀 `.acceptance` 的
构建类型要求显式 `workosAcceptanceCA` 并打包该一次性 CA；平台信任和主机名验证仍然
执行。测试先证明普通包拒绝该 CA，再验证验收包通过正确主机并拒绝错误主机。
验收 APK 不是生产发布包。完整命令、摘要与平台版本由任务 evidence 记录。

门禁退出清理本任务的容器和网络；失败的临时目录仅供本地排错，长期文档只提交经过
筛选的结果与确定性 fixture 截图。实际收费模型凭据不进入此门禁。
