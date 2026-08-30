# 下一位智能体 Prompt：受监督的 Rootless Web Service Workload 纵向切片

> 将本文件完整交给下一位实现智能体。目标是直接完成实现、测试、UI 视觉证据、文档和聚焦提交，
> 不是只输出计划。

## 你的角色与最终结果

你是 WorkOS 的下一位实现智能体。仓库位于 `/home/aquatao/workos`。App Agent 持久预算策略、运行前
审批、配额与四轮审查修复已经合入本地 `main`。你的任务是实现下一条严格限定的纵向切片：

**让一个 Project 中已安装、使用 immutable digest-pinned OCI image 的 Personal Web App，在
`runtime-host` 中以 rootless Podman 和有限 cgroup v2 资源启动，通过只读 Web Service Surface 在现有
Desktop 窗口内渲染；`reliability-host` 独立观察真实 Workload，在退出、健康失败或资源事件后持久创建
Incident，并按有限策略确定性重启或停止。**

最终链路必须闭合：

```text
canonical container manifest
  → immutable Registry version
  → active Project installation + pinned manifest digest
  → Desktop CreateSurface(renderer=auto)
  → private Core installed-instance launch resolution
  → runtime-host server-owned finite resource/health policy
  → durable Workload identity + rootless Podman container + real cgroup v2
  → bounded startup health succeeds
  → owner/device-bound Web Service Surface session
  → Gateway /surfaces/<session>/... read-only reverse proxy
  → opaque-origin iframe + existing least-privilege App Bridge
  → reliability-host polls neutral Workload observations
  → durable Incident + bounded idempotent restart/stop
  → runtime/reliability restart 后仍不重复容器、Incident 或动作
```

持续推进到实现、生成物、真实测试、UI 前后截图、ADR、任务记录、架构文档、状态事实源和聚焦提交全部
完成。不要 merge 或 push。只有遇到以下情况才停止并留下证据与选项：必须破坏已有 v1 字段/编号、修改
已执行 migration、改变六进程所有权、需要 Core 或 Reliability 直接读取 Runtime 数据库、必须使用
rootful/privileged 容器或 Docker socket、无法在不暴露凭据和宿主能力的前提下实现，或执行环境没有可用
的 rootless Podman/cgroup v2，因而无法取得本任务定义要求的真实 E2E 证据。

## 为什么现在做这个

当前主线已经闭合：

```text
Project → Harness binding → durable Agent task → canonical events
Registry → installation → Web Bundle Surface
requested permission → mutable grant epoch → App Bridge
App policy → approval → quota reservation → usage circuit break
```

但运行时仍只有静态 Web Bundle：

- `WorkloadService` 除只读 node probe 外仍是空实现；
- `runtime-host` 不会启动用户程序，也没有 durable Workload identity；
- manifest 中的 `runtime.type=container`、resources 和 health 尚未形成可执行、安全的事实；
- `SURFACE_RENDERER_WEB_SERVICE` 已在契约中声明，但 CreateSurface 会拒绝；
- `reliability-host` 只有 health 和 Unimplemented IncidentService，所有 enforcement 都明确 unavailable；
- OS 还不能回答“哪个 App 正在占用资源”，更不能在 Harness/模型不可用时限制和重启它。

`docs/structure.md` 的核心原则是“确定性的系统负责制止故障，Agent 负责理解和修复故障”。本任务先完成
不依赖模型的 L0/L1 最小闭环：隔离、硬限制、观察、Incident、有限重启和最终停止。Repair Agent、候选
版本部署与 rollback 仍是后续任务，不能用本次“重启相同 immutable image”冒充。

## 当前仓库事实

- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。
- 本 Prompt 编写时，本地 `main` 与当前分支 `feat/supervised-web-service-workload` 都位于 `907fcdc`；
  本地 `main` 领先 `origin/main` 3 个提交，工作树在新增本 Prompt 前干净。执行时必须重新检查，并以
  执行时本地历史为准；不得从落后的远端重建、reset 或丢弃本地提交。
- 当前实现分支已经建立为 `feat/supervised-web-service-workload`。若交接时仍在该独立分支，直接继续；
  不要再创建同名分支或切回 `main` 覆盖本 Prompt。若环境已经变化，先确认任务仍有独立 branch/worktree。
- `docs/status.json` 是进度事实源：Runtime / Surface 为 working，但证据严格限定 Web Bundle；
  `container/native runners unavailable`。Reliability 为 scaffolded，证据只有 health。
- `api/proto/workos/workload/v1/workload.proto` 的 `StartWorkloadRequest` 允许携带完整
  `WorkloadIdentity`，这是未实现 scaffold，不是可直接公开的安全启动命令；Gateway 明确对整个
  WorkloadService 返回 404。
- `api/proto/workos/incident/v1/incident.proto` 只有基础 Get/List/Acknowledge 合同，当前没有 owner
  enforcement、revision/idempotency 或持久实现。
- App manifest Schema 已声明 `container`、`image`、`command`、`port`、`web-service`、`resources`、
  `health`，但后二者仍是自由对象，container cross-field policy 和 launch resolver 都未实现。
- Core 私有 Surface resolver 只解析 Web Bundle；runtime Surface session 表的 CHECK 与非空列也只容纳
  Web Bundle。已有 `ResolveWebBundle`/`ReadWebBundleAsset` 和历史 session/replay 必须继续兼容。
