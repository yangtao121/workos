# 下一位智能体 Prompt：第一版收口——Adaptive Shell、Rootless Runtime、Reliability 与确定性回滚

> 将本文件完整交给下一位实现智能体。目标是长时间自主执行并直接完成实现，不是只输出计划、审查报告
> 或下一份 Prompt。用户即将离线，已经明确希望一次承担较多任务；不要因为工作量大、测试耗时或遇到
> 一个独立环境 blocker 就提前停下。整个批次只允许一个 branch、一个 worktree、一个任务记录、一个写入
> 智能体，所有阶段严格串行，防止 Proto、migration、截图和状态文档互相覆盖。

## 你的角色与最终目标

你是 WorkOS 第一版剩余能力的收口智能体。仓库位于 `/home/aquatao/workos`，产品架构主线是
`docs/structure.md`，当前实现边界是 `docs/architecture/implementation.md`，唯一进度事实源是
`docs/status.json`。

`docs/structure.md` 第 18 节共有 14 个第一版优先项。当前主要还差三块：

1. **Workload Identity + cgroup Monitoring**：代码、fake engine、PostgreSQL 和 opt-in Podman fixture 已有，
   但缺真实 rootless Podman + cgroup v2 + 六进程/浏览器闭环证据，能力仍必须 unavailable。
2. **Resource Alert + Restart + Rollback**：Incident、有限 restart/stop、System Monitor 已有实现，但缺真实
   observation → Incident → action E2E；上一已安装版本的确定性回滚仍未实现。
3. **iPad/Android Adaptive Shell**：目前只有 `phone/tablet/foldable/desktop` 分类契约，没有可用的
   Compact / Medium / Expanded / Fold-separated 布局、按设备类别隔离的 UI 状态或多 viewport 视觉证据。

本批次的最终目标是：

```text
同一个本地 branch
  → 真实可用的 adaptive PWA shell（phone / tablet / desktop / fold-separated）
  → UI 状态按 Project + device class 隔离，Desktop 行为不回退
  → 尽可能取得真实 rootless Podman + cgroup v2 Workload 证据
  → 尽可能取得真实 observation → Incident → restart/stop 证据
  → owner 明确触发、Core 原子裁决的上一 pinned App 版本回滚
  → Web Bundle 链路在无 Podman 主机也能证明版本切换、旧 Surface 失效与回滚
  → 若主机满足 rootless 前提，再证明 container App 的相同闭环
  → 实现、测试、截图、ADR、任务记录、implementation 与 status 完全一致
```

成功结束时应做到：

- Adaptive Shell 具有真实 Chromium E2E 和确定性多 viewport 截图，可以从 `contract-only` 升级为
  `working`；
- App 版本 transition/rollback 是真实持久化功能，不是 UI 假按钮、固定成功响应或直接改数据库；
- Runtime/Reliability 只有在真实宿主链路通过时才升级 capability/status；若宿主仍不满足前提，保留
  unavailable/scaffolded，并把可复现 blocker 精确记录在仓库中，但继续完成不依赖该宿主的所有阶段；
- `make generate` 幂等、`make check`、全量集成与相关浏览器门禁通过，工作树干净；
- 留下按阶段组织的聚焦提交，供用户醒来后直接审查；未经用户新授权不 merge 到 `main`、不 push。

## 单分支纪律（不可偏离）

开始时以执行时的本地 `main` 为准，创建且只创建：

```text
feat/v1-runtime-reliability-adaptive-closeout
```

并且只建立一个批次任务记录：

```text
docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md
```

强制规则：

1. 不创建第二个 branch，不创建额外 worktree，不 stash 后切分支，不让其他智能体写入仓库。
2. 不 reset/rebase/squash 已有历史，不 amend 已经交接的阶段提交，不覆盖用户或其他人的未提交改动。
3. 若启动时目标 branch 已存在，先只读确认它是否正是本任务、基线与工作树是否安全；无法确认时停在
   原分支并记录，不得强行删除或重建 branch。
