# 共享桌面 Core 与 Gateway（2026-09-22）

状态：done（后端及总任务跨进程自动验收通过）；分支 `feat/shared-desktop-core`；依赖已合并契约 bca25f8 / ADR-0037。

范围：Core 桌面领域/应用/端口/PostgreSQL/Connect，076 迁移，Core 装配，Gateway allowlist 与持续流设备复核，后端测试和模块文档。不得修改其他模块数据表或 UI；整体状态与 E2E 由总任务集成。

验收：owner 隔离、引用所属模块授权、UUIDv7/有界输入、原子并发命令、持久幂等/游标/重启、关闭后迟到聚焦不复活、失效引用清理、Gateway 私有路由隔离与撤权。运行 Go 单元测试和隔离真实 PostgreSQL 测试，不改共享开发栈。

本任务后端验证及总任务20项Chromium/WebKit门禁、实际Core/Gateway重启持久性均通过；物理设备另列待验收。

## 实现与验证

- 新增 Core Desktop 分层模块、076 三张独占表和 Core handler 装配；每 owner 行锁、幂等摘要、revision 事件和引用清理原子提交。
- 同 key 重放返回当前投影；旧窗口 focus/select 不复活；项目切换保留其他项目窗口；初始化过滤失效引用。
- 所属模块 application port 与 Runtime canonical 精确实例 RPC 校验引用；中断/停止实例保留。读取期间依赖不可用不误删引用，新引用仍失败。
- 可恢复有界 Watch、心跳和游标 reset；Gateway 对新 Desktop/Session 长流持续检查设备身份，私有路由不开放。
- 模块行为、上限、兼容及授权异常语义见 [共享桌面](../architecture/shared-desktop.md)。安装应用、终端和 Native pin 持久化 workload UUID/generation；迟到旧代次不能覆盖同实例新代次。

验证使用固定 `golang:1.26.7-bookworm`，挂载当前工作树和既有 Go 缓存，以宿主 UID/GID 运行：

```sh
go vet ./internal/core/desktop/... ./internal/core/orchestration ./internal/gateway ./cmd/workos-core
go test -race ./internal/core/desktop/... ./internal/core/orchestration ./internal/gateway ./cmd/workos-core
WORKOS_DESKTOP_TEST_DATABASE_URL=<isolated-postgresql-url> go test -tags=integration -race -count=1 -v ./internal/core/desktop/adapters/postgres
```

隔离 PostgreSQL 使用 `pgvector/pgvector:pg18`，容器 `workos-shared-desktop-core-pg`，本机 16479；每项测试创建自己的随机数据库、执行全部迁移、结束后删除自身数据库。合成 fixture 密码只用于临时容器，不使用真实凭据。真实数据库已证明：20 个相同 key 并发只生成一份窗口/事件；20 个不同资源并发无丢失；新连接池及应用实例读取持久幂等；失败事务不消费 key；foreign owner/project 拒绝及实际 Project archive 后授权清理。该重建应用测试不冒充真实 Core 进程重启。

单元及 Connect 测试覆盖输入/消息大小上限、app 与交互程序代次 pin、窗口去重、会话选择、停止实例引用、断流补齐、未来/过期 cursor reset、撤权历史不泄漏、流数量及断开清理。Gateway 测试通过实际上游流取消证明新持久流进行中设备撤销生效。

## 交接边界

后端实现与以上验证完成后仍按 scaffolded 交接；跨进程、客户端镜像及真机体验由 [总任务](20260922-shared-desktop.md) 统一验收并更新 `docs/status.json`。未修改共享开发栈、未调用付费 Provider、未改 UI 或生成协议。总任务合并后运行统一 `make generate`/`make check`；本分支只消费主线已合并的协议提交。

最终结果：以上 Go vet、race 单元/Connect/Gateway 回归、两个 scratch PostgreSQL 集成测试全部通过；`sqlc vet` 与文档 Prettier 检查通过。运行日志为工作树忽略目录 `tmp/shared-desktop-evidence/core/shared-desktop-core-check.log`。未执行共享栈破坏性操作。

整合复核修正了 domain 测试对 platform/ids 的引用，测试现使用纯 UUID 库，完整架构检查通过。
