# Cursor 执行任务书：V2 P3 发布链收尾、故障验收与修复

> 将本文件完整交给 Cursor。你是实现智能体，需要完成代码、测试与验收证据，
> 不要只返回计划或另写一份 prompt。仓库默认位于 `/home/aquatao/workos`。
> 本任务基于 `main@6e8bf37` 的静态核对，日期为 2026-09-17 UTC；开始时必须核对实际 HEAD。
> 本文件中的历史 PASS 来自仓库记录，不是本任务已经运行的结果。

## 1. 下一步为什么是这个任务

先读 [V2 设计](../structure-v2.md)，再看实现和证据。V2 文档写于 2026-09-15，
其中“下一项开发任务建议”是当时的判断，不能跳过后续实现，重新从 P0/P1 开始。

目前的事实是：

| 阶段  | 当前事实                                                                 | 本轮处理                                                   |
| ----- | ------------------------------------------------------------------------ | ---------------------------------------------------------- |
| P0–P1 | 持续会话、Runtime 隔离工作区、真实 DeepSeek 两轮改码已有记录             | 保留实现，运行相关确定性回归                               |
| P2    | 程序与 Surface 生命周期、接管已有软件证据；第二台物理 LAN 设备验收仍缺失 | 保留缺口，不用本机多 context 替代                          |
| P3    | `app-bundle.v1`、Docker Build/Test、正式运行、发布和 A→B→A 回滚已合并    | **补齐失败、并发、崩溃、授权及兼容性矩阵，修复发现的问题** |
| P4    | 跨网络、候选图形后端等仍是后续扩展                                       | 本轮不启动                                                 |

P3 总任务明确保留 `scaffolded`，A06/A08/A14/A15/A16/A17 为 PARTIAL。
不能因为相关模块的 `docs/status.json` 总状态是 working，就认定完整 P3 已验收。

静态核对还发现两处证据边界，必须在本轮补齐：

1. `tests/integration/p3_delivery_test.go` 的 `p3StartRepair` 直接插入测试 Incident。
   它证明 Incident 之后的真实交付链，但没有证明真实 Docker 故障自动产生 Incident 并进入修复。
   原 P3 任务书 C09 要求的完整维护主链，需要至少一条真实监督触发的验收。
2. `apps/desktop-web/e2e/p3-delivery.spec.ts` 用 `page.request` 调 RPC，并直接打开 Surface URL。
   这是真实浏览器/HTTP 证据，但没有完整证明用户从桌面应用入口、版本窗口完成操作。
   原 C07/C09 的桌面用户链需要补一条不 mock 业务 RPC 的 E2E。

参考事实源：

- [P0–P2 最终任务](../tasks/20260915-v2-agent-workspace-web-continuity.md)。
- [P3 单一功能任务](../tasks/20260916-v2-p3-real-artifact-delivery.md)。
- [P3 合并验证记录](../tasks/evidence/20260916-v2-p3-delivery/review-validation.md)。
- [原 P3 任务书](20260916-glm-5.3-v2-p3-real-delivery-goal.md)，重点 C08–C10、A01–A20。
- [ADR-0033](../decisions/0033-app-bundle-v1-artifact-delivery.md)。

## 2. 唯一目标与范围

完成现有 P3 的 C08 和遗漏的用户链验收，让每个必需项都有可复现证据，
并修复测试实际暴露的问题。正常路径必须继续证明：

```text
真实 A 应用 → 真实运行故障 → Runtime observation → Reliability Incident
→ Harness fixture 提交修复源码 → Docker Build/Test → preparing 包
→ 持久 succeeded 作业精确绑定 → Runtime ready 权威事实
→ Core staged 版本 → 正式 Docker B → 实际健康与身份 canary
→ published → 用户回滚或 B 故障自动回滚 → 真实 A 行为恢复
```

默认技术选择已经冻结：沿用 ADR-0033 的固定 digest 基础镜像、不可变应用包、
Docker 正式 runner、现有 Core/Runtime/Reliability 服务。不要更换发布架构。

包含：故障测试设施、真实 PostgreSQL/容器/RPC/E2E、新发现缺陷的最小修复、
必要的增量协议或迁移、文档和状态同步。

