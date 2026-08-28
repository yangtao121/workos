# Task: Minimal Web Bundle Surface vertical slice

- 状态：done（实现与首轮审核修复完成），**等待合并就绪复审**：行为复审基本通过后，2026-08-28 合并
  就绪复审（`docs/prompts/20260828-fix-web-bundle-merge-readiness.md`）确认分支仍不满足
  `--ff-only` 合并条件（历史污染 blob、acceptance-volume 计数 flake、dbtransient 健壮性）。三项
  修复在干净候选分支 `feat/web-bundle-surface-merge-candidate` 完成，合并证据以下方
  "2026-08-28 合并就绪修复"一节与该候选 HEAD 为准，不再引用污染分支的提交。
- 历史状态：done 曾于 2026-08-25 宣布（实现提交 `9ad7ffa`），被 2026-08-28 审核撤销并降级为
  active；同日修复后凭新证据恢复 done（修复提交 `2f1cad8`，见下方"2026-08-28 审核阻断项/修复记录"
  两节）。原始结论中"无 root-owned/临时文件"与"并发已覆盖"两项陈述被审核证伪（阻断项一/八），
  已在原交接处标注。
- 事实漂移说明（2026-08-28 合并就绪复审开始时核对）：合并就绪 prompt 记载被审核 HEAD 为
  `2f1cad8`、领先 `main` 3 个提交；实际核对时污染分支 `feat/minimal-web-bundle-surface` 已被
  审核者的 prompt 提交推进到 `f55a870`、领先 4 个提交（新增仅 `20260828-fix-web-bundle-merge-
readiness.md` 文档）。物化以分支最终树 `f55a870` 为准，与 prompt 记载的 `2f1cad8` 树只差该
  prompt 文档；两个提交号均作为审核历史保留。
- Owner/Agent：web bundle surface builder
- 进程/模块：workos-core `internal/core/artifact`（新增）、`internal/core/appregistry`（web-bundle launch descriptor）、`internal/core/project`（active installation 解析）、`internal/core/orchestration`（private SurfaceLaunchResolverService）；runtime-host `internal/runtime/surface`（新增 Surface Broker）；workos-gateway（Runtime upstream + `/surfaces/` 路由）；desktop-web（App window + sandboxed iframe）
- 依赖：App Registry（`002`/`003`）、Project App Installation（`004`/`005`）、`workos.surface.v1`/`workos.artifact.v1` contract、Gateway identity 注入模式

## 目标与范围

由真实已安装实例驱动的受限 Web Bundle Surface 纵向切片：

```text
测试/开发者客户端
  → Gateway public ArtifactService.CreateArtifact
  → Core identity + bounded WebBundleContent validation
  → Core Artifact-owned immutable metadata/files + durable idempotency

RegisterApp
  → canonical manifest runtime.type=web-bundle（artifactId/artifactDigest）
  → neutral ArtifactDirectory verifies same-owner artifact + exact digest
  → immutable Registry version

InstallApp → active Project installation（pinned version/digest/app_instance_id）

Desktop App Library: Open
  → Gateway public SurfaceService → runtime-host
  → runtime-host identity + durable idempotent session command
  → private Core SurfaceLaunchResolverService（active installation → pinned
    registry version → exact manifest digest → exact bundle artifact）
  → runtime-host persists owner/device-bound Surface session
  → Gateway /surfaces/<session>/... → runtime-host
  → Core revalidates active installation，返回单个有界 asset
  → CSP/sandboxed iframe inside WorkOS window

Close / uninstall / expiry → subsequent asset requests fail closed（404）
```

在范围内：`app.web-bundle.v1` Artifact subtype（显式 `repeated WebBundleFile` 上传、canonical digest、持久幂等、owner-scoped Get/List）；manifest v1 additive web-bundle launch descriptor（schema + cross-field policy + same-owner/digest 校验）；Core private resolver（每次 asset 请求重新验证 active installation）；runtime-host Surface session（TTL/Close/幂等/restart 持久化）与静态资产托管（CSP/nosniff/no-store、server-derived MIME、仅 GET/HEAD）；Gateway Runtime upstream 与身份注入；Desktop Open/窗口/iframe 与错误映射；单元/集成/迁移/并发/重启/HTTP 安全/浏览器 E2E 测试；文档与状态同步。

