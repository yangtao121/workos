# notes — 20260830-project-artifact-review

## 任务

- 任务记录：`docs/tasks/20260830-project-artifact-review.md`；ADR：
  `docs/decisions/0008-project-agent-artifact-review.md`。
- 受影响界面：task composer 新增 "Markdown document"/"Unified diff" 两个 artifact 请求
  复选框；timeline 的 Core-minted `ArtifactCreated` 事件从纯文本变为可点击按钮（"Open
  review"，仍经 ArtifactService 取权威内容）；dock 新增 ☰ "Open Artifact Center" 普通窗口
  （分页列表、loading/empty/unavailable/retry 状态、key/remount 于 active Project）；
  新增 "Artifact Review" 只读 viewer 窗口（inert Markdown / unified-diff renderer，同
  artifact 重复打开聚焦既有窗口；Project 切换关闭跨 Project 的 viewer）。Desktop 其余像素
  与交互不变。

## 采集环境与口径

- 浏览器/视口：Playwright Chromium，1440×900，deviceScaleFactor 1，locale `en-US`，
  timezone `UTC`。
- before/after 两轮截图全部使用固定 network fixture：导航前以 Playwright route 固定
  DeviceService/ProjectService/HarnessCatalog/AgentTaskService（SubmitTask、WatchTaskEvents
  流含 Connect EndStream 帧、GetTask）与 ArtifactService（GetReviewArtifact/ListArtifacts）
  响应；artifact ID/title/timestamp（固定 2026-08-30 09:00 UTC）/content 全部确定，无随机
  UUID、无 wall-clock、无 cursor、无 session facts。Markdown/Diff 内容与 Fake Harness 真实
  输出逐字节一致（`internal/harness/adapters/fake/provider.go`）。
- 真实链路由独立门禁证明，不用截图冒充：`make test-artifact-review`（真实 PostgreSQL +
  Core + harness-host + Gateway + Chromium，走 Task Router → Fake 输出 → 私有
  AppendTaskArtifact → Artifact PostgreSQL facts → 公开 ArtifactService → viewer）。

## 采集命令

```sh
# after/（当前分支构建，vite preview + 固定 route fixture）
corepack pnpm --filter @workos/desktop-web build
docker run --rm --network host … workos-playwright:1.62.1 \
  pnpm exec playwright test artifact-review-visual.spec.ts
# before/（任务基准 d80320c worktree，同一套 fixture，适配旧 UI：无复选框/无 artifact 事件/无 Center）
```

- `make test-artifact-review`：真实链路门禁（与截图分开命名、分开说明）。

## 文件

### before/

- `agent-center--task-run-baseline--1440x900.png`：任务基准 d80320c 的旧 UI——timeline 事件
  为纯文本、composer 无 artifact 复选框、dock 无 Artifact Center 按钮。
- `agent-center--{approval-pending,approval-decided,usage-quota}--1440x900.png`：从任务开始时
  的 `docs/ui/desktop-web/current/` 复制的基线（docs/ui/README.md 约定）。
- `artifact-viewer--*`、`artifact-center--*` 无 before：这些界面在本任务前不存在。

### after/（同时已更新 `docs/ui/desktop-web/current/`）

- `agent-center--artifact-created--1440x900.png`：timeline 两个 Core-minted artifact 事件
  （03 markdown / 04 diff）为可点击 "Open review" 按钮；composer 两个请求复选框。
- `artifact-viewer--markdown-review--1440x900.png`：只读 Markdown 审阅窗口（标题/段落/列表/
  引用/围栏代码，全部转义 inert 文本）。
- `artifact-viewer--unified-diff-review--1440x900.png`：只读 unified diff 审阅窗口（file
  header、hunk header、增删行配色，全部转义 inert 文本）。
- `artifact-center--project-list--1440x900.png`：Artifact Center 普通窗口列出当前 Project
  的两个 review artifact（title/type/固定 created time）。

## 有意差异

- before/ 的 timeline 截图没有 artifact 事件行（该能力尚不存在）；after/ 的 timeline 多出
  两个 artifact 行与两个复选框，即本切片的用户可见变更。
- 截图中 provider snapshot/task UUID 均为 `01990000-…` 固定 fixture 值；真实链路的动态 UUID
  只出现在 `make test-artifact-review` 的运行断言里，不进入像素证据。