不包含：P4、公网部署、Kasm/KasmVNC、镜像市场、OCI registry、包自动 GC、多主机调度、
Provider 升级、真实模型收费验收、移动原生或 rootless 专项。
Release 的 AppID、旧包摘要和细粒度 failure category 当前未完整填充，默认保留空值及
诚实显示，不为了展示完整而增加一条新业务主线。若发现虚假成功、越权读取或误导状态，
属于本轮必须修复的问题；不得用候选 B 的摘要补作旧包 A 的摘要。

## 3. 工作方式与不可越过的边界

1. 阅读根 `AGENTS.md`。先执行 `git status --short --branch`、`git worktree list`、
   `git log -8 --oneline`，确认已有工作及分支。
2. 本轮只用一个功能分支 `feat/v2-p3-closeout`、一个 worktree、一个写入智能体，串行推进。
   分支已存在就检查并续作。沿用当前工作区即可；不要覆盖用户改动、自动 stash/reset/rebase。
   若当前是仅包含本任务书的文档分支，可从当前提交创建功能分支，记录基线和保留的文档改动。
   不另建 review/fix 分支，不启动并行写入智能体。
3. 认领 `docs/tasks/20260916-v2-p3-real-artifact-delivery.md`，追加“2026-09-17 收尾”章节。
   **这是唯一功能任务记录**；不要把 prompt 编写任务当功能任务，也不要覆盖 09-16 的历史证据。
4. 在任务记录建立下文 D00–D07 工作包和 F01–F27 用例映射，先写范围、依赖和验收。
   新证据放 `docs/tasks/evidence/20260917-v2-p3-closeout/`。
5. 六个进程和数据所有权不变。Domain 不导入数据库、HTTP、Connect、文件系统或 adapter。
   产品代码不得跨模块查表，不引用其他模块的 internal adapter。跨进程通过现有 port/RPC。
6. 契约确需变化时，先改 `api/proto`/唯一 manifest JSON Schema，再 `make generate`。
   v1 字段号不复用；删字段/枚举必须 reserved；破坏性变化要新版本与 ADR。
   迁移在 `internal/platform/migrations/files/` 检查最新全局编号后追加，不能重写已合并迁移。
7. 不手改 `gen/`、`src/gen/`、sqlc 生成文件和 README 状态区块。
   不用 TODO、fake runner、常量成功、吞异常、删断言或无限增大 timeout 消除失败。
8. 所有模型响应使用已有确定性 fixture；测试数据、凭据和证书仅属于隔离测试环境。
   不读真实 Provider key，不消耗真实模型额度，不把部署环境的 secret 放到诊断里。
9. 本任务不要求 push、合并 main 或部署。完成实现和验证后交付当前分支与精确差异。
10. 每做完一个工作包，记录实际命令、结果、风险和下一步。会话中断后重读此文件与总任务续作。
    不因上下文不足而宣称完成；不把“没有运行”改写成“应该通过”。

## 4. 先读哪些文件

按顺序读，结合目标代码与现有测试。下列是当前存在的入口，不是要求全部修改。