不在范围内：ZIP/TAR 上传器、container/native runner、Web Service/Declarative/Remote Native Surface、App Bridge/MessageChannel/bridge token/capability grant（`bridge_token` 保持空，`clipboard`/`file_picker` 保持 false）、iframe storage/network、App upgrade/downgrade、Surface attach/resize/suspend、窗口布局持久化、Reliability/Indexer/Mobile、真实 Provider 网络或 Key。未实现能力继续真实返回 unavailable。

## 协议/数据影响

- `api/proto/workos/artifact/v1/artifact.proto` additive：`WebBundleFile`、`WebBundleContent`、`CreateArtifactRequest.web_bundle`、`Artifact.total_size_bytes`/`file_count`。
- `api/proto/workos/surface/v1/surface.proto` additive：`CreateSurfaceRequest.idempotency_key`、`SurfaceSession.created_at`/`expires_at`。
- `api/proto/workos/surface/v1/surface_resolver.proto` 新增（private，不进 Gateway allowlist）：`SurfaceLaunchResolverService`（`ResolveWebBundle`/`ReadWebBundleAsset`）。
- `schemas/workos-app-manifest-v1.schema.json` 兼容性扩展：`runtime.type` 增加 `web-bundle`，`runtime.artifactId`/`runtime.artifactDigest`；Go policy 校验 cross-field 规则。
- migration `006_web_bundle_artifacts.sql`（owner：workos-core Artifact）：`workos_core.web_bundle_artifacts`/`web_bundle_files`/`web_bundle_artifact_requests`。
- migration `007_surface_sessions.sql`（owner：runtime-host Surface Broker）：`workos_runtime.surface_sessions`/`surface_session_requests`。
- sqlc 新增 `artifactdb`、`surfacedb` 两个 package；001–005 逐字节不变。
- compose/Makefile：`runtime-host` 进入 integration/E2E up 列表与 restart 阶段；Gateway 增加 `WORKOS_RUNTIME_URL`；runtime-host 增加 `WORKOS_SURFACE_SESSION_TTL`/`WORKOS_CORE_URL`。

## 关键不变式

- Bundle 上限：≤128 files、总 ≤2 MiB、单文件 ≤512 KiB、path ≤240 ASCII bytes（安全相对 POSIX segment，拒绝空/绝对/反斜杠/控制字符/dot-segment/重复 slash/percent-encoding/重复与 case-fold collision）；entrypoint 必须是 bundle 内 `.html`；MIME 仅由服务端按受控扩展名表派生；canonical digest 长度前缀编码覆盖版本标识/entrypoint/按 path 排序的 path+bytes，文件顺序无关。
- Artifact `(owner_user_id, idempotency_key)` 持久唯一：同 key 同 canonical request 精确 replay；不同 request `Aborted`；失败不消费 key；metadata/files/idempotency 单事务。
- Registry：`runtime.type=web-bundle` 时 artifactId/artifactDigest 必填、image/command/port 禁止、仅允许单一 web-bundle surface；Register 经 `ArtifactDirectory` 验证 same-owner artifact + exact digest；foreign/unknown 统一 `NotFound`；legacy manifest 不受影响，缺 descriptor 时 `CreateSurface` `FailedPrecondition`。
- Core resolver 每次从权威事实解析（active installation → exact pinned version → manifest digest 一致 → same-owner artifact + exact digest），不信任 Runtime snapshot。
- Runtime：session TTL 默认 15m（`WORKOS_SURFACE_SESSION_TTL`，启动校验上下界）；`(owner,idempotency_key)` 持久幂等；Close owner/device-scoped 幂等；expired/closed/foreign/uninstalled asset 统一 404；Core 不可用净化 503；不查询/FK Core 表。
- Gateway：仅 `SurfaceService` 与 `/surfaces/` 到 Runtime，同一 device-session gate 与 identity 覆盖；`SurfaceSession.url` 为同 origin 相对路径；asset 仅 GET/HEAD。
- Desktop：iframe `sandbox="allow-scripts"`（无 `allow-same-origin`）、`referrerPolicy="no-referrer"`；window state 引入 discriminated kind；不读取/转发 `bridge_token`。

