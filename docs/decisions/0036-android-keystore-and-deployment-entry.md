# ADR-0036：Android Keystore 与部署入口

- 状态：accepted
- 日期：2026-09-21
- 任务：[移动与 Android](../tasks/20260921-v2-mobile-android.md)
- 前序：ADR-0019

## 决策

Android 直接注册 WorkOSSecureStorage 插件，使用 AndroidKeyStore 非导出的 AES-256-GCM
密钥加密 canonical device-auth 的身份记录。随机 IV、带认证的密文和 origin-scoped slot
的 AAD 防止静态泄露和跨槽替换；普通偏好文件只保存版本化密文。缺失记录、损坏记录、
Keystore 丢失与写/删失败分别处理，失败不能成为普通存储回退或成功响应。关闭 Android
backup，并关闭 Capacitor 参数日志，避免设备 JWK/配对参数出现在备份或 logcat。

原因：锁定的 capacitor-secure-storage-plugin 0.13.0 Android 实现会在 Keystore 初始化
失败时降级为普通 Base64，且写入错误可被吞掉；仅检查插件存在不能证明安全存储可用。
Android sync 排除此插件，iOS 保留原插件且继续标记未验收。没有自动迁移旧 Android
插件的未经验证记录；旧实验包用户需重新配对。设备身份 JWK 仍需在受控 JS 内签名，
不把此架构说成设备签名私钥始终不离开 Keystore，也不对模拟器声称硬件安全元件。

原生壳首次启动提供 HTTPS 地址或配对链接输入。只保存公开 origin，ticket 保留在内存，
输入提交即清空，沿用 canonical 配对和 origin proof。换部署按 origin 隔离身份，原生
HTTP 保持严格 TLS、固定 Origin/Host、禁止重定向。未接入深链唤起或扫码时不展示虚假入口。

验收 APK 使用独立 applicationId 后缀 `.acceptance`，只有该构建类型可显式加入一次性
fixture CA。普通 debug/release 包不携带 fixture CA，不忽略证书错误或主机名校验。

实际平台、UI、Cookie/重认证和清理结果以任务记录为准；本 ADR 本身不提升完成状态。