- 当前 Desktop Open 明确提交 `WEB_BUNDLE`；iframe 已固定 `sandbox="allow-scripts"`、无
  `allow-same-origin`，现有 bridge token/grant epoch/MessageChannel 边界已经有 E2E 证据，不能削弱。
- `runtime-host` 与 `reliability-host` 当前都运行在通用 WorkOS 镜像中；镜像没有 Podman。systemd 模板
  也明确要求未来 runner 使用只针对 Runtime 的审查 drop-in，不能放宽全部六个进程。
- 本机在 Prompt 编写时有 cgroup v2，但 `podman` 不在 PATH。下一位智能体必须重新探测；未经用户明确
  授权不得安装系统软件、启用 privileged 容器或更改宿主安全配置。
- migrations `001`–`014` 已执行并受 checksum/forward 测试保护，禁止修改。预计由 Runtime 拥有的
  新数据从 `015` 开始、Reliability 拥有的下一份 migration 从 `016` 开始；执行时若编号已占用必须
  顺延，不能复用。
- 当前验收 PostgreSQL volume 含用户已有数据。禁止 `docker compose down -v`、TRUNCATE、broad
  DELETE、wildcard DROP、Podman system prune 或顺手清理历史资源。

## 凭据、宿主与最高优先级安全边界

- **本任务不需要任何真实 DeepSeek、OpenAI、Codex、GitHub、OCI Registry 或其他外部凭据。**
- 不得使用、保存、转述、验证或尝试恢复聊天中曾出现的真实 Key；不得扫描 shell history、环境变量、
  credential store 或本机私有文件搜集凭据。
- container image 只允许本地已经存在、以 `@sha256:<64 lowercase hex>` 固定的 OCI reference；Runtime
  必须使用 `pull=never`，不得登录 registry、读取 auth file、隐式联网拉取或把 tag 当 immutable digest。
- 不得把 Docker socket、Podman socket、宿主 PID/network namespace、任意设备、宿主目录、Project
  workspace、用户 home、credential 文件或 WorkOS 数据库 socket 挂进不可信 App。
- Runtime 必须验证 engine 是 rootless 且 cgroup v2 hard limits 可用；不得在能力不足时 fallback 到
  rootful Podman、Docker、裸 `exec`、host network、无 cgroup 进程或固定成功 adapter。
- 容器不得使用 `--privileged`、额外 capabilities、`allow-same-origin`、host IPC/PID/network、任意
  bind mount 或未限制的 writable rootfs。无法满足时如实报告 unavailable。
- manifest 的 resources/health 是 App 的请求事实，不是授权。最终 effective policy 必须由
  runtime-host 的版本化、有限、server-owned 上限裁决，并持久快照；App 不能通过填写大数获得宿主资源。
- cgroup limit、健康探测、restart limit 和 safety stop 必须由确定性代码执行，不能依赖 Harness、模型、
  App 自报或 Desktop 保持在线。
- container stdout/stderr、HTTP body、URL query、manifest、用户内容和 App Bridge payload 不得进入
  Incident summary、日志、错误或截图。测试只使用明确的合成 fixture 文本。
- WorkloadService、Runtime supervisor/control RPC、Core launch resolver 都保持 Gateway 404；如果本任务
  公开 IncidentService，它只能经独立 Reliability upstream、可信 identity 和 owner scope 暴露。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`deploy/README.md`、
     `docs/ui/README.md`；
   - `docs/structure.md` 中 1.4、3、4.3–4.4、8、9、10、11、14–18；
   - `docs/architecture/implementation.md`、`docs/status.json`；
   - ADR `0001`–`0005`，尤其 App Bridge、mutable grant 和 Agent policy 的现有边界；
   - `docs/tasks/20260825-minimal-web-bundle-surface.md`；
   - `docs/tasks/20260828-minimal-project-agent-app-bridge.md`；
   - `docs/tasks/20260829-mutable-project-app-grants.md`；
   - `docs/tasks/20260829-app-agent-approval-budget-policy.md`；
   - `api/proto/workos/{app,surface,workload,incident,common}/v1` 的相关 Proto；
   - `schemas/workos-app-manifest-v1.schema.json` 与 manifest validator/domain/application 全部测试；
   - Project installation、Registry exact-version read、Core Surface resolver 和 private transport；
   - `internal/runtime/surface` 全部分层、PostgreSQL adapter、HTTP policy、bridge、restart 测试；
   - `internal/runtime/transport/workload.go`、`cmd/runtime-host/main.go`；
   - `cmd/reliability-host/main.go`、Gateway routing/header cleaning、platform config/systemhandler；
   - migrations `007`、`010`、`012`、`014`、migration checksum/forward tests 与 `sqlc.yaml`；
   - Desktop App Library/Open/window/AppSurface、SDK clients、Window Manager、现有 Playwright E2E；
   - `Dockerfile`、`compose.yaml`、systemd 模板、Makefile、`workosctl doctor`。

2. 运行并记录基线：

   ```sh
   git status --short --branch
   git log --oneline --decorate -15
   git branch -vv
   git diff --check
   make bootstrap
   make check
   make test-integration
   make test-e2e
   ```

   基线失败必须判断归属并写入任务记录。禁止删 volume、历史测试、断言或固定返回绕过失败。