4. 所有阶段在同一 branch 严格串行；一个阶段完成并提交后才开始下一个阶段。
5. 允许阶段因真实外部环境 blocker 保持未完成，但必须提交诚实证据并立刻继续后续不依赖阶段。
6. 建议提交序列如下；可因实际 contract 拆出一个前置提交，但不得把全部工作压成一个巨型提交：

   ```text
   feat: add adaptive project shell layouts
   feat: persist device-class shell state safely
   test: prove rootless supervised workload acceptance       # 仅真实通过时
   feat: add deterministic installed app version rollback
   test: close runtime reliability and rollback user journeys
   docs: record v1 closeout evidence and remaining blockers
   ```

7. 每次提交前执行 `git diff --check`，检查 staged diff 不含 secret、私钥、二进制、测试报告、trace、视频、
   数据库、容器归档或宿主绝对路径镜像。

## 无人值守执行规则

用户离线期间不要等待普通澄清。优先从架构文档、Proto、实现和测试推导最保守的正确方案。

- 不要只写计划；读完后立即实现。
- 不要因一个测试慢、镜像构建慢、依赖下载重试或任务量大而停止。
- 网络下载发生偶发连接重置时，可有界重试；记录最终结果，不把一次瞬时失败冒充代码问题。
- 每 60 秒以内给出简短进度更新，但持续推进。
- 遇到 Podman/user namespace/cgroup 宿主 blocker 时，不得反复空转：记录一次完整探测，继续 Adaptive
  Shell、Web Bundle version transition/rollback、单元/集成和文档工作。
- 不得自行 `sudo`、安装/升级宿主软件、修改内核参数、放宽 user namespace、安全策略、systemd 全局
  配置或防火墙。若现有宿主缺少 rootless 前提，保持诚实 blocker。
- 不得使用 Docker/rootful Podman、privileged container、host PID/network、挂载宿主根目录或伪造
  cgroup 文件来替代 rootless 验收。
- 不得访问真实 DeepSeek/OpenAI/Codex 或其他收费 API；现有 Provider 测试继续使用本地 fixture。
- 不得搜索 shell history、用户 home、环境变量或 credential store 获取 key。
- 只有必须新增权限、破坏 v1、增加第七个常驻进程、改已执行 migration 或删除用户数据时才停止请求
  用户决定；除此之外自主完成。

## 写 Prompt 时的仓库事实（执行时必须重新核对）

- 本文件编写时本地 `main` 为 `8c124a7`；执行时以真实本地历史为准，不 reset 到远端旧提交。
- 六个进程边界固定：`workos-gateway`、`workos-core`、`harness-host`、`runtime-host`、
  `reliability-host`、`indexer`。本批次不得增加 mobile daemon、notification daemon 或 deployment daemon。
- migrations `001`–`024` 已存在且可能已经在持久卷执行。禁止修改；若确需新表，使用执行时下一个空闲
  编号并保持 forward-only、单一进程所有权和 checksum pin。
- `apps/mobile-shell` 当前只有 device-class contract；真实 UI 仍在 `apps/desktop-web`，共享窗口逻辑在
  `clients/window-manager`。
- `api/proto/workos/surface/v1/surface.proto` 已有 `DeviceClass` 与 `Viewport`，不得另造同义枚举/DTO。
- Desktop 已有 Project Spaces、普通窗口、Dock、App Library、Agent Center、Artifact Center/Viewer、
  Device Center、System Monitor 和 Gateway device-auth gate；Adaptive 改造必须复用这些行为。
- Runtime 的 Web Bundle Surface 已 working；container/web-service 部分有 migrations `015/018`、严格
  manifest profile、Podman adapter、cgroup read-back、Workload Manager、只读 proxy 与 opt-in fixture，
  但没有真实 acceptance host 证据。
- `docs/tasks/20260829-supervised-web-service-workload.md` 仍 active：最近记录的宿主没有 Podman，且
  unprivileged user namespace 不可用。不要假设该事实仍然不变，也不要假设它已经解决。
- Reliability 有 migrations `016/017/019`、IncidentService、action ledger、有限 restart/stop 和
  System Monitor；`supervisor` / `incident-manager` capability 固定 false，直到真实跨进程链路成立。
- App Registry 保存 immutable SemVer 版本；Project Installation pin exact version/digest/grants，但没有
  正式的 active installation version transition/rollback 产品链路。