| 主题            | 入口                                                                                                                                                                                                                                                             |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 设计/边界       | `docs/structure-v2.md`；`docs/structure.md` 的相关 Runtime/Reliability/Surface 章节；`docs/architecture/implementation.md` 的 P3、Build/Test、App grants、版本回滚章节                                                                                           |
| 已接受规则      | `docs/decisions/0033-app-bundle-v1-artifact-delivery.md`；`0003-mutable-app-grants.md`、`0012-owner-triggered-app-version-rollback.md`、`0026-runtime-build-test-and-candidate-publication.md`、`0032-runtime-isolated-development.md`（均在 `docs/decisions/`） |
| 私有构建/包协议 | `api/proto/workos/taskexecution/v1/build.proto`、`repair.proto`；`api/proto/workos/runtime/v1/artifact.proto`                                                                                                                                                    |
| 运行/公开状态   | `api/proto/workos/workload/v1/workload.proto`；`api/proto/workos/surface/v1/surface.proto`、`surface_resolver.proto`；`api/proto/workos/release/v1/release.proto`；`api/proto/workos/app/v1/installation.proto`                                                  |
| 包格式/存储     | `internal/platform/appbundle/`；`internal/runtime/artifactstore/{application,adapters/files,adapters/postgres,transport}/`                                                                                                                                       |
| 构建与 lease    | `internal/runtime/buildtest/application/service.go`；`adapters/postgres/queries.sql`、`repository.go`；`adapters/dockerbuild/engine.go` 和现有测试（后三项相对 `internal/runtime/buildtest/`）                                                                   |
| 注册与版本      | `internal/core/appregistry/application/staging.go`、`staging_test.go`；该模块 postgres adapter；沿调用关系读取安装版本 CAS 与授权实现                                                                                                                            |
| 正式容器        | `internal/runtime/workload/adapters/dockerapp/engine.go`、`engine_test.go`、`engine_docker_test.go`；`application/{manager,control,observe}.go`                                                                                                                  |
| 发布/回滚       | `internal/reliability/application/{buildtest,deployment,release}.go`；`transport/deployment.go`、`deployment_bundle_test.go`；`adapters/postgres/queries.sql`                                                                                                    |
| 主门禁          | `tools/v2-p3-delivery/gate.sh`、`compose.yaml`、`probe.sh`；`tests/integration/p3_delivery_test.go`、`repair_buildtest_test.go`、`deployment_reconciliation_test.go`                                                                                             |
| 旧行为回归      | `tests/integration/mutable_grant_revocation_test.go`、`mutable_project_app_grants_test.go`、`web_bundle_surface_test.go`、`web_bundle_surface_resilience_test.go`、`session_tool_authority_test.go`；`tools/v2-completion/gate.sh`                               |
| 桌面/E2E        | `apps/desktop-web/src/VersionDialog.tsx`、`VersionDialog.test.tsx`；`apps/desktop-web/e2e/p3-delivery.spec.ts`、`version-dialog-visual.spec.ts`、`mutable-grants.spec.ts`、`web-bundle-surface.spec.ts`、`app-version-rollback.spec.ts`、`open-app.ts`           |
| 仓库门禁/视觉   | `Makefile`、`docs/ui/README.md`、`docs/status.json`、`docs/status.schema.json`                                                                                                                                                                                   |

搜索优先 `rg`；没有 `rg` 就用 `grep`/`find`，不为搜文件安装一套新工具链。

## 5. 必须始终成立的断言

后续每个测试都要明确验证其中哪些断言。不要只断言 RPC 返回 200 或台账有一行。

- **I1 来源精确**：job、task、owner、project、installation、app、源码摘要、manifest 摘要、
  配方、基础镜像、artifact ID/digest 按既有协议一一对应。
- **I2 发布权精确**：只有持久成功作业所绑定的有效包可以 ready；Core 必须独立反查 Runtime。
  同一作业只有一个有效胜者。相同内容的不同合法来源可以有不同 metadata ID，不能错误合并来源。
- **I3 失败无新增部署副作用**：对于 build/test/output 失败，候选无 ready、无 staged、
  无 canary/published、无候选 B 容器、无安装 pin/revision 改动；A 保持可用。
  失败记录或不可部署的 preparing/failed 元数据可以存在，不能把它们误当成“零记录”要求。
- **I4 真实运行**：声明 B 成功必须同时匹配 Core pin、Runtime 包身份、容器 inspect、
  Workload ID **及** generation、Surface 和 Gateway HTTP B 行为。不能只比较 generation 数字。
- **I5 回滚真实**：只有旧 A 包可用且 A 确实启动并可访问才可 `rolled_back`。
  仅回退 Core pin 不等于恢复；旧包缺失保持准确的 pending/失败状态与有界重试。
- **I6 用户选择优先**：用户换版、卸载、归档或相关授权撤销不能被迟到的自动请求恢复。
  按既有授权定义判断影响；撤一个无关 App grant 不等于撤销 owner 的全部发布权限。
- **I7 代次隔离**：旧 worker 的 token、旧 Workload/Surface 的动作回执不能操作新实例。
- **I8 失败关闭与隔离**：包/镜像/挂载/资源/网络身份不满足时明确失败或 unavailable，
  不自动换成宿主执行、旧镜像内容、重新构建或 fake engine。

## 6. 工作包与执行顺序