3. 做只读宿主能力探测并记录真实结果：

   ```sh
   command -v podman
   podman --version
   podman info --format json
   stat -fc %T /sys/fs/cgroup
   test -r /sys/fs/cgroup/cgroup.controllers
   id
   ```

   不得因为探测失败而自行 `sudo apt install`、启用 rootful daemon、把 Docker socket 挂给 Runtime 或
   启动 privileged Podman-in-Docker。可以继续完成 fake-engine/unit/integration 代码和 opt-in fixture，
   但没有真实 rootless Podman + cgroup + cross-process + browser 证据时：任务不能标记 done，
   `container-runner`/Reliability 不能标 working，必须把环境缺口作为 blocker 交接。

4. 从 `docs/tasks/TEMPLATE.md` 创建：

   ```text
   docs/tasks/20260829-supervised-web-service-workload.md
   ```

   初始状态 active，写清每个事实 owner、manifest profile、resource policy、Workload/Surface/Incident
   状态机、external side-effect recovery、Proto、migrations、错误、测试、UI 和非目标。

5. 新增聚焦 ADR，建议：

   ```text
   docs/decisions/0006-supervised-web-service-workload.md
   ```

   ADR 至少固定：为什么 Workload/runner 属于 runtime-host、监督/Incident 属于 reliability-host；为什么
   public App resources 只是 requested policy；为什么只运行本地 digest-pinned image；如何恢复数据库与
   Podman 两个事实系统之间的 crash window；为什么第一版 Web Service proxy 是只读 opaque-origin；
   为什么“重启相同版本”不等于 rollback/repair。

6. 按 `docs/ui/README.md` 建立任务级 before：

   ```text
   docs/ui/desktop-web/changes/20260829-supervised-web-service-workload/before/
   ```

   至少记录当前 container installation 无法打开 Web Service Surface 的确定性状态，以及当前 Desktop
   没有可用 Workload/Incident 视图的基线。固定 Chromium 1440×900、deviceScaleFactor 1，只用合成
   fixture。

## 必须保持分离的事实

| 事实                                      | 唯一 owner                       | 语义                                                         |
| ----------------------------------------- | -------------------------------- | ------------------------------------------------------------ |
| canonical manifest + immutable version    | workos-core App Registry         | App 声明 image/argv/port、requested resources/health/surface |
| active installation + pinned digest/grant | workos-core Project Installation | 某个 Project 当前允许启动的 exact App instance               |
| resolved launch descriptor                | Core orchestration 的只读投影    | 从 installation + exact Registry facts 得出的中立快照        |
| effective resource/health policy          | runtime-host Runtime Manager     | server maxima 与 manifest request 裁决后的有限执行快照       |
| Workload identity/generation/container    | runtime-host Runtime Manager     | 实际运行对象、engine identity、cgroup、状态和操作幂等        |
| Surface session/token/grant epoch         | runtime-host Surface Broker      | owner/device 交互会话及已有 App Bridge 授权                  |
| observation                               | runtime-host 的中立只读输出      | engine/cgroup/health 的有界数字事实，不是 Incident 决策      |
| supervision policy/action/Incident        | reliability-host                 | 违规判定、有限 restart/stop、Incident 和消费进度             |

禁止用一张跨进程共享表、Core SQL join Runtime 表、Reliability SQL 查询 Runtime 表、Surface session 冒充
Workload、container ID 冒充 bearer token，或由客户端提交 `runtime_id`/`cgroup_path`。跨进程只能使用
版本化 Proto/RPC 或 durable event；若使用事件，consumer 必须 at-least-once、幂等并持久化 cursor。

## 必须完成的目标链路

### 1. 安全、可执行的 container manifest profile

只为以下 profile 宣布 working：

```yaml
apiVersion: workos.app/v1
runtime:
  type: container
  image: localhost/workos-web-fixture@sha256:<64 lowercase hex>
  command: ["/workos-web-fixture", "serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
    adaptive: true
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 2
```

具体字段的数值上限、整数/小数表达与 canonical 规则必须在 ADR、Schema 和测试中只定义一次。要求：

- `runtime.type=container` 必须同时具有严格 OCI digest reference、非空 bounded argv 和 container port；
  image tag、短 digest、uppercase digest、credential-bearing URL、控制字符、shell 字符串替代 argv、空参数、
  过长参数和非法端口均拒绝。
- Runtime 通过 argv 调用 engine，绝不把 manifest command 拼进 shell，也不接受 manifest 提供 Podman flags、
  host port、container name、network、mount、device、user、env、secret、capability 或 cgroup path。
- container profile 第一版只允许一个 `web-service` surface，route 固定 `/`；renderer 不匹配、多 surface、
  Web Bundle artifact 字段混入 container、container 字段混入 web-bundle 都 fail closed。
- `resources`/`health` 对 container 必须有显式字段、shape、关系与上限：memory high 不得高于 max，所有
  hard limit 有限，startup/restart 有界。未知字段不能被 runner 静默忽略。
- 保持既有 working Web Bundle manifest 完全兼容。container 以前从未是 working 行为；对它新增严格
  launch policy 仍需在 ADR 说明 v1 兼容性理由，并通过 `buf breaking`/Schema 回归证明没有破坏既有事实。
- Registry 只做 canonical syntax/security policy，不访问 Podman、不检查本机 image、不拉取网络资源。
  本机是否有 exact image 只在 Runtime launch 时裁决。
- canonical manifest digest 必须覆盖全部 container/resource/health/surface 字段；顺序等价输入保持 digest
  稳定，任何有效 policy/argv/image 变化都改变 digest。

### 2. Additive private launch resolution

