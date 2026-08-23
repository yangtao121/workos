# Task: Harness Provider Catalog and Project Binding UX

- 状态：done
- Owner/Agent：Codex
- 进程/模块：workos-core Catalog/Project orchestration；workos-gateway；desktop-web
- 依赖：HarnessHostService.DescribeProviders、Project optimistic revision、Agent Task provider snapshot、PostgreSQL 18、Docker

## 目标与范围

实现 harness-host private provider description → Core-owned public Catalog → Gateway/Desktop，以及
Desktop revision-safe bind/unbind → 后续 Task provider snapshot → canonical ordered events 的纵向链路。

范围包含 vendor-neutral Catalog、服务端 binding preset、Project settings、Task provider snapshot、默认
Fake 与本地 DeepSeek fixture 浏览器 E2E。不包含真实 DeepSeek API、Credential Vault、Key UI、其他
Provider、tools/sessions/approvals、Runtime/Reliability/Indexer 等能力。

### Catalog 设计

- 在 `workos.harness.v1` 新增独立公开 `HarnessCatalogService`，复用 canonical
  `HarnessProviderInfo`；现有 `HarnessHostService` 保持 private 且语义不变。
- Core Catalog 按 `domain → application → ports ← adapters` 分层，通过 private Connect client
  直接调用 `DescribeProviders`，不缓存、不读取 Harness 数据库，也不参与 Core readiness。
- Core 确定排序、字段边界、unknown health 和固定安全 unavailable reason；原始 transport/provider
  reason、地址、配置路径或 credential 状态不进入公开响应或日志。
- Gateway 使用显式 public-service allowlist；Harness execution/cancel 与 Core TaskExecution 不对浏览器开放。

### Binding 设计

- 新增 Core-owned `ProjectHarnessBindingService`，请求只包含 Project、expected revision，以及
  provider ID 或 Use Global Default；客户端不提交 policy/profile/credential。
- orchestration 层先 owner-scoped 读取 Project、检查 archive/revision，再对 bind 查询 Catalog；clear
  完全跳过 Catalog。healthy/degraded 可绑定，其余 health 或未知 ID拒绝新绑定。
- 服务端注入默认 `ephemeral` + `project-no-tools` policy reference，profile/credential 为空，并复用
  Project application 的 optimistic update。现有 `UpdateProject` 保持兼容。
- Project 改绑只影响随后新建的 Task；既有 Task 与幂等重试继续使用 Submit 时的 provider snapshot。

## 协议/数据影响

- Additive v1：新增 public Harness Catalog 与 Project Harness Binding Proto/service。
- 复用现有 `HarnessProviderInfo`、`Project`、`HarnessBinding` 和 `AgentTask.provider_id`。
- 不新增 migration，不改变表所有权、进程边界或私有 Harness service。
- 新增 Core-only 非 secret Catalog timeout 和 binding preset 配置；DeepSeek credential 仍只进入 harness-host。

## 验收

- [x] Catalog mapping、排序、health/capability、边界、safe error 与 Gateway private-service 测试
- [x] Binding owner/revision/archive、health selection、clear-without-Catalog 与 preset 测试
- [x] Desktop Catalog/settings/conflict/provider snapshot 组件测试
- [x] 默认 Fake 浏览器 E2E 与 DeepSeek fixture 浏览器 E2E
- [x] 改绑、幂等和 Core/Harness 重启持久化集成证据
- [x] `make generate` 二次执行无漂移
- [x] `make check`
- [x] `make test-integration`
- [x] `make test-deepseek-fixture`
- [x] `make test-e2e`
- [x] README、部署资产与 `docs/status.json` 同步

## 交接

实现前基线（2026-08-23 UTC）均通过：`make bootstrap`、`make check`、
`make test-integration`、`make test-e2e`、`make test-deepseek-fixture`。开始时工作树 clean，任务分支为
`feat/harness-catalog-binding-ux`。未使用真实 Key，未运行真实 DeepSeek smoke。

实现结果：

- 新增 additive v1 `HarnessCatalogService` 和 `ProjectHarnessBindingService`；Gateway 改用公开 service
  allowlist，private `HarnessHostService` 与 `TaskExecutionService` 均返回 404，不会暴露执行/取消接口。
- Core Catalog 完成独立 domain/application/ports/private Connect adapter/transport 分层；无缓存，排序、
  health/capability、字段上限、deadline/cancellation 与固定安全错误均有测试。
- binding orchestration 使用 owner-scoped Project 读取和 expected revision，healthy/degraded 可绑定，
  clear 不访问 Catalog；服务端注入 `ephemeral` + `project-no-tools` reference，credential 始终为空。
- SDK 公开最小 Catalog/binding client；Desktop 增加 Project settings、Catalog outage/provider health 区分、
  revision conflict refresh、bind/unbind 与 Task provider snapshot。组件/控制器共 12 项测试通过。