## 验收

- [x] 行为测试（单元/传输/集成/迁移/并发 `-race`/重启/HTTP 安全/浏览器 E2E）
- [x] `make generate`×2 无差异、`make check`、`buf breaking --against '.git#branch=main'`
- [x] `make test-integration`×2（含 runtime-host、资源计数、scratch database 零新增）
- [x] `make test-deepseek-fixture`、`make test-e2e`
- [x] 文档与 `docs/status.json`（README 状态区块仅经 `make generate`）

## 2026-08-28 审核阻断项（修复中）

审核（`docs/prompts/20260828-review-minimal-web-bundle-surface.md`）确认以下合并阻断项：

1. 分支误跟踪根目录 `restart`（100755、~19 MiB、ELF x86-64 Go 可执行文件，blob
   `2dd6362e90c544487d391314732aa4063eabee41`）：是本地 `go build ./tests/restart` 的默认输出，
   与上文"无 root-owned/临时文件"陈述矛盾；污染 Git/Docker build context。
2. Surface canonical create digest 未覆盖 Gateway 注入的可信 `DeviceID`：同 owner/key 不同 device
   的第二次请求 digest 相同，裁决依赖 session 的 device lookup（NotFound），既非精确 replay 也非
   稳定 `Aborted`，破坏持久幂等契约。
3. `preferredRendererFromProto` 把 `WEB_SERVICE`/`DECLARATIVE`/`REMOTE_NATIVE` 及未知 enum 数值
   全部折叠为空串并被当作 unspecified 默认成 Web Bundle，未按契约返回 `InvalidArgument`；既有
   domain 测试只传字符串未走真实 Proto 转换。
4. `ValidSessionUUID`/`ValidArtifactUUID` 只检查 36 字符外形，不检查 version 7/variant/canonical
   lowercase，与文档声明的 UUIDv7 边界不符；`ValidViewport` 普通比较使 `pixel_ratio=NaN`
   （protobuf binary 可表达）被接受进 canonical digest。
5. 错误分类：Runtime application 原样透传 repository 错误、transport 落默认 `Internal`；asset
   handler 把 `domain.ErrUnavailable` 以外全部映射 404——Runtime/Core PostgreSQL 暂时中断被伪装成
   "资源不存在"；private Core resolver 依赖故障也可能映射成 Internal。与"暂时不可达=净化
   `Unavailable`"的原任务契约冲突。
6. iframe 仅靠 `sandbox="allow-scripts"` 属性约束；Runtime 响应 CSP 无 `sandbox` directive，
   用户 HTML/JS 托管在 WorkOS 同 origin 可导航 URL 下，顶层打开即恢复 origin 权限——服务端未强制
   隔离。
7. `ValidateGateway` 使用 `url.ParseRequestURI`，相对 URI/非 HTTP(S) scheme 可通过，违背
   `WORKOS_RUNTIME_URL` 启动 fail-fast 要求。
8. 任务要求的并发证据实际缺失：`go test -race ./internal/runtime/...` 无任何并发 goroutine；
   `TestArtifactRepositoryConcurrency` 的 `artifactSequence++` 在无锁 goroutine 中递增（测试代码
   自身数据竞争）；Artifact 事务中途失败回滚无真实 PostgreSQL 故障注入断言。
9. Desktop 整个组件卸载时已打开的 App Surface 无 best-effort `CloseSurface`，只能等 TTL。

修复方式、新增测试与真实命令/结果见下方"2026-08-28 修复记录"；006/007 migration 与 001–005 逐字节
保持不变。

## 2026-08-28 修复记录

逐项修复与新增证据（命令均于本机 Docker Go/Node 工具链内实际运行，exit 0）：

1. **误提交二进制**：`git rm restart` 删除根目录生成 ELF（blob `2dd6362e`，~19 MiB）；根
   `.gitignore` 增加精确 `/restart` 规则（仅根路径，`tests/restart/` 源码保持跟踪）。修复后扫描
   全部 tracked files 无 ELF/大文件/临时产物。本节同时更正原交接中"无 root-owned/临时文件"的
   不实陈述。