- Credential Vault、Artifact Context 与 DeepSeek Structured Review 已 working；本批次不得回退其
  mTLS、credential lease、artifact trust boundary 或视觉证据。
- `gen/`、`src/gen/` 与 README 状态表均由工具生成，禁止手改。

## 开始前必须完成

完整阅读而不是只搜索片段：

1. `AGENTS.md`、`README.md`、`ROADMAP.md`、`CONTRIBUTING.md`、`docs/ui/README.md`；
2. `docs/structure.md` 的 0、1.4、1.5、3、4、8–18，尤其 9、10、11、12、14、17、18；
3. `docs/architecture/implementation.md` 全文与 `docs/status.json`；
4. ADR `0002`、`0003`、`0005`、`0006`、`0007`、`0008`、`0009`、`0010`、`0011`；
5. `docs/tasks/20260829-supervised-web-service-workload.md` 及其 UI notes；
6. Surface/Surface Resolver、App Registry/Installation、Workload、Incident、Project、Auth Proto；
7. `apps/desktop-web`、`apps/mobile-shell`、`clients/window-manager`、`clients/app-host`、
   `sdk/surface-sdk` 的实现与测试；
8. Runtime workload/surface、Podman adapter、Reliability domain/application/PostgreSQL/transport、
   Gateway allowlist/identity cleaning、Core App Registry/Installation/orchestration；
9. Compose、Dockerfile、systemd、Makefile、现有 Playwright fixtures 与截图目录。

随后建立批次任务记录，写清基线、阶段依赖、验收、环境探测和提交计划。运行并记录：

```sh
git status --short --branch
git log --oneline --decorate -20
git branch -vv
git diff --check
make bootstrap
make generate
make check
make test-integration
make test-e2e
make test-lan-pairing
make test-artifact-review
make test-artifact-context
make test-deepseek-structured-review
```

不得为基线测试清 PostgreSQL volume。失败时判断是基线、环境还是本分支问题并记录；独立失败不阻止读取
和实现其他阶段。

## 全批次不可违反的边界

### 架构与数据

- 依赖方向保持 `domain → application → ports ← adapters`。
- Domain 不得导入 PostgreSQL、Connect、HTTP、浏览器 API、文件系统、Podman 或其他模块 adapter。
- 每张表只属于一个进程；Reliability/Runtime/Core 禁止互查对方 schema。
- 跨进程契约先修改 `api/proto`，再 `make generate`；不手写同义 Go/TypeScript wire DTO。
- v1 字段号不得复用；删除字段/枚举值必须 reserved；真正破坏性变化使用新版本和 ADR。
- 所有外部写操作有 idempotency key 或 revision/etag，首响应 snapshot 可重放；所有时间 UTC，ID UUIDv7。
- at-least-once consumer 必须幂等并持久化 cursor；不能以内存 map 冒充 durable adjudication。
- App version transition、rollback 和 Project revision/event 必须在 Core-owned transaction 中原子收敛。
- 未实现能力必须返回 Unimplemented/Unavailable/FailedPrecondition 的正确语义，不能固定成功。

### 身份、权限与隐私

- 浏览器不能提交 owner/user/device 身份；Gateway 清洗后注入，服务端按 session 再派生。
- 移动布局状态不得存 bridge token、session cookie、credential、task goal 全文、Artifact content、用户内容
  全文或 Provider 输出；只保存有界的 UI reference/placement/preference。
- iframe 继续 opaque origin + MessageChannel；Adaptive 布局不得放宽 CSP、sandbox、App Bridge capability、
  grant epoch 或 Surface token 边界。
- rollback 不得扩大 permission/grant，不得接受客户端提供 image digest、container ID、host endpoint、
  credential ref 或任意数据库版本。目标必须由 Core 的 immutable registry/install history 推导并重验。
- 自动安全保护不能依赖 Harness；restart/stop/rollback 的 L0/L1 路径不得调用模型。
- 日志、事件、错误、截图、DOM attribute、URL、Playwright artifact 中不得出现 secret、raw credential、
  task goal/Artifact 全文或宿主内部路径。

### UI 与视觉证据

