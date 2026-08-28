# 下一位智能体 Prompt：Minimal Web Bundle Surface 审核修复

> 将本文件完整交给修复智能体。当前实现未达到合并条件；请直接修复、补齐证据、同步任务记录并提交，
> 不要只输出计划，也不要扩展成 App Bridge、Provider 或 Runtime runner 任务。

## 你的角色与当前审核结论

你是 WorkOS `feat/minimal-web-bundle-surface` 分支的审核修复智能体。仓库位于
`/home/aquatao/workos`。本轮审核时：

- 审核的功能实现提交为 `9ad7ffa7bd0422574a736d09b208cce68863d097`（
  `feat: implement minimal web bundle surfaces`）；本审核 prompt 提交后分支 HEAD 会向前移动；
- 本地 `main` 为 `1ca326266f9b02ca8efea40119daf1e5523b8e02`，且是功能分支的直接祖先；
- 写入本审核 prompt 前工作树干净，`main...9ad7ffa` 只有一个实现提交；
- `git diff --check main...HEAD` 通过；
- 审核者于 2026-08-28 UTC 实际运行 `make check`，exit 0；
- 审核者实际运行仓库 Docker Go 环境下的 `go test -race ./internal/runtime/...`，exit 0；
- 006 checksum 为
  `628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27`；
- 007 checksum 为
  `b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986`；
- 001–005 相对 `main` 逐字节未变。

开始时必须重新核对这些事实，不能假定提交号、工作树、数据库或容器状态仍未变化。

现有实现的 Artifact persistence、Registry descriptor、Project installation resolution、private Core
resolver、Runtime session、Gateway route 和 Desktop iframe 主链路总体成形，基础门禁也通过。但是静态审核
确认了请求语义、安全隔离、错误分类和并发证据上的合并阻断项；分支还误提交了一个约 19 MiB 的本地 Go
可执行文件。当前分支不得合并，也不得保持“全部验收完成”的结论。

只修复本文列出的 Web Bundle Surface 问题及其测试、文档和状态。不要实现 App Bridge、MessageChannel、
capability token/grant、Credential Vault、ZIP/TAR importer、object store、container/native/web-service
runner、App upgrade、Surface attach/resize/suspend、Reliability、Indexer、Mobile 或真实认证。

## 凭据与安全边界

- 本任务不需要真实 DeepSeek、OpenAI、GitHub 或其他 Provider Key。
- 不得使用、保存、转述、验证或尝试恢复聊天中出现过的真实 Key。
- 不得从聊天历史、shell history、进程环境或本机文件搜集凭据。
- DeepSeek 回归只使用仓库已有 fixture 假 credential，禁止访问真实 Provider 网络。
- 测试只能使用明显虚构的合成值；日志、错误、任务记录和截图不得包含 credential、raw bundle 全文或用户
  内容全文。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`
   - `docs/structure.md` 的进程边界、App Runtime、Surface 和安全章节
   - `docs/architecture/implementation.md`
   - `docs/status.json`
   - `docs/prompts/20260825-next-agent-minimal-web-bundle-surface.md`
   - `docs/tasks/20260825-minimal-web-bundle-surface.md`
   - `api/proto/workos/artifact/v1/artifact.proto`
   - `api/proto/workos/surface/v1/surface.proto`
   - `api/proto/workos/surface/v1/surface_resolver.proto`
   - `schemas/workos-app-manifest-v1.schema.json`
   - `internal/core/artifact`、`internal/core/appregistry`、
     `internal/core/orchestration/surface_resolver*` 的实现与测试
   - `internal/core/project` 的 installation authority read
   - `internal/runtime/surface` 下全部实现与测试
   - `internal/gateway`、`internal/platform/config`、三个 composition root
   - `internal/platform/migrations/files/001` 至 `007`
   - Desktop App Library、Desktop、AppSurface、Window Manager 及其测试/E2E
   - `tests/integration/web_bundle_*`、`tests/restart/main.go`
   - `Makefile`、`compose.yaml`、`Dockerfile`、`.gitignore`、`sqlc.yaml`
2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -8
   git branch -vv
   git diff --check
   git diff --check main...HEAD
   git merge-base --is-ancestor main HEAD
   git ls-files -s restart
   file restart
   ```

