# 移动壳部署入口与配对表单

- 任务：[移动与 Android](../../../../tasks/20260921-v2-mobile-android.md)。
- before 复制修改前 current；after 使用相同 `launch.spec.ts` 确定性 RPC fixture，
  Chromium、390×844、deviceScaleFactor=1、本地 `/`，四个场景全部通过。
- unpaired 新增掩码配对链接输入与提交按钮；unavailable 文案同时覆盖网络、证书或
  安全存储导致无法建立会话的情况，避免把所有失败误称为网关断线。
- 按钮触控高度至少 44px，footer 可换行，避免窄屏操作目标重叠。
- paired alerts 与 Forget failed 继续证明已读和失败时保留身份的行为；使用 Atlas/
  Nadir 固定项目和 `Task completed` 通知，不包含真实账号、凭据或用户内容。
- 采集：构建后在 18099 启动静态服务，设置
  `WORKOS_MOBILE_CAPTURE=../../docs/ui/mobile-shell/changes/20260921-v2-mobile-android/after`，
  运行 `playwright test`。默认采集路径改为临时 test-results，避免普通门禁改写旧任务证据。
- 结果：`tmp/mobile-final-browser-visual.log`，4 passed；after 同名文件更新 current。
  这里是 Web 挂载证据，实际 APK 平台截图单独归档于 android-shell client。
