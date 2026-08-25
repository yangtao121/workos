# 下一位智能体 Prompt：Minimal Web Bundle Surface 纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、文档和提交，不是只输出计划。

## 你的角色

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。Project App Installation 已完成审核，
并快进合并到本地 `main`；你的任务是实现下一条严格限定的纵向切片：
**由真实已安装实例驱动的 Minimal Web Bundle Surface**。

本任务要让一个 owner 发布受限、不可变的静态 Web Bundle，将引用该 bundle 的 App version 注册并安装到
Project，再从 Desktop 的 App Library 打开一个由 `runtime-host` 托管、由 Gateway 认证、运行在 sandboxed
iframe 中的 Surface。不得让客户端用任意 app ID、manifest、文件路径或 URL 冒充可运行实例。

持续推进直到实现、测试、文档、状态和聚焦提交全部完成。只有遇到必须破坏 v1 契约、改变六进程所有权、
修改已执行 migration，或必须新增生产信任根时，才停止并向用户报告证据与选项。

## 为什么下一步是 Web Bundle Surface

当前已经具备：

- App Registry 的 canonical manifest、immutable version 和 digest；
- Project App Installation 的 owner/project 约束、pinned version/digest 与稳定 UUIDv7 installation ID；
- Desktop App Library 的安装/卸载用户入口；
- `workos.surface.v1.SurfaceService`、`workos.artifact.v1.ArtifactService` 和 `runtime-host` scaffold。

当前仍缺：

- installation 引用的 App version 没有可验证、可读取的 bundle artifact；
- `runtime-host` 不能向 Core 解析 installation，更不能信任客户端提交的 launch descriptor；
- Gateway 不公开 Surface RPC 或 `/surfaces/` 资源路由；
- Surface session 没有持久身份、幂等、失效、关闭或 owner/device 绑定；
- Desktop 没有 App window/iframe，Surface capability 仍诚实报告 unavailable。

依赖顺序固定为：

```text
已完成：canonical manifest → immutable Registry version
  → 已完成：Project installation → pinned version/digest → stable app_instance_id
  → 本任务：immutable Web Bundle → installed-instance resolution
             → runtime-host Surface session → authenticated static assets
             → sandboxed Desktop window
  → 后续：App Bridge handshake + capability grant/token
  → 后续：Web Service/container runner + Workload/cgroup/Reliability
```

不要跳到 container runner，也不要用仓库内固定 HTML、data URL、外部 URL 或固定成功响应冒充自定义 App
Surface。必须由公开 Artifact API 写入真实持久 bundle，再经 Registry、Installation、Core 私有解析和
runtime-host 托管完成整条链路。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时本地 `main` 为 `bb1dc74`，工作树干净，领先 `origin/main` 20 个提交；执行时必须
  重新检查，不能把哈希或 ahead 数量当作永久事实。
- `docs/status.json` 中 App Registry、Project App Installation、Desktop 已为 working；Artifact 为
  contract-only，Runtime / Surface 为 scaffolded。
- Registry 位于 `internal/core/appregistry`，持久化 canonical manifest，但公开 `WorkOSApp` 不返回 raw
  manifest 或 runtime descriptor。
- installation 位于 `internal/core/project`，`project_app_installations.id` 是后续
  `app_instance_id`；只有 active、同 owner、同 Project 的 installation 才能启动 Surface。
- `api/proto/workos/surface/v1/surface.proto` 已有 `CreateSurface`/`CloseSurface`，但缺少外部写操作所需
  的 idempotency key 和 session 时间语义；`runtime-host` 当前注册 Unimplemented handler。
- `api/proto/workos/artifact/v1/artifact.proto` 已有 metadata/Create/Get/List contract；Core 当前注册
  Unimplemented handler。Gateway 已 allowlist ArtifactService，但 Core handler 尚无 working 行为。
- Gateway 当前只有指向 Core 的 reverse proxy；`SurfaceService` 与 `WorkloadService` 都返回 public 404。
  `config.URLs.Runtime` 已存在默认值，但 Gateway 尚未建立受控 Runtime upstream。
- `runtime-host` 只实现只读 node capability probe，未连接 PostgreSQL；`surface-broker` capability 为
  unavailable，container/native runner 继续 unavailable。
- `001` 至 `005` migration 均已在持久验收 volume 执行并受 checksum 保护，禁止修改。下一编号预期从
  `006` 开始。
