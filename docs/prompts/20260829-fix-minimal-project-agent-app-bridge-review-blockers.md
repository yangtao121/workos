# Fix: Minimal Project-scoped Agent App Bridge review blockers

你是 WorkOS 的下一位修复智能体。仓库位于 `/home/aquatao/workos`。本轮只修复
`feat/minimal-project-agent-app-bridge` 的审核阻断项，使该分支达到可重新审核、可快进合并到
`main` 的状态；不要开始下一条产品纵向切片，也不要自行合并或推送 `main`。

## 当前基线

- 工作分支：`feat/minimal-project-agent-app-bridge`
- 待修提交：`fc48a7b`（`feat: add project-scoped app bridge`）
- 本地 `main`：`2c233ca`（当前分支相对 `main` 线性领先 1 个提交，可 fast-forward）
- 原始实现任务：`docs/tasks/20260828-minimal-project-agent-app-bridge.md`
- 原始任务 prompt：`docs/prompts/20260828-next-agent-minimal-project-agent-app-bridge.md`
- 信任边界 ADR：`docs/decisions/0002-app-bridge-trust-boundary.md`

开始前先阅读仓库根 `AGENTS.md`、上述任务/prompt/ADR、相关 Proto、migration、实现与测试，并检查
工作树。继续认领现有任务记录，不要建立同义重复任务。保留所有不属于本任务的改动。

## 不可扩大范围

- 保持六进程边界和 `domain → application → ports ← adapters` 依赖方向。
- Runtime 不直接读取 Core schema，Core 模块间不直接 SQL；跨边界继续使用中立 port/RPC。
- 不改变 Provider、Harness、Project binding 的既有选择语义。
- 不新增 Credential Vault、真实认证、多 Runtime 副本、mutable grant API 或其他 Bridge 方法。
- 不修改历史 migration `001`–`010`；需要数据库修复时新增 forward-only migration，除非只需修改
  尚未合并且明确属于本分支的 query/application 逻辑。禁止重写已应用 migration 的内容来掩盖问题。
- 不手改 `gen/`、`src/gen/` 或 README 状态区块；协议变更先改 Proto，再运行 `make generate`。
- 不使用、保存、打印或搜索聊天中出现过的真实 DeepSeek API key。本任务不需要真实 Provider 网络；
  DeepSeek 回归只能使用仓库既有的 fixture 假凭据和本地 fixture 服务。

## 必须修复的审核问题

### 1. Bridge token 只允许进入 public AppBridge RPC

当前 `internal/gateway/gateway.go` 以 upstream 名称决定是否保留 token；因此所有 Runtime 路由都会收到
`X-WorkOS-Bridge-Token`，包括 `SurfaceService` 与 `/surfaces/` asset。修复为默认剥除，只在
`/workos.bridge.v1.AppBridgeService/` 的 public Connect 路由保留。

要求：

- Core public RPC、Core private RPC、SurfaceService、`/surfaces/` asset、Desktop static/fallback 都不得把
  token 转发给下游。
- AppBridge 的 `RunAgentTask` 与 `WatchAgentTaskEvents` 必须继续收到 token。
- 不要依赖 Desktop “正常情况下不会附带”来构成保护；Gateway 必须对恶意客户端 fail closed。
- 日志、proxy error、测试失败文本不得包含 token。
- 修正 `internal/gateway/gateway_test.go`：分别使用可检查 header 的 Core/Runtime upstream，明确断言
  Bridge 两条路由保留、SurfaceService 与 asset 剥除。现有测试注释声称 asset 被剥除，但实际上没有
  正确验证，必须补成能在旧实现上失败的回归测试。

### 2. 修复并发首次 CreateSurface 返回未落库 token

当前 application 在调用 repository 前铸造 token，并无条件返回本地 token；repository 遇到并发
same-key winner 时返回 winner session，却没有保存 loser 的 token。结果是一个成功响应携带从未落库、
无法认证的凭据。

要求：

- 任何成功 `CreateSurface` 响应中的非空 token，其 hash 都必须真实成为该返回 session 的有效持久事实；
  禁止只因为 session ID 相同就认为并发测试通过。
- 保持同 key/同 canonical request replay、同 key/不同 request `Aborted`、不同 device 绑定等既有语义。
- 保持 ADR 的单 active token 轮换模型，或在 ADR 中给出同等严格、仍在本任务范围内的修正；不要偷偷
  引入明文 token at rest、多 token 长期共存或静态仓库密钥。
- 明确并测试并发线性化：每个返回的 token 必须在其成功响应的线性化点真实有效；最终数据库 hash
  必须对应最后一次线性化的成功 Create。若后一次 replay 按 ADR 轮换，前一 token 的失效必须是明确的
  后续轮换结果，而不是“该 token 从未存过”。
- 单 Runtime 假设已写入 ADR，可以采用与该限制一致的有界串行化，但必须避免无界 per-key 锁泄漏，
  并保持 restart 后 PostgreSQL 事实有效。
