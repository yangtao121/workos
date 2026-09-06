# Workspace file picker

任务：[能力修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线为 9ea6ac0 的已构建桌面资产，after 为真实文件 Bridge。Chromium，viewport
1440×900、deviceScaleFactor 1，路由 `/`。固定项目显示名 `Files workspace`，固定
`Workspace editor` App、20 个 example 文本及 notes.txt；只有专用 fixture 内容，无凭据。
窗口/选择器截图裁剪掉随机项目列表、时间和 ID；SDK fixture 位于
`apps/desktop-web/e2e/fixtures/files-bridge.ts`。

before 使用同一真实后端与 App，将 shell 的 HTML/assets 路由到保存的基线构建，
点击 Choose file 得到 permission_denied，因为旧 host 没有文件能力；它不伪造选择器。
after 相同入口显示原生文件选择器，Load more 后选中 notes.txt；次要按钮为深色，
确认按钮高对比，Tab/Escape 和模态快捷键不会进入背景窗口。

`make test-app-files` 使用临时目录显式绑定到 Runtime。
`WORKOS_FILES_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260906-files-bridge`；
`WORKOS_FILES_BEFORE_DIR` 指向从 9ea6ac0 Gateway 容器保存的静态构建目录。
门禁构建真实 App SDK fixture，执行前后采集、文件保存和物理磁盘复核，
还验证旧 etag、路径穿越、符号链接和跨 API 撤销 grant 后的读写拒绝。
门禁结束会恢复默认 Runtime 配置并清理临时目录；after 同步 current。
