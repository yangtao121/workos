# 项目工具窗口与桌面侧栏

任务：[能力修复](../../../../tasks/20260903-v1-remaining-capability-sweep.md)。
基线 9d52fd3；after 为本阶段源码。路由 `/`，Chromium，deviceScaleFactor 1；
1440×900、820×1180、390×844，固定时间 2026-09-06 09:00 UTC。

共享 `desktop-fixture.ts` 的 Studio/Field notes；`desktop-tools-visual.spec.ts` 追加三个
确定性未安装应用，不调用外部服务。原四态由 `desktop-repair-visual.spec.ts` 提供；
另有应用库目录、项目设置两态。每种尺寸各六组 before/after，after 同步 current。
基线 fixture 显式提供空分页对象，否则旧应用库会无限请求第一页；修复已有单测覆盖。

```sh
docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e WORKOS_E2E_URL=http://127.0.0.1:5173 \
  -e WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260907-desktop-tools/after \
  -e WORKOS_TOOLS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260907-desktop-tools/after \
  -v "$PWD:/workspace" -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 node node_modules/@playwright/test/cli.js test \
  desktop-repair-visual.spec.ts desktop-tools-visual.spec.ts --workers=1
```

before 使用相同命令和 9d52fd3 源码，目录改为 before。有意变化：桌面侧栏改为紧凑项目列表，
应用库与设置使用统一标题栏、窗口几何、层级、关闭、最小化和 Dock 恢复；平板/手机沿用
同一内容函数，保持其原生布局。启动应用后应用库关闭，避免遮挡 Surface。