3. 保留不属于本任务的已有改动；继续在功能分支工作，不得 reset、rebase、直接改 `main`、merge 或 push。
4. 保留本审核 prompt，并随修复提交。
5. 先把 `docs/tasks/20260825-minimal-web-bundle-surface.md` 状态从 `done` 改回 `active`，写明本次审核
   阻断项。全部修复和完整门禁真实通过后才能恢复 `done`。
6. 修复期间 Runtime / Surface 不得继续宣称已完整 working。按仓库状态规则暂时降级，最终只有在新的
   并发、错误分类、HTTP sandbox 和浏览器 E2E 证据成立后才能恢复；README 状态区块只能由生成工具更新。
7. 006/007 已进入持久验收 volume 并受 checksum 保护，绝对禁止修改、重命名、squash、重排或覆盖。
   本轮缺陷不需要改表；不要为了修复应用语义新增无必要 migration。
8. 禁止 `docker compose down -v`、删除 `workos_workos-postgres` volume、TRUNCATE/批量删除验收数据、
   删除历史 scratch database，以及手改 `gen/`、`src/gen/` 或 README 生成状态区块。

## 阻断项一：根目录误提交本地 `restart` 可执行文件

分支跟踪了仓库根目录的 `restart`：mode `100755`、约 19,126,918 bytes、ELF x86-64 Go executable，
blob 为 `2dd6362e90c544487d391314732aa4063eabee41`。它显然是对 `tests/restart` 运行本地 `go build`
产生的输出，不是产品源码或发布资产。

这与任务记录中“无 root-owned/临时文件”的声明直接矛盾，也污染 Git、Docker build context 和后续审查。

必须：

- 只从 Git 和工作树删除根目录这个生成二进制，不得删除 `tests/restart/` 源码；
- 在根 `.gitignore` 增加精确的 `/restart` 规则，防止再次误提交；不要用宽泛规则忽略其他应跟踪文件；
- 后续若需构建 helper，明确 `-o` 到安全的临时目录；
- 最终扫描全部 tracked files，确认没有其他 ELF、测试报告、临时 bundle、credential 或异常大文件；
- 更正任务记录中不准确的“无临时文件”证据，保留审核发现和修复事实。

## 阻断项二：Surface canonical idempotency 没有绑定可信 device ID

当前 `domain.CreateRequestDigest` 覆盖 project、installation、device class、viewport 和 renderer，但没有
覆盖 Gateway 注入的可信 `DeviceID`；幂等 mapping 主键却只有 `(owner_user_id, idempotency_key)`。

可复现时序：

```text
owner O / device A / key K / body R → session SA（绑定 A）
owner O / device B / key K / 同 body R → mapping digest 相同
                                  → GetSession(O, B, SA)
                                  → NotFound
```

第二次请求既没有精确 replay，也没有按“同 key 不同 device”返回 `Aborted`。这破坏持久幂等契约，并让
错误码取决于 session 的 device lookup，而不是 key 的权威裁决。

必须达到：

- canonical create digest 明确覆盖可信 `device_id`；owner 仍由 mapping namespace 隔离，不必重复进
  request body；
- 同 owner/key/可信 device/相同 canonical request 精确返回第一次 session snapshot，即使已关闭或过期；
- 同 owner/key 但可信 device ID、Project、installation、device class、viewport 或 renderer 任一不同均
  稳定返回 `Aborted`；
- 失败解析和 validation 不消费 key；并发 loser 不遗留 orphan session；
- device ID 只能来自 identity context，绝不能添加到 public request body。

必须新增 application、Connect 和真实 PostgreSQL 双 repository 测试，明确模拟同 owner 的两个可信
device identity；不要只改 fake repository 让测试变绿。

## 阻断项三：不支持的 preferred renderer 被静默当作 unspecified

`internal/runtime/surface/transport/connect.go` 的 `preferredRendererFromProto` 只有
`WEB_BUNDLE` 返回 `web-bundle`，其他所有值都返回空字符串。application 又把空字符串解释为
unspecified 并默认成 Web Bundle。

