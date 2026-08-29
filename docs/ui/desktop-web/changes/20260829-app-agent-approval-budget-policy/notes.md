# Task: 20260829 App Agent 持久预算策略与运行前审批

- 任务记录：`docs/tasks/20260829-app-agent-approval-budget-policy.md`
- 受影响 client：`desktop-web`
- 受影响界面：App Library（已安装行新增 `Agent policy:` 摘要与 `Agent policy`
  按钮、新增 Agent policy 编辑对话框）、Agent Center 窗口（新增
  Tasks / Approvals / Usage 三个内嵌视图标签；Approvals 支持 owner
  Approve/Reject，Usage 展示 reserved allowance 与 reported usage）、App
  窗口默认打开位置调整（左侧 launch 区，不再遮盖 Agent Center）、窗口按
  管理器 rect 定位且头部可拖拽
- 决策：`docs/decisions/0005-app-agent-approval-budget-policy.md`
- 基准提交：before/ 采集自分支 base `4159789`（main，无 policy 入口、
  Agent Center 仅 task composer/timeline、App 窗口默认遮盖 Agent Center）；
  after/ 采集自本分支实现工作树（`feat/app-agent-approval-budget-policy`）。

## 截图清单

`before/`（基线提交运行栈，3 张）：

- `app-library--installed-granted--1440x900.png`：已安装行——按钮只有
  Open / Manage permissions / Remove，行内无 policy 摘要。
- `app-library--manage-permissions--1440x900.png`：Manage permissions
  对话框（grant 管理，本任务未改动其行为）。
- `agent-center--tasks--1440x900.png`：Agent Center 窗口仅有 task
  composer + snapshot + timeline，无 Approvals/Usage 视图。

`after/`（实现 HEAD 运行栈，7 张）：

- `app-library--agent-policy-default--1440x900.png`：已安装行显示
  `Agent policy: system default (allow)`，按钮含 `Agent policy`。
- `app-library--agent-policy-editor--1440x900.png`：Agent policy 编辑
  对话框——Allow / Require approval / Block 三选一，4 个限额输入
  （4096 / 120 / 50 / 204800），`Current: system default (allow)`，
  明示 policy 不替代 permissions。
- `app-library--installed-granted--1440x900.png`：新基线的已安装行
  （同 before 状态在新 UI 下的样子，含 policy 摘要行与按钮）。
- `agent-center--approval-pending--1440x900.png`：Agent Center
  Approvals 视图——pending 审批项显示 app、provider、budget（256
  output tokens / 60s / policy revision 1）与有界 goal 摘要，附
  Approve / Reject。
- `agent-center--approval-decided--1440x900.png`：批准后状态——
  成功反馈 "Task approved and queued…"、Recent decisions 出现
  approved 记录，同一 App 窗口内同一 task 已 terminal（fake harness
  摘要），证明无需重新握手。
- `agent-center--usage-quota--1440x900.png`：Usage 视图——Reserved
  （1 tasks · 4096 output tokens）与 Reported（1 tasks · 22 in / 4 out）
  分列，Cost 明示 unavailable（不是 0），Circuit normal，注明 UTC
  reset 语义。
- `agent-center--quota-exhausted--1440x900.png`：第二个 waiting task
  批准被服务端拒绝——错误消息 "Approving would exceed the app's daily
  allowance. Raise the policy or wait for the UTC reset."（ResourceExhausted
  的净化文案），approval 保持 pending。

`current/`：已用 after/ 同名文件替换，并新增 6 张新状态（共 15 张），
代表包含本任务的当前基线。

## 采集条件

- 浏览器：Chromium（`workos-playwright:1.62.1` 镜像内置 Playwright
  Chromium）。
- Viewport：`1440x900`、`deviceScaleFactor: 1`。
- 运行栈：`docker compose up -d --build postgres bootstrap workos-core
harness-host runtime-host workos-gateway`（实现 HEAD 构建，
  `WORKOS_AGENT_DEFAULT_PROVIDER=fake`）。
- Fixture：确定性合成 App——两文件 Web Bundle（index.html + app.js），
  app.js 内联实现 `workos.app-bridge/v1` 协议的 iframe 侧（与 e2e spec
  相同），request `agent.task.run` + `agent.event.watch`。Project/App
  ID 为 run-unique 时间戳（持久验收 volume 资源卫生）。policy 数值
  （require approval、每任务 256→4096 token、每日 1 task/4096 token）
  为测试有意设置，用于让 quota-exhausted 状态可确定性到达。
- 采集命令（仓库根目录；驱动脚本为一次性临时 Playwright 脚本，采集时
  放在 git 忽略的 `tmp/` 下、采集完成后移出仓库树，不入库）：

  ```sh
  docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
  docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    -e WORKOS_E2E_URL=http://127.0.0.1:8080 \
    -e OUT_DIR=/workspace/docs/ui/desktop-web/changes/20260829-app-agent-approval-budget-policy/after \
    -v "$PWD":/workspace -w /workspace \
    workos-playwright:1.62.1 node tmp/capture-policy.mjs
  ```

  （`app-library--installed-granted` 由同模式的 `tmp/capture-installed.mjs`
  采集。）

- 每张截图前的门控断言（等待精确文案/状态出现再截）：已安装行
  `Agent policy: system default (allow)`；对话框 `Current: system default`
  与 `Agent policy saved.`；App 窗口 `bridge-ready` /
  `waiting:approval-required` / `terminal:Task … completed by fake
harness`；Approvals 视图 pending goal 摘要与 `Task approved and
queued…`；Usage 视图 `1 tasks · 4096 output tokens`；quota-exhausted
  状态等待 `Approving would exceed the app's daily allowance`。

## 与 before 的有意差异

- 已安装行新增 `Agent policy:` 摘要与 `Agent policy` 按钮（功能本体）。
- Agent Center 窗口新增 Tasks / Approvals / Usage 三个视图标签；Tasks
  视图内容与 before 的 composer/timeline 相同。
- App 窗口默认打开位置从（遮挡 Agent Center 的）中央改为左侧 launch
  区上方，窗口几何改由窗口管理器 rect 驱动且头部可拖拽——使审批期间
  Agent Center 可达（结构文档 1.5 的窗口管理器行为）。
- quota-exhausted / approval 系列（4 张）为本任务新增的用户可见界面，
  before 无对应。
- 所有截图中的时间戳、UUID 均为 run-unique fixture 值，非用户数据。

## 安全与内容边界

- 仅使用 fixture/seed 合成数据；无真实凭据、token、cookie、API key 或
  真实用户内容。bridge token 只存在于可信 Desktop 内存与 Connect
  metadata，未进入 DOM/URL/MessageChannel（E2E 有显式断言）。goal
  摘要为 fixture 文本。单张截图均 < 2 MiB。