在 Core 私有 Surface resolver 中增加中立的 generic launch resolution；不要改变已有字段含义，也不要
让 Runtime 导入 Core internal package。建议新增 additive RPC/message，并使用明确 oneof：

```text
ResolveSurfaceLaunch(active project + app_instance)
  → pinned app/version/manifest digest
  → oneof web_bundle | web_service_container
  → installation current grants + grant revision
```

要求：

- 每次从 owner-scoped、未归档 Project 的 active installation 开始，读取 exact pinned Registry version，
  并比较 installation manifest digest；不得解析 Registry current。
- Web Service descriptor 只返回中立 immutable facts：app/version/digest、image digest ref、argv、container
  port、requested resource/health policy、surface route。它不返回 Podman flags、host endpoint、container
  ID、credential 或 effective policy。
- stored canonical manifest 不满足已承诺的 profile、digest drift 或内部字段损坏是 sanitized Internal，
  不能静默补默认值；正常但未支持的 runtime 是 FailedPrecondition。
- 现有 `ResolveWebBundle`/`ReadWebBundleAsset` 继续工作，旧 session 与全部 Web Bundle E2E 不回归。
- resolver 暂时不可达映射 Unavailable；unknown/foreign/archived/uninstalled 统一 NotFound，不泄漏存在性。

### 3. Runtime-owned durable Workload lifecycle

新增符合 `domain → application → ports ← adapters` 的 Runtime Manager 模块；Podman 只能出现在 adapter。
最小状态机至少区分：

```text
pending/starting → running → stopped
                    └──────→ failed
restart: old generation terminal → next generation starting/running
```

要求：

- Workload ID 是 UUIDv7；一个 owner + active app instance + exact manifest digest 在任一时刻最多一个 active
  Workload。Surface session 可以多对一引用 Workload，不能每次 Open 都启动新容器。
- Workload 持久快照至少包含 owner/project/app instance/app/version/manifest digest、kind、effective
  resource/health policy、generation、engine container identity、真实 cgroup path、server-derived loopback
  endpoint、state、restart count、UTC timestamps。公共响应不能泄漏 host endpoint/cgroup/container ID。
- `WorkloadIdentity` 的 server-owned 字段全部由 Runtime 生成。不得实现现有
  `StartWorkload(WorkloadIdentity)` 为“照单全收”；应新增安全的 server-derived Ensure/control contract，
  或保持危险 scaffold RPC Unimplemented。Workload/observer/control service 仍不进 Gateway allowlist。
- ensure/start、restart、stop 都有持久 idempotency key 或 generation/etag。same key/same canonical command
  精确 replay；different command 稳定 Aborted；失败不伪造 running 结果。
- PostgreSQL 事务不能假装覆盖外部 Podman side effect。使用 durable operation/lease + deterministic
  container name/labels，或等价 recoverable protocol，明确每个 crash window：DB reserved 后未 create、
  create 后未 start、start 后未 persist、stop 后未 persist、Runtime crash/restart、两个 Runtime 实例竞争。
- Runtime restart 后按自身 DB 与只属于 WorkOS 的 engine labels 对账：重新附着 exact surviving container、
  完成/回滚中断操作、清理本任务自己创建的 orphan；不得收养或删除未带完整 WorkOS identity labels 的
  外部容器，不得重复启动。
- uninstall/archive 经 Core definitive NotFound 后立即停止对应 Workload；Core 临时 Unavailable 不是
  NotFound。持续无法重验超过 server-owned finite grace 时应 fail safe 停止，而不是无限运行。
- 无 active Surface 后允许按有界 idle TTL 停止，下一次 Open 可按新 operation 启动；不要声称已实现
  永久 background service。TTL、reconcile interval、operation timeout 都有启动校验和测试时可控 clock。
- Stop/cleanup 只针对解析出的精确 UUID/container ID 和 WorkOS labels；禁止 wildcard、name prefix broad
  delete、image prune、volume prune 或杀死宿主无关进程。

### 4. Rootless Podman 与真实 cgroup v2 enforcement

定义中立 `ContainerEngine`/`CgroupReader` ports；production adapter 必须满足：

- 启动时/能力探测执行有界 `podman info`，确认 rootless=true、cgroup v2 与所需 controller/manager 可用；
  `command -v podman` 本身不等于 runner available。
- 所有 Podman 调用使用 `exec.CommandContext`/等价 argv API、绝对 executable、deadline、bounded output；
  不使用 shell，不把 raw stderr 返回给用户或无界写日志。
- image 使用 exact digest reference 和 `--pull=never`；本地缺 image 为明确 FailedPrecondition，engine
  暂时不可达为 Unavailable，不访问 registry。
- 容器以 rootless engine 创建，`--restart=no`，read-only rootfs、bounded tmpfs、drop all capabilities、
  no-new-privileges、无 host mount/device、无继承 credential/env、有限 pids/memory/cpu。若某项无法证明
  已应用，runner capability 必须 false。
- 使用 WorkOS-owned internal network（无外部 egress）和仅 `127.0.0.1` 的随机 host port 映射；绝不
  `--network=host`、`0.0.0.0` publish 或信任 manifest host。Runtime 从 engine inspect 得到 endpoint，
  验证它确为 loopback 后才持久化。
- 将 server-owned requested→effective policy 映射到真实 `cpu.max`、`memory.high`、`memory.max`、
  `pids.max`（或经验证等价的 Podman flags + cgroup values）。启动后读取实际 cgroup 文件核对；配置未生效
  不能只告警后继续。