因此客户端提交 `WEB_SERVICE`、`DECLARATIVE`、`REMOTE_NATIVE`，甚至 protobuf 中的未知枚举数值，都可能
启动 Web Bundle，而不是按契约返回 `InvalidArgument`。现有 domain 测试只直接传字符串
`web-service`，没有经过真实 Proto 转换，所以没有发现该缺陷。

必须：

- 明确区分 `UNSPECIFIED`、`WEB_BUNDLE` 和 unsupported/unknown enum；
- 只有前两者可进入 resolver，其他值稳定 `InvalidArgument`；
- rejected renderer 不调用 Core resolver、不写 session、不消费 idempotency key；
- 使用真实 Connect handler 覆盖所有已声明但未实现 renderer 和至少一个未知数值；
- 随后用同 key + 合法 renderer 成功，证明失败 key 未消费。

## 阻断项四：UUIDv7 与 viewport 边界没有按声明执行

当前 `ValidSessionUUID` 和 `ValidArtifactUUID` 只检查 36 字符 UUID 外形，不检查 version 7、RFC variant
或 canonical lowercase。任务 prompt 和文档却明确声称 CreateSurface 的 Project/installation、Surface
session、Artifact ID 都执行 UUIDv7 边界。当前测试也只证明生成结果是 UUIDv7，没有证明 v1/v4/错误 variant
输入会被拒绝。

同时，`ValidViewport` 使用普通大小比较；IEEE `NaN` 与所有比较均为 false，因此 `pixel_ratio=NaN` 会被
接受并进入 canonical digest。protobuf binary 可以表达该值，“合理 viewport”边界并未成立。

必须：

- 在所属 domain 中建立小而明确的 canonical UUIDv7 validator，检查 hyphen、lowercase hex、version nibble
  `7` 和 RFC variant；不要让 domain 导入数据库、Proto 或 HTTP；
- Surface Create 的 project/installation、Close 的 session ID、Artifact Get/List cursor 等本切片新增边界
  按文档一致使用 UUIDv7；HTTP 的畸形/非 v7 session path 继续统一 404；
- server generator 仍使用现有 `ids.UUIDv7`，UTC 行为不回归；
- viewport 明确拒绝 NaN、正负 infinity 和其他非有限值；对 `0`/negative zero 是否代表 unspecified 只保留
  一个 canonical 语义并写测试；
- 增加 domain + Connect 测试，覆盖 UUIDv4、错误 version/variant、uppercase、NaN/Inf，以及失败 key 未消费；
- 如果核对现有历史数据发现非 UUIDv7，不得擅自迁移或删除，先停止并报告只读证据。

## 阻断项五：暂时不可达被错误分类成 Internal 或 404

原任务错误契约明确要求：Core/Runtime/PostgreSQL 暂时不可达为净化 `Unavailable`；数据不变量破坏、digest
漂移和内部读取损坏才是净化 `Internal`。

当前 Runtime application 对 repository 错误原样返回，public Connect transport 将它们落入默认
`Internal`；asset handler 更把除 `domain.ErrUnavailable` 外的所有错误统一成 404。因此 Runtime 数据库连接
中断会伪装成“资源不存在”。Core private resolver 的依赖故障也可能被 private transport 映射成 Internal，
Runtime client 无法识别为暂时不可达。现有 `TestRepositoryFailureIsNotADomainVerdict` 反而固化了错误分类。

必须达到：

- 为本切片各 port 明确区分 transient dependency unavailable 与 invariant/internal failure；分类应发生在
  adapter/port 边界，不得依赖 SQL/constraint 错误文本字符串；
- Runtime Create/Close 在 Runtime PostgreSQL 暂时不可达时返回 `CodeUnavailable`；asset 返回净化 503；
- private Core resolver 的 Project/Registry/Artifact PostgreSQL 暂时不可达返回净化
  `CodeUnavailable`，Runtime 正确转换并向 public Surface 暴露 `Unavailable`/503；
- Gateway 到 Core/Runtime 的 upstream failure 继续为固定 503；
- digest drift、stored descriptor corruption、未知普通错误继续为净化 Internal，不能一律改成 Unavailable；
- NotFound/FailedPrecondition/Aborted/InvalidArgument 现有分类不回归；所有响应和日志不泄漏 SQL、路径、
  manifest 或 bundle bytes。