- 当前验收 volume 含用户已有数据和 6 个历史 migration scratch database；不得删除 volume、TRUNCATE、
  broad DELETE、wildcard DROP 或顺手清理历史数据。

## 凭据与安全边界

- 本任务不需要真实 DeepSeek、OpenAI、GitHub 或其他 Provider Key。
- 不得使用、保存、转述、验证或尝试恢复聊天中曾出现的真实 Key；不得从 shell history、环境变量、本机
  文件或聊天历史搜集凭据。
- DeepSeek 回归只使用仓库已有的本地 fixture 假凭据，禁止访问真实 Provider 网络。
- Bundle 是不可信用户代码/内容：不得记录文件内容、HTML、JS、完整文件名列表或原始请求；错误响应不得
  回传 SQL、DSN、本地路径、canonical manifest 或 bundle bytes。
- App iframe 不得获得 WorkOS origin 权限、cookie、模型凭据或任意系统能力。本任务不签发 bridge token，
  不实现 MessageChannel/App Bridge，不把 requested permissions 当作 grant。
- `SurfaceSession.bridge_token` 必须保持空，`clipboard`/`file_picker` 等未实现能力必须为 false；
  不得生成一个没有验证方的装饰性 token。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`CONTRIBUTING.md`；
   - `docs/structure.md` 中 App、App Runtime、Surface、Desktop、Gateway、数据存储和第一版产品边界；
   - `docs/architecture/implementation.md`、`docs/decisions/0001-foundation-boundaries.md`；
   - `docs/status.json`；
   - `docs/tasks/20260823-app-manifest-registry.md`；
   - `docs/tasks/20260825-project-app-installation.md` 及对应实现/审核 Prompt；
   - `api/proto/workos/app/v1/app.proto`、`installation.proto`、`artifact/v1/artifact.proto`、
     `surface/v1/surface.proto`、`workload/v1/workload.proto`；
   - `schemas/workos-app-manifest-v1.schema.json`；
   - `internal/core/appregistry`、`internal/core/project`、`internal/core/orchestration` 及测试；
   - `cmd/workos-core/main.go`、`cmd/runtime-host/main.go`、`internal/runtime`；
   - `internal/gateway`、`internal/platform/config`、`compose.yaml`、`Makefile`；
   - `internal/platform/migrations`、001 至 005、`sqlc.yaml`；
   - `sdk/protocol`、`sdk/agent-sdk`、`sdk/surface-sdk`、`clients/app-host`、
     `clients/window-manager`、`apps/desktop-web`；
   - 现有 integration、restart 和 Playwright E2E。
2. 运行并记录：

   ```sh
   git status --short --branch
   git log --oneline --decorate -10
   git branch -vv
   git diff --check
   ```

   保留所有既有改动；不得 reset、rebase 或覆盖用户文件。

3. 从当前本地 `main` 创建独立分支，建议 `feat/minimal-web-bundle-surface`。禁止直接在 `main` 实现，
   不要 merge 或 push。
4. 从 `docs/tasks/TEMPLATE.md` 创建
   `docs/tasks/20260825-minimal-web-bundle-surface.md`，状态先设为 active，写清：
   - Artifact、Registry、Installation、Runtime 各自的唯一事实与 owner；
   - public/private RPC、Gateway 路由和信任边界；
   - bundle limits/digest、session 幂等/TTL/revocation；
   - iframe sandbox/CSP、错误映射、验收和明确不在范围内的能力。
5. 记录基线并运行：

   ```sh
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败必须保留证据并判断归属；禁止通过删除 PostgreSQL volume、放宽断言、跳过服务或固定成功响应
   绕过。

## 目标用户链路

完成并证明以下真实链路：

```text
测试/开发者客户端
  → Gateway public ArtifactService.CreateArtifact
  → Core identity + bounded WebBundleContent validation
  → Core Artifact-owned immutable metadata/files + durable idempotency

RegisterApp
  → canonical manifest runtime.type=web-bundle
  → neutral ArtifactDirectory verifies same-owner artifact + exact digest
  → immutable Registry version

InstallApp
  → active Project installation
  → pinned app version + manifest digest + UUIDv7 app_instance_id

