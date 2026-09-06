# App artifact creation and review

任务：[能力修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线为 0aee618 构建的桌面资产，Chromium，viewport 1440×900、deviceScaleFactor 1，
路由 `/`；真实测试后端、固定 Writing workspace / Document author fixture。
SDK fixture：`apps/desktop-web/e2e/fixtures/artifacts-bridge.ts`。截图裁剪到 App 窗口或
查看器，不包含随机项目列表、UUID、时间、凭据或真实内容。

before/after 的 app-create 使用相同 Save document 入口：旧 shell 未实现能力，
返回 permission_denied；新 shell 完成真实 Core 事务并返回 saved。after 的 app-review
是相同新产物打开后的补充证据；查看器样式沿用已有实现。

采集命令：

```sh
WORKOS_ARTIFACTS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260906-artifacts-bridge \
WORKOS_ARTIFACTS_BEFORE_DIR=/workspace/tmp/20260906-artifacts-ui-before \
make test-app-artifacts
```

before 的 HTML/assets 从 0aee618 Gateway 镜像保存到上述临时目录，仅重定向静态资产，
授权、存储及 SDK 均为真实链路。app-create after 同步 current；review after 为补充证据。
