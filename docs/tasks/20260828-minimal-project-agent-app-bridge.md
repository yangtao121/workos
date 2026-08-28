# Task: Minimal Project-scoped Agent App Bridge vertical slice

- 状态：active
- Owner/Agent：project agent app bridge builder
- 进程/模块：workos-core `internal/core/project`（installation grant snapshot）、`internal/core/agent`（App principal/provenance/idempotency）、`internal/core/orchestration`（private AppAgentService + grant revalidation）；runtime-host `internal/runtime/surface`（bridge token + public AppBridgeService）；workos-gateway（AppBridge allowlist）；desktop-web / app-host / app-sdk / surface-sdk（MessageChannel bridge + permission consent）
- 依赖：App Registry（`002`/`003`）、Project App Installation（`004`/`005`）、Web Bundle Artifact（`006`）、Surface Session（`007`）、Agent Task Router、Harness Broker、Fake Harness、Gateway identity 注入

## 目标与范围

一个安装在 Project 中的不可信 Web Bundle App，在用户明确批准最小权限后，通过版本化
MessageChannel 调用 WorkOS canonical Agent Task Router，并从 Fake Harness 的持久事件流读取结果：

```text
requested permission（manifest，永远只是请求）
  → explicit installation grant（安装时不可变快照，安装级事实，桌面逐项勾选、默认全不选）
  → effective bridge capability（requested ∩ granted ∩ runtime 已实现方法）
  → Surface session + 短期 bridge token（只在可信 Desktop/AppHost 内存）
  → versioned MessageChannel handshake（exact iframe.contentWindow + nonce + transfer port）
  → public AppBridgeService（runtime-host）→ private AppAgentService（core）
  → active installation/grant 再验证 → Project-scoped Task Router → Harness Broker → 持久事件流
  → MessagePort 流回 iframe 渲染唯一 terminal 结果
```

在范围内：`workos.app.v1` 安装级 `granted_permissions` 快照（additive）；`workos.surface.v1` resolver
返回 grant snapshot、`SurfaceSession.bridge_token`/`bridge_capabilities` 真实生效；runtime public
`workos.bridge.v1.AppBridgeService`（`RunAgentTask`/`WatchAgentTaskEvents`，token 走专用 header）；
core private `workos.agent.v1.AppAgentService`；migration `008`（installation grant）/`009`（App task
provenance + request digest 幂等）/`010`（runtime bridge token 事实）；bridge token 生命周期
（CSPRNG、256-bit、sha256 at rest、恒定时间比较、session TTL 绑定、restart 持久、Close/replay 轮换）；
MessageChannel handshake 与有界 envelope；`@workos/app-sdk`/`@workos/surface-sdk`/`clients/app-host`
单一协议；Desktop permission consent UX + bridge 状态；合成 E2E App 内真实 Fake Harness terminal 文本；
单元/集成/并发/重启/浏览器 E2E 测试；文档与状态同步。

不在范围内：mutable grant update/revoke API、global Agent invocation、Provider 选择/credential、
Credential Vault、approval/budget/cancel/steer/parent task/incident/context refs、App Bridge 的
project/files/artifacts/knowledge/notifications/theme/window 方法、clipboard/file picker/resize、
iframe same-origin/storage/network、Web Service/Declarative/Remote Native Surface、container/native
runner、ZIP/TAR/Object Store、App upgrade/downgrade、Reliability enforcement、Indexer/RAG、Mobile、
真实 Provider Key、多 runtime 副本共享 token key。

## 关键概念区分（requested ≠ granted ≠ effective）

- **requested permission**：manifest `permissions`，Registry 只验证不铸造，语义永远不变。
- **installation grant**：Project Installation 持有的安装级不可变快照，必须是该 exact pinned
  version manifest requested set 的子集，canonical 排序、无重复；空 grant 合法；本阶段更改 grant
  只能 uninstall 后重新 install。
- **effective bridge capability**：`CreateSurface` 时计算的 `requested ∩ granted ∩ runtime 已实现`
  （当前只有 `agent.task.run`、`agent.event.watch`），随 session 返回；未实现/未授权 capability
  绝不出现在列表中。

## 进程/table owner