Desktop App Library: Open
  → Gateway public SurfaceService → runtime-host
  → runtime-host identity + durable idempotent session command
  → private Core SurfaceLaunchResolverService
  → Core verifies active owner/project installation
  → exact pinned Registry manifest → exact immutable bundle artifact
  → runtime-host persists owner/device-bound Surface session
  → Gateway /surfaces/<session>/... → runtime-host
  → Core revalidates active installation and returns one bounded asset
  → CSP/sandboxed iframe inside WorkOS window

Close / uninstall / expiry
  → subsequent asset requests fail closed
```

E2E 必须上传至少两个文件（例如 `index.html` + `app.js`）的合成 bundle，并在 iframe 内执行脚本后显示唯一
文本，证明不是 Desktop 固定 HTML。随后关闭或卸载并证明旧 Surface URL 不再可用。

## 固定进程与模块边界

采用以下边界，除非仓库证据证明必须写 ADR：

- `workos-core Artifact` 拥有 immutable bundle metadata/content；它不托管公开 Surface URL。
- `workos-core App Registry` 只通过中立 `ArtifactDirectory` port 校验 bundle reference；禁止直接 SQL
  查询 Artifact 表。
- `workos-core Project Installation` 仍是 active installation 的唯一事实；不得复制成 Runtime 的第二套
  安装权威。
- `internal/core/orchestration` 组合 Installation、Registry launch descriptor 与 Artifact read，向
  `runtime-host` 暴露一个版本化、private、canonical Core resolver RPC。
- `runtime-host Surface Broker` 拥有 Surface session、TTL、Close 和静态 HTTP 托管；它不得查询 Core
  schema、Registry table 或 Project table。
- Gateway 只把 public `SurfaceService` 和 `/surfaces/` 资产路由到 Runtime；现有 Core public services
  继续到 Core。Workload/private resolver/host management RPC 继续 public 404。
- Desktop 只使用生成的 Surface client 和返回的相对 URL；不得拼接 Core/Runtime 私有地址。

依赖必须保持：

```text
core/artifact:    domain → application → ports ← postgres/transport
runtime/surface:  domain → application → ports ← postgres/core-client/http
core orchestration → neutral application ports
```

Domain 禁止导入 pgx、SQLC、Connect、Proto、HTTP、文件系统、ZIP/vendor SDK 或其他模块 adapter。跨模块、
跨进程只走 port/RPC；不得共享 mutable entity，不得跨模块 SQL。

## 协议优先

先完成所有 `api/proto` additive 变更，立即运行 `make generate`，确认 Go/TypeScript 生成物，再实现
producer/consumer。禁止手写同义 DTO。

### Artifact 的最小 Web Bundle 输入

在现有 `workos.artifact.v1` 上做 additive 扩展，不删除/复用任何字段号。推荐：

```text
WebBundleFile
  path
  content

WebBundleContent
  entrypoint
  repeated files

CreateArtifactRequest
  existing idempotency_key
  existing artifact metadata
  optional web_bundle

Artifact
  existing metadata
  additive total_size_bytes / file_count（如实现需要）
