# Shared desktop and launcher

Task: [shared desktop UI](../../../../tasks/20260922-shared-desktop-ui.md).

- Before: existing `current/home--launchpad` baseline, copied before editing.
- After/current: shared owner desktop, default Home, Agent Sessions primary entry,
  Tasks and approvals retained as a secondary application, Running apps before
  launch tiles, one focused responsive pane on phone/tablet.
- Route `/`; deterministic Studio/Field notes projects, empty installed/running
  app lists, fixed UTC time `2026-09-06T09:00:00Z`. No live account, credentials,
  user content or paid provider requests.
- Chromium, device scale factor 1, viewports 1440×900, 820×1180, 390×844.
- Fixture: `apps/desktop-web/e2e/shared-desktop.spec.ts` uses the existing desktop
  fixture plus canonical desktop RPCs served by an actual local Connect stream.
  The same file separately checks three independent contexts in Chromium and
  WebKit. Linux WebKit does not substitute for physical Safari acceptance.

Capture with a Vite instance serving this worktree and the pinned test image:

```sh
WORKOS_E2E_URL=http://127.0.0.1:5185 \
WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260922-shared-desktop-ui/after \
pnpm exec playwright test shared-desktop.spec.ts --workers=1
```

Images were inspected after capture. Application-specific conversation/pending
states are covered by the parent continuity task after integration.

## 开发状态回归

共享桌面改变了旧开发状态 fixture 的入口：当前视图由 DesktopService 决定，刷新后仍选中同一会话，Native/Terminal 恢复要求 workload/generation。`v2-completion-visual.spec.ts` 已采用真实 HTTP sharedDesktopFixture，并明确断言没有隐式 CreatePty/CreateNative 调用；fixture reopen 保留最新 canonical target。

新增24张同尺寸开发状态 before/after/current：Home运行列表、工作区设置、文件冲突草稿、运行预览、执行问答、需核对会话、Native观察者、已停止终端。before 复制各对应 current 历史基线；after 来自新集成 UI 的相同确定性业务数据与三尺寸流程。旧入口动作（刷新返回列表、启动按钮隐式发现）按新的明确恢复语义调整，未通过改DOM制造界面。

命令：`WORKOS_V2_CAPTURE_DIR=... playwright test v2-completion-visual.spec.ts --workers=1`，Chromium3/3通过。手机问答、桌面Native观察者、平板停止终端均人工检查；最大图小于420KiB。