| 事实                                      | owner                            | 表                                                                                |
| ----------------------------------------- | -------------------------------- | --------------------------------------------------------------------------------- |
| requested permissions（immutable）        | workos-core App Registry         | `workos_core.app_versions`                                                        |
| 安装级 granted_permissions snapshot       | workos-core Project Installation | `workos_core.project_app_installations`（008 加列）                               |
| App task provenance + request digest 幂等 | workos-core Agent                | `workos_core.agent_app_task_requests`（009 新表）+ `agent_tasks` composite UNIQUE |
| Surface bridge token 事实                 | runtime-host Surface             | `workos_runtime.surface_sessions`（010 加列）                                     |

Runtime 不查 Core schema、Core 不查 Runtime schema；跨模块只走 port/RPC（AppCatalog、resolver、
AppAgentService）。

## 服务边界

- public `workos.bridge.v1.AppBridgeService`（runtime-host）：`RunAgentTask`/`WatchAgentTaskEvents`。
  Request body 只有 bounded 输入（idempotency key/role/goal 或 task id/after_sequence），绝不接受
  owner/device/project/app_instance/provider/capability——全部从 Gateway identity、validated token
  与 stored session 派生。
- private `workos.agent.v1.AppAgentService`（core，不进 Gateway allowlist）：只信任 identity
  context + runtime 转发的 session 派生字段；每次调用再验证 active installation、grant、Project
  未归档，强制 `target_scope.project_id` = installation Project，构造 canonical `AgentTaskInput`
  （不允许 iframe smuggle capabilities/output types/budget/parent/incident/global scope）。
- Gateway：allowlist 只新增 `AppBridgeService` 前缀到 runtime upstream；`X-WorkOS-Bridge-Token`
  只转发到 runtime Connect 路由，Core 路由与 `/surfaces/` asset 一律剥除；private Core AppAgent
  RPC 继续 public 404。

## bridge token 语义（详见 ADR-0002）

- `crypto/rand` 32 字节 → base64url（≥256-bit entropy）；at rest 只存 sha256 hex（010 列），
  验证用 `subtle.ConstantTimeCompare`。
- 绑定 owner + device + session + Project + app instance（session 行即绑定事实）；expiry =
  session expiry（`WORKOS_SURFACE_SESSION_TTL`，默认 15m）。
- 每次 `CreateSurface`（fresh 或 open replay）轮换 token（旧 hash 覆盖失效）；closed/expired
  replay 不铸造 token；`CloseSurface` 清除 token；restart 后 token 继续有效（PostgreSQL 持久）。
- token 只出现在 CreateSurface Connect response 与 AppBridge 专用 header；绝不进入 URL/query/
  cookie/DOM/MessageChannel payload/日志/错误/截图。
- 验证失败统一净化 `Unauthenticated`；session 有效但方法未 grant → 净化 `PermissionDenied`。

## MessageChannel 契约

- envelope 版本 `workos.app-bridge/v1`；parent 每次 iframe load 关旧 port、生成一次性 nonce、
  向 exact `iframe.contentWindow`（`targetOrigin="*"`，opaque origin 下安全性不依赖 origin 串）
  发送 hello 并 transfer port2；iframe SDK 只接受 `event.source === window.parent` + 正确
  version/type + 恰好一个 port，在 port 上回 ack nonce；parent 只接受一次正确 ack。
- 单消息 ≤64 KiB、单 Surface inflight ≤32、request 超时 15s；未知 method/字段 fail closed；
  只实现 `agent.run`、`agent.stream` 两个方法；stream 提前结束只取消本地/server stream，不取消
  durable Agent task。
- 业务 payload 复用 `@workos/protocol` 生成类型（AgentEvent/AgentTaskState），不在
  app-sdk/app-host/surface-sdk 各写一套字段。

## 错误映射