必须新增 deterministic fake-port transport tests，并用真实服务进程停止/不可达或等价的真实 adapter 证据
覆盖至少 Runtime DB 和 private Core dependency 两条路径。不要通过把所有 error 都包成 unavailable 来通过。

## 阻断项六：iframe sandbox 不是静态内容的服务端安全边界

Desktop 正确设置了 `iframe sandbox="allow-scripts"` 且没有 `allow-same-origin`。但是 Runtime 响应的 CSP
没有 `sandbox` directive，用户控制的 HTML/JS 又直接托管在 WorkOS 同 origin 的可导航 URL 下。

iframe 属性只约束该次嵌入；若 `/surfaces/<session>/` 被顶层打开，响应本身会重新获得 WorkOS origin，
可尝试读取同 origin storage，并能利用顶层导航做外带。`connect-src 'none'` 和 `form-action 'none'` 很重要，
但不能把“仅在 Desktop iframe 内才安全”变成服务端强制的不变量。Surface URL 不是 secret，安全边界不能依赖
用户永远不直接打开它。

必须：

- 对所有 Surface document 响应增加服务端强制的 CSP sandbox，最小目标为 `sandbox allow-scripts`，绝不
  包含 `allow-same-origin`、forms、popups、top-navigation、downloads 或 storage 权限；若选择独立 origin
  方案必须先说明，因为这超出本轮最小修复，优先使用 CSP sandbox；
- 保留现有 `default-src`、script/style/img/font/connect/object/base/form/frame-ancestors/worker、nosniff、
  no-referrer、no-store 和 server-derived MIME 约束；
- unit/integration tests 断言 CSP 含 `sandbox allow-scripts` 且不含危险 sandbox token；
- 浏览器 E2E 除 iframe 属性外，必须把同一个 live Surface URL 顶层打开，并用合成 localStorage marker
  证明页面仍是 opaque/sandboxed origin、无法读取 WorkOS origin storage；不得使用真实 credential 作 marker；
- 继续证明外部 `app.js` 能在允许脚本的 sandbox 中运行，Close/Remove 后旧 URL 404；
- 对 HTML 与 SVG 等可成为 active document 的受控 MIME 做同样的服务端隔离，不得只给 entrypoint 特判后
  留下直接 asset URL 绕过。

## 阻断项七：Gateway 对 Runtime URL 没有真正 fail-fast

`ValidateGateway` 当前使用 `url.ParseRequestURI`。相对 URI 或非 HTTP(S) scheme 可能通过该检查，随后
`httputil.ReverseProxy` 只会在请求时失败；这不符合 `WORKOS_RUNTIME_URL` 启动时 fail-fast 的明确要求。

必须：

- Gateway 启动前要求 Core/Runtime upstream 都是 absolute `http`/`https` URL，具有非空 host；按现有部署
  约定拒绝空值、相对路径、scheme-less、unsupported scheme 和其他不能作为 upstream 的形态；
- `gateway.New`/proxy constructor 也不能在绕过 composition-root validation 时接受明显无效 target；
- 增加 table-driven config/gateway tests，证明非法 Runtime URL 在启动阶段失败，合法 loopback URL 保持；
- 不把 private Runtime 服务加入 Gateway allowlist，不改变 loopback/TLS/dev-bypass 边界。

## 阻断项八：任务要求的并发和原子性测试实际上缺失

当前 `go test -race ./internal/runtime/...` 通过，但 Runtime 测试没有启动任何并发 goroutine；它只能证明现有
串行用例没有触发 race，不能证明任务记录声称的：

- 双 repository/runtime instance 并发 same key 只有一个 session fact；
- same key 不同请求的确定裁决和 loser 零 orphan；
- different key 并发；
- Create/Close/asset race 的线性化结果与 close 后 fail-closed。

`TestSurfaceSessionRepositoryDurability` 只证明 restart/durability，不是并发测试。另一方面，Artifact 的
PostgreSQL 并发测试在两个 goroutine 中无锁执行共享 `artifactSequence++`；用 race detector 运行该集成测试
会使测试代码自身产生数据竞争，削弱其证据。Artifact metadata/files/request mapping 的事务中途失败回滚也
没有真实 PostgreSQL 故障注入断言。