- 扩展 application fake 和真实 PostgreSQL integration concurrency 测试：收集完整 `CreatedSurface`，
  验证 token/hash/最终 winner、无 orphan session/request，并验证旧 token 的失效只来自已记录轮换。

### 3. CloseSurface 必须原子 tombstone 并清空 token hash

当前 repository 先执行 `CloseSession`，再执行只匹配 `closed_at IS NULL` 的
`ClearSessionBridgeToken`，所以第二条 UPDATE 永远匹配不到首次关闭的行。active 查询虽然会因
`closed_at` 拒绝 token，但 at-rest hash 并未按 ADR/任务记录清除。

要求：

- 在同一 Runtime-owned transaction 中原子完成首次 close 与 `bridge_token_hash = NULL`。
- repeated close 对同 owner/device 继续幂等成功，并保留第一次 `closed_at`；foreign/missing session
  继续安全 `NotFound`。
- Close/replay/bridge RPC race 不得重新激活凭据或产生越权。
- 增加真实 PostgreSQL 断言，直接查询关闭后列为 SQL `NULL`，不能只断言 active token lookup 失败。
- 若 query 变化，重新运行 sqlc；不要手改 `surfacedb` 生成文件。

### 4. 在可信 AppHost 端真正执行不可信 MessagePort 边界

当前 `clients/app-host` 的 `pending` map 从未写入，timeout 实际无效；host 没有入站消息大小、并发数、
request ID 唯一性/长度和完整 payload shape 检查。iframe 是不可信方，不能依赖 `@workos/app-sdk`
自觉执行限制；iframe 可以绕过 SDK 直接 `port.postMessage`。同时 `@workos/app-sdk` 自己发送 request
时也绕过了共享的 bounded post helper。

要求：

- trusted host 在 dispatch 前验证完整 envelope：version/type、bounded canonical request ID、允许方法、
  exact payload shape、字段类型和既有 role/goal/key/task/cursor 边界；未知/多余字段按协议约定 fail closed。
- host 对收到的 structured-clone 消息按 UTF-8 byte size 执行 `MAX_SINGLE_MESSAGE_BYTES`，不能用 JS
  UTF-16 `string.length` 冒充字节数。
- host 自己强制 `MAX_INFLIGHT_REQUESTS`；duplicate request ID、超限、oversize、unknown method、畸形
  sequence 都返回稳定安全 code，且不调用 transport。
- timeout 必须真正登记和清理 pending operation；对 stream 要 abort 本地/server stream，对 run 至少让
  迟到结果 inert。close/reload/unmount 必须清空 timer、pending 与 stream，迟到回调不得再发消息。
- iframe SDK 的所有 outbound request/cancel 使用共享的 bounded protocol helper或同一底层实现；不能
  只测试一个从未用于真实 request 的 `encodeBridgeMessage`。
- 握手继续绑定 exact `iframe.contentWindow` + transferred port + nonce；double ack、旧 port、reload、
  wrong version/source 必须按原任务 fail closed。不要以 `origin === "null"` 作为身份。
- 添加能在旧实现上失败的 deterministic TS 测试：host inbound oversize（含多字节文本）、33 个直接
  port 请求、duplicate ID、真实 timeout、close 后迟到 run、畸形 cursor、SDK outbound oversize。

### 5. 保留 MessagePort 的稳定错误语义

当前 host 把所有 `agent.run` transport error 映射为 `internal`，把所有 stream error 映射为
`unavailable`，因此服务端的 `InvalidArgument`、`Unauthenticated`、`PermissionDenied`、`NotFound`、
`Aborted`、`Unavailable` 全部丢失。

要求：

- 在 Desktop Connect adapter 与 AppHost 间定义有限、无敏感详情的 typed error/code contract。
- Connect code 必须稳定映射到共享 `BridgeErrorCode`：至少覆盖上述六类；未知错误才是 `internal`，
  本地 timeout 是 `timeout`。
- run 与 stream 使用同一映射，不得同一服务端 verdict 因 RPC 形态不同而改变类别。
- `BigInt(afterSequence)` 等本地解析失败应为 `invalid_argument`，不能伪装 `unavailable`。
- 绝不把 raw Connect message、SQL、DSN、token、goal/event 全文、stack 或内部地址传入 iframe。
- 增加 AppHost/Desktop 组件测试，逐一钉住稳定 code 与安全短消息。

### 6. Agent PostgreSQL 暂时故障必须是 Unavailable

当前 Core Agent PostgreSQL adapter 的新 App task mapping/create/read/event 路径只包装普通 error；private
AppAgent transport 只识别 Project store 的 `ErrStoreUnavailable`，因此 Agent DB 断连会落入
`Internal`，违反任务错误矩阵。

要求：

- 在 Agent consumer-side port 增加或复用明确的 store-unavailable sentinel，并用
  `internal/platform/dbtransient` 对真实 transient PostgreSQL/connection/resource exhaustion 分类。
- 至少覆盖 Bridge 会调用的 `GetAppTaskRequest`、`CreateForApp`、`GetAppTaskByTask`、task read 与 event
  read；保持 invariant/constraint/programming error 为 opaque `Internal`，不要按 SQL message 文本分类。
