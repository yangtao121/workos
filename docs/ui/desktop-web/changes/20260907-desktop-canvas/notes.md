# 完整桌面画布

任务：[能力修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线 d70f72d；路由 `/`，Chromium，deviceScaleFactor 1；1440×900、820×1180、390×844。
固定时间 2026-09-06 09:00 UTC，共享 `desktop-fixture.ts` 的 Studio/Field notes。
项目工具使用三个确定性未安装应用；空项目使用空 ListProjects 和固定 CreateProject 响应。
不调用外部服务，不含真实凭据或用户数据。

既有六态 before 来自 current（与基线相同像素）；新增 empty 状态从仍运行基线 UI 的
Gateway 采集。每个尺寸共七态，after 同步 current。

```sh
docker run --rm --network host --user 1000:1000 -e HOME=/tmp \
  -e WORKOS_E2E_URL=http://127.0.0.1:5173 \
  -e WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260907-desktop-canvas/after \
  -e WORKOS_TOOLS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260907-desktop-canvas/after \
  -e WORKOS_CANVAS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260907-desktop-canvas/after \
  -v "$PWD:/workspace" -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 node node_modules/@playwright/test/cli.js test \
  desktop-repair-visual.spec.ts desktop-tools-visual.spec.ts desktop-canvas.spec.ts --workers=1
```

empty before 使用同一命令的 desktop-canvas.spec.ts，URL 改为基线 Gateway 的 8080，
CANVAS_CAPTURE_DIR 改为 before，增加 WORKOS_CANVAS_BASELINE=1。

有意变化：取消永久侧栏，项目切换与创建在顶部 Mission Control；卡片和设置隐藏 revision，
Home 加宽居中、卡片等高，1440×900 全部工具无溢出。空 Agent Center 提供明确的创建按钮。
三种尺寸的项目创建和 Home→设置→项目切换均测试；修复 adaptive 父容器抢回新窗口焦点。
Terminal 保持 unavailable，截图不作为真实 Native Runner 的证据。
