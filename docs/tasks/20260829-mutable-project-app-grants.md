# Task: Mutable Project App Grants 与立即撤销纵向切片

- 状态：active
- Owner/Agent：orchestrator（ZCode 主智能体）+ 多个并行实现子智能体
- 进程/模块：workos-core（Project Installation、orchestration AppAgent/Surface resolver）、
  runtime-host（Surface session/App Bridge）、workos-gateway（既有 allowlist 前缀自动公开）、
  Desktop Shell（App Library 权限管理）
- 依赖：`feat/minimal-project-agent-app-bridge`（已合入 main 的 2486ca3 及后续修复）

## 目标与范围

让用户在不卸载 App 的前提下，显式替换某个 Project App installation 的 grant 集合；
每次真实变更产生 installation grant revision 并使旧 Surface 的全部 App Bridge 方法失效；
用户重新打开 Surface 后才按新 grant 建立能力。

包含：

- additive public RPC `SetAppGrants`（full replacement 语义）；
- `AppInstallation.grant_revision`、私有 surface resolver / App Agent 链路的 revision 传递；
- migration `011_mutable_project_app_grants.sql`（owner: workos-core Project Installation）与
  `012_surface_grant_revision.sql`（owner: runtime-host Surface）；
- Core 侧事务线性化（grant + Project revision + event + outbox + idempotency 同事务）；
- runtime Surface session 持久化 grant revision，私有调用携带，Core 每次授权比对；
- Desktop `Manage permissions` dialog（exact pinned version requested set 基准）；
- 集成/撤销/E2E/restart 测试与 UI before/after 视觉证据。

不包含：App Agent approval/quota/budget、自动取消既有 durable task、manifest requested
permissions 变更、App 升降级、新 Bridge executor、container/Web Service Workload、
Reliability、跨 schema SQL/FK 实现撤销、`docs/structure.md` 主线修改。

## 四种事实（语义基准）

| 概念                           | owner                               | 语义                                                     |
| ------------------------------ | ----------------------------------- | -------------------------------------------------------- |
| requested permissions          | Core App Registry immutable version | App 只提出请求，永不自动授权                             |
| current installation grant     | Core Project Installation           | 用户最后一次成功确认的 canonical 完整集合                |
| installation grant revision    | Core Project Installation           | 从 1 开始、仅在 grant 集合真实改变时 +1                  |
| effective Surface capabilities | runtime Surface session             | 创建时 `current grant ∩ implemented bridge methods` 快照 |

## 协议/数据影响

- `api/proto/workos/app/v1/installation.proto`：additive `SetAppGrants` RPC、
  `AppInstallation.grant_revision`；
- `api/proto/workos/surface/v1/surface_resolver.proto`：`ResolveWebBundleResponse` additive
  grant revision；
- `api/proto/workos/agent/v1/app_agent.proto`：私有 Run/Watch 请求 additive session 派生
  revision 字段；
- event `project.app.grants.updated.v1`（sequence = new Project revision）；
- migrations `011`/`012`（单一 process owner；001–010 逐字节不变）；
- idempotency 命名空间沿用 `project_app_installation_requests (owner_user_id, idempotency_key)`，
  digest 扩展 set-grants command，结果快照增加 grant/revision 列。

决策细节见 `docs/decisions/0003-mutable-app-grants.md`。

## 执行计划（子智能体拆分）

| 波次   | 子任务           | 范围                                                                            |
| ------ | ---------------- | ------------------------------------------------------------------------------- |
| Wave 0 | orchestrator     | 分支、基线门禁、任务记录、ADR、before/ 基线                                     |
| Wave 1 | S1 契约与存储    | Proto additive、migration 011/012、sqlc queries、`make generate`、checksum 测试 |
| Wave 2 | A Core 纵向      | `internal/core/project` SetAppGrants 全链路 + orchestration revision 二次授权   |
| Wave 2 | B Runtime 纵向   | `internal/runtime/surface` revision 持久化、私有传递、public bridge 失效        |
| Wave 2 | C Desktop        | AppLibrary `Manage permissions` dialog + unit 测试                              |
| Wave 3 | D 测试门禁       | 集成/撤销/E2E/restart/race 全量验证与修复                                       |
| Wave 4 | E 文档与视觉证据 | after 截图、current/ 更新、文档同步、交接记录                                   |

## 验收

- [ ] 行为测试（domain/application/postgres/transport/runtime/desktop/E2E）
- [ ] `make bootstrap` / `make generate`（二次无差异）/ `make check`
- [ ] `buf breaking --against '.git#branch=main'`
- [ ] `make test-integration` ×2（资源计数与 scratch DB 卫生）
- [ ] `make test-deepseek-fixture`（仅仓库假凭据）
- [ ] `make test-e2e`
- [ ] `go test -race ./internal/core/project/... ./internal/core/orchestration/... ./internal/runtime/...`
- [ ] migrations 001–010 逐字节不变；011/012 checksum 钉住
- [ ] UI before/after/current/notes；`git diff --check` 干净
- [ ] 文档与 `docs/status.json` 同步

## 交接

（完成后填写：branch/HEAD/base、Proto 与 migration 说明、验证命令与真实结果、
授权矩阵、资源计数、视觉记录路径、secret/生成物审计、未决风险与下一步。）