2. **Device-bound 幂等**：`domain.CreateRequestDigest` 明确覆盖 Gateway 注入的可信 `device_id`
   （仍在 identity context，绝不进入 public request body）。同 owner/key 不同可信 device、或
   project/installation/device class/viewport/renderer 任一不同 → 稳定 `Aborted`（由 key 的
   stored digest 裁决，而非 session device lookup）；同 owner/key/device/相同 canonical request
   即使已 closed/expired 也精确 replay 第一次 snapshot。新增：domain digest 测试、application
   `TestSameKeyFromAnotherTrustedDeviceAborts`、真实 Connect handler 测试
   `TestCreateSurfaceBindsIdempotencyToTheTrustedDevice`、真实 PostgreSQL 集成
   `TestSurfaceSessionRepositoryConcurrency/SameKeyFromAnotherTrustedDeviceAborts`（双 pool）与
   经 runtime-host 直连双 device identity 的 `SurfaceIdempotencyBindsTrustedDevice`。
3. **Renderer fail closed**：`preferredRendererFromProto` 只接受 `UNSPECIFIED`（默认
   web-bundle）与 `WEB_BUNDLE`；`WEB_SERVICE`/`DECLARATIVE`/`REMOTE_NATIVE` 及未知 enum 数值在
   public Connect handler 边界直接 `InvalidArgument`——不调 Core resolver、不写 session、不消费
   key（随后同 key 合法 renderer 成功证明）。测试：`TestCreateSurfaceFailsClosedOnUnsupportedRenderers`
   （含 `SurfaceRenderer(99)`）+ 集成 `CreateSurfaceResolvesInstalledInstance` 经真实 Gateway。
4. **UUIDv7 与 viewport 边界**：runtime surface 与 artifact domain 各自建立 canonical UUIDv7
   validator（hyphen/lowercase hex/version nibble `7`/variant `[89ab]`），`ValidSessionUUID`、
   `ValidArtifactUUID`（Get ID 与 List cursor）及 `splitSurfacePath` 全部按 v7 拒绝（HTTP 畸形
   path 仍统一 404）；`ValidViewport` 用 `math.IsNaN/IsInf` 显式拒绝 NaN/±Inf，`0` 与 `-0` 归一
   为唯一 unspecified 语义（digest 中亦归一）。server generator 仍为 `ids.UUIDv7`（UTC）。只读
   核对验收 volume：21 个 surface_sessions、11 个 web_bundle_artifacts 全部为 canonical v7，
   非 v7 计数为 0，未做任何迁移/删除。新增 domain + Connect 测试覆盖 v1/v4/坏 variant/大写/NaN/Inf
   及失败 key 未消费。
5. **Transient unavailable 分类**：新增 `internal/platform/dbtransient`（按 Go 错误类型与
   SQLSTATE class 08/53/57/58 分类，不读 constraint 名或消息文本）；四个 postgres adapter
   （artifact/project/appregistry/runtime surface）在 port 边界把 transient 失败包装为各自
   `ports.ErrStoreUnavailable`；runtime Connect transport 映射 `CodeUnavailable`，asset handler
   映射净化 503（repository outage 不再伪装 404）；private Core resolver transport 把
   Project/Registry/Artifact store outage 映射净化 `CodeUnavailable`，runtime coreclient 转换为
   public `Unavailable`/503；digest drift、descriptor corruption、未知错误继续 Internal；
   Gateway upstream failure 继续固定 503。真实 adapter 证据（真实 pgx pool 指向真实关闭端口）：
   `TestRuntimeStoreOutageIsUnavailableNotMissing`（CreateSurface→Unavailable）、
   `TestCoreResolverDependencyOutageExposesUnavailable`（真实 project repo+真实 private
   resolver transport+真实 coreclient+真实 public handler 全链→Unavailable）、
   `TestRuntimeAssetOutageServes503`（503 + sandbox 头）。