- 任何可见 UI 改动都按 `docs/ui/README.md` 建立任务级 `before/`、`after/` 和更新 `current/`。
- 固定 viewport、固定 seed、固定 Project/App/Incident/Artifact 名称；随机 UUID/time 必须从视觉区域隐藏或
  由确定性 fixture 控制。
- 禁止把 after 复制成 before；before 必须在改动前从当前 main 的真实界面采集/复制并注明来源 hash。
- 截图输出使用容器内 `/workspace/...` 路径，禁止传宿主 `/home/...` 绝对路径造成仓库路径镜像。
- 不保存 trace/video，除非仅在失败诊断期间临时生成且提交前删除。

## 阶段 A：把 Adaptive Shell 从 contract-only 做成 working

这一阶段不等待 Podman。目标不是把 Desktop 等比缩小，而是基于相同 Project/Surface/Agent/Artifact 状态
实现四种确定性布局模式：

```text
Compact          phone，单一主内容全屏 + 底部导航 + Project sheet
Medium           tablet，单一主内容 + 可选 Agent slide-over + 自动隐藏 Dock
Expanded         desktop，保持现有自由窗口、Project Spaces 与 Dock 行为
Fold-separated   仅在真实/注入的 window segments 存在时双 pane；否则安全退化为 Expanded/Medium
```

### A1. 共享 device/layout contract

- 复用 Proto `DeviceClass` 与 `Viewport`，在 TypeScript 共享包中建立唯一映射；不要继续维护另一套含义不同
  的字符串 enum。
- 设备模式由 viewport、orientation、safe-area 与可选 fold segments 派生。浏览器不支持 fold API 时
  fail soft，不通过 UA sniff 或品牌列表猜测设备。
- 把浏览器 API 封装在 adapter，纯 domain/layout reducer 接受普通值，能在 Vitest 中覆盖 boundary：
  599/600、1023/1024、portrait/landscape、zero/NaN DPR、fold gap、segment 变化与 resize storm。
- 保持 `clients/window-manager` 的 desktop reducer；新增 adaptive projection/action 时不能让 window
  entity 依赖 React/DOM。

### A2. 真实 responsive UX

- Compact：一次只显示一个主窗口；Project 切换是可关闭 sheet；Agent Tasks/Approvals/Usage 是全屏或
  bottom sheet；Artifact Markdown/Diff 可读且操作区不溢出；Device Center/System Monitor 可用。
- Medium：默认单窗口；Agent 是用户主动打开的 slide-over，不是永久分栏；Dock 自动隐藏/显式呼出；
  keyboard focus 和 Escape/back 行为明确。
- Expanded：现有 drag/focus/z-order/close、App Surface、Artifact viewer 和 Dock E2E 不回退。
- Fold-separated：当两个 segment 明确存在时，主 App 与 Agent/Artifact 可分配到两个 pane，hinge/gap
  不承载点击目标；用户可选择单 pane。没有 segment 时绝不强制双栏。
- Project 切换必须关闭或重新绑定旧 Project 的 Surface、Artifact、context chip、pending feedback；不得
  让 late response 污染新 Project。
- 使用 CSS safe-area env，处理软键盘/visual viewport；按钮最小触控尺寸和可见 focus 必须满足现有 UI
  规范。不能用仅 hover 才可发现的移动操作。
- Desktop 的 auth/device pairing、App permission、Agent approval、Artifact context 与 rollback 入口必须
  在 Compact/Medium 仍可达。

### A3. 按设备类别隔离 UI 状态

首版允许使用 versioned device-local IndexedDB adapter；不为此增加常驻进程。若选择服务端同步，则必须
先有 additive Proto、明确 owner 和单一进程 migration，且 Gateway 注入 device identity，绝不能由请求
伪造。不要为了“云同步”把范围扩成新服务。

最低 durable state：

```text
schema_version
project_id
device_class
active_app_instance_id? / active_system_window?
active_artifact_id?
recent_app_instance_ids[]
dock_app_instance_ids[]
layout preference（single/dual，非精确跨设备像素）
updated_at
```

要求：

