# P0 原生窗口控制租约续期

- 状态：in_progress
- Owner：Surface continuity / Desktop Web；分支 `feat/p0-surface-renew`，worktree `/home/aquatao/workos-p0-renew`，基线 `main@62963d2`。
- 范围：新增 Surface-owned、只允许当前 attachment 和 control generation 续期的 RPC；原生窗口持有控制时主动续租，失控后停止输入并提供显式接管。
- 依赖：现有 Surface attachment/lease、Gateway 注入的 owner/device 身份、Greenfield Window 输入链路。先合入 Proto 和 Runtime producer，再合入 Desktop consumer。
- 验收：续期只延长同一代次当前持有者的到期时间；旧 attachment、旧代次、过期租约、设备接管、Detach 与 Stop 均不能续期；客户端在续期失败或输入拒绝后停止提交输入，不自动调用接管，用户可通过可见操作恢复；旧 Xvfb 路线继续工作。
- Producer 验证：`make generate`（使用临时复制的 GNU make，仍执行仓库原目标）与 `make proto-check` 通过；Go 1.26.7 容器运行 `go test ./internal/runtime/surface/application ./internal/runtime/surface/adapters/postgres ./internal/runtime/surface/transport ./cmd/runtime-host` 通过；隔离 `pgvector/pgvector:pg18` 数据库上运行 `TestContinuityStore*` 三项真实 PostgreSQL 测试通过。新增 AttachSurface 会话 generation 投影回归，避免首次创建时浏览器取得 0 代。
- Consumer 验证：`pnpm --filter @workos/desktop-web check` 通过（38 个文件、247 个测试）；固定合成双窗口 fixture 的 `greenfield-viewer-visual.spec.ts` 在 Chromium 通过（2 个场景）；`git diff --check` 通过。`before/` 取自基线 `62963d2` 的同一路由和同一 fixture，`after/` 取自本分支，两者均为 1440×900。见 [before](../ui/desktop-web/changes/20260926-surface-control-renewal/before/greenfield-resident-windows--control-lost--1440x900.png)、[after](../ui/desktop-web/changes/20260926-surface-control-renewal/after/greenfield-resident-windows--control-lost--1440x900.png) 与 [采集说明](../ui/desktop-web/changes/20260926-surface-control-renewal/notes.md)。
- 全仓检查：本独立 worktree 中运行 `make check` 时，Proto、sqlc vet、`go vet ./...` 和 `go test ./...` 已通过；随后 Web 阶段的非 root Node 容器因 pnpm SQLite store `attempt to write a readonly database` 停止。这是本 worktree 的 Docker UID/cache 权限问题，未得到完整 `make check` PASS；主线合并后需重跑。
- 风险和下一步：真实 HTTPS/LAN 和 WorkOS Code 控制端的长时间续租仍需集成门禁实测；短时 fixture 只证明浏览器状态传播、明确的按钮接管和 Surface 服务端的隔离数据库事务。