- cgroup path 必须来自 engine inspection，规范化并证明位于允许的 delegated cgroup subtree；拒绝
  traversal、symlink escape、空路径和 host/system cgroup。Workload API 不把它公开给 Desktop。
- 默认 Compose 可以继续诚实报告 container runner unavailable。为真实宿主部署提供只针对
  `runtime-host` 的最小 systemd drop-in/文档（StateDirectory、RuntimeDirectory、Delegate 等按实需）；
  不得放宽其他五个进程或提交机器特定 UID/path。
- 测试 fixture image 必须 `FROM scratch` 或使用已固定、可复现的本地输入；构建发生在测试准备阶段，
  Runtime 不 build。fixture container/image 只按精确 ID 清理，不 prune 用户资源。

### 5. 只读 Web Service Surface 与反向代理边界

扩展现有 Surface，而不是另起一套 session/URL/iframe DTO：

- `preferred_renderer=UNSPECIFIED` 表示 server 根据 exact installed descriptor 选择；显式 WEB_BUNDLE 或
  WEB_SERVICE 必须与 descriptor 精确匹配，不能 silently fallback。其他 renderer 和未知 enum 仍拒绝。
- 兼容既有 CreateSurface request digest/replay。若引入 v2 digest 区分 `auto`、explicit web-bundle 与
  web-service，必须识别历史 v1 Web Bundle mapping 并精确重放；不能让旧 key 在升级后 Aborted，也不能
  让新 auto 与 explicit 请求意外视为相同。
- Web Service session 复用现有 owner/device/idempotency/TTL/Close/bridge-token/grant-revision 语义，并
  持久引用 Workload ID + generation。migration 对 renderer-specific 列使用明确互斥 CHECK，不填假
  artifact/entrypoint 或假 workload 值满足旧 NOT NULL。
- Create 只有在 exact container 已 running 且 startup health 成功后才返回可用 session。启动失败不消费
  Surface create key；已经创建的 Workload side effect由 reconciliation 收敛，不能留下无界 orphan。
- 每个 proxy request 先验证 trusted Gateway identity、open/unexpired owner/device session，再经 Core
  重验 active installation + pinned manifest，并核对 Workload/generation/descriptor。uninstall/archive/
  stop/generation drift 立即 fail closed。
- grant 改变沿用 ADR-0003：旧页面内容可以继续在 installation active 时渲染，但旧 session 的全部 App
  Bridge 方法在 Core epoch 比较处失败；Create replay 不得为 stale epoch 轮换 token。新 Surface 可复用
  同一 Workload，但获得新 grant snapshot/token。
- `/surfaces/<session>/...` 第一版仅允许 GET/HEAD，query、body、WebSocket、upgrade、form submit、写方法
  都明确拒绝；这是 server-rendered/read-only Web Service slice，不声称完整 full-stack transport。
- path 沿用严格未编码 POSIX 规则；禁止 traversal、反斜杠、dot segment、double slash、percent encoding。
  backend target 只能来自 server-owned、已验证 loopback endpoint，任何 client/manifest URL 都不能参与，
  防止 SSRF/open proxy。
- 请求不转发 Cookie、Authorization、bridge token、WorkOS identity、Forwarded/X-Forwarded-\*、Host 或
  hop-by-hop headers。响应移除 Set-Cookie、认证 challenge、Server、hop-by-hop headers；redirect 只可在
  严格验证后重写到当前 session prefix，否则拒绝。
- 响应 body、header count/size、duration 和允许 media type 有明确上限；HTML/JS/CSS/image MIME 由安全
  allowlist/路径推导并带 nosniff，不能盲信 backend Content-Type。
- 每个成功文档响应覆盖为 WorkOS 固定 CSP，至少保持 `sandbox allow-scripts`、无
  `allow-same-origin`/forms/popups/top-navigation/downloads/storage，`connect-src 'none'`、
  `frame-ancestors 'self'`、no-referrer、no-store。backend 不能通过自己的 CSP/X-Frame-Options 放宽它。
- Desktop iframe 继续只有 `sandbox="allow-scripts"`；existing MessageChannel App Bridge 可在 Web Service
  fixture 中运行，但 bridge token 仍只在可信 parent 内存和专用 RPC metadata，绝不进入 container、
  URL、DOM、iframe、日志或数据库明文。

### 6. Reliability-owned observation、Incident 与有限动作

`reliability-host` 不能查询 Runtime schema，也不能自己调用 Podman adapter。它通过 private、版本化、
中立的 Runtime observer/control RPC 获取和操作 Workload：

```text
List/Observe supervised Workloads
  → bounded engine state + health verdict + numeric cgroup counters/events
  → Reliability policy engine
  → idempotent RestartWorkload or StopWorkload
```

要求：

- observation 只含稳定 ID、generation、状态、health verdict、exit category 和有界数字指标；不含 cgroup
  path、loopback endpoint、container ID、raw process error、日志、HTTP body 或用户内容。
- Reliability 持久化自己的 poll/cursor/checkpoint、Incident 与 action idempotency。相同
  `(workload, generation, violation kind, occurrence)` 只能产生一个 Incident 和一个裁决链；进程重启和
  at-least-once 重读不能重复 restart。
- 至少处理：unexpected exit、startup/ongoing health failure、OOM/memory event、pids limit event。
  CPU hard quota 始终由 kernel 执行；若没有稳定的“CPU 违规”定义，不要用单次高 usage 伪造 Incident。