必须新增/修正：

1. Runtime 两个独立 pool/repository 的 same-key/same-request race：两个调用返回同一 session，数据库只有
   一个 session 和一个 mapping。
2. Runtime same key/different canonical request race：一个成为权威结果，另一个 Aborted；失败侧无 orphan。
3. 同 owner 的不同 device + same key 按阻断项二稳定 conflict；different keys 可各自创建 session。
4. 使用 channel/barrier 的 deterministic Create/Close/asset race，不用 sleep；允许明确记录的线性化结果，
   但 Close 返回后的新 asset 请求必须始终 404。
5. 修复 Artifact integration test 自身的共享 counter race；预生成唯一 command/ID 或使用正确同步，不得
   通过关闭 race detector 隐藏。
6. Artifact 真实 PostgreSQL transaction 中途失败：artifact metadata、已插入的 file 和 request mapping
   全部回滚；随后同 key 合法请求可成功。
7. 对新增并发用例实际运行 race detector；任务记录写真实命令和结果，不能只写“`-race` 通过”。

scratch database helper 必须继续精确清理本轮创建的数据库；禁止清理 6 个历史 scratch database 或用户
验收数据。

## 同轮补齐 Desktop session 生命周期证据

当前 Desktop 已覆盖窗口关闭、Project 切换、卸载，以及 AppLibrary 卸载后迟到的 Create response；但已打开
的 App Surface 在整个 Desktop 组件卸载时没有 best-effort `CloseSurface`，只能等待 TTL。原任务明确要求
组件卸载使相关 session inert 并 best-effort close。

在不发明页面 unload 可靠投递协议的前提下：

- 使用 ref/明确生命周期保存当前 app-surface session 集合；React 组件 cleanup 对仍打开 session 发起
  best-effort Close；
- 避免 cleanup 闭包捕获旧 windows，避免 Project switch/window close 与 unmount 造成有害重复；后端 Close
  本身幂等，重复 best-effort 调用允许但测试应说明；
- 用确定性 component test：先成功打开，再 unmount，断言对应 session 被关闭；迟到 response 用例继续通过；
- 页面 crash/网络断开仍由 TTL 和每次 Core revalidation fail closed，文档不得声称 unload RPC 必达。

## 回归边界

修复后必须继续证明：

- 六进程边界与 `domain → application → ports ← adapters` 不变；Runtime 不导入 Core internal package，
  Runtime SQL 不查询/FK Core 表；Registry/Project/Artifact 不跨模块 SQL。
- Artifact bundle limits、path/case-fold/digest/owner isolation/paging、public metadata-only 行为不回归。
- Registry descriptor 仍由唯一 manifest Schema + cross-field policy 校验，Register 验证 same-owner/exact
  artifact digest；legacy manifest 仍能安装但 Surface 返回 FailedPrecondition。
- Core 每次 asset 请求重走 active installation → exact pinned version → manifest digest → exact artifact；
  uninstall/archive/foreign owner/device 后 fail closed。
- session TTL、UTC、Close、restart、expired/closed replay、bridge token 为空和未实现 capability flags 不回归。
- Gateway 仍只公开 SurfaceService 与 `/surfaces/`，Workload/private resolver/其他 runtime RPC 继续 404；
  spoofed identity 被覆盖、缺 device session fail closed。
- Desktop 不读取 bridge token、不注入 WorkOS client/App Bridge；iframe 无 `allow-same-origin`；Agent Center 与
  App window 继续并存。
- Workload/container/native capability 继续如实 unavailable；Artifact 状态仍限定为 web-bundle subtype。

## 文档与状态

- 更新 `docs/tasks/20260825-minimal-web-bundle-surface.md`：记录每个审核缺陷、修复方式、新增测试、真实命令
  和结果；删改“无临时文件”“并发已覆盖”等不准确陈述，而不是覆盖历史。
- 更新 `docs/architecture/implementation.md`：明确 Surface digest 包含可信 device、renderer 枚举 fail
  closed、transient unavailable 分类、CSP response sandbox 和 Desktop unmount best-effort close。
