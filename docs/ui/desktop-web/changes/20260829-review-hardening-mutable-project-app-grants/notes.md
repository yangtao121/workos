# Task: 20260829 Review Hardening — Mutable Project App Grants

- 任务记录：`docs/tasks/20260829-review-hardening-mutable-project-app-grants.md`
- 受影响 client：`desktop-web`
- 受影响界面：App Library（保存成功但保存后 facts 重读失败时，已安装行与
  Project revision 的同步行为）
- 基准提交：before/ 采集自本任务 HEAD `7e5fa46`（修复前的前端行为——
  `git stash` 暂存 4 个前端修复文件后重建 workos 镜像）；after/ 采集自
  包含修复的工作树（同一栈重建）。
- 正常路径（重读成功）的像素与上一任务
  `20260829-mutable-project-app-grants` 完全一致，本目录只记录故障注入
  路径的新行为证据。

## 截图清单

- `before/app-library--saved-reread-failed--1440x900.png`：SetAppGrants
  已被服务端确认（对话框出现 "Permissions saved…" 后已 Close），但保存后
  的 GetProject/ListInstalledApps 重读被注入失败——旧行为下 App Library
  行停留在 `Granted: agent.event.watch, agent.task.run · grant revision 1`，
  头部停留 `revision 2`（服务端事实已是空 grant / grant revision 2 /
  revision 3）。
- `after/app-library--saved-reread-failed--1440x900.png`：同一 fixture、
  同一故障注入、同一交互序列——行立即采纳 Set 响应显示
  `Granted: none · grant revision 2`，头部显示 `revision 3`。

`current/`：新增 `app-library--saved-reread-failed--1440x900.png`（取自
after/），其余 8 张与上一任务一致（正常路径像素无差异）。

## 采集条件

- 浏览器：Chromium（`workos-playwright:1.62.1` 镜像内置）。
- Viewport：`1440x900`、`deviceScaleFactor: 1`。
- 运行栈：`docker compose up -d --build postgres bootstrap workos-core
harness-host runtime-host workos-gateway`（采集 before 时以 stash 暂存
  前端修复后仅重建 `workos-gateway`，after 反之；后端镜像两次一致）。
- Fixture：确定性合成 App——两文件 Web Bundle（index.html + app.js 静态
  文案），manifest request `agent.task.run`、`agent.event.watch` 与仅存储
  的 `artifact.read`；安装时授予前两项。Project/App ID 为 run-unique
  时间戳（持久验收 volume 资源卫生），可见名称 "Fixture Surface App" /
  "Fixture Space …"。
- 故障注入：对话框打开并加载完 fresh facts（`Current grant revision 1`
  可见）之后，Playwright `context.route` 将
  `**/workos.project.v1.ProjectService/GetProject` 与
  `**/workos.app.v1.AppInstallationService/ListInstalledApps` 全部
  `abort("connectionfailed")`；SetAppGrants 与 GetApp 不受影响。因此 Save
  本身经真实 Gateway → Core → PostgreSQL 成功，仅保存后的重读失败。
- 交互步骤：注册 fixture → 建 Project → Install（授予
  agent.task.run + agent.event.watch）→ `Manage permissions` → 全不选
  （"Revoke all permissions"）→ 注入故障 → Save → 等 "Permissions saved…"
  → Close → 截图（含断言门控：`Current grant revision 1`、
  "Saving with nothing selected…"、"Permissions saved…"、dialog hidden）。
- 采集命令（脚本在仓库树外 `/home/aquatao/workos-capture/`，避免 eslint
  扫到未纳入 tsconfig 的 .mjs；沿用上一任务"采集脚本不入库"惯例）：

  ```sh
  docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -e PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    -e WORKOS_E2E_URL=http://127.0.0.1:8080 \
    -e NODE_PATH=/usr/local/lib/node_modules \
    -e OUT_DIR=/workspace/docs/ui/desktop-web/changes/20260829-review-hardening-mutable-project-app-grants/after \
    -v "$PWD":/workspace \
    -v /home/aquatao/workos-capture/capture-reread-fail.mjs:/tmp/capture.mjs:ro \
    -w /workspace workos-playwright:1.62.1 node /tmp/capture.mjs
  ```

  （before/ 采集时 `OUT_DIR` 指向 `before/`，且先
  `git stash push -- apps/desktop-web/src/PermissionDialog.tsx
apps/desktop-web/src/AppLibrary.tsx …` 再 `docker compose up -d --build
workos-gateway`。）

## 有意差异

- before 与 after 使用同一 fixture 形状与同一故障注入；唯一差异是行与
  头部采纳的服务端事实（旧行为停留 pre-save 值，新行为采纳 Set 响应）。
- 两次采集的 Project/App/时间戳为 run-unique，属既定惯例，非用户数据。

## 安全与内容边界

- 仅 fixture/seed 合成数据；无真实凭据、token、cookie、API key 或真实
  用户内容。单张截图 < 2 MiB。