6. **服务端强制 CSP sandbox**：`/surfaces/` 全部响应（含 HTML entrypoint、SVG、404、503）固定
   CSP 追加 `sandbox allow-scripts`，绝不包含 `allow-same-origin`/forms/popups/top-navigation/
   downloads/storage；既有 default-src/script-src/connect-src/frame-ancestors/nosniff/
   no-referrer/no-store/server-MIME 不变。单测 `TestAssetHandlerSandboxesEveryResponse` 与集成
   `AssetsAreServedWithSecurityHeaders` 断言 sandbox 存在且无危险 token；浏览器 E2E 把同一 live
   Surface URL 顶层打开：响应 CSP 含 `sandbox allow-scripts`、bundle script 仍执行、合成
   localStorage marker（`workos-e2e-synthetic-probe`，非真实 credential）读取抛异常证明页面
   仍是 opaque/sandboxed origin、无法读取 WorkOS origin storage；iframe 内 storage probe 同样
   opaque；Close/Remove 后旧 URL 404、无 SPA fallback 均保持。
7. **Gateway URL fail-fast**：`ValidateGateway` 要求 Core/Runtime upstream 均为 absolute
   `http`/`https` 且 host 非空（拒绝空值/相对路径/scheme-less/`ftp`/`http://`）；`gateway.New`/
   `newUpstreamProxy` 二次校验 target 形态，绕过 composition-root 也不能构造出明显无效 proxy。
   table-driven 测试：`TestValidateGatewayRejectsUnusableUpstreams`、
   `TestNewRejectsUnusableUpstreamTargets`；loopback 合法 URL 保持。
8. **并发与原子性证据**：
   - Runtime：`TestConcurrentSameKeyCreatesOneSessionFact`（barrier 双 create → 一个 session
     fact + 一个 mapping）、`TestConcurrentCloseAndAssetServeLinearizesClose`（32 轮 Close 与
     asset race，Close 返回后 serve 必须 404）、`TestTransientStoreFailureIsUnavailable`；
     `go test -race -count=2 ./internal/runtime/...` exit 0。
   - 真实 PostgreSQL：`TestSurfaceSessionRepositoryConcurrency`（双 pool：same-key/same-request
     唯一事实、same-key/different-request 一胜一 Aborted 零 orphan、device-bound 冲突与
     replay、不同 key 各自建 session）、`TestSurfaceSessionCreateCloseAssetRace`（barrier，
     无 sleep）、`TestArtifactCreateRollsBackMidTransaction`（同 key 不同内容并发注入真实
     PK 冲突：败者的 metadata/files/mapping 全部回滚、胜者 replay 成功）。
   - 修正 `TestArtifactRepositoryConcurrency` 测试代码自身的共享 `artifactSequence++` 数据竞争
     （改为 `atomic.Int64`），未关闭 race detector。
   - race 覆盖：`go test -race -tags=integration -count=1 -run "TestSurfaceSessionRepositoryConcurrency|TestSurfaceSessionCreateCloseAssetRace|TestArtifactCreateRollsBackMidTransaction|TestArtifactRepositoryConcurrency|TestSurfaceSessionRepositoryDurability"` exit 0。
9. **Desktop unmount close**：Desktop 以 ref 维护当前 app-surface session 集合（open/close/
   project-switch 显式增删，cleanup 不捕获旧 windows）；整个 Desktop 组件卸载时对仍打开 session
   逐个 best-effort `CloseSurface`（后端幂等，偶发重复安全；页面 crash/断网仍由 TTL 与逐请求
   Core revalidation 兜底，文档不声称 unload RPC 必达）。确定性组件测试
   `closes still-open app surfaces when the Desktop unmounts`：打开成功→unmount→断言 Close 被
   调用；既有窗口关闭/Project 切换/卸载/迟到 response 用例全部保持。

## 2026-08-28 修复后完整门禁（实际运行）

- `make generate` ×2：exit 0，第二次后无生成漂移。
- `git diff --check`、`git diff --check main...HEAD`：干净。
- `make check`：exit 0（proto-check + go-check + web-check + status render check）。
- `buf breaking --against '.git#branch=main'`：exit 0（无 Proto 变更，纯 Go/TS/文档修复）。
- `make test-integration` ×2：exit 0，各 28 个 integration/migration 测试 PASS，restart 阶段两轮
  分别输出 `surface persistence verified for session 01a04928…`/`01a0492a…`（session URL 仍
  200+CSP、create key replay 同一 session、Close 后 404）。