### D00：记录基线，建立验收清单

**依赖：无。** 完成第 3、4 节要求，并把实际 HEAD、未提交改动、工具链记录到总任务。

先读 `gate.sh` 的 prerequisites 和退出码，再运行现有主门禁取得当前基线。
默认入口是 `make test-v2-p3-delivery`；`sh tools/v2-p3-delivery/gate.sh` 为同一 bundle 门禁。
旧模式是 `sh tools/v2-p3-delivery/gate.sh legacy`，不能拿它替代 Docker bundle 证据。

不要重新跑真实模型验收。若宿主没有 make/Go/Node，先检查本地固定工具链镜像，
参考 09-16 验证记录使用 Docker；记录实际执行命令，不能只写等价的理想命令。
主门禁依赖 `workos:dev`、固定 Go/Playwright/PostgreSQL 镜像等，读取脚本确认完整清单。
镜像准备和 runner 执行分开：运行时 `pull=never` 不得为测试便利改掉。

产出：基线命令/日志；F01–F27 尚未运行或已有覆盖的逐项映射。已有单测不足时标 PARTIAL。

### D01：只增加本轮需要的确定性故障注入

**依赖：D00。** 优先复用真实 repository 的包装器、测试 HTTP 代理和现有隔离 compose。
建议新测试放 `tests/integration/p3_*_test.go`，沿用当前 build tags；实际名称在任务记录固定。

注入必须能证明“已经到达指定边界”，不能用 `sleep 1` 后随便杀进程碰运气：

1. 用 barrier 通知测试驱动：已写 bundle、已写 preparing、已提交 verdict、已提交 Core CAS、
   已创建/启动容器等；驱动确认到达，再中断或释放。
2. “丢回复”要让真实 handler/数据库提交完成，再切断调用者收到的响应；
   请求未到服务端的普通 503 不能代替提交后丢回复。
3. 多 worker 测试使用两个独立 repository/执行者，共享本用例 Runtime DB/包库、不同 lease token。
   这是同一 Runtime 角色的竞争测试，不是新增生产进程类型或生产多实例部署功能。
4. 内部提交窗口可用仅测试构建启用的 hook，或测试包装真实 port。hook 只负责暂停/出错，
   不伪造业务事实。默认生产构建不得暴露故障 HTTP endpoint、绕过授权或开放注入配置。
5. 若集成测试用 SQL 检查 owner 的数据或构造损坏，严格限于自有测试 DB并注释目的。
   真实业务操作仍走服务；不插入成功 job/ready 包/published 台账来冒充主链。
6. 每次阻塞有 deadline，超时输出固定阶段名及脱敏身份；每个用例独立项目/安装/键，
   不依赖其他用例的运行顺序。失败也要清理自有资源。

产出：可控注入点与用例映射。不要造通用 chaos 平台、公开调试 API 或全仓 fault framework。

### D02：补真实故障触发和桌面用户链

**依赖：D01。** 保留已有 SQL-seeded Incident 测试，注明其证据范围；新增 F01/F02。

- F01：先经 admin 导入、Core 注册/安装、真实 Surface 启动 A，断言 A HTTP 可用。
  然后只针对本用例、已核对标签与 ID 的 A 容器制造确定性异常退出/健康故障。
  用真实 Runtime observation 和 Reliability 监督生成 Incident；读取事件/查询接口证明
  Incident 对应这次 Workload 和 occurrence，重复观察不会重复生成相同 occurrence 的修复。
  从该 Incident 继续走真实 B 构建、canary 和发布；独立 HTTP oracle 证明 B 与 A 不同。
  不用旧 `real_supervision` fake-fixture gate 冒充此次 Docker 监督证据。
- F02：浏览器准备数据可调用 RPC，但操作阶段从真实 Desktop 切项目、打开 App Library/
  应用窗口、进入 Versions、点击已有 owner rollback 入口；验证用户实际看到 B、回滚后 A。
  业务 RPC 不 route mock。网络断开/查询失败不能继续显示为新成功。
  前端现有命名和导航通过 `open-app.ts` 等确认，不为了测试重写 UI 或加入专用按钮。