- `docs/status.json` 只能在真实纵向、restart、并发、HTTP security、顶层 sandbox E2E 和全部门禁通过后恢复
  Runtime / Surface working；README 状态区块只经生成工具同步。
- 不把本轮修复写成 App Bridge 或生产认证已经完成。

## 验收顺序

### 定向修复验证

先运行新增的 domain/application/Connect/Gateway/config/Desktop 测试。再使用真实 PostgreSQL scratch database
运行 Artifact/Surface transaction 与双 repository 并发测试，并用 `-race` 覆盖测试代码本身。所有竞态测试
必须使用 barrier/channel，不得依赖随机 sleep。

至少实际运行并记录等价于：

```sh
go test -race ./internal/runtime/...
go test ./internal/core/artifact/... ./internal/core/appregistry/... ./internal/core/orchestration/...
go test ./internal/gateway/ ./internal/platform/config/
```

integration-tag race 命令应通过仓库 Docker Go 环境连接 loopback PostgreSQL，只选择新 Artifact/Surface
并发与事务用例；不得因此删除或重建 acceptance volume。

### 生成与完整门禁

完成修复后按顺序执行并记录：

```sh
make generate
make generate
git diff --check
make check
buf breaking --against '.git#branch=main'
make test-integration
make test-integration
make test-deepseek-fixture
make test-e2e
git diff --check
git diff --check main...HEAD
git diff --exit-code main -- internal/platform/migrations/files/001_foundation.sql
git diff --exit-code main -- internal/platform/migrations/files/002_app_registry.sql
git diff --exit-code main -- internal/platform/migrations/files/003_app_registry_idempotency.sql
git diff --exit-code main -- internal/platform/migrations/files/004_project_app_installations.sql
git diff --exit-code main -- internal/platform/migrations/files/005_project_app_installation_request_owner.sql
sha256sum internal/platform/migrations/files/006_web_bundle_artifacts.sql
sha256sum internal/platform/migrations/files/007_surface_sessions.sql
git status --short --branch
```

第二次 `make generate` 后必须无生成漂移。DeepSeek 只跑 fixture target，不设置或使用真实 Key。

两次 integration 前后按原任务口径记录 Artifact/file/request、Surface session/request、Registry version、
installation、event/outbox 的真实计数增量；核对 scratch database 精确集合在每轮后零新增。不要为了固定计数
删除旧数据或降低断言。

浏览器 E2E 必须同时证明：真实 bundle script、iframe sandbox、response CSP sandbox、顶层 direct URL 仍
opaque、reload 后重新 Open、Close/Remove revocation、无 SPA fallback。若浏览器/标准行为与预期不符，保留
失败证据并选择安全方案，不得删除 sandbox 断言让测试变绿。

### 最终一致性

另外确认：

- 006/007 checksum 仍分别为本 prompt 记录的值，001–005 未变化，没有 008 migration；
- `docs/structure.md` 无意外变化；
- 根目录 `restart` 不再 tracked/存在，`.gitignore` 只含精确防回归规则；
- tracked/worktree 无 ELF、root-owned 文件、临时 bundle、测试报告、raw content 或真实 credential；
- Surface URL 无 bearer/query secret，bridge token 为空，iframe/CSP 均无 `allow-same-origin`；
- `workos_workos-postgres` volume 未删除，历史 scratch database 未清理；
- 最终工作树 clean，提交聚焦，未 merge、未 push。

## 提交与交接

全部修复和门禁通过后，在当前功能分支创建 Conventional Commit，建议：

```text
fix: close web bundle surface review gaps
```

最终向用户简洁报告：

1. 每个阻断项如何修复，尤其是 device-bound idempotency、renderer fail-closed、Unavailable 与 response
   sandbox；
2. 新增的真实 PostgreSQL/race/browser证据；
3. 006/007 checksum、两次 integration 计数与 scratch database 结果；
4. 完整门禁命令与 exit 结果；
5. 提交 ID 和 clean worktree；
6. 明确说明未 merge、未 push、未使用真实 Key、未访问真实 Provider、未删除 volume/历史数据。

不要自行合并到 `main`；留给审核者复审后执行本地 `git merge --ff-only`。