- Podman 使用 `--restart=no`；自动重启权只属于 Reliability。每次 restart 使用独立持久 action key，
  Runtime 返回新 generation。crash 发生在“Runtime 已重启、Reliability 尚未落库”之间时，同 key replay
  必须返回同一结果。
- restart limit 来自 Runtime 持久的 effective health snapshot，并受 server hard maximum 约束。超过
  limit 后确定性 stop/fail closed，不形成无限 crash loop；Reliability/Harness/Core 断开时 cgroup hard
  limits 仍独立有效。
- 成功 restart 后 Incident 可标 mitigated，连续稳定观察后 resolved；用户 acknowledge 与系统
  mitigation 是不同事实。若实现 Acknowledge，按外部写边界增加 idempotency key 或 revision/etag，不能
  把“acknowledged”冒充“故障已修复”。
- Incident owner/project/workload scope、revision、UTC 时间和 evidence digest 持久化；summary/reason 从
  固定枚举映射，不拼接 raw engine/HTTP/log 内容。
- 实现 Incident Get/List/Acknowledge 时必须 identity protected、owner-scoped、有界分页、固定错误和
  transient PostgreSQL→Unavailable。如果通过 Gateway 公开，只新增受控 Reliability upstream；Gateway
  core readiness 不应因可选 Incident UI 暂时不可达而整体失败，bridge header 在该路由必须被剥除。
- Supervisor、Incident manager 的 capability 只有在真实 observation→incident→action E2E 后才 available；
  repair-orchestrator、deployment-controller、rollback 始终明确 unavailable。

## 协议与数据要求

- 所有跨进程或 Go/TypeScript 新事实先改 `api/proto`，再运行 `make generate`。只能 additive 新字段、
  message、RPC、enum value；不得改号、复用、删除或重新解释已有 v1 字段。
- `WorkloadIdentity` 可以 additive 增加 app instance、manifest digest、generation、restart count 和安全
  时间事实，但 public/Desktop projection 不得携带 runtime/container/cgroup endpoint。若内外视图需求不同，
  建立明确 private observation message，不要让一个 DTO 同时承担 secret-bearing host internals 与 public UI。
- generic Surface launch 应使用 oneof，不复制一套 JSON manifest DTO；Schema/canonical manifest 是 App
  声明唯一事实源，Proto 是跨进程投影。
- 预计新增两份 migration：
  - Runtime owner：Workload、operation/idempotency、必要 reconcile facts，并 additive 演进 Surface
    renderer-specific session shape；
  - Reliability owner：Incident、ack/action idempotency、supervisor checkpoint。
    两者不能跨 schema FK/SQL，不能合并成 owner 不清的共享表。
- migrations `001`–`014` 逐字节不变。新 migration 必须 pristine、forward、重复执行 no-op、checksum
  固定；已有 Web Bundle sessions/requests backfill 后仍可 replay/serve/close。
- 所有 ID UUIDv7、时间 UTC、revision/generation 正数；digest lowercase sha256；表级 CHECK、owner-scoped
  unique、renderer oneof coherence、操作结果 snapshot 与必要 composite FK 都要下沉数据库。
- SQLC 生成文件、`gen/`、`src/gen/` 和 README 状态区块只经工具更新，禁止手改。
- inbound Connect body 有推导出的解压后 wire cap；application 仍做 UTF-8、C0/C1、长度、enum、UUID、
  number overflow/NaN/Inf 和矛盾字段验证。gzip bomb 在业务代码前 ResourceExhausted。

## 错误与 HTTP 语义

至少固定并测试以下净化映射：

| 条件                                                   | 结果                                              |
| ------------------------------------------------------ | ------------------------------------------------- |
| 缺可信 identity                                        | Connect `Unauthenticated`                         |
| 非法 UUID/enum/argv/image/policy/path/limit/key        | Connect `InvalidArgument`                         |
| unknown/foreign/archived/uninstalled installation      | Connect `NotFound`                                |
| explicit renderer 与 manifest 不匹配、image 本地不存在 | Connect `FailedPrecondition`                      |
| rootless/cgroup 能力不满足                             | 明确 unavailable/failed precondition，不 fallback |
| same key different request、stale revision/generation  | Connect `Aborted`                                 |
| PostgreSQL/Core/engine 暂时不可达                      | Connect `Unavailable`                             |
| stored invariant、descriptor/cgroup/endpoint 漂移      | Connect `Internal`                                |
| surface/session/installation/workload 不可用           | HTTP 404 固定短消息                               |
| backend 暂时失败或正在重启                             | HTTP 503 固定短消息                               |
| backend 非法响应                                       | HTTP 502 固定短消息                               |
| 非 GET/HEAD                                            | HTTP 405 + `Allow`                                |

所有分类使用 typed/domain sentinels 和 `errors.Is`，PostgreSQL transient 使用共享 dbtransient 原则。错误、
日志和 Incident 不得包含 SQLSTATE、constraint、DSN、host path/port、container/cgroup ID、image auth、argv、
backend body、raw stderr 或用户内容。

## Desktop 与视觉证据

保持完整桌面 + 内部窗口，不增加永久侧栏：

- App Library Open 改为 server-selectable renderer（通常提交 UNSPECIFIED），Web Bundle 继续按原链路打开；
  container App 显示 bounded “Starting workload…”/ready/unavailable/restarting 文案，迟到响应在 Project
  切换、uninstall、关窗后 inert。
