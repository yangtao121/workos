# Task: 20260829 Mutable Project App Grants

- 任务记录：`docs/tasks/20260829-mutable-project-app-grants.md`
- 受影响 client：`desktop-web`
- 受影响界面：App Library（安装确认文案、已安装行的 grant revision 与
  `Manage permissions` 按钮、Manage permissions 对话框）、App window
  （撤销后按新 epoch 重新打开的 bridge 任务结果）
- 决策：`docs/decisions/0003-mutable-app-grants.md`（局部替代 ADR-0002 §3 的
  安装级不可变 grant）
- 基准提交：before/ 采集自分支 base `afa05d2`（main，安装级不可变 grant、
  无 Manage permissions、consent 文案为 “changing this later requires removing
  and reinstalling the app”）；after/ 采集自实现 HEAD `2af3736`
  （`feat/mutable-project-app-grants`，工作树干净）。

## 截图清单

`before/`（基准提交运行栈，4 张，沿用上一任务的采集约定）：

- `app-library--consent--1440x900.png`：Install 确认对话框——2 项 requested
  permissions 全部未选，文案明确“更改需卸载重装”。
- `app-library--consent-selected--1440x900.png`：勾选两项后按钮变为
  “Install with 2 permissions”。
- `app-library--installed-granted--1440x900.png`：安装完成行显示
  `Granted: agent.event.watch, agent.task.run`，按钮只有 Open / Remove
  （无 grant revision、无 Manage permissions）。
- `app-surface--bridge-result--1440x900.png`：App window 内 bridge 握手后
  真实任务 terminal 结果。

`after/`（同一栈重建于实现 HEAD，8 张）：

- `app-library--consent--1440x900.png`：更新后的 consent 文案——“Nothing is
  granted by default; you can change these later from Manage permissions.”
  （替换过时的“requires removing and reinstalling”），全部 checkbox 仍未选。
- `app-library--consent-selected--1440x900.png`：勾选两项 bridge 权限后按钮
  “Install with 2 permissions”（与 before 同一选择状态）。
- `app-library--installed-granted--1440x900.png`：已安装行显示
  `Granted: agent.event.watch, agent.task.run · grant revision 1`，按钮
  Open / **Manage permissions** / Remove。
- `app-library--manage-permissions--1440x900.png`（新）：Manage permissions
  对话框初始态——checkbox 以 current grant 为初值（两项已选、artifact.read
  未选，绝非默认全选），显示 `Current grant revision 1` 与
  “No changes to save.”。
- `app-library--manage-permissions-diff--1440x900.png`（新）：修改后的确认
  状态——`Adding artifact.read`（新增）与 `Removing agent.task.run`（移除）
  的完整 diff，Save 按钮为 “Save permissions”。
- `app-library--manage-permissions-revoke-all--1440x900.png`（新）：全部取消
  勾选的撤销确认状态——提示 “Saving with nothing selected revokes every
  permission.”，按钮变为 “Revoke all permissions”，diff 列出两项 Removing。
- `app-library--manage-permissions-saved--1440x900.png`（新）：保存成功状态
  ——提示 “Permissions saved. Reopen the app for the new permissions to take
  effect.”；对话框后的行显示 `Granted: none · grant revision 2`（撤销全部后
  grant epoch +1）。
- `app-surface--bridge-result--1440x900.png`：撤销 → 重新授予两项（grant
  revision 3）→ 重新 Open 后的 App window：bridge-ready 握手与
  `terminal:Task … completed by fake harness` —— 新 epoch 的能力经真实
  Gateway → runtime-host → Core → Fake Harness 链路重新生效。

`current/`：已用 `after/` 的同名文件替换并新增新文件（共 8 张），代表包含
本任务的当前基线。

## 采集条件

- 浏览器：Chromium（`workos-playwright:1.62.1` 镜像内置 Playwright Chromium）。
- Viewport：`1440x900`、`deviceScaleFactor: 1`。
- 运行栈：`docker compose up -d --build postgres bootstrap workos-core
harness-host runtime-host workos-gateway`（实现 HEAD 构建的镜像，经检查
  所服务的 desktop-web bundle 只包含新文案）。