| 条件                                                                 | 净化结果                                                                    |
| -------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| malformed envelope/input/key/sequence                                | `InvalidArgument`                                                           |
| missing/invalid/expired/tampered token、wrong device                 | `Unauthenticated`                                                           |
| valid session 未 grant 方法 / grant 越界安装                         | `PermissionDenied`                                                          |
| same App key different canonical request                             | `Aborted`                                                                   |
| closed/expired session（token 解析失败）                             | `Unauthenticated`                                                           |
| Core 二次裁决失败：installation 不 active/grant 缺失/provenance 不符 | 公共 Bridge 统一净化 `PermissionDenied`（ADR-0002 §7，不形成存在性 oracle） |
| Core/Runtime/PostgreSQL 暂时故障                                     | `Unavailable`                                                               |
| invariant drift/descriptor corruption                                | `Internal`（不降级继续）                                                    |

MessagePort 侧映射为有限稳定 error code + 固定短消息；任何错误不携带 token 片段、goal/event 全文、
manifest、SQL、constraint、DSN、路径、Provider raw error 或 stack。

## UI 视觉证据

`docs/ui/desktop-web/changes/20260828-minimal-project-agent-app-bridge/`：`before/`（基线提交的
App Library 与 Web Bundle window）、`after/`（permission consent、App Surface 真实 Fake Harness
terminal 结果）、`notes.md`；`docs/ui/desktop-web/current/` 用 after 更新。全部 Chromium
1440x900、deviceScaleFactor 1、确定性 fixture。

## 验收

- [x] 行为测试（单元/传输/集成/迁移 checksum/并发 `-race`/重启/HTTP 安全/浏览器 E2E）
- [x] `make generate`×2 无差异、`make check`、`buf breaking --against '.git#branch=main'`
- [x] `make test-integration`×2（资源计数、scratch database 零新增、001–007 逐字节不变）
- [x] `make test-deepseek-fixture`、`make test-e2e`
- [x] `go test -race ./internal/core/agent/... ./internal/core/project/... ./internal/core/orchestration/... ./internal/runtime/...`
- [x] 文档（ADR、implementation.md、status.json、任务记录）与 UI before/after/current

## 交接

### 提交

`feat/minimal-project-agent-app-bridge` 分支 HEAD 的单一聚焦提交
`feat: add project-scoped app bridge`（本任务记录即位于该提交内；审核时以
`git log --oneline -1 feat/minimal-project-agent-app-bridge` 的实际哈希为准），未 merge、
未 push。

### 真实门禁（全部本机 Docker 工具链运行）

- `make check`：exit 0（proto lint/format + sqlc vet + gofmt/go vet/go test + TS
  architecture/eslint/prettier/vitest×6 包/desktop build + README 状态渲染检查）。
- `make generate` ×2：第二次后 `git status --porcelain` 与生成前逐行一致
  （NO-GENERATION-DRIFT）。
- `buf breaking --against '.git#branch=main'`：exit 0（仅 additive proto）。
- `make test-integration` ×2：exit 0/0；两轮各 31 个 integration/migration 测试 PASS；
  restart 阶段依次输出 task/app registry/installation/surface/`bridge persistence verified
for task …`（restart 后 token 仍解析、run key replay 同一 task、事件流 resume 到 terminal）。
- `make test-e2e`：exit 0，4 passed（`app-bridge.spec.ts` 新增：consent 默认全不选 → 勾选 →
  Install with 2 permissions → Granted 摘要 → Open → iframe sandbox 无 same-origin →
  bridge 握手 → 真实 Fake Harness terminal 文本 `terminal:Task <uuid> completed by fake
harness` → Close App；桌面 DOM 断言不含 `X-WorkOS-Bridge-Token`）。旧 spec 已更新为走
  consent 对话框。
- `make test-deepseek-fixture`：exit 0（仅 fixture 假凭据）。
- `go test -race ./internal/core/agent/... ./internal/core/project/... ./internal/core/orchestration/... ./internal/runtime/...`：exit 0。
- TypeScript 定向：`@workos/surface-sdk` 4、`@workos/app-sdk` 10、`@workos/app-host` 10、
  `apps/desktop-web` 36 个 vitest 全部通过（handshake wrong-source/nonce replay/double
  ack/timeout、unoffered method、inflight 上限、stream cancel、late response inert、
  oversize、token 常量时间匹配、capability 交集等）。

### Migration 与资源计数