- key 至少隔离 browser profile/device + Project + device class；Desktop 几何不能覆盖 Phone/Tablet。
- 有界长度、canonical ID、version migration、损坏记录 fail closed/重置当前 key；不能整库清空。
- Surface bridge token、Auth cookie、Agent task goal、Artifact bytes、credential 绝不持久化在 layout state。
- uninstall、grant revision 变化、Project archive/logout 后 stale reference 被清理；late async write 不复活旧状态。
- 多 tab 同 key 写入采用 revision/updated-at adjudication或明确 single-writer lease，不 silently last-write
  corrupt。

### A4. PWA 与验收

- 至少提供有效 Web App Manifest、theme/display/start URL 与静态资产缓存边界；Service Worker 若启用，只
  缓存 immutable shell assets，绝不缓存 API、auth、Surface/Artifact/Agent 响应。
- Capacitor iPad/Android wrapper、push、native secure storage 本批次不是硬性完成条件；没有真实 SDK/设备
  证据时必须明确保留为 unavailable，不能生成空 wrapper 冒充 working。
- 增加一个聚焦门禁，例如 `make test-adaptive-shell`，至少覆盖：
  - Vitest：layout reducer/device mode/storage corruption/migration/project isolation；
  - Chromium：390×844 Compact、820×1180 Medium、1440×900 Expanded；
  - 可控 segment fixture：Fold-separated 双 pane与无 segment fallback；
  - real Gateway session + Project + App Surface + Agent/Artifact/Approval 至少各一条移动可达链路；
  - desktop 回归。
- 视觉证据建议目录：

  ```text
  docs/ui/desktop-web/changes/20260831-adaptive-shell/
    before/
    after/
    notes.md
  docs/ui/desktop-web/current/
  ```

阶段 A 只有在代码、测试、三类真实 viewport + fold fixture 截图、文档、task/status 同步后才能提交。

## 阶段 B：真实 Rootless Podman + cgroup Workload 证据

阶段 A 提交后才进入。首先运行一次有界宿主探测并把原始结论（不含敏感环境内容）写入任务记录：

```sh
command -v podman
podman version                         # 仅存在时
podman info --format json              # 仅存在时，有界保存必要 verdict
stat -fc %T /sys/fs/cgroup
cat /sys/fs/cgroup/cgroup.controllers
test -f /sys/fs/cgroup/cgroup.subtree_control
unshare --user --map-root-user true
systemctl --user is-system-running     # 可用时
```

### B1. 宿主满足前提时

- 运行并修复 `make test-podman-fixture`，不得弱化现有 identity/security/cgroup/cleanup 断言。
- 建立真实跨进程门禁，例如 `make test-supervised-workload-e2e`：PostgreSQL + Core + runtime-host +
  reliability-host + Gateway + Chromium + rootless Podman fixture image。
- fixture image 使用唯一、精确 digest pin，不 pull registry，不带 secret，不含网络依赖；前后只按本测试的
  exact label/ID 清理，不运行 prune、broad rm 或删除其他镜像/容器/network。
- 真实证明：
  1. container manifest 注册、安装、Surface 创建；
  2. rootless、internal network、read-only rootfs、zero effective/bounding caps、no-new-privileges；
  3. exact argv/image/labels、单 loopback publish、无 host mount/device/env secret；
  4. cgroup path 位于 delegated subtree，cpu/memory/pids hard policy 回读一致；
  5. healthy UI ready，restart 后 generation 邻接且旧 Surface 失效；
  6. uninstall/grant/version drift 与 Core outage grace 按设计 fail safe；
  7. 进程和 PostgreSQL restart 后 operation/lease/idempotency 收敛；
  8. 测试失败也清理 exact 自有对象。
- 采集真实 container App ready 的 deterministic after/current 截图；不得把 Starting/Unavailable 截图当
  ready。

### B2. 宿主不满足前提时

- 不安装、不绕过、不伪造。`make test-podman-fixture` 保持 loudly fail，记录准确命令与错误类别。
- 检查 fixture 在缺 Podman 时是否快速、安全、无临时产物退出；可补充相关单测，但不得宣称真实通过。
- `container-runner`、Runtime container/native 状态不升级，原 active task 不关闭。
- 立即继续阶段 C/D 中不依赖真实 Podman 的代码、Web Bundle E2E 和 PostgreSQL 测试。