- 资源计数（持久验收 volume）：第一轮前 artifacts=11/requests=11/files=22/sessions=21/
  session_requests=21/app_versions=1348/installations=271/events=2515/outbox=1477；第一轮后
  13/13/26/27/27/1372/281/2552/1506；第二轮后 15/15/30/33/33/1396/291/2589/1535。两轮增量完全
  一致（artifact +2、session +6、version +24、installation +10、event +37、outbox +29），
  无累计漂移。scratch database 每轮后保持且仅保持既有 6 个历史库，零新增；
  `workos_workos-postgres` volume 未删除、未清理任何历史数据。（执行中发现 postgres 容器曾因
  宿主重启 exited(255)，仅 `docker compose up -d postgres bootstrap` 原地重启恢复，数据 volume
  全程未动。）
- 定向安全/并发：`go test -race ./internal/runtime/...`、
  `go test ./internal/core/artifact/... ./internal/core/appregistry/... ./internal/core/orchestration/...`、
  `go test ./internal/gateway/ ./internal/platform/config/` 均 exit 0（race 以 `-count=2` 复跑
  亦通过）。
- `make test-deepseek-fixture`：exit 0（仅 fixture 假 credential，1 passed，未访问真实 Provider
  网络）。
- `make test-e2e`：exit 0（3 passed / 1 skipped——skip 为需 fixture profile 的 deepseek spec）。
  `web-bundle-surface.spec.ts` 新增：response CSP `sandbox allow-scripts` 顶层生效、iframe 与
  顶层 URL 的 localStorage 探针均 opaque、脚本化 bundle 仍执行、Close/Remove revocation 与无
  SPA fallback 保持。
- 最终一致性：006 `628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27`、007
  `b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986` 与审核记录一致；001–005 对
  `main` 逐字节不变；无 008；`docs/structure.md` 无变化；根目录 `restart` 不再 tracked/存在。

## 2026-08-28 合并就绪修复（候选分支）

合并就绪复审确认的三个问题及修复。全部工作在干净候选分支
`feat/web-bundle-surface-merge-candidate` 上完成（从本地 `main` `1ca3262` 精确创建，`main` 是其
直接祖先）；污染分支 `feat/minimal-web-bundle-surface` 保留本地仅作审计。

1. **历史污染 blob**：`feat/minimal-web-bundle-surface` 虽在最终树删除了 `restart`，但对象审计
   显示 blob `2dd6362e90c544487d391314732aa4063eabee41`（19,126,918 字节 ELF）仍经实现/修复提交
   在 `main..HEAD` 可达，`--ff-only` 会把它永久带入 `main`。修复：以 `git diff --binary main
f55a870 | git apply --index` 把被审核分支**最终树**物化为候选分支上的单一提交
   `7dc3f7a feat: implement minimal web bundle surfaces`（树与 `f55a870` 逐字节一致，`git diff
f55a870 HEAD` 为空）。候选分支 `main..HEAD` 对象审计：最大 blob 为 31,182 字节测试文件，
   无 `2dd6362e`、无路径 `restart`、无 `restart` 历史；根目录无 `restart`、`tests/restart/`
   源码与精确 `/restart` ignore 规则保留。
2. **acceptance-volume 一致性 flake**：`TestProjectInstallationMigrationAppliedToAcceptanceVolume`
   曾用两条独立 `SELECT count(*)`（total 与 join）在 Read Committed 下读共享 volume，并行测试在
   两条语句之间提交新 mapping 时出现 "434 total, 435 owner-consistent" 的观测竞态（非数据损坏，
   该文件相对 `main` 未改；单独复跑与第二次完整门禁均通过佐证）。修复：改为**单条 SQL 单
   statement snapshot** 的 `LEFT JOIN … WHERE i.id IS NULL` 计数并断言为 0；保留 `t.Parallel()`、
   不 skip、不 retry/sleep，005 owner-bound composite FK 验证强度不变，共享 volume 数据原样保留。
3. **dbtransient 健壮性**：`IsTransient` 原对 `pgErr.Code[:2]` 直接切片，空/1 字符畸形 SQLSTATE
   （畸形代理/测试 double 可构造）会 panic。修复：切片前检查长度，短 code 安全返回 false，不按
   message 猜测。新增 `internal/platform/dbtransient/transient_test.go` 钉死分类矩阵：nil、
   普通/wrapped 错误、wrapped `context.DeadlineExceeded`、真实 `net.Error`（真实拨号超时）与
   wrapped `*net.OpError`、`*pgconn.PgError` 08/53/57/58 class 为 transient；23/42/00 class、
   `context.Canceled`、空/1 字符 code、未知 class 为非 transient。`go test -race -count=2
./internal/platform/dbtransient ./internal/runtime/...` exit 0。