至少一次主贯穿序列带真实自动恢复 A；已有启动失败回滚仍保留。
正常用户版本替换必须继续断言没有新误报 Incident 导致系统又把 A 改成 B。

### D03：构建、产物、幂等与租约

**依赖：D01；在 D02 基础上验证失败路径。** 必需用例见下表。

| 编号 | 场景与注入点                                                          | 必须断言                                                                 |
| ---- | --------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| F03  | 真实 Docker build 非零退出                                            | I3；失败阶段/退出码准确；A HTTP 仍可用                                   |
| F04  | build 成功并冻结输出，真实 test 非零退出                              | 冻结包不得 ready；I3；不是只有原 process engine 测试                     |
| F05  | 缺输出目录、空输出、缺 runtime 入口、超文件数、单文件/总量越限        | 按实际 ADR 限制分别拒绝；代表性六进程用例+完整格式/engine矩阵；I3        |
| F06  | timeout、输出日志超预算、显式 cancel 分别发生在执行/提交前            | 进程有界退出，取消不能被晚到 success 覆盖；I2/I3                         |
| F07  | 同 task/key 同输入并发提交；分别改变源码、配方、来源或包字节重试      | 同输入一个结果，漂移稳定冲突；拒绝请求不覆盖胜者；重启后重放一致         |
| F08  | 两个真实执行者竞争，旧 lease 到期，新 worker 接管，旧 worker 晚到提交 | 旧 renew/verdict 无效；至多一个有效成功 job/包绑定；旧执行不会停止新实例 |
| F09  | 真实导入及构建结果查询/注册调用提交后丢回复，再以同键重试             | 返回原 identity/结果，不重复 metadata/版本/pin历史；与“提交前失败”分开测 |
| F10  | 相同字节、不同 task/import key/合法 app 来源                          | 磁盘可去重；metadata 来源独立；其他 owner 或 app 不能借 digest 冒用      |

F08 必须含真实 PostgreSQL lease/CAS 测试和至少一个真实 Docker 交接实例。
不能仅用一个 goroutine 加内存 mutex 或 monkey patch 完成。
取消与成功竞争时，按数据库真实线性化先后断言：已提交成功之后的取消不能被测试强行要求
改写成功；需要证明的是“取消先提交时，旧 lease 的成功无效”。

### D04：逐一覆盖持久化窗口

**依赖：D03。** 对每行记录恢复前事实、重启的进程、恢复后身份与最终结果。

| 编号 | 中断边界                                                           | 恢复要求                                                                           |
| ---- | ------------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| F11  | 包文件已持久化，metadata 尚未提交                                  | 文件存在不等于 ready；重试能安全收敛或明确失败；孤儿仍计入容量，不越界清理         |
| F12  | preparing 已提交，succeeded verdict 尚未提交                       | 重启读取不得 ready；失效 token 无提交权；新 lease 完成合法成功才可继续             |
| F13  | succeeded verdict 已提交，ready 查询晋升/回复之前                  | GetBuildArtifact 必须比对 job ID/digest及真实字节；同包恢复 ready；取消/不匹配拒绝 |
| F14  | Core 注册 staged 前/后；安装 pin CAS 成功而 Reliability 未保存回执 | 重放收敛同版本/台账，来源不漂移；不重复历史；用户已换版则停止旧流程                |
| F15  | 容器 create/start 已完成，Workload/Surface 回执未持久化            | 精确 inspect 后认领/终止自有容器，不能仅据 label 宣称 running；无泄漏的重复实例    |
| F16  | canary 中间 Runtime/可靠性进程重启；Workload ID 或 generation 改变 | 身份未变才能按合法窗口继续；替代实例不能继承旧成功观察；不能只等墙钟到期           |
| F17  | publish/rollback RPC 已提交，Reliability 台账提交前进程中断        | 同键收敛，至多一个有效发布；不能重复回滚到更早版本；未实测 A 不得 rolled_back      |

选择测试边界要对应真实实现的事务边界。若某两步已在同一事务，测试原子回滚/丢回复，
不要为了构造不存在的窗口拆开事务。F11–F13 至少有真实文件系统和 PostgreSQL，
F14/F17 要有真实 RPC 与持久化，F15/F16 要有真实容器，不能全降为 mock。

