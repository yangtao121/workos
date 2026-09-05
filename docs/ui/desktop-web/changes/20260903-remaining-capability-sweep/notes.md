# notes: 2026-09-03 remaining capability sweep — W1 provider catalog & harness settings

任务文件：`docs/tasks/20260903-v1-remaining-capability-sweep.md`（W1）
实现依据：ADR-0015（`docs/decisions/0015-provider-expansion-and-vault-rotation.md`）

## 受影响界面

- Project settings → Harness settings（provider 单选列表 + capability 徽标列表）。
- 变更：catalog 出现 `Codex Harness`（codex）与 `MCP Server Harness`（mcp）两个新
  provider 行；capability 列表 additive 增加 "Lease purpose" 条目，显示该 provider
  消费的精确凭据种类（如 `codex-auth.v1`）或 `none`。未改动任何布局/交互结构。

## 采集

- viewport：1440x900（expanded 桌面）；Chromium（仓库 Playwright E2E 镜像）。
- fixture：确定性 fixture 数据（项目名含时间戳仅用于避免幂等键冲突，不在断言中）。
- 命令：`make capture-provider-catalog`
  - before 帧：`WORKOS_PROVIDERS_EXPANDED=false`（harness-host 未启用 codex/mcp
    adapter，catalog 只含既有 provider），采集
    `harness-settings--provider-catalog-baseline--1440x900.png`
  - after 帧：`WORKOS_CODEX_ENABLED=true WORKOS_MCP_ENABLED=true`，采集
    `harness-settings--provider-catalog-expanded--1440x900.png`
- before/ 说明：本 surface 此前没有 current/ 基线截图；采用"同一 UI 代码、
  adapter 未启用"的运行帧作为 before，直观呈现本次能力扩展带来的 provider 行
  差异；"Lease purpose" 徽标行本身属于本次 UI 代码变更（两帧均含）。
- current/：`harness-settings--provider-catalog--1440x900.png`（= after 帧）。

## 验证

- `make test-codex-harness` PASS（含 Chromium 绑定/运行旅程）
- `make test-mcp-harness` PASS
- `make test-credential-vault-expansion` PASS
- `corepack pnpm exec vitest run src/HarnessSettings.test.tsx`：6 passed

# notes: W6 desktop system apps & entries（追加）

任务文件：`docs/tasks/20260903-v1-remaining-capability-sweep.md`（W6）

## 受影响界面

- Command Palette（⌘K/Ctrl+K）：固定动作集（切换项目/打开系统应用/聚焦 Agent），
  有界结果 ≤8，键盘导航，失效目标固定 stale 文案；expanded 与 adaptive（compact）
  布局均可达。
- Project Mission Control：项目卡片（revision/未读/active）+ 新建 Project 表单。
- Home 启动台：系统应用入口网格；Terminal 明确标注 unavailable（依赖 Native Runner）。
- Files：对已索引 workspace 文件的有界查询视图（消费 W4 hybrid 检索，
  workspace.file.v1 来源过滤）。
- Docs/Code：项目 markdown 文档 / 只读 diff 列表（消费既有 artifact 服务）。
- Browser：WorkOS 窗口内沙箱 iframe（sandbox="allow-scripts allow-forms
  allow-same-origin"，无 allow-popups/allow-top-navigation 即 \_blank/top 拦截边界），
  仅接受 http(s) URL。
- 窗口管理：标题栏新增 snap left/right（半屏几何，restore 保留原 normal 矩形）。

## 采集

- viewport：1440x900（expanded）与 390x844（compact Palette 可达性）；Chromium
  （仓库 Playwright E2E 镜像）；fixture 为确定性 E2E 旅程（项目名含时间戳仅避免冲突）。
- 命令：`make capture-desktop-system-apps`，输出至本目录 after/：
  - `mission-control--projects--1440x900.png`
  - `command-palette--open--1440x900.png`
  - `home--launchpad--1440x900.png`
  - `browser--sandboxed--1440x900.png`
  - `desktop-system-apps--snap-left--1440x900.png`
  - `command-palette--open--390x844.png`
- current/：以上 6 张同步更新（本 surface 均为新增界面，无既有基线；before/ 不适用）。

## 验证

- `make test-desktop-system-apps` PASS（5 个 E2E 场景）
- `corepack pnpm --filter @workos/desktop-web test`：120 passed
- `corepack pnpm --filter @workos/window-manager test`：7 passed（snap 几何）