- 默认集成验证 Fake healthy/default、DeepSeek unavailable、bind/clear、Fake Task/RunStarted 与重启；
  DeepSeek fixture 验证浏览器选择、revision 2、Task/RunStarted=`deepseek`、ordered stream/usage、改绑后
  新 Task=`fake`、幂等与 Core/Harness 重启后 binding/task/events 持久化。

最终证据（2026-08-23 UTC）：

- `make generate`：通过；紧接着二次执行，generated tree 与 README SHA-256 均未变化。
- `make check`：通过；Go/Proto/架构、全部 workspace 与 Desktop 12 tests 通过。
- `make test-integration`：通过；默认纵向集成与 restart helper 通过。
- `make test-deepseek-fixture`：通过；Go 主测试及 5 个失败映射子测试、restart helper、Playwright
  fixture spec 1/1 通过。首次新增浏览器阶段暴露的 spec 选择/locator 问题已修复，并完整重跑 target。
- `make test-e2e`：通过；默认 foundation 1/1 通过，fixture-only spec 按设计 skip。
- `git diff --check`、`docs/structure.md` unchanged、无 root-owned 文件、无新增 migration/secret 形态、
  PostgreSQL volume `workos_workos-postgres` 存在。

限制与后续：Catalog health 是即时只读结果且不缓存；resource policy 目前仅是 binding reference，未实现
cgroup/resource enforcement；生产认证、Credential Vault 与 live provider smoke 仍不在本任务范围。既有
完整 `UpdateProject` v1 契约为兼容性继续保留，但新 Desktop/command 不提交或展示 credential 字段。
未使用真实 Key，未运行真实 DeepSeek smoke，未删除 Docker volume。

### 审核阻断项修复（2026-08-23 UTC）

评审发现两个 Desktop 竞态与一个生成文件门禁问题，已修复并补确定性测试（desktop-web 测试从 12 增至
15，全部通过）：

1. Project 切换异步串线：绑定状态从全局值改为按 `project_id` 隔离（`bindingDrafts`、
   `bindingSaving`、`bindingFeedback` map），每次保存持有不可变 project ID 与单调递增 operation
   token；continuation 只在 token 仍为该项目最新时写编辑器状态，服务端返回始终更新同 ID 列表缓存。
   移除旧的 `bindingProjectID` 初始化 effect，编辑器在切换后立即以目标 Project 持久化 binding 惰性
   初始化。新增两个 deferred-Promise 确定性测试：
   - A 保存 pending 期间切到 B；A 成功后仅 A 列表项更新（revision 2），B 的选中值、feedback 均不被
     覆盖，B 仍可按自己的 selection 保存（第二次调用携带 project-2 / revision 8 / fake）。
   - A 保存返回 `Code.Aborted`、`GetProject` refresh 未完成时切到 B；refresh 返回只更新 A 缓存，
     B 的 draft 与提示不变；切回 A 时显示刷新后的 revision 2 与 conflict 提示。
2. Catalog 非 ready 保存门禁：`HarnessSettings.draftCanSave` 增加 `catalogState === "ready"` 前提；
   loading、error 或 provider 不在当前 Catalog 时禁止提交具体 provider，`Use Global Default` 不依赖
   Catalog 仍可保存；已保存但 unknown/unavailable 的 binding 保持显示且可解除。新增组件测试覆盖
   ready→loading→error→provider-消失 四种状态与全局默认兜底。
3. 生成文件 whitespace：根目录新增 `.gitattributes`，仅对 `sdk/protocol/src/gen/**` 关闭
   `blank-at-eof` 检查（protoc-gen-es 生成的 TS 文件以空行结尾），未放宽其他 whitespace 规则；
   `git diff --check main...HEAD` 通过。

修复后验收命令（实际执行，2026-08-23 UTC）：

- `make generate` 三次执行：通过，无漂移；`gen/` 与 `sdk/protocol/src/gen/` 全树 SHA-256 一致，
  README 与 `docs/status.json` 不变。
- `make check`：通过；Go/Proto/架构、全部 workspace 与 desktop-web 15 tests 通过。
- `make test-integration`：通过；默认纵向集成 + restart persistence verified。
- `make test-deepseek-fixture`：通过；Go 主测试及 5 个失败映射子测试、restart helper、Playwright
  fixture spec 1/1。
- `make test-e2e`：通过；foundation 1/1，fixture-only spec 按设计 skip。
- `buf breaking --against '.git#branch=main'`：通过，无 breaking change。
- `git diff --check`、`git diff --check main...HEAD`：通过；`docs/structure.md` unchanged、无
  root-owned 文件、无新增 migration/secret 形态、PostgreSQL volume 未删除。
- 修复提交：`fix: isolate harness settings state by project`；提交后 `git status --short` 为空；
  未 merge、未 push。