- private Core transport 映射为 sanitized `Unavailable`，Runtime core client/public Bridge/MessagePort
  全链路保持 retryable `Unavailable`。
- 加 unit/transport 测试和可行的 PostgreSQL outage integration；测试错误不得泄露 DSN/SQL/constraint。

### 7. 完整验证 stored grant，再决定授权

当前 `AppAgentService.authorize` 遍历 grant 时一遇到目标 capability 就立即返回，导致
`["agent.task.run", "totally.unknown"]` 之类尾部损坏数据仍授权成功。

要求：

- 先完整验证整个 stored grant snapshot，再做目标 capability membership 判断。
- 验证 canonical vocabulary、排序、无重复；任何 invariant drift 都返回 sanitized `Internal`，不得静默
  降级或提前授权。
- 保持 requested ≠ granted ≠ effective；若为满足原始 prompt 的 exact pinned manifest/grant drift
  检查需要 Registry 信息，只能通过中立 application port/orchestration 组合，禁止 Project/Agent 直接
  读取 Registry SQL 或导入 adapter。
- 增加 unknown-before/unknown-after、duplicate、unsorted、missing capability、合法 run/watch 分离测试，
  特别保证旧实现的“合法 capability 在前、损坏值在后”用例失败。

### 8. 修复并诚实执行 race 门禁

审核实际执行以下命令退出 1：

```sh
go test -race ./internal/core/agent/... ./internal/core/project/... \
  ./internal/core/orchestration/... ./internal/runtime/...
```

报告的数据竞争位于 Surface application 测试桩：`fakeResolver.calls` 与
`staticGenerator.counter`。它们在 `main` 已存在，但当前任务记录明确把该命令勾为通过，因此本分支仍
必须修复测试桩并让真实命令通过。使用 mutex/atomic 或并发安全的 deterministic generator；不要删掉
并发测试、去掉 `-race`、取消断言或串行化测试来制造假通过。

## 测试最低要求

先增加能在旧实现上失败的回归测试，再修实现。至少执行并记录真实退出结果：

```sh
git diff --check main...HEAD

make generate
git diff --exit-code
make generate
git diff --exit-code

make check
buf breaking --against '.git#branch=main'

go test -race ./internal/core/agent/... ./internal/core/project/... \
  ./internal/core/orchestration/... ./internal/runtime/...

# 运行相关 TS workspace 的真实 check/test，不只做 tsc
pnpm --filter @workos/surface-sdk check
pnpm --filter @workos/app-sdk check
pnpm --filter @workos/app-host check
pnpm --filter @workos/desktop-web check
```

随后按原任务验收运行：

```sh
make test-integration
make test-deepseek-fixture
make test-e2e
```

`make test-deepseek-fixture` 必须只使用 Makefile 内的 fixture 假 key和本地 fixture URL，不得替换成真实
DeepSeek key，不得访问真实 Provider。Integration/migration 测试继续遵守持久 acceptance volume 与
scratch database 保护约定：不删除 volume，不清理既有 6 个历史 scratch DB，记录本轮精确资源增量与
零 scratch 泄漏。若当前环境无法安全执行某门禁，必须在任务记录中如实写明，不得勾选为通过。

## 文档与交接

- 修复完成后同步 `docs/decisions/0002-app-bridge-trust-boundary.md` 中的并发 token 线性化、close 清理与
  MessagePort/error 语义。
- 更新 `docs/tasks/20260828-minimal-project-agent-app-bridge.md`：先保持 `active`；所有实现、测试、文档和
  状态均真实完成后才改为 `completed`。删除或改正任何未经本轮实际执行支撑的“已通过”描述。
- 按真实证据更新 `docs/status.json`，再通过生成工具更新 README；禁止手改生成状态区块。
- 本轮预计无 UI 视觉变化。若实际改变任何用户可见 UI，必须按 `docs/ui/README.md` 更新对应
  before/after/current 截图与 `notes.md`；否则不要伪造新截图。
- 检查 diff 中没有 secret、fixture 输出、测试结果目录、编译产物或无关文件。
- 提交到当前 feature branch，留下工作树干净。不要 merge/push `main`；交给下一轮审核者执行
  `git merge --ff-only`。

## 完成判定

只有同时满足以下条件才可宣称 ready for review：

- 上述 8 类问题均有旧实现会失败、修复后会通过的回归测试；
- 并发 Create 返回的 token 与持久 hash 语义闭合，Close 后 hash 真实为 NULL；
- bearer header 只到 AppBridge，host 能抵抗绕过 SDK 的恶意 MessagePort 流量；
- PostgreSQL transient failure 与 MessagePort error code 全链路映射正确且不泄密；
- stored grant corruption 始终 fail closed；
- `make generate` 二次无差异、`make check`、Proto breaking、定向 `-race`、integration、fixture、E2E
  均有真实通过证据；
- 任务记录状态与证据一致，工作树干净，没有真实 DeepSeek key。

若任一项未完成，保持任务 `active` 并明确写出 blocker；不得以 TODO、固定成功响应、跳过测试或文档
声明冒充完成。