### 合并就绪验收（实际运行，候选分支上执行）

- `make generate` ×2：exit 0/0，第二次后无生成漂移（README 状态区块仅经工具因 status.json 更新）。
- `git diff --check`：0；`git diff --check main...HEAD`：0。
- `make check`：exit 0（首轮失败为两个 markdown 未过 prettier——含审核者 prompt 文档本身——
  以 `prettier --write` 工具修复后通过；失败与修复事实保留于此）。
- `buf breaking --against '.git#branch=main'`：exit 0。
- `go test -race -count=2 ./internal/platform/dbtransient ./internal/runtime/...`：exit 0。
- 真实 PostgreSQL 定向 race（Surface concurrency / create-close race / Artifact rollback /
  Artifact concurrency / Surface durability，`-race -tags=integration`）：exit 0。
- `TestProjectInstallationMigrationAppliedToAcceptanceVolume` `-count=10` 定向复跑：exit 0；
  连续两轮完整 `make test-integration`（各含 restart seed/verify，session `01a0494c…` /
  `01a0494e…`）均 exit 0、28 测试 PASS，非失败后重试。
- `make test-deepseek-fixture`：exit 0（仅 Makefile 内置 fixture 假凭据）。
- `make test-e2e`：exit 0（3 passed / 1 fixture-only skipped）。
- 计数（持久验收 volume）：第一轮前 artifacts=20/requests=20/files=40/sessions=50/
  session_requests=50/versions=1443/installations=312/events=2791/outbox=1659；第一轮后
  22/22/44/56/56/1467/322/2828/1688；第二轮后 24/24/48/62/62/1491/331/2865/1717。Web Bundle
  切片增量两轮完全一致（artifact +2、session +6、files +4、version +24、event +37、outbox +29）；
  installations +10/+9 的 1 行差异经逐行核对为既有安装并发测试的锁内 no-op（第二轮
  `inst-race-two` 萁复为 no-op 未产生新行，调度相关、先于本轮存在），非本轮修复引入。scratch
  database 每轮后保持且仅保持既有 6 个历史库，零新增。
- 最终一致性：001–005 对 `main` 逐字节不变；006 `628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27`、
  007 `b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986` 保持；无 008；
  `docs/structure.md` 不变；无 Proto/Schema/migration 变更；候选分支 `main..HEAD` 对象扫描无
  ELF/大 blob（最大 31,182 字节测试文件）、无路径 `restart`、污染 blob `2dd6362e` 不可达；
  secret 扫描仅核对是否命中、不回显内容。

## 交接

基线（分支创建前、`main` 快进到 `bb1dc74`+prompt doc 后）：

- `git status --short --branch`：clean，`main...origin/main [ahead 21]`。
- `make bootstrap`：通过。
- `make check`：通过（proto-check + go-check + web-check + status render check）。
- `make test-integration`：通过（16 个 integration/migration 测试全 PASS，restart seed/verify 三组通过）。
- `make test-e2e`：通过（2 passed / 1 skipped——skip 为 deepseek-fixture spec，需 fixture profile）。

执行轮最终证据（命令与退出结果，均在本机 docker 工具链内运行）：