只有 B1 全部成立时才创建建议的 `test: prove rootless supervised workload acceptance` 提交；B2 只把
blocker 证据包含在后续 docs 收尾提交，避免伪造一个“验收通过”提交。

## 阶段 C：Reliability 真实 observation → action 闭环

### C1. 有真实 Runtime 时

在 B1 的唯一 fixture 上证明：

1. health failure、unexpected exit、受控 `pids.events max` 或 OOM 中至少两个可确定触发的 episode；
2. Runtime 只输出中立、有界 observation，不泄露 endpoint/cgroup path/container ID/log/user content；
3. Reliability 持久化每 occurrence 唯一 Incident，重复 poll/restart 不重复；
4. restart action 使用 durable key，crash window/replay 不二次烧 generation；
5. restart limit exhausted 后 deterministic stop，failed/unsupported/unavailable 不混淆；
6. 新 generation 稳定观测后旧 mitigated Incident 正确 resolved，acknowledge 独立保持；
7. System Monitor 经 Gateway owner/project 隔离展示，Reliability 暂时不可达只降级该窗口；
8. 六进程相关 restart 后 cursor、Incident、action outcome 与 UI 仍一致。

通过后才把 `supervisor` / `incident-manager` capability 和 `docs/status.json` 从 scaffolded 升级到
working；任何只用 fake engine、直接 SQL seed 或进程内 mock 的证据都不足以升级。

### C2. 没有真实 Runtime 时

- 保持 capability false；继续加固 domain/application/PostgreSQL 的 pending action、terminal outcome、
  idempotency、corruption 与 restart 测试，但不要重复已经充分覆盖的测试来刷工作量。
- 为阶段 D 的 owner-triggered rollback 提供准确 Incident eligibility/read model；不建立 Reliability 对
  Core 数据库的直接依赖。
- 任务记录明确“软件实现证据”和“真实能力证据”的差别。

## 阶段 D：确定性的上一 pinned App 版本切换与回滚

该阶段不依赖 Podman，必须至少通过 Web Bundle 全栈完成。先新增 ADR，明确首版是 **owner 明确触发的
L1 previous-pinned-version rollback**，不是模型自动修复，也不宣称候选版本经过完整 canary。未来自动
Deployment Controller 可以复用同一 Core command，但本批次不建立未认证的 privileged service call。

### D1. 所有权与协议

- immutable App versions 仍由 Core App Registry 持有；active installation、version transition/history、
  grant snapshot、Project revision/event 仍由 Core Project Installation 持有。
- Reliability 只拥有 Incident/action facts；不能 SQL 读取/修改 Registry/Installation，不能在自己的表
  复制 active installation 真相。
- 若新增公共 command，放在现有 Core-owned App Installation contract 中，经 Gateway identity 注入、
  owner/project scope、wire budget 与固定错误矩阵；不要让浏览器直连 Reliability/Core 私网。
- 先修改 Proto，再生成；使用 additive 字段/RPC。请求至少具备：project/app instance、expected
  revision/etag、idempotency key，以及必要的显式用户 consent。不得接受 image/container/digest/owner/
  device/credential 等可伪造事实。

### D2. 版本 transition 与历史

为了让 rollback 有真实前态，实现有界、显式的 active installation version transition：

- 目标 version 必须是 same owner + same app ID 的 immutable registered SemVer；Core 重算 exact digest。
- permission/grant 绝不扩大：当前 grants 若不是新版本 requested permissions 的子集，必须
  FailedPrecondition 并要求已有 consent UX 重新选择；不能自动授予新增权限。
- 同一 `(owner, project, app instance, idempotency key)` 物理仲裁；same request 精确重放首响应，different
  request 稳定 Aborted；失败不消费 key。
- expected Project/installation revision 串行化并防 lost update；transition、history snapshot、Project
  revision、event/outbox 和首响应 snapshot 同事务。
- history 是 append-only、有界可分页/可裁剪策略明确的 Core-owned fact；不能用日志或事件临时反推。
- stored history/version/digest/grants/UUID/time 每次读出都重验，损坏 fail closed。
- 已有 Surface session 的 pinned version/digest/grant epoch 在 transition 后立即失效；late response 不得
  恢复旧 Surface。下一次 Open 解析新 pinned descriptor。