- checksum（`TestAllMigrationChecksumsArePinned` 钉死 001–010）：008
  `180ba05df3c54c45d16dd1c67f8b45cacdde8d6ac1a77ae5338abc3dd0055766`、009
  `233ea77ca9f3dc0d18362c0cc2a650eb288c5bc90d0c0e01e3ec9428b6f411db`、010
  `91f47007a071915e0d6c2b39f35f2611f2b1f30c72781d113fd801368045896a`；001–007 对 main
  逐字节不变（`git diff --exit-code main -- …` 全部通过）。pristine scratch database 前向
  执行 + 二次运行 no-op + 当前持久 acceptance volume bootstrap 均验证。
- 两次 `make test-integration` 资源计数（持久验收 volume，前后差值逐轮一致）：
  第一轮 artifacts 49→54、artifact_requests 49→54、bundle_files 98→107、
  surface_sessions 122→132、session_requests 122→132、app_versions 1656→1684、
  installations 409→423、agent_tasks 391→396、agent_app_task_requests 8→11、
  events 3308→3369、outbox 2018→2059；第二轮 54→59、54→59、107→116、132→142、
  132→142、1684→1712、423→436、396→401、11→14、3369→3430、2059→2100。固定增量
  （artifact +5、file +9、session +10、version +28、installation +13/+14、task +5、
  app mapping +3、event +61、outbox +41）与既有切片及本任务测试 seed 一致；installations
  两轮差 1 行为既有安装并发测试锁内 no-op 的调度相关波动（上一任务已记录同类现象）。
  scratch database 每轮保持且仅保持既有 6 个历史库，零新增；`workos_workos-postgres`
  volume 未删除、未清理任何历史数据。

### Authorization matrix（真实链路验证）

| 身份/状态                                              | run                   | watch                 |
| ------------------------------------------------------ | --------------------- | --------------------- |
| 同 owner+device、open session、grant 含对应 capability | ✅                    | ✅                    |
| 同 owner 换 device（token 相同）                       | ❌ `Unauthenticated`  | ❌                    |
| token 篡改/伪造/缺失                                   | ❌ `Unauthenticated`  | ❌                    |
| session 关闭/过期                                      | ❌（token 无法解析）  | ❌                    |
| 空 grant（只 request 不 grant）                        | ❌ `PermissionDenied` | ❌                    |
| 只 grant `agent.task.run`                              | ✅                    | ❌ `PermissionDenied` |
| uninstalled 后再调用（token 仍有效）                   | ❌ 净化 denied        | ❌ 净化 denied        |
| foreign task ID / provenance 不符                      | —                     | ❌ 净化 denied        |
| same key different goal                                | ❌ `Aborted`          | —                     |

### 对象卫生

分支新增对象最大 blob 为 ~423 KiB 截图（<2 MiB 上限）；无 ELF/编译产物/临时数据库文件；
无 root-owned 文件；`git diff --check` 与 `git diff --check main...HEAD` 干净；
`docs/structure.md` 无变化；secret 扫描无命中。

### UI 视觉证据

`docs/ui/desktop-web/changes/20260828-minimal-project-agent-app-bridge/`：`before/`
（基线 App Library 直接安装 + 无 bridge 的 Web Bundle window）、`after/`（consent 默认全不选、
勾选状态、Granted 摘要、App window 内真实 Fake Harness terminal 文本）、`notes.md`；
`docs/ui/desktop-web/current/` 已用 after 更新。采集命令与 fixture 见 notes.md。

### 未决风险与下一步

- Gateway device-session gate 仍是 loopback DevBypass 语义（继承上一任务）；生产部署前必须
  补真实认证。
- token 轮换语义下，同一 surface 的旧 credential 在每次 Create replay 后失效；若未来需要
  多标签同 surface 并存，需引入受控的多凭证方案（另立 ADR）。
- watch 流当前逐请求实时轮询 Core（200ms ticker），无缓存；高并发下应在 runtime 增加受控
  事件缓存并保持 revocation 界。
- capability 只实现 `agent.task.run`/`agent.event.watch`；artifact/project/knowledge 的
  grant 只是持久事实，executor 与 approval/budget 留待后续。
- 建议下一独立任务（二选一）：mutable grant/revocation + approval/budget policy；或
  rootless Web Service/container runner + Workload 最小链路。
