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