### D3. Rollback command

- rollback 目标由服务端从该 installation 的 durable history 选择“最近一个不同的 previous pinned
  snapshot”；客户端不能提交任意 target digest/image/container ID。
- 如果不存在 previous snapshot、App/version 已不可用、owner/project/app binding 不一致、grant 不兼容、
  expected revision 过期或记录损坏，稳定 fail closed 且零副作用。
- rollback 保持当前 grant 的最小权限或要求显式重新 consent；绝不恢复历史中更宽的权限。
- rollback 自身有独立 idempotency digest/first-response snapshot；并发 rollback/version update
  exactly one winner。
- rollback 不读取/改变 Credential Vault secret，不触发 Harness，不依赖模型。
- Core-minted event 只含安全的 app/version/revision reference，不含 manifest 原文、credential、用户内容。

### D4. UX 与 E2E

- App Library 提供明确的版本查看/transition consent；System Monitor 对绑定同一 Project/app instance 且
  eligible 的 Incident 显示 “Rollback previous version”。不能对无历史/foreign/resolved-ineligible 项显示
  可执行按钮。
- 双击/重试有 pending guard；冲突后重新读取 authoritative state；Project 切换使迟到结果 inert。
- UI 文案必须区分：rollback completed、no previous version、permissions need review、conflict、Runtime
  unavailable。不能把 Core transition 成功等同于 container 已健康。
- 无 Podman 主机的强制 E2E：
  1. 注册两个 deterministic Web Bundle 版本；
  2. 安装 v1、显式 transition v2；
  3. 证明旧 v1 Surface 失效、新 v2 Surface 可打开；
  4. 通过 owner-visible Incident/System Monitor 触发 rollback；
  5. 证明 exact previous v1 恢复、v2 Surface 失效、重试 exact replay；
  6. foreign owner/project、无历史、permission expansion、stale revision、并发点击 fail closed；
  7. Core/Gateway/Runtime/PostgreSQL restart 后历史、first response 与 session invalidation 仍成立。
- 有 Podman 主机时追加 container v1/v2 digest-pinned 链路，证明旧 Workload 收敛停止、新 descriptor 启动，
  rollback 后 generation/Surface/Incident 显示一致。
- 新增聚焦门禁，例如 `make test-app-version-rollback`，并加入合理的全量 E2E/CI 聚合目标。
- 保存 task 级 before/after/current 截图：App Library transition、System Monitor rollback eligible、rollback
  complete；固定 viewport/fixture，不显示随机 ID/时间/用户内容。

## 阶段 E：最终收口与状态裁决

完成所有可执行阶段后：

1. 同步模块 README、ADR、`docs/architecture/implementation.md`、唯一 task record、
   `docs/status.json`；运行状态生成器更新 README，禁止手改状态表。
2. Adaptive Shell 有真实多 viewport E2E 后可将 Mobile Shell 改为 working；若只有 CSS/unit test 或没有
   视觉证据，最高仍是 scaffolded/contract-only。
3. Runtime container capability 只有 B1 全过才 working；Reliability capability 只有 C1 全过才 working。
4. Rollback 可按 Web Bundle 链路独立标为 working，但文档必须明确 container rollback 是否因 Podman
   blocker 未验收、自动 canary/repair/deployment controller 仍 unavailable。
5. `docs/tasks/20260829-supervised-web-service-workload.md` 只有真实 Podman/cgroup/cross-process/browser
   gate 全过才改 done；否则保持 active 并补最新 blocker，不得因本批次结束而关闭。
6. 本批次任务只有最终目标全部完成才 done；若只剩宿主 blocker，保持 active/blocked 必须遵循仓库的
   blocked 判定规则，并写清已完成提交、唯一 blocker、复现命令和下一台合格主机需要执行的命令。
7. 检查 `docs/structure.md` 不要被改成迎合现状；它是目标，不是状态文件。

最终验证至少包括：