- App window 根据 response renderer 渲染同一个安全 AppSurface host；不要给 iframe 新 WorkOS client、
  endpoint、credential 或 `allow-same-origin`。
- 若 IncidentService 本任务公开，使用现有 Window Manager 增加一个最小 System Monitor 普通窗口或
  等价非永久入口：列出当前 Project 的 sanitized Incident severity/state、App、restart outcome 和
  acknowledge；不显示 cgroup path、host port、container ID、raw logs/HTTP/argv。Reliability 不可达时
  只降级该视图，不拖垮 Desktop/Agent/App Library。
- 所有 loading/empty/error/restart-limit-exhausted 状态有可访问、固定文案；mutation 防双击，Project
  切换后旧响应不能串台。

完成后至少保存：

```text
docs/ui/desktop-web/changes/20260829-supervised-web-service-workload/after/
  app-surface--web-service-ready--1440x900.png
  system-monitor--workload-incident-restarted--1440x900.png   # 若实现 public Incident UI
```

用 after 更新对应 `docs/ui/desktop-web/current/`，并在 `notes.md` 记录 fixture image digest、合成数据、
路由、viewport、浏览器、采集命令和有意差异。截图、DOM、console/network diagnostics 不得含 bridge token、
host endpoint、container/cgroup ID、credential 或真实用户数据。若最终范围没有 Incident UI，任务记录必须
说明原因，并至少保存 Web Service Surface 的 before/after/current；用户可见变化没有视觉证据时不得 done。

## 必须补齐的测试

### Manifest / Core

- container/web-service happy path、canonical digest 稳定与任一 launch/policy 字段变化敏感；
- tag/短 digest/credential-shaped image、空/超长 argv、控制字符、非法 port、unknown resource/health key、
  limit 关系、renderer/route/multi-surface 混配全部拒绝且 violation 不回显值；
- 既有 Web Bundle fixtures/digest/registration 全部不变；
- exact pinned resolution、current version drift 不影响 installation、manifest digest drift Internal；
- foreign/archived/uninstalled/unsupported/transient failure 的固定映射与零 Runtime 调用。

### Runtime domain/application/PostgreSQL

- requested→effective resource clamp/default、有限 policy、state/generation/operation digest；
- ensure replay/conflict、同 installation 并发只启动一个、两个 installation 隔离、多 Surface 共用；
- start/stop/restart 每个外部 side-effect crash window 的 fake-engine deterministic recovery；
- Runtime restart reattach、engine orphan/DB orphan 收敛、foreign unlabeled container 永不触碰；
- uninstall/archive/authorization stale/idle TTL 收敛，Core Unavailable 与 NotFound 不混淆；
- `015` pristine/forward/no-op/checksum、旧 Web Bundle rows backfill/replay；双 pool 并发和注入失败回滚；
- real pgx refusal 从 repository 到 Connect 为 retryable Unavailable。

### Podman / cgroup

- command adapter argv 精确断言：无 shell、pull never、rootless/security/network/resource flags、bounded output
  与 deadline；恶意 image/argv 不能变成 option/shell injection；
- capability probe 对 missing binary、rootful、cgroup v1/missing controller、bad JSON、timeout 如实 false；
- **真实 opt-in fixture**：本地 digest-pinned image 启动为 rootless，host publish 只在 127.0.0.1，外部
  egress 不可达，rootfs/capability/mount/user 边界符合约定；
- 真实读取 `cpu.max`、`memory.high`、`memory.max`、`pids.max` 与 effective snapshot 一致；受控 OOM 或
  pids event 被观察且只影响 fixture cgroup；
- 清理只删除本次精确 container ID；测试前后列出并证明未碰用户容器/image/volume/network。

### Surface / HTTP / Bridge

- auto/explicit renderer、legacy v1 create digest replay、new v2 conflict、concurrent Create 与 token pairing；
- Web Service session renderer-specific DB invariants、TTL/Close/device/owner/restart persistence；
- proxy identity/session/Core/workload/generation revalidation，每个 denial 都在 backend 前发生；
- SSRF、non-loopback endpoint、path encoding/traversal、query、写方法、WebSocket、redirect、oversize headers/
  body、slow backend、bad MIME、Set-Cookie/Authorization/Forwarded header stripping；
- CSP/sandbox/nosniff/no-referrer/no-store 固定且 backend 无法放宽；
- Web Bundle HTTP、App Bridge run/watch、grant revocation、token rotation原测试全部通过；Web Service fixture
  内的 JS 可完成现有 MessageChannel handshake，但拿不到 token/endpoint。

### Reliability / Incident

- observation validation、counter monotonic/reset、health/exit/OOM/pids 分类；
- 同 occurrence at-least-once 只建一个 Incident，同 action key 只 restart 一次；
- restart exact limit、successful mitigation、stable resolution、limit exhausted→stop，无无限循环；
- Runtime/DB/Core/Harness 分别不可达时的确定性行为；Harness 全停仍能执行 hard limit/restart/stop；
- `016` pristine/forward/no-op/checksum、两个 Reliability 实例并发只一个动作、restart/commit crash replay；
- Incident owner isolation、pagination limit+1、ack idempotency/revision、wire cap/gzip bomb、transient
  PostgreSQL→Unavailable、错误/summary 无 raw evidence。

### 跨进程 / restart / browser

- 真实链路：Register digest-pinned container manifest → Install → Desktop Open → Core resolve → rootless
  container/cgroup → health → Web Service iframe marker + optional existing App Bridge task；
