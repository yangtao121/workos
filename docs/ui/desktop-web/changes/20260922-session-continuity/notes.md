# Session continuity visual evidence

Task: [session continuity](../../../../tasks/20260922-session-continuity.md).

Six states: the selected session with completed input/summary and a local draft,
then the same session immediately after pressing Send while offline, each at
1440×900, 820×1180, and 390×844. Viewport scale factor is 1, route `/`, fixed UTC
clock `2026-09-06T09:00:00Z`. Studio, Studio source, provider `fixture`, all messages
and IDs are deterministic test data. No actual account, credentials, provider or
external service is used.

Before renders baseline commit `b4f7020`; after renders the real session component
from `39daa2e` on the shared desktop. The same HTTP fixture and browser actions are
used. Existing `current/` images did not describe these states, so the before
images were captured from an isolated baseline worktree instead of substituting
an unrelated old screenshot.

Intentional difference: the old offline Send clears the composer and shows a
failed pending input. The new component keeps the draft, displays its real offline
notice, and makes no SubmitSessionInput request. No DOM injection, custom overlay
or screenshot text modification is performed. The completed message and bounded
server summary remain visible in both versions.

The test uses `shared-desktop-fixture.ts` with an actual Connect HTTP stream for
the selected desktop window, and deterministic session/workspace RPC fixtures.
Production UI components render every pixel. Client snapshots and offline behavior
are exercised by normal browser controls; no screenshot-only product route was
added.

Capture commands against separate Vite servers for baseline and after:

```sh
WORKOS_E2E_URL=http://127.0.0.1:5186 \
WORKOS_SESSION_BASELINE=true \
WORKOS_SESSION_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260922-session-continuity/before \
pnpm exec playwright test session-continuity-visual.spec.ts --workers=1

WORKOS_E2E_URL=http://127.0.0.1:5185 \
WORKOS_SESSION_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260922-session-continuity/after \
pnpm exec playwright test session-continuity-visual.spec.ts --workers=1
```

Runner: `workos-playwright-shared:1.62.1`, Chromium. Browser subdirectories prevent
parallel projects from overwriting outputs. The six Chromium `after/` images also
update the same named `current/` PNGs. Screenshots were inspected at mobile and
desktop sizes. All images are below 2 MiB.

The same tests also pass with `--browser webkit`, storing disposable outputs under
`tmp/`; those results are browser-engine behavior evidence. Physical Android,
iPhone and iPad acceptance remains separate. Later journal race fixes may require
recapture on the final integration tree, using the same fixture and commands.

## 集成复验与 Forget

最终集成树（含事务 journal、5 秒桌面补读、退出清理）再次运行同一 fixture，Chromium/WebKit 共 20 项共享桌面门禁全部通过；当前 after/current 已更新为本次集成输出。手机离线草稿及桌面 Forget 失败画面已人工查看，未包含真实内容或凭据。

补充 `auth-gate--forget-failed--1440x900.png`：同一 `session-forget.spec.ts` 固定 DeviceService 401／Logout 503，点击 Forget，before 在 `d39c275` 的真实旧 AuthGate 上采集，after 在本次集成构建采集。旧画面吞掉失败；新版显示无法完全清除并允许重试，保留本地内容，后续 Logout 成功才清理。基准服务是独立 Vite 端口6147，现已停止。

复现：设置 `WORKOS_CAPTURE_DIR`，运行 `playwright.shared.config.ts`；旧版本采集额外设置 `WORKOS_FORGET_BASELINE=true` 并仅运行 `session-forget.spec.ts`。截图输出按浏览器分目录，current 使用 Chromium 固定1倍像素；WebKit结果作为行为验证。
