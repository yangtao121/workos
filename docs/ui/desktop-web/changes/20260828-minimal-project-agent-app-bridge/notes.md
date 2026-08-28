# Task: 20260828 Minimal Project-scoped Agent App Bridge

- 任务记录：`docs/tasks/20260828-minimal-project-agent-app-bridge.md`
- 受影响 client：`desktop-web`
- 受影响界面：App Library（安装确认对话框、已安装行 Granted 摘要）、App window（bridge
  状态与真实 Agent 任务 terminal 结果）
- 基准提交：`2c233ca`（main，App Library 直接安装、无 consent、无 bridge）

## 截图清单

`before/`（采集自基准提交的运行栈，无本任务改动）：

- `app-library--installed--1440x900.png`：App Library，行内 Install 后直接 Installed ·
  pinned 1.0.0（无 permission 确认步骤，不显示 Granted 摘要）。
- `app-surface--ready--1440x900.png`：打开的 Web Bundle window，iframe 内只渲染 bundle
  自身的 `fixture-surface-ready` 文本（无 bridge 状态、无任务结果）。

`after/`（同一栈，包含本任务实现）：

- `app-library--consent--1440x900.png`：点击 Install 后的确认对话框——显示 exact registry
  version 的 requested permissions（agent.task.run、agent.event.watch），checkbox 全部
  默认未选；文案明确“Nothing is granted by default”。
- `app-library--consent-selected--1440x900.png`：用户勾选两项后的状态，确认按钮变为
  “Install with 2 permissions”。
- `app-library--installed-granted--1440x900.png`：安装完成行显示
  `Granted: agent.event.watch, agent.task.run`（与Requested 区分，安装级不可变快照）。
- `app-surface--bridge-result--1440x900.png`：Open 后 App window：iframe 完成版本化
  MessageChannel 握手（bridge-ready），点击 “Run project task” 后显示
  `terminal:Task 01a04a15-…-47b5655b6a0f completed by fake harness` —— 唯一 terminal 文本
  中的 task ID 由 Core 经 AppBridge → AppAgent → Task Router → Fake Harness 真实链路铸造，
  事件经持久事件流通过 MessagePort 流回 iframe。

`current/`：已用 `after/` 的同名文件更新，代表包含本任务的当前基线。

## 采集条件

- 浏览器：Chromium（`workos-playwright:1.62.1` 镜像内置 Playwright Chromium）。
- Viewport：`1440x900`、`deviceScaleFactor: 1`。
- Fixture：确定性合成 App——两文件 Web Bundle（index.html + app.js），app.js 内联实现
  `workos.app-bridge/v1` 协议的 iframe 侧（hello/ack nonce、agent.run、agent.stream），
  无真实数据、无凭据；Project/ID 为 run-unique 时间戳（持久验收 volume 资源卫生）。
- 采集命令（仓库根目录）：

  ```sh
  docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    -e WORKOS_E2E_URL=http://127.0.0.1:8080 \
    -e NODE_PATH=/usr/local/lib/node_modules \
    -e MODE=before -e OUT_DIR=/workspace/docs/ui/desktop-web/changes/20260828-minimal-project-agent-app-bridge/before \
    -v "$PWD":/workspace -w /workspace \
    workos-playwright:1.62.1 node tools/ui/capture-desktop.mjs
  ```

  `after/` 使用相同命令、`MODE=after`、`OUT_DIR=…/after`。`before/` 在任务基准代码构建的
  compose 栈上采集；`after/` 在含本任务实现的重建栈上采集。驱动脚本
  `tools/ui/capture-desktop.mjs` 已入库，两个模式共用同一 fixture 生成逻辑。

- 安全边界：截图不包含 token、Provider ID、凭据或真实用户数据；bridge token 只存在于
  可信 Desktop 内存与 Connect metadata，从未进入 DOM、URL 或 MessageChannel payload
  （由 E2E 与集成断言覆盖）。