### D05：授权、损坏、canary 和回滚失败

**依赖：D02–D04。** 实现以下矩阵，沿用现有业务授权和错误语义。

| 编号 | 场景                                                                                    | 必须断言                                                                                            |
| ---- | --------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| F18  | canary 中 owner 主动升级到 C 或手动回滚 A，随后旧流程继续/重启                          | 旧流程 superseded 或依契约停止；不能覆盖用户 pin/revision；迟到 rollback 不降回更旧版本             |
| F19  | canary 中卸载、项目归档；相关 grant 撤销/epoch变化后重放旧请求                          | 无“复活”安装/授权；旧 Surface/Bridge 被拒；重新授权使用当前事实；合法 owner 行为不被无关 grant 错杀 |
| F20  | 同一安装重复 Offer；两个不同 Incident 的候选竞争；另一安装同时发布                      | 同安装候选按现有锁/CAS串行或拒绝，不互相回滚；不同安装不会错误阻塞或共享结果                        |
| F21  | 启动前包删除/篡改、缺基础镜像；canary 期间包/运行身份漂移                               | 返回明确不可用/失败并停止错误推广；不隐式拉取、不重编代替、不启动旧字节冒充 B                       |
| F22  | B 启动失败、canary 真实健康故障或新 Incident；回滚需要的 A 包缺失                       | A可用时实测恢复；A不可用时 pending/有界失败；绝不仅凭 pin 写 rolled_back                            |
| F23  | 非法 tar 类型、symlink/hardlink、绝对/逃逸/重复路径、畸形结束、容量耗尽、损坏后去重命中 | 格式/存储逐项拒绝；至少一个真实 admin 流式恶意包和配额/损坏集成；不读写包库外文件                   |
| F24  | 其他 owner/project/app 引用 ready 包；把 import 当本次 build 产物；公开调用私有包服务   | 来源/授权失败，Core 无注册/安装副作用；Gateway 不暴露 admin/private service                         |
| F25  | 版本切换后旧 stop/Surface 回执迟到；正式 runner 重启对账                                | 不影响新 Workload；按 ID+generation拒绝旧动作；不清理 workspace/interactive 的容器                  |

F19 在测试前先从 ADR/实现列出“每个动作所需授权、撤销后预期错误/状态”。
若当前 P3 fixture 没有相关 grant，应增加确实需要该权限的 fixture，而不是把空权限表
改空后宣称“撤权已测”。用户改版/卸载/归档走公开 API；不得直接改 Core SQL 绕过授权逻辑。

F21 的 canary 包篡改只针对测试 namespace 的数据；证明 Runtime 的权威检查发现问题。
不要求把宿主管理员任意内存修改当作容器隔离承诺，也不能以管理员权限为由省略已承诺的
持久包/挂载摘要检查。缺镜像用本测试独有的不存在 digest，不删除共享基础镜像。

### D06：旧行为、桌面与门禁集成

**依赖：D02–D05。** 执行 F26：新旧链路回归，覆盖原 P3-A17。

最低集合：

1. 现有 bundle 主链及全部新 F01–F25 用例进入可重复运行的门禁。
   优先扩展 `tools/v2-p3-delivery/gate.sh`；若增加 `faults` 模式或新 Make target，
   必须真实实现并在总入口调用/明确列出，不能在文档中虚构现有目标。
2. 旧 image-only/process/fake-fixture：在隔离环境运行
   `sh tools/v2-p3-delivery/gate.sh legacy`，覆盖原 repair-buildtest 全组。
   原 `make test-repair-buildtest` 保留兼容入口，不改成新 bundle gate 的别名。
3. App grants：`mutable_project_app_grants_test.go`、`mutable_grant_revocation_test.go`
   对应集成测试，以及 `mutable-grants.spec.ts`；新旧 grant epoch、重新授权和旧入口拒绝。
4. Web Bundle：现有 surface/bridge 集成、`web-bundle-surface.spec.ts`，必要的
   `app-bridge.spec.ts`/`app-bridge-full.spec.ts`；旧 bundle资产、授权、消息通道与清理不被破坏。