```sh
make generate
make generate                         # 第二次无生成差异
make check
make test-integration
make test-e2e
make test-adaptive-shell              # 本批次新增
make test-app-version-rollback        # 本批次新增
make test-lan-pairing
make test-artifact-review
make test-artifact-context
make test-deepseek-structured-review
make test-credential-vault
make test-podman-fixture               # 只有真实支持宿主可计 PASS
make test-supervised-workload-e2e      # 若 B1 新增并可运行
git diff --check
docker compose config --quiet
```

并对改动的 Go 安全/并发包运行聚焦 `go test -race`；对 Proto 执行 lint/breaking 检查；确认 migrations
`001`–`024` 与基线逐字节一致，只新增 forward migration；确认截图 after/current hash 相等且 before/after
不同；确认仓库没有 root-level ELF、Podman/Docker archive、私钥、credential token、数据库、绝对路径镜像、
Playwright trace/video 或测试临时目录。

## 必须覆盖的失败矩阵

至少覆盖并给出固定、安全错误语义：

- viewport/segment 畸形、storage schema 未知、损坏 layout、multi-tab 并发、Project 快速切换、logout；
- Compact/Medium 打开 foreign Project Artifact/Surface/Incident；
- version target 不存在、wrong owner/app、digest corruption、stale revision、same key different request；
- transition/rollback event 或 snapshot 写失败时整事务回滚；commit ambiguity 的 replay 不重复推进 revision；
- permission expansion 与历史宽 grant 不得通过 rollback 恢复；
- 旧 Surface/bridge token 在 version/grant transition 后立即失败；
- Reliability/Runtime/Core/PostgreSQL 分别暂时不可达；UI 只降级相关功能；
- observation 重放、Incident 重放、action response loss、restart limit、unsupported vs unavailable；
- Podman create/start/inspect/cgroup read-back/stop/remove 任一步失败，无 orphan、无伪 running/stopped；
- fixture cleanup 重跑安全且不触碰非本任务对象。

## 明确非目标

为保证一个 branch 可控，本批次不顺手实现：

- Indexer、Archive/RAG、pgvector、知识图谱；
- 公网暴露、mDNS、Push Relay、APNs/FCM、完整 Capacitor 原生发布；
- Native App Streaming、background-service/native runner、WebSocket/写代理；
- 自动 Agent 代码修复、build farm、canary、完全自动 promote、数据 migration 自动批准；
- 多 Harness 协作、Codex adapter、新 Provider、模型能力扩张；
- master-key rotation、credential reveal/export、OAuth credential；
- 第七个常驻进程或把现有进程拆分/合并。

发现这些需求时记录为后续，不实现、不创建额外 branch。

## 停止条件

只有以下情况可停止整个批次并等待用户：

- 必须破坏已有 v1 字段/编号或修改已执行 migration；
- 必须增加第七个常驻进程或违反固定进程/模块所有权；
- 必须让 App/Gateway/Desktop 获得 raw credential，或放宽现有 auth/CSP/sandbox/mTLS；
- 必须运行 privileged/rootful 容器、修改宿主安全策略或删除不属于本任务的数据；
- 工作树含无法安全归属、与目标文件直接冲突的用户未提交改动；
- 同一不可绕过 blocker 连续满足仓库规定的 blocked 判定，且所有独立阶段均已完成。

Podman 不存在本身不是停止整个批次的条件，只是 B/C 真实验收 blocker。

## 最终交接格式

不要只在聊天里说“完成”。将以下事实写进唯一 task record，并在最终回复中简洁复述：

```text
branch / HEAD
阶段提交列表
Adaptive 四种模式与截图路径
layout state owner、schema version、损坏/并发策略
App version transition/rollback 的 owner、事务与幂等证据
Podman/cgroup 宿主探测原始 verdict
Runtime/Reliability capability 是否真的升级及理由
每条验证命令与真实 PASS/FAIL/SKIP/BLOCKED
未决风险与下一台 acceptance host 的精确复验命令
工作树是否干净
是否 merge/push（默认都没有）
```

如果宿主满足全部条件，持续推进到第一版 14 项全部具备真实 working 证据。如果宿主不满足，持续推进到
Adaptive Shell 与 Web Bundle version rollback 完整闭环、所有软件侧门禁通过、唯一剩余项被精确收敛为
rootless acceptance host blocker；绝不以等待用户或环境为由放弃其余可完成工作。
