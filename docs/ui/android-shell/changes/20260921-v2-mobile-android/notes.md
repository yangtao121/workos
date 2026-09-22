# Android APK WebView 初始视觉基线

- 任务：[移动浏览器与 Android](../../../../tasks/20260921-v2-mobile-android.md)。
- 首次为 `android-shell` client 记录真实 Android 平台证据；根据 UI 规范，before 与本次
  初始 current 相同，after/current 都来自最终 APK，不把已有 Chromium 壳截图冒充旧 APK。
- Android 15/API 35 Google APIs x86_64，KVM Emulator 37.1.11，安装实际 `.acceptance` APK。
  WebView 通过 Playwright Android CDP 截图；390×844、density 160、实际 DPR 1，动画关闭，
  截图前等待键盘退出。不是 PNG 后期合成，也没有网页 RPC 或插件 mock。
- 六种状态：空部署输入、配对后项目、已读通知、网关离线时 Forget 失败、成功 Forget
  后待配对、真实密文损坏后 unavailable。配对输入在截图前已清空，未截图凭据或密文。
- `/` 使用确定性 Development fixture 和 Task completed 通知；模型为本地 fixture。
  截图时只把随机公开端口归一为 `https://workos.fixture`；真实 TLS/RPC 与身份断言均使用
  原 origin，截图后恢复显示。平台 safe-area 保持实际值，不伪装为浏览器设备。
- 命令：`WORKOS_V2_SKIP_BUILD=1 sh tools/android-acceptance/gate.sh`，
  `tmp/android-final-deterministic.log` / `tmp/v2-completion.XWsdqN/android-visuals`。
  Android instrumentation 2 passed、touch browser 1 passed、APK 1 passed（29.5s）。
- 该记录证明模拟器 APK，可见按钮最小高度 44px、footer 换行可访问。不证明 iOS、
  真机/折叠屏或商店发布；普通包未加入测试 CA。