- `make generate` ×2：exit 0，第二次后无新增差异（`git status` 仅任务自身文件）。
- `make check`：exit 0（proto-check + go-check + web-check + status render check）。
- `buf breaking --against '.git#branch=main'`：exit 0（仅 additive）。
- `git diff --check`、`git diff --check main...HEAD`：干净；001–005 与 `docs/structure.md` 对 main 逐字节不变；无 root-owned/临时文件。
- `make test-integration`（完整通过轮 ×2）：exit 0。22 个 integration/migration 测试 PASS（新增 `TestWebBundleSurfaceVerticalSlice` 8 个子测试、`TestAllMigrationChecksumsArePinned`、`TestWebBundleMigrationsFromEmptyDatabase`、`TestWebBundleMigrationsAppliedToAcceptanceVolume`、`TestArtifactRepositoryConcurrency`、`TestSurfaceSessionRepositoryDurability`）；restart 阶段在 `docker compose restart workos-core harness-host runtime-host` 后输出 `surface persistence verified for session …`（session URL 仍 200+CSP、create key replay 同一 session、Close 后 404）。
- 资源计数（持久验收 volume，两次完整通过轮之间）：artifacts 5→7、artifact_requests 5→7、bundle_files 10→14、surface_sessions 8→11、session_requests 8→11（固定每轮增量：artifact +2、session +3）；app_versions 1313→1337、installations 252→262、events 2381→2418、outbox 1392→1421（与本任务测试/seed 的固定增量一致）。scratch database 集合保持 6 个历史库，零新增；`workos_workos-postgres` volume 未删除。
- migration checksum（sha256，已由 `TestAllMigrationChecksumsArePinned` 钉死）：006 `628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27`、007 `b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986`；pristine scratch DB 前向执行 + 二次运行 no-op + 当前验收 volume bootstrap 应用均验证。
- 定向安全/并发：`go test -race ./internal/runtime/...` exit 0；`go test ./internal/core/artifact/... ./internal/core/appregistry/... ./internal/core/orchestration/... ./internal/gateway/ ./internal/platform/config/` exit 0。
- `make test-deepseek-fixture`：exit 0（fixture key，1 passed）。
- `make test-e2e`：exit 0（3 passed / 1 skipped——skip 为需 fixture profile 的 deepseek spec）。`web-bundle-surface.spec.ts` 证明：CreateArtifact 上传 index.html+app.js → RegisterApp 引用 exact digest → UI Install → Open → iframe `sandbox="allow-scripts"`+`referrerpolicy="no-referrer"` 且脚本写入唯一文本（非 Desktop 固定 HTML）→ reload 后重新 Open → 关窗后旧 URL 404 → Remove 后旧 URL 404 且无 SPA fallback。
- HTTP 安全证据（integration `AssetPolicyFailsClosed`）：traversal/dot/dotdot/double-slash/未知文件/未知 session/未知 MIME/反斜杠/percent-encoding 全部 404；POST 405；成功响应带完整 CSP/nosniff/no-referrer/no-store/ETag/服务端 MIME。Gateway 单元测试证明 SurfaceService+/surfaces/ 仅到 runtime upstream 且 identity 覆盖注入、WorkloadService/private resolver 仍 404、runtime 不可达净化 503。
- `SurfaceSession.bridge_token` 为空、`clipboard`/`file_picker`/`resize` 为 false（integration 断言）；iframe 无 `allow-same-origin`（E2E 断言）；Workload container/native capability 继续 unavailable（runtime-host SystemService 如实报告）。

## 未决风险与下一步

- Gateway 的 device-session gate 仍是 DevBypass 语义（loopback-only），真实 device authentication 未实现；Surface URL 无 bearer/secret，授权完全依赖 gateway identity + owner/device-bound session，公网部署前必须补真实认证。
- Surface asset 的 Core revalidation 逐请求发生，未做短 TTL 缓存；大流量下需要在 runtime-host 增加受控缓存并保持 revocation 界。
- Surface 文档隔离目前依赖同 origin 下的 CSP `sandbox allow-scripts`；若未来需要让 Surface 持有
  非 opaque origin 的能力（如 storage），必须先迁移到独立 origin/域名方案并写 ADR。
- transient 分类按 SQLSTATE class + Go 错误类型在 adapter 边界判定；尚未做健康探测/熔断，持续
  outage 表现为每请求 Unavailable。
- Desktop window 布局未持久化（按范围约定）；`WORKOS_SURFACE_SESSION_TTL` 目前仅 compose 暴露。
- Artifact 仍只有 `app.web-bundle.v1` subtype（status=scaffolded，证据限定）；ZIP/TAR importer、Object Store、通用 artifact storage 未实现。
- 下一任务建议：**App Bridge handshake + least-privilege capability grant/token for Web Bundle Surface**（继续经 WorkOS Agent API/Harness Broker，不给 iframe provider credential）；另一独立后续为 rootless Web Service/container runner。