- controlled fixture exit/health failure → reliability observation → exactly one Incident → bounded restart → new
  Workload generation → Surface 按明确语义恢复或要求新 session；
- Runtime 在 container start 后、DB finalize 前被终止，重启后不产生第二个 container；
- Reliability 在 restart 成功后、action commit 前被终止，重启/replay 不产生第二次 restart；
- Core/Runtime/Reliability 重启后 installation、workload、surface、incident、action/replay 保持；
- uninstall 后 public Surface 立即 404，container 在 bounded reconciliation 内精确停止；
- 所有现有 integration、DeepSeek keyless fixture 与 Playwright E2E 无回归。

## 非目标

- image pull/build/signing、registry login、Credential Vault、secret/environment injection；
- writable Project workspace、host filesystem、persistent volume、file picker、container-to-host socket；
- arbitrary outbound network、App-declared network policy、public port、host network；
- POST/PUT/PATCH/DELETE、form、WebSocket/SSE、opaque-origin fetch/CORS、完整 full-stack Web Service API；
- background-service、native runner、remote browser/native/WebRTC、GPU、device passthrough；
- App upgrade/downgrade、blue/green、canary、candidate promotion、rollback；
- Agent repair、Repair Orchestrator、Deployment Controller、自动修改代码；
- 生产 device enrollment/LAN pairing/public Internet；现有 loopback DevBypass 风险不在本任务修复；
- 通用 Artifact/Object Store、Indexer/RAG、Mobile adaptive shell；
- 用 Docker/rootful/裸进程作为 Podman 缺失时的功能 fallback。

## 文档与状态同步

完成后同步：

- ADR-0006 与任务记录；
- `docs/architecture/implementation.md`：container manifest profile、Core private resolver、Runtime Workload
  owner、Podman/cgroup、Surface proxy、Reliability observation/action/Incident、数据/migration owner、失败边界；
- 受影响模块和 deploy README/systemd 说明；
- `docs/status.json`：
  - Runtime / Surface 只有真实 rootless Podman + browser 证据后才增加 Web Service/container evidence；
  - Reliability 只有真实 observation→Incident→restart/stop + restart evidence 后才从 scaffolded 升级；
  - 必须继续注明 native/background/full-stack write transport/repair/rollback unavailable；
- README 状态区块只通过 `make generate`/`make docs` 生成；
- UI before/after/current/notes。

如果只有 fake engine 或 Docker 测试，代码可以交接为 scaffolded，但任务状态不得 done，status 不能声称
container/Reliability working。任务记录必须列出真实命令、环境能力、资源计数、Podman 对象前后清单、
migration checksum、未决风险和下一步，不能用聊天记录代替仓库事实。

## 完成门禁

至少运行并记录：

```sh
make bootstrap
make generate
make generate
make check
buf breaking --against '.git#branch=main'
go test -race ./internal/core/appregistry/... ./internal/core/orchestration/...
go test -race ./internal/runtime/... ./internal/reliability/...
make test-integration
make test-integration
make test-deepseek-fixture
make test-e2e
make test-podman-fixture        # 新增的 opt-in 真实 rootless/cgroup/Surface/Reliability 门禁
git diff --check
git diff --check main...HEAD
git status --short --branch
```

`internal/reliability` 尚不存在时应按脚手架/分层规则建立；不得用 composition-root 单文件堆业务逻辑。
若主机缺少真实能力，`make test-podman-fixture` 必须明确 skip/fail 原因且不能被计作 PASS，任务按前述规则
保持 active/blocked evidence，而不是删除该门禁。

还必须证明：

- 第二次 generate 后无生成漂移，历史 migrations `001`–`014` 逐字节不变；
- 两轮 integration 的相关表增量可解释，scratch DB 精确 cleanup，不碰验收历史；
- Podman 测试前后用户既有 containers/images/volumes/networks 不变，只清本次精确 fixture 对象；
- repository diff、Git object、日志、截图、fixture、环境文件中无 credential/token/真实用户内容；
- 无 ELF、Podman image archive、Playwright report/trace/video、临时数据库、root-owned 文件进入提交；
- Gateway 仍对 Workload/private resolver/control/HarnessHost 返回 404，bridge token 只进 AppBridge RPC；
- iframe/CSP 仍无 `allow-same-origin`，backend 无法 set cookie 或访问 WorkOS API；
- 不依赖 Harness/模型完成限制、Incident、restart 或 stop；
- branch 基于执行时本地 `main`，提交聚焦，未 merge、未 push。

## 最终交付格式

实现完成后向审核者报告：

1. branch、base 与 commit；
2. manifest requested policy、Runtime effective policy、Workload、Surface、observation、Incident 的 owner；
3. Proto、Schema、ADR 与 migration 变更及历史兼容；
4. rootless Podman argv/security/network/image/cgroup 的真实证据；
5. DB↔Podman crash-window、并发、idempotency、generation/restart 恢复证据；
6. Web Service proxy、opaque iframe、App Bridge/grant revocation不回归证据；
7. Incident/restart-limit/stop 在 Harness 不可用时仍成立的证据；
8. UI 视觉记录路径；
9. 全部验证命令、真实结果、PostgreSQL 与 Podman 对象前后计数；
10. 未决风险，尤其 write transport、network capability、production auth、rollback/repair；
11. 明确声明未使用真实凭据、未安装/放宽宿主安全、未 merge、未 push。