```

本任务只声明并实现明确的 `app.web-bundle.v1` artifact subtype：

- `project_id` 为空表示 owner-scoped、可被该 owner 的多个 Project installation 引用；
- `type`、`media_type` 使用单一文档化常量；
- client 只提交 title 与 bundle input；`id`、`content_ref`、`digest`、`created_at` 等 server-owned 字段
  非空时拒绝，不能静默信任；
- Core 生成 UUIDv7、UTC 时间、canonical digest 和不暴露文件系统路径的 opaque `content_ref`；
- `GetArtifact`/`ListArtifacts` 对该 subtype 提供真实 owner-scoped metadata 行为与有界分页；
- public RPC 永不返回文件 bytes；文件读取仅能经下述 installed-instance resolver；
- 其他 Artifact 类型未实现时必须明确返回 unsupported/unavailable，状态和文档不得声称通用 Artifact
  storage 已完成。

字段名可以依据现有契约微调，但不能把 bundle 放回 manifest bytes、数据库路径或客户端 URL。

### App Manifest v1 的 additive launch descriptor

`schemas/workos-app-manifest-v1.schema.json` 是唯一 Schema 事实源。只做兼容性扩展，推荐形态：

```yaml
runtime:
  type: web-bundle
  artifactId: 019...
  artifactDigest: sha256:...
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
```

要求：

- 为 `runtime.type` additive 增加 `web-bundle`，并在现有 `additionalProperties: false` 下声明 artifact
  reference 字段；不得另写第二套 manifest DTO/Schema。
- `runtime.type=web-bundle` 时 artifact ID/digest 必填，只允许受支持的 web-bundle surface；本切片只支持
  一个确定 entry surface，避免多 surface 选择歧义。
- 既有不含 bundle reference 的合法 manifest/version 不得被破坏或改写；它们仍可查询/安装，但
  `CreateSurface` 必须明确 `FailedPrecondition`，不能回退到固定页面。
- `ValidateManifest` 可保持离线结构/semantic 校验；`RegisterApp` 必须经 application port 验证
  same-owner artifact、subtype 与 exact digest 后才持久化 launchable version。
- foreign/unknown artifact 统一 `NotFound`；digest/subtype 不一致 fail closed，错误不泄漏其他 owner 的
  artifact 存在性。
- canonical manifest digest 必须覆盖新增 descriptor；public `WorkOSApp` 仍不返回 raw manifest。

### Public Surface 与 private resolver

对现有 `surface.proto` 只做 additive 调整：

- `CreateSurfaceRequest` 增加必填语义的 `idempotency_key`；
- `SurfaceSession` 可增加 `created_at`/`expires_at`，所有时间 UTC；
- 继续使用现有 `app_instance_id`、`project_id`、device/viewport/preferred renderer；
- `bridge_token` 保持空，未实现 capability flags 保持 false；
- `CloseSurface` 对第一次成功创建的 session 是 owner/device-scoped 且幂等。

新增独立 private service（名称可按证据微调，例如
`workos.surface.v1.SurfaceLaunchResolverService`），至少提供：

```text
ResolveWebBundle
  input: project_id + app_instance_id
  output: neutral immutable launch descriptor
          (app/version/manifest digest/artifact id/artifact digest/entrypoint)

ReadWebBundleAsset
  input: project_id + app_instance_id + normalized relative asset path
  output: bounded bytes + media type + digest/etag
