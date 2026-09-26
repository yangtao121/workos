# P0 原生窗口控制租约续期

- 状态：in_progress
- Owner：Surface continuity / Desktop Web；分支 `feat/p0-surface-renew`，worktree `/home/aquatao/workos-p0-renew`，基线 `main@62963d2`。
- 范围：新增 Surface-owned、只允许当前 attachment 和 control generation 续期的 RPC；原生窗口持有控制时主动续租，失控后停止输入并提供显式接管。
- 依赖：现有 Surface attachment/lease、Gateway 注入的 owner/device 身份、Greenfield Window 输入链路。先合入 Proto 和 Runtime producer，再合入 Desktop consumer。
- 验收：续期只延长同一代次当前持有者的到期时间；旧 attachment、旧代次、过期租约、设备接管、Detach 与 Stop 均不能续期；客户端在续期失败或输入拒绝后停止提交输入，不自动调用接管，用户可通过可见操作恢复；旧 Xvfb 路线继续工作。
- Producer 验证：`make generate`（使用临时复制的 GNU make，仍执行仓库原目标）与 `make proto-check` 通过；Go 1.26.7 容器运行 `go test ./internal/runtime/surface/application ./internal/runtime/surface/adapters/postgres ./internal/runtime/surface/transport ./cmd/runtime-host` 通过；隔离 `pgvector/pgvector:pg18` 数据库上运行 `TestContinuityStore*` 三项真实 PostgreSQL 测试通过。新增 AttachSurface 会话 generation 投影回归，避免首次创建时浏览器取得 0 代。
- 风险和下一步：待记录。