5. Workspace/Task lease：`make test-v2-completion` 的确定性官方运行时 fixture 链，
   同工作区文件/命令/预览、会话恢复和 `session_tool_authority_test.go` 的相关授权回归。
   不运行 `make test-real-model-acceptance`；fixture 的证据不写成新增真实 DeepSeek 验收。
6. owner 手动版本回滚相关集成与 `app-version-rollback.spec.ts`，加 F02 桌面真实用户链。

第 3/4/6 项没有可直接套用的隔离专项 target 时，应复用现有隔离门禁并参数化测试 URL/
owner/端口；不能因为默认测试硬编码 8080 就重置共享开发栈。记录每个实际测试名和命令。
一个 `make check` 不会自动覆盖带 build tags 的集成和 Playwright，不足以交付 F26。

门禁必须：

- 每次自有 DB、包目录、compose project、端口、namespace；所有主链二进制由当前源码构建。
- 缺 Docker/镜像/浏览器/DB 时退出非零并说明条件；选中的必需用例为零、被跳过也不能总 PASS。
  特别核对 Go `-run` 和 build tags，收集测试清单或 JSON 结果防止“没有匹配测试”伪通过。
- 清理只针对自有资源。分别验证成功、失败和可捕获的 INT/TERM 退出路径。
  禁止全局 prune、删除共享 volume 或 `docker compose down -v` 操作共享栈。
  SIGKILL 无法执行 trap，应保留 namespace 并记录精确补清理方法，不虚称所有信号都能自动清理。
- 原始日志放临时目录；提交脱敏摘要及必要 fixture 证据，不能提交 token、cookie、源码全文。

UI 规则：仅补不可见测试且产品无可见变化时，在任务记录说明并链接现有 A18 证据即可。
一旦状态、文案、像素或交互发生变化，执行 `docs/ui/README.md`：

```text
docs/ui/desktop-web/changes/20260917-v2-p3-closeout/before/
docs/ui/desktop-web/changes/20260917-v2-p3-closeout/after/
docs/ui/desktop-web/changes/20260917-v2-p3-closeout/notes.md
docs/ui/desktop-web/current/
```

改 UI 前复制受影响 current 作为 before；使用实际组件、同一确定性 RPC fixture、Chromium、
deviceScaleFactor=1，沿用 `1440x900`、`820x1180`、`390x844`。
复用 `version-dialog-visual.spec.ts`，覆盖受影响的成功、回滚和失败状态。
视觉 fixture 仅用于截图；F02 必须另跑真实业务栈。没有视觉证据的 UI 修改不得标 done。

### D07：生成、全仓检查与事实同步

**依赖：D00–D06。** 执行 F27：仓库一致性和交付。

按实际修改运行必要的 targeted test/race、全部必需集成/E2E，再执行：

```sh
make generate
make check
git diff --check
git status --short
```

对 lease/产物/容器/发布并发修改，至少执行受影响包的 `go test -race`，例如：

```sh
go test -race ./internal/runtime/buildtest/application ./internal/runtime/artifactstore/... ./internal/reliability/application ./internal/reliability/transport
go test -race ./internal/runtime/workload/adapters/dockerapp ./internal/runtime/workload/application
```

没有宿主 Go 就用 Makefile 固定 Go 镜像执行上述同一命令，不升级依赖绕过现有工具链。
Proto 有变化时，再执行相对实际开发基线的 buf breaking；基线固定到 D00 记录的提交，
不要对包含自身变更的引用做空比较。

生成一致性分清两件事：预期生成物属于提交；再次 `make generate` 不得产生新增差异。
未提交阶段可以先保存第一次生成文件的校验和/完整 diff，再比较第二次；
不能要求功能改动相对 main 的生成差异为零，也不能只忽略未跟踪生成文件。

更新：

- 单一 P3 总任务：D00–D07、F01–F27 结果，原 C08 与 A01–A20 当前结论，命令和证据链接。
- `docs/architecture/implementation.md` 的相关 P3 边界和新增证据；受影响模块文档；
  若确定行为变化，同步 ADR，不能让实现与 Accepted ADR 相互矛盾。
- `docs/status.json` 仅更新受影响模块的 evidence/日期及有证据支持的状态；
  既有模块整体 working 不等于所有 P3 用例 PASS；缺项明确写入 evidence。
  不为本任务任意升级未涉及模块，不新增不符合 schema 的顶层阶段字段。
