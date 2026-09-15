# 移动认证合并审查

- 任务：[总任务记录](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
- before：修改前的 current 基线，来源为 GLM `da8a262`。
- after/current：`make test-mobile-wrappers`，Chromium，390×844，deviceScaleFactor=1；
  本地 18099 HTML 入口，页面级确定性 RPC fixture，无外部服务、真实用户或真实密钥。
- 状态：Gateway unavailable、unpaired、paired alerts（已读）及新增 Forget failed。
  失败态在同一配对 fixture 上令 Logout 返回 503；保留已登录界面及告警，允许重试。
  旧版本吞掉该错误并显示未配对界面；before 的 unpaired 图记录其原有表现，
  不将它冒充原版本已具备失败提示。
- 验证：tmp/codex-final-mobile2.log（软件检查、构建、平台引用检查、Android sync、
  4 个 Chromium 场景，含 Forget 期间迟到投影失效回归）。四张 after 均逐张查看，尺寸正确，无溢出或敏感数据。
- 原生 HTTP、证书、Keychain/Keystore、链接唤起及推送仍需实际平台验收；截图仅证明 Web 软件链。