```

owner/device 只能来自 Gateway 注入并由 Runtime 转发的受信 identity metadata，不得成为浏览器可提交的
owner 字段。private resolver 不得加入 Gateway allowlist。当前本地六进程均绑定 loopback；不要在本任务
发明一个未落地的 service token。若实现确实要求新的生产信任根，停止并先报告，而不是伪造认证。

## Web Bundle Artifact 不变式

不要接收 ZIP/TAR 后在服务端随意解压。本切片使用显式 `repeated WebBundleFile`，把 archive CLI/导入器留给
后续，缩小路径穿越和 decompression bomb 面。

至少实施并测试以下上限；常量可在任务记录中合理收紧，但不得放宽为无界：

- 最多 128 个 regular files；
- 总内容最多 2 MiB，单文件最多 512 KiB；
- path 最多 240 ASCII bytes，只允许安全的相对 POSIX segments；
- 拒绝空 path、绝对路径、反斜杠、NUL/控制字符、`.`/`..` segment、重复 slash、percent-encoded
  ambiguity、重复 path 和 Unicode/case-fold collision；
- entrypoint 必须是 bundle 内存在的 `.html` regular file；
- media type 只能由服务端按受控扩展名表决定，未知/可执行服务端类型拒绝；
- 文件顺序不影响 canonical digest；digest 使用长度前缀或等价无歧义编码覆盖版本标识、entrypoint、按
  path 排序后的 path 与完整 bytes，格式 `sha256:<lowercase hex>`；
- 同一逻辑文件集、不同提交顺序得到相同 digest；任何 path/content/entrypoint 改变都得到不同 digest。

`CreateArtifact` 的 `(owner_user_id, idempotency_key)` 是持久唯一命名空间：

- 同 key + 同 canonical request 返回第一次 Artifact；
- 同 key + 不同 metadata/entrypoint/path/content digest 返回 `Aborted`；
- validation/数据库失败不消费 key；
- 并发由 PostgreSQL unique/transaction 裁决，禁止进程内 mutex；
- metadata、files 和 idempotency result 在一个事务提交或全部回滚。

## Core installed-instance resolution

Core private resolver 每次必须从权威事实解析，不能信任 Runtime session 中的 app/version/artifact snapshot：

1. 按 identity owner + `project_id` + `app_instance_id` 查询 active installation；
2. 验证 Project active；unknown、foreign、archived、tombstoned 或 Project 不匹配统一 `NotFound`；
3. 用 installation 的 pinned `app_id/version` 读取 exact immutable Registry version；
4. 验证 Registry `manifest_digest` 与 installation snapshot 完全一致；不一致视为内部数据损坏并 fail closed；
5. 从 canonical manifest 得到唯一受支持的 web-bundle descriptor；
6. 通过 Artifact application port 验证 same-owner artifact、subtype 与 exact digest；
7. `ReadWebBundleAsset` 只读取该 descriptor 指向 artifact 中已规范化的一个文件。

`runtime-host` 的每次 asset request 都要调用 Core 重新验证 active installation；不得只在 Create 时验证后
永久缓存权限。这样 uninstall/archive 后的新请求立即失败。允许一个已在线性化通过验证的 in-flight 响应
完成，但提交后的后续请求必须 404。

Registry、Project 和 Artifact repository 不得互相 SQL/join。需要的新读取方法通过各模块 application port
暴露，再由 orchestration 组合；不得让 Runtime 导入任何 `internal/core/*` package。

## Surface session 与 migration

新增 forward-only migrations，预期：

- `006_web_bundle_artifacts.sql`：owner 为 `workos-core Artifact`；
- `007_surface_sessions.sql`：owner 为 `runtime-host Surface Broker`。

如果编号因执行时仓库变化而冲突，重新按当前下一编号命名；禁止改动、squash 或覆盖 001 至 005。

### Artifact tables

Core Artifact 表至少持久化 owner、UUIDv7 ID、idempotency key/request digest、type/title/media type、opaque
content ref、canonical digest、entrypoint、file/size counts、UTC created time。文件表保存规范化 path、
server-derived media type、bytes、size/digest，并只 FK 到同一 Artifact-owned fact。

不要建立 Artifact → Project/Registry/Installation 的跨模块 FK，不要在 Artifact SQL 引用其他模块表。
如需要 Project-scoped generic artifact，留到后续；本任务 owner-scoped bundle 不需要伪造 Project 归属。

### Runtime Surface tables

`workos_runtime.surface_sessions` 至少持久化：

- UUIDv7 session ID；
- owner user ID、device ID、`(owner,idempotency_key)`、canonical request digest；
- project ID、installation/app instance ID；
- renderer 与 Core 返回的 immutable descriptor snapshot；
- 相对 URL、UTC created/expires/optional closed time。

Runtime 表不得 FK Core 的 user/project/installation/artifact 表，也不得查询它们。数据库约束至少保证
ID/digest/path/renderer/time 形态、owner key 唯一和 `closed_at >= created_at`。

### Session 语义

- `CreateSurface` 只接受有效 UUIDv7 installation/project、非空安全 idempotency key、明确 device class、
  合理 viewport；preferred renderer 仅允许 unspecified 或 web-bundle。
- 同 key 同 canonical request 精确返回第一次 session；同 key 不同 Project/installation/device/viewport/
  renderer 返回 `Aborted`。失败解析不消费 key。
- session 默认 TTL 建议 15 分钟，并通过 `WORKOS_SURFACE_SESSION_TTL` 配置，启动时验证合理上下界；
  不得静默接受零、负数或超大 TTL。
- `CloseSurface` 首次设置 `closed_at`；same owner/device 重复 close 成功且不改变第一次结果。unknown/foreign
  统一 `NotFound`。
- expired/closed session 不再提供 asset；旧 Create key replay 仍返回第一次 session snapshot，不自动复活。
  重新打开必须使用新 key。
- Surface session 与 idempotency 必须在 runtime-host restart 后保持；重启不得让已关闭 session 恢复。
- 多 repository/runtime instance 并发同 key只能产生一个 session fact。

## Gateway 与静态资源安全

Gateway 建立两个明确 upstream：

- 现有 public Core allowlist → Core；
- 仅 `/workos.surface.v1.SurfaceService/` 和 `/surfaces/` → Runtime。

要求：

- 两条路径都应用同一 device-session gate，并删除客户端伪造的 identity headers 后写入可信 owner/device；
- `WORKOS_RUNTIME_URL`/对应 config 必须实际加载并在 Gateway 启动时 fail-fast 校验；
- 不得把 `WorkloadService`、private resolver、runtime health/debug 或任意 `/workos.*` 路径顺带暴露；
- Runtime/Gateway upstream 失败返回净化 `503`，不能落入 Desktop SPA fallback；
- `SurfaceSession.url` 是同 origin 相对路径，只含 session UUID 和 asset path，不含 Core/Runtime 地址、
  owner/project/app ID、签名密钥或 bearer query；
- 本切片明确采用“Gateway 已认证 identity + owner/device-bound server-side session”，不是 bearer/signed
  URL。session ID 不是授权凭据，猜到 ID 仍不能跨 owner/device 读取；
- `/surfaces/<session>/` 只允许 GET/HEAD；closed/expired/foreign/uninstalled/unknown asset 统一 404，
  Core 临时不可用返回净化 503。

静态响应至少设置：

```text
Content-Security-Policy:
  default-src 'none';
  script-src 'self';
  style-src 'self' 'unsafe-inline';
  img-src 'self' data:;
  font-src 'self';
  connect-src 'none';
  object-src 'none';
  base-uri 'none';
  form-action 'none';
  frame-ancestors 'self';
  worker-src 'none'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Cache-Control: no-store
```

不要使用目录 SPA fallback 读取任意文件。`/` 只映射到已验证 entrypoint；其他 path 精确匹配 Artifact file。
必须测试原始/双重编码 traversal、反斜杠、dot segment、重复 slash、未知 MIME、非 GET/HEAD 和 Desktop
static root 逃逸。

## Desktop 最小 UX

- `sdk/protocol` 导出新增生成类型；`sdk/agent-sdk` 的统一 clients 加入 `SurfaceService` 与 Artifact
  client（若 E2E/setup 需要）；不得在组件手写 RPC DTO。
- 已安装且 launchable 的 App 在 App Library 显示 `Open`；未安装或无受支持 bundle descriptor 的 App
  不得显示虚假可打开状态。若 launchability 只能在 Create 时裁决，可以显示 Open，但
  `FailedPrecondition` 必须变成清晰反馈，不能打开空窗口。
- Open 使用 `crypto.randomUUID()` idempotency key、当前 Project ID、installation ID、desktop device
  class 和实际 viewport；重复点击/乱序响应不能创建重复窗口或污染当前 Project。
- 成功后在现有 Window Manager 内创建 App window，并用返回的相对 URL 渲染 iframe；不得新开标签页。
- iframe 必须 `sandbox="allow-scripts"`，不得包含 `allow-same-origin`、forms、popups、top navigation、
  downloads 或 storage 权限；设置 `referrerPolicy="no-referrer"`。
- Desktop 不读取/转发 `bridge_token`，不注入 App Bridge，不向 iframe 暴露 WorkOS clients。
- 关闭窗口调用 `CloseSurface`；Project 切换、App 卸载或组件卸载要使旧异步 response inert，并 best-effort
  关闭相关 session。后端 active-installation revalidation 是最终安全保证，不能只依赖 UI。
- 有 opening/loading/error/retry/closed 状态；`NotFound`、`FailedPrecondition`、`Unavailable` 映射为
  用户可理解且净化的提示。
- Agent Center window 现有行为不能因多窗口渲染而回归；为 window state 引入明确的 discriminated kind
  或等价结构，不得让所有窗口继续错误渲染 Agent Center 内容。

本任务不持久化 Desktop window layout。页面 reload 后允许窗口关闭，但用户重新点击 Open 必须恢复真实
Surface；Runtime/Core restart 后既有未过期 session URL 必须仍可读取。

## Connect 与错误映射

- missing identity → `Unauthenticated`；
- malformed UUID/idempotency/path/viewport/device/renderer/bundle shape → `InvalidArgument`；
- unknown/foreign/archived Project、foreign/tombstoned installation、foreign artifact/session →
  `NotFound`；
- same key different canonical request → `Aborted`；
- App 已安装但没有受支持的 Web Bundle descriptor → `FailedPrecondition`；
- Core/Runtime/PostgreSQL 暂时不可达 → `Unavailable`；
- 数据不变量破坏、digest 漂移、内部读取错误 → 净化 `Internal`。

HTTP asset handler 对外只用 404/405/503 和固定短消息；不得把 Connect/SQL 错误或 manifest/bundle 内容写入
body。错误分类必须在 domain/application 建模，不得通过匹配 constraint 名或错误字符串决定业务结果。

## 必须测试的行为

### Artifact / Registry

- bundle path/file count/单文件/总大小/entrypoint/MIME 全边界与恶意输入；
- canonical digest 对文件顺序稳定，对 path/content/entrypoint 改变敏感；
- UUIDv7、UTC、opaque content ref，raw bytes 不进入 metadata/log/error；
- same key replay、same key conflict、失败 key 未消费、双 repository 并发；
- metadata + 全部 files + idempotency 同事务回滚；
- owner 隔离，Get/List paging 的 default/clamp/negative/exact-final-page；
- Registry web-bundle descriptor Schema/cross-field policy、same-owner artifact/digest 校验；
- 既有 legacy manifest 仍可注册/查询，缺 launch descriptor 时 Surface fail closed。

### Core resolver

- active same-owner/same-project installation 成功；
- foreign owner、错误 Project、archived Project、tombstone、未知 installation 均净化 NotFound；
- exact pinned version 与 manifest digest 被验证，Registry current 后移不改变 launch；
- artifact foreign/missing/type/digest mismatch fail closed；
- asset path 只能访问被 pinned descriptor 引用的 bundle，不能任意读其他 Artifact。

### Runtime / PostgreSQL

- Create/Close 输入、错误码、TTL、expiry、same key replay/conflict；
- UUIDv7 session、UTC、owner/device 绑定；
- 并发 same key、不同 key、Create/Close/asset race，运行 `go test -race` 覆盖内存协作部分；
- restart 后 active session/idempotency/closed state 保持；
- uninstall/archive 后后续 asset 失败；
- Runtime 不查询或 FK Core tables。

### Gateway / HTTP security

- Surface RPC 与 asset 路由只到 Runtime，现有 Core RPC 仍到 Core；
- spoofed identity headers 被覆盖，缺 device session fail closed；
- Workload/private resolver/其他 runtime 路径继续 404；
- CSP、nosniff、referrer、no-store、server-derived MIME、GET/HEAD；
- traversal/encoding/unknown/closed/expired/foreign/uninstalled 都不泄漏。

### Desktop / E2E

- App Library component tests：Open loading/success/error、重复点击、Project switch、stale Promise、window close、
  uninstall close；
- iframe sandbox/referrer policy 和 Surface URL 使用；
- Window Manager 同时正确渲染 Agent Center 与 App Surface；
- 浏览器 E2E 通过真实 Gateway：
  1. CreateArtifact 写入合成 `index.html` + `app.js`；
  2. RegisterApp 引用 exact artifact digest；
  3. InstallApp；
  4. 点击 Open；
  5. iframe 显示脚本生成的唯一文本；
  6. reload 后重新 Open；
  7. Close 或 Remove 后旧 URL 返回 404；
- E2E 不得直接写数据库冒充用户链路；数据库只读断言可作补充。

## Migration 与持久验收

- 006/007 必须能从 pristine database 与当前持久 acceptance volume 前向执行。
- 为 001 至 005 增加或保留 checksum/逐字节不变回归；绝不修改历史 migration。
- 两个 migration 分别只有一个明确 owner；Core Artifact SQL 与 Runtime Surface SQL 使用独立 SQLC package。
- 复用已加固 scratch database lifecycle；测试前后记录精确 scratch database 集合，连续运行 migration
  tests 两次必须零新增。
- 集成测试 fixture 使用 run-unique UUID/prefix；清理只能精确匹配本轮 IDs，在单事务中按 FK 顺序进行。
- 连续两次 `make test-integration` 前后记录 Artifact/session/Registry/installation/event/outbox 行数；
  区分已有固定增量和本任务增量，不能只看退出码。
- 禁止删除 `workos_workos-postgres` volume 或现有 6 个历史 scratch database。

## 明确不在范围内

- ZIP/TAR 上传器、Git clone、image pull/build、签名、软件供应链验证、外部 Object Store。
- `runtime-host` container/native/background/remote-browser runner、Podman、Workload Start/Stop。
- Web Service reverse proxy、Declarative Surface、WebRTC/Remote Native Surface。
- App Bridge、MessageChannel、bridge/capability token、permission grant、approval、budget、Credential Vault。
- iframe clipboard/file picker、same-origin storage、service worker、network access。
- App upgrade/downgrade、自动跟随 Registry current、system/trusted App 公共路径。
- Surface attach/resize/suspend/resume、多设备迁移、窗口布局持久化、Dock pin。
- Reliability enforcement、Indexer/RAG、Mobile、LAN pairing、生产 device authentication。
- 修改 Harness、DeepSeek、Task Router 或 Provider binding 语义。
- 真实 Provider 网络或真实 Key。

未实现项必须继续返回真实的 unavailable/unimplemented；不能因为 web-bundle 子能力 working 就把 container
runner、App Bridge 或通用 Artifact storage 描述为完成。

## 文档与状态

完成时同步：

- `docs/tasks/20260825-minimal-web-bundle-surface.md`：最终协议、digest/limits、表 owner、private resolver、
  session/idempotency/TTL、Gateway/iframe 安全、实际命令、资源计数、未决风险和下一步；
- `docs/architecture/implementation.md`：增加 Artifact → Registry → Installation → Core resolver →
  Runtime Surface → Gateway/Desktop 的真实链路及表/进程所有权；
- `docs/status.json`：
  - Runtime / Surface 只有在真实跨进程、restart、HTTP security 和浏览器 E2E 成立后才能升为 working，
    evidence 必须限定为 Web Bundle；
  - Artifact 若只支持 Web Bundle subtype，应按事实标为 scaffolded 或用 evidence 明确限定，不能声称通用
    Artifact 已完成；
  - Desktop evidence 增加 sandboxed Web Bundle window；
  - container/native runner、App Bridge/capability 继续 unavailable；
- README 状态区块只用 `make docs`/`make generate` 生成，禁止手改；
- `docs/structure.md` 原则上不改；若必须偏离产品主线，先写 ADR。

任务记录中的下一任务建议为：
**App Bridge handshake + least-privilege capability grant/token for Web Bundle Surface**。它必须继续通过
WorkOS Agent API/Harness Broker，不得让 iframe 获得 provider credential。rootless Web Service/container
runner 可作为另一条后续独立任务。

## 验收顺序

### 基础与生成

```sh
make generate
make generate
git diff --check
make check
buf breaking --against '.git#branch=main'
```

第二次 generation 后必须无新增差异；Proto、JSON Schema、SQLC、Go、TypeScript 和 README/status 生成物
一致。

### 数据与纵向

```sh
make test-integration
make test-integration
make test-deepseek-fixture
make test-e2e
```

更新 `make test-integration`，确保启动 `runtime-host`，并把 Artifact + Surface seed/verify 纳入 restart 阶段。
DeepSeek 门禁只使用 target 自带 fixture credential。

### 定向安全与并发

至少实际运行并记录：

```sh
go test -race ./internal/runtime/...
go test ./internal/core/artifact/... ./internal/core/appregistry/... ./internal/core/orchestration/...
```

如果本机依赖必须通过仓库 Docker toolchain 执行，可使用等价容器命令；任务记录必须写真实命令和退出结果，
不能只复制本 Prompt。

### 最终一致性

```sh
git diff --check
git diff --check main...HEAD
git diff --exit-code main -- internal/platform/migrations/files/001_foundation.sql
git diff --exit-code main -- internal/platform/migrations/files/002_app_registry.sql
git diff --exit-code main -- internal/platform/migrations/files/003_app_registry_idempotency.sql
git diff --exit-code main -- internal/platform/migrations/files/004_project_app_installations.sql
git diff --exit-code main -- internal/platform/migrations/files/005_project_app_installation_request_owner.sql
git status --short --branch
```

另外确认：

- 只有预期的 006/007 migration，且 owner、checksum、pristine/current-volume upgrade 证据完整；
- `docs/structure.md` 无意外变化；
- 无 root-owned 文件、临时 bundle、测试报告、raw user content 或 credential；
- Surface URL 不含 bearer/secret，`bridge_token` 为空，iframe 无 `allow-same-origin`；
- Workload/container/native capability 仍 unavailable；
- worktree 最终干净，功能分支提交聚焦；未 merge、未 push。

## 完成与交接

- 完成所有范围内实现，不以 TODO、空 adapter、内存固定页面、data URL、外部 URL 或只测 fake repository
  冒充 working。
- 只有真实 Artifact persistence、Core resolver、Runtime session/static host、Gateway、Desktop iframe、restart
  与 revocation E2E 全部成立，才能把 Runtime / Surface 标为 working。
- 在 `feat/minimal-web-bundle-surface` 创建聚焦提交；提交信息建议：
  `feat: implement minimal web bundle surfaces`。
- 最终交接必须写明提交哈希、实际运行命令、006/007 checksum 与前向升级证据、两次 integration 资源计数、
  HTTP/iframe 安全证据、未决风险、下一任务依赖和 worktree 状态。
- 不要 merge 到 `main`，不要 push；留给审核者静态复审和本地 `--ff-only` 合并。
