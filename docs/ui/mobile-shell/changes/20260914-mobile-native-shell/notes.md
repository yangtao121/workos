# 20260914-mobile-native-shell 采集说明

- 任务：`docs/tasks/20260903-v1-remaining-capability-sweep.md`（GLM-5.3 续作段）。
- 受影响界面：mobile-shell 全部状态（unavailable / unpaired / paired-alerts）。
- 变更：Capacitor 8 平台工程重生成 + capacitor-secure-storage-plugin 连入；
  device-auth 可选 KeyBackend；shell 接通配对 fragment、会话恢复、项目/通知
  投影与已读、fold 段检测、visualViewport 键盘 inset、Forget device。
- 采集：`make test-mobile-wrappers`（e2e/launch.spec.ts）内联截图，Chromium，
  viewport 390×844，deviceScaleFactor 1，确定性页面级路由 fixture（无真实网络）。
  - unavailable：GetCurrentDevice → 503。
  - unpaired：GetCurrentDevice → 401（配对指引 + APNs/FCM 边界文案）。
  - paired-alerts：GetCurrentDevice → 200（deviceId/name/deviceClass=phone），
    ListProjects → 2 项，ListNotifications → 1 条未读（已点击 Mark read 后截取，
    卡片为已读态、角标清零）；Timestamp 用 RFC3339 字符串。
- before：复制自 `docs/ui/mobile-shell/current/`（20260910 首次基线，仅
  unavailable 一张；当时无配对/通知界面，故 before 无对应状态图，属新增状态）。
- current 已用 after 同名文件更新。
- 已知边界：原生二进制/真机配对与推送无 Android SDK/Xcode/签名设备，保持
  scaffolded；web runtime 的 vault 状态如实显示 "profile storage fallback"。
