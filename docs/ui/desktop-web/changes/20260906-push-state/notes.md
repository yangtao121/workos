# 界面状态修复

任务：[能力总攻修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线 3831ea1 的 Gateway 静态资源，after 为同分支本阶段源码。
Chromium，deviceScaleFactor 1，1440×900 / 820×1180 / 390×844，路由 `/`。
共享 `desktop-fixture.ts` 固定 Studio/Fieldnotes、2026-09-06 09:00 UTC 与所有公共 RPC；
无真实账号内容或外部服务。Core 已撤销、浏览器仍保留相同 VAPID key 的旧订阅。before 误显示仅停用按钮；after 明确要求重新连接，同时允许清理旧订阅。

```sh
docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e WORKOS_E2E_URL=http://127.0.0.1:5173 \
  -e WORKOS_PUSH_STATE_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260906-push-state/after \
  -v "$PWD:/workspace" -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 node node_modules/@playwright/test/cli.js test push-subscription-visual.spec.ts --workers=1
```

before 使用 3831ea1 Gateway 的 8080 地址、`WORKOS_VISUAL_BASELINE=1`、输出到 before。
命令面板通过点击过滤后的动作进入，避免基线旧光标导致 Enter 无效；该问题已有回归单测。
after 相同名称图片同步 current。截图仅证明确定性 UI 状态，完整跨进程推送组合门禁仍待完成。
