# 20260831-adaptive-shell 视觉记录

- 任务：`docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md` 阶段 A
  （Adaptive Shell：Compact / Medium / Expanded / Fold-separated）。
- 采集方式：`apps/desktop-web/e2e/adaptive-visual.spec.ts`（显式运行，需要
  `WORKOS_CAPTURE_DIR`），全路由拦截的确定性 fixture（固定 device/project/catalog/task
  timeline，无真实后端、无随机 ID/时间），同一 spec 先后对基线 bundle 与实现后 bundle
  采集 before/ 与 after/。页面由 `vite preview` 提供对应构建的 dist；
  `WORKOS_E2E_URL` 指向该服务器。
- viewport / 浏览器：Chromium，deviceScaleFactor 1，locale en-US，UTC。
  before/ 采集自基线 main（commit `12a53ab`，dist JS sha256 前缀 `610cd2cb`）。

## 受影响界面与状态

| 文件                              | 界面                                | 说明                                            |
| --------------------------------- | ----------------------------------- | ----------------------------------------------- |
| expanded--desktop--1440x900       | Expanded 桌面（基线行为）           | before/after 内容逐像素一致（见下方"噪声说明"） |
| compact--home--390x844            | Compact 主屏 + 底部导航             | after 新增；before 为旧桌面挤压渲染             |
| compact--agent-task--390x844      | Compact 单 pane Agent 任务时间线    | after 新增（旧 UI 在 390px 不可达）             |
| compact--apps--390x844            | Compact 全屏 App Library            | after 新增（旧 UI 不可达）                      |
| compact--project-sheet--390x844   | Compact Project sheet               | after 新增（旧 UI 不可达）                      |
| medium--home--820x1180            | Medium 主屏 + 显式 Dock 按钮        | after 新增；before 为旧桌面挤压渲染             |
| medium--agent-slideover--820x1180 | Medium Agent slide-over             | after 新增（旧 UI 不可达）                      |
| fold--dual-pane--1280x800         | Fold-separated 双 pane + hinge 空隙 | after 新增；before 忽略 segment 渲染旧桌面      |
| fold--single-pane--1280x800       | Fold-separated 用户选择单 pane      | after 新增（旧 UI 无此概念）                    |

before/ 仅包含旧 UI 在对应 viewport 真实可达的状态；旧构建在 390/820px 只会把桌面
布局挤压渲染（body overflow hidden，Agent 窗口在视口外不可点），因此这些状态没有
before 帧——这正是本任务要修复的可用性缺陷，notes.md 与任务记录已注明。

## 噪声说明（expanded 帧哈希差异）

before/after 的 `expanded--desktop--1440x900.png` 文件哈希不同，但内容逐像素对比
仅有 21 个字节、全部位于文字边缘、差值 ≤2（文字抗锯齿噪声）。对同一 bundle 连续
两次采集（fold 帧）哈希同样每次不同（`2f87ebe1…` / `3fd59fd1…`），证明这是
Chromium 截图光栅化的运行间波动，不是 UI 变化。Expanded 桌面的布局、窗口、Dock、
文案与交互（自由窗口、drag/focus/z-order）未回退，`make test-adaptive-shell` 的
expanded 回归用例与既有桌面 E2E 共同证明。

## 复现命令

```sh
# after/（当前实现）
docker run -d --rm --name workos-adaptive-preview --network host --user "$(id -u):$(id -g)" \
  -e HOME=/tmp -v "$PWD":/workspace -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 pnpm exec vite preview --host 127.0.0.1 --port 4173 --strictPort
docker run --rm --network host --user "$(id -u):$(id -g)" \
  -e HOME=/tmp -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
  -e WORKOS_E2E_URL=http://127.0.0.1:4173 \
  -e WORKOS_E2E_OUTPUT_DIR=/tmp/workos-playwright-results \
  -e WORKOS_CAPTURE_DIR=/workspace/docs/ui/desktop-web/changes/20260831-adaptive-shell/after \
  -v "$PWD":/workspace -w /workspace/apps/desktop-web \
  workos-playwright:1.62.1 pnpm exec playwright test adaptive-visual.spec.ts
docker stop workos-adaptive-preview
```

before/ 由基线 commit `12a53ab` 的 dist 以同一 spec、同一命令采集（该版本 spec 的
挂载断言针对旧 UI，见 git 历史）。

## 安全边界

- 全部内容为确定性合成 fixture（"Fixture Project"、"Adaptive fixture goal"、固定
  UUIDv7 形状的 fixture ID）；无真实凭据、真实用户数据、token、cookie。
- 截图不包含宿主绝对路径；`current/` 已同步 after/ 同名文件。