- Fixture：确定性合成 App——两文件 Web Bundle（index.html + app.js），app.js
  内联实现 `workos.app-bridge/v1` 协议的 iframe 侧（hello/ack nonce、
  agent.run、agent.stream），与 e2e/spec 相同的 bridge fixture；manifest 在
  e2e 两个 bridge 权限之外额外 request 一项仅存储的 `artifact.read`，使同一
  张 diff 截图能同时呈现 Adding 与 Removing。Project/App ID 为 run-unique
  时间戳（持久验收 volume 资源卫生），可见名称沿用 before 的
  “Fixture Surface App” / “Fixture Space …”。
- 采集命令（仓库根目录；驱动脚本为一次性临时 Playwright 脚本，采集时放在 git
  忽略的 `tmp/` 下、采集完成后已移出仓库树，不入库——上文的 fixture 与交互
  步骤即其全部逻辑）：

  ```sh
  docker compose up -d --build postgres bootstrap workos-core harness-host runtime-host workos-gateway
  docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    -e WORKOS_E2E_URL=http://127.0.0.1:8080 \
    -e NODE_PATH=/usr/local/lib/node_modules \
    -e OUT_DIR=/workspace/docs/ui/desktop-web/changes/20260829-mutable-project-app-grants/after \
    -v "$PWD":/workspace -w /workspace \
    workos-playwright:1.62.1 node tmp/capture-grants.mjs
  ```

- 每张截图的交互步骤（单页单 fixture 的连续状态机）：
  1. 注册 fixture app → 创建 `Fixture Space <stamp>` Project → 打开 App Library；
  2. 点击 Install → consent 对话框（截图 consent）→ 勾选 agent.task.run 与
     agent.event.watch（截图 consent-selected）→ “Install with 2 permissions”；
  3. 行显示 Granted + `grant revision 1`（截图 installed-granted）；
  4. 点击 `Manage permissions` → 对话框以 current grant 初始化
     （截图 manage-permissions）；
  5. 取消 agent.task.run、勾选 artifact.read → 出现 Adding/Removing diff
     （截图 manage-permissions-diff）；
  6. 再取消 agent.event.watch 与 artifact.read → 全不选
     （截图 manage-permissions-revoke-all）；
  7. 点击 “Revoke all permissions” → 保存成功提示 + 行变为
     `Granted: none · grant revision 2`（截图 manage-permissions-saved）；
  8. Close 后重新授予两项（grant revision 3）→ 重新 Open → bridge-ready →
     点击 “Run project task” 至 terminal（截图 app-surface--bridge-result）。
- 每次截图前脚本都以精确文案/状态断言（等待 “you can change these later
  from Manage permissions”、“Current grant revision 1”、“No changes to
  save.”、“Adding artifact.read”、“Removing agent.task.run”、“Saving with
  nothing selected revokes every permission.”、“Permissions saved. Reopen the
  app …”、“Granted: none”/`grant revision 2`/`grant revision 3`、iframe
  `terminal:`）门控，保证截图时 UI 处于所述状态。

## 与 before 的有意差异

- consent/consent-selected：描述文案第二句更新（移除“必须卸载重装”），且
  fixture 多 request 一项 `artifact.read`（第三个 checkbox，未选）——用于让
  diff 截图同时呈现 Adding 与 Removing；按钮行为与选择状态与 before 一致。
- installed-granted：行尾新增 `grant revision 1`，按钮区新增
  `Manage permissions`。
- manage-permissions 系列（4 张）：本任务新增的用户可见界面，before 无对应。
- app-surface--bridge-result：布局与内容与 before 相同；本张采集自
  撤销 → 重新授予 → 重新打开后的新 grant epoch（行内 grant revision 3），
  用于证明旧 Surface 失效后重新打开即恢复能力。
- 所有截图的 Project 名称尾部时间戳与 bridge-result 内 task UUID 为
  run-unique 值（与上一任务的视觉记录惯例一致），非用户数据。

## 安全与内容边界

- 仅使用 fixture/seed 合成数据；无真实凭据、token、cookie、API key 或真实
  用户内容。bridge token 只存在于可信 Desktop 内存与 Connect metadata，
  从未进入 DOM/URL/MessageChannel（e2e 有显式断言）。单张截图均 < 2 MiB。