- README 状态只经生成工具同步；UI 有变化时更新 before/after/current/notes 并在任务链接。

新增证据目录至少包含 `README.md`：代码提交、环境、命令/退出码、用例结果、
测试数据/HTTP marker、job/artifact/manifest/Workload 的对应关系、限制和原始日志索引。
历史日志可引用，但不能伪装成本次重跑。临时目录不是长期证据的唯一链接。

## 7. 验收映射与完成判定

原 A 编号必须写作 **P3-Axx**，避免与 P2-A16“物理 LAN 第二设备”混淆。

| 原缺口/要求                             | 本轮用例         | 最低证据                                                           |
| --------------------------------------- | ---------------- | ------------------------------------------------------------------ |
| P3-A04/A05/A09/A10/A19 完整维护与用户链 | F01/F02          | 真实故障→Incident→Build/Test→Docker B；桌面操作和真实 Gateway HTTP |
| P3-A06 失败零发布                       | F03–F06          | Docker失败+PG/Core/Runtime/Reliability零新增发布副作用             |
| P3-A08 并发与重放                       | F07–F10、F20     | 真实PG竞争/lease、丢回复、不同来源同字节                           |
| P3-A14 崩溃恢复                         | F11–F17、F25     | 指定提交边界的真实中断、重启和迟到请求                             |
| P3-A15 授权/用户改版                    | F18/F19/F24      | 公开API改变用户选择、旧流程拒绝、跨作用域无副作用                  |
| P3-A16 安全/包损坏                      | F21–F25          | 真实包/镜像/身份拒绝，格式和来源矩阵                               |
| P3-A11/A12/A13 canary/回滚              | F16–F18、F20/F22 | 精确ID+generation，真实HTTP恢复及恢复失败诚实状态                  |
| P3-A17 新旧兼容                         | F26              | 原修复、grants、Workspace/lease、Web Bundle、owner rollback 实测   |
| P3-A18 UI证据                           | F02及D06视觉流程 | 用户交互E2E；可见变更时三尺寸before/after/current                  |
| P3-A20 仓库一致                         | F27              | 生成一致、make check、相应race、文档状态一致                       |

一行用例含多个子场景时，全部完成才能该行 PASS；至少给每个子场景关联测试名称或证据。
“部分是单测、部分是真实链”要明确写出；不要把全部组合都声称六进程 E2E。
旧主链 P3-A01/A02/A03/A07 等也必须确认仍有效，不能只填这张缺口表就删掉原验收项。

完成必须同时满足：

1. F01–F27 和原 P3 必需验收均有证据，发现的问题已修复并验证，相关回归通过。
2. `make generate` 可重复无新增生成差异、`make check` 通过；有 UI 变化则证据齐全。
3. 任务、实现说明、状态、协议与真实行为一致；不依赖未提交的临时脚本才能复现。

若缺外部环境，写明缺什么、哪个命令失败、哪些编号受影响及恢复步骤，继续所有不依赖它的工作。
未完成项保持 PARTIAL/BLOCKED，P3 整体仍 scaffolded，不能写“任务完成，只差测试”。
P2 的第二台物理 LAN 设备、P4、rootless、Docker memory.high 不属于本轮完成条件，
但必须原样保留限制；不能拿这些明确排除项阻塞本轮，也不能因 P3 收尾成功顺手声称它们通过。

## 8. 最终向用户交付什么

最终回复简洁，但必须包含：

1. 本轮是否完成 P3 收尾；实际修复了什么。
2. F01–F27/原 P3 缺项的 PASS、PARTIAL、BLOCKED 概况，链接总任务和证据索引。
3. 已验证的实际命令、失败或未运行项目、当前分支/HEAD及工作树状态。
4. 用户可复现的主门禁与新增故障门禁入口；如有 UI 改动，附视觉证据链接。
5. 保留 P2-A16/P4/rootless/memory.high 边界。全部 P3 验收通过后，才建议独立安排
   物理 LAN 验收，并根据可用网络条件另开 P4 最小跨网络浏览器接续任务。

现在开始 D00。完成前持续实现和验证，不要停在这份任务书的复述上。
