# 2026-08-29 · Mutable Project App Grants 审核修复波次

## 背景

`docs/prompts/20260829-next-agent-mutable-project-app-grants.md` 主体任务已在
branch `feat/mutable-project-app-grants` 完成并通过门禁（generate/check/race/
E2E）。人工审核发现 4 个未被既有测试覆盖的缺陷，本任务一次修复并补齐证据。

基线提交：`7e5fa46 docs: mark mutable project app grants task done`。

## 范围

1. **高：公开 AppInstallationService 无解码前请求体上限。**
   `internal/core/project/transport/installation.go` 的
   `NewInstallationConnectHandler` 未配置 `connect.WithReadMaxBytes`；实测
   540,210 字节、30,000 项的 SetAppGrants/Install 请求被完整解码后才由
   `domain.CanonicalGrantShape` 返回 InvalidArgument，违反 spec「request body
   上限仍在解码前」。修复：按 `internal/core/appregistry/transport/connect.go`
   的模式增加显式 `MaxRequestBytes` 并在 handler 构造时启用，测试覆盖超大
   请求与压缩炸弹（ResourceExhausted、业务代码不运行）。
2. **高：真实 PostgreSQL 暂时不可用被映射为 Internal。**
   `internal/core/project/adapters/postgres/installation.go` 多个存储失败路径
   （`LookupInstallationRequest`、三个事务 `Begin`、`classifyUnderLock`、
   `applyProjection`、`commitInstallationRequest` 等）用裸 `fmt.Errorf` 包装，
   绕过 `storeError`，不携带 `ports.ErrStoreUnavailable`，transport 因此落入
   Internal 而非可重试 Unavailable。修复：全部改走 `storeError`；新增测试用
   真实 pgx 断连错误（而非 fake sentinel）证明 `ErrStoreUnavailable` 端到端
   可达。
3. **中：保存成功但重读失败时父组件不更新。**
   `apps/desktop-web/src/PermissionDialog.tsx` 的 save 流程中，`readFacts()`
   失败时只有对话框采用 Set 响应兜底，`onFactsRefreshed` 不会调用，App
   Library 行的 `Granted:`/grant revision 与 Project revision 停留旧值，违反
   spec §7「成功后以服务端 response + 重新 List/Get 为准」。修复：重读失败
   时仍以 Set 响应的 installation + projectRevision 同步父组件；成功路径
   行为不变；补组件测试与视觉证据。
4. **中：artifact 验收测试分页上限 20 页。**
   `tests/integration/web_bundle_surface_test.go` 的 owner-list 成员性证明
   最多翻 20 页（2,000 条）；持久验收库超过 2,000 条后新建 artifact 无法被
   找到。修复：跟随 cursor 直到耗尽，仅保留防服务器分页死循环的兜底。

非目标：repository.go 中项目 CRUD 的同类裸包装（不在 installation 调用链，
另行立项）；错误映射表、Proto、migration 均不变。

## 验收

- [x] 超大/压缩炸弹请求在解码前被 ResourceExhausted 拒绝，业务代码不执行
      （`TestInstallAppRejectsOversizedGrantsBeforeDecode`、
      `TestSetAppGrantsRejectsOversizedGrantsBeforeDecode`、
      `TestInstallationRejectsDecompressionBombs`；合法尺寸请求照常到达
      业务层）。
- [x] 真实 pgx 断连经仓储路径返回 `ErrStoreUnavailable`
      （`TestRepositoryTransientFailuresCarryStoreUnavailable`：真实
      `*pgconn.ConnectError`/refused dial，非注入哨兵；
      `TestStoreErrorSentinelMatrix` 钉住哨兵包装契约）。
- [x] 保存成功 + 重读失败时 App Library 行与 Project revision 由 Set 响应
      更新（`PermissionDialog.test.tsx` 传播断言 + `AppLibrary.test.tsx`
      真实交互断言）；重读成功路径回归通过；视觉证据见下。
- [x] artifact 成员性遍历不再有 20 页上限（cursor 耗尽驱动，1000 页兜底仅
      防服务器分页缺陷）；`make check` 通过、`make generate` 无差异、
      `TestWebBundleSurfaceVerticalSlice` 显式 PASS。
- [x] `docs/status.json` 同步。

## 验证命令与结果

- `make generate` ×2：gen/、src/gen/、README 状态区块无差异。
- `make check`：proto-check + go-check（gofmt/vet/`go test ./...` 全量）+
  web-check（architecture/eslint/prettier/`pnpm -r check` 含 vitest
  62/62/desktop-web build）+ render --check，全部通过。
- `make test-integration`：integration 全绿 + restart 持久化验证全通过
  （task/app registry/installation/surface/bridge/mutable grants）。
- 定向显式 PASS：`go test -tags=integration -count=1 -run
TestWebBundleSurfaceVerticalSlice -v ./tests/integration`。

## 证据

- 问题 1：`MaxRequestBytes = 288 KiB`（推导注释在
  `internal/core/project/transport/installation.go`），三个 wire 级测试
  证明解码前拒绝且业务层零调用。
- 问题 2：`internal/core/project/adapters/postgres/installation.go` 16 处
  裸包装改走 `storeError`；断连注入测试 0.00s、`-count=2 -race` 稳定。
- 问题 3：
  [before/](../ui/desktop-web/changes/20260829-review-hardening-mutable-project-app-grants/before/)
  （行停留 `Granted: agent.event.watch, agent.task.run · grant revision 1`、
  头部 `revision 2`）、
  [after/](../ui/desktop-web/changes/20260829-review-hardening-mutable-project-app-grants/after/)
  （行 `Granted: none · grant revision 2`、头部 `revision 3`）、
  [notes.md](../ui/desktop-web/changes/20260829-review-hardening-mutable-project-app-grants/notes.md)；
  `current/` 新增 `app-library--saved-reread-failed--1440x900.png`。
- 问题 4：排序根因（`ListArtifactIDPage` `ORDER BY id` 升序 keyset，
  UUIDv7 新建 artifact 在 owner 列表尾部）已写入测试注释。

## 未决风险与下一步

- `internal/core/project/adapters/postgres/repository.go` 的项目 CRUD 路径
  存在同类裸 `fmt.Errorf` 绕过 storeError（~39/51/55/63/88/112/131/139/
  157/167/204/214 行），真实断连下同样会误映射 Internal——不在本次范围，
  建议后续任务统一收敛。
- `MaxRequestBytes` 依赖 registry `MaxManifestBytes`（256 KiB）作为合法
  grant 内容上界；若 registry 上调该值需同步复核（注释已写明耦合）。
- 同 mux 中 project 基础 Service（`transport/connect.go`）尚无读上限，
  建议与上项一并处理。
