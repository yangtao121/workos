# V2 P3 真实产物、运行版本与回滚（C00–C10）

- 范围：[任务书](../prompts/20260916-glm-5.3-v2-p3-real-delivery-goal.md) 的 P3 单机 App 发布链；六进程边界不变。
- 09-16 历史基线：本地 main `3820447`；接管 `feat/v2-p3-real-artifact-delivery@df47ecf` 与同任务未提交实现。
- 用户已授权：审查、修复、验证并合并本地 main；不推送。无关 `.zcode/plans/` 文件保留。
- 决策：[ADR-0033](../decisions/0033-app-bundle-v1-artifact-delivery.md)。
- 当前结论：P3 F01–F27 完整矩阵已通过；见 [09-20 最终验收](evidence/20260920-v2-p3-final-matrix/results.md)。V2 的原生自动化、网络与移动交付继续独立推进。
- [09-16 审查记录](evidence/20260916-v2-p3-delivery/review-validation.md) 保留历史；[09-17 修复接续](evidence/20260917-v2-p3-closeout/repair-validation.md) 为当前合并证据入口。

## 合并前审查与修复

1. 产物：修复管理员流式上传重复读取同一 chunk；按 task/import key 保存独立来源，
   相同字节只在磁盘去重。owner/app/配方/源摘要/输出字节均参与重放验证；损坏文件
   不因去重而被接受。owner 文件锁与实际磁盘用量覆盖跨进程写入、暂存和孤儿文件。
2. 构建：用 1 GiB tmpfs volume 和 Docker archive API 替代宿主可写 bind mount；
   测试前冻结输出，拒绝链接、越界、缺入口和超额文件。实时日志预算、阶段 timeout、
   唯一 lease token、短租约续期及到期 SQL 守卫限制取消/重试/崩溃后的提交权。
   包先 preparing，只有持久成功作业精确绑定该包才可 ready。
3. 正式运行：持久化 Workload artifact 身份；补齐私网桥 endpoint 的数据库约束；
   校验真实镜像配置 ID、包摘要、挂载源、内部网络及资源策略；包由非 root 用户运行。
   修复 Docker Start/Stop 的 304 幂等响应及改版后旧 Workload 阻止立即启动的问题。
   解包在私有临时目录完成后原子发布，避免并发对账误读半包；启动前无 IP 的容器可
   安全接管，运行态仍要求有效 endpoint。新增正常换版不得产生额外 incident 的 E2E 断言。
4. 发布：回滚重新解析旧 pin 对应的 A 包；canary 每轮验证 pin、运行身份和健康，
   并持久化初始 Workload ID/generation，替代进程不得继承观察窗口。
5. UI/门禁：版本窗口轮询 Release 状态，错误显示 unavailable。截图改用真实组件
   与 RPC fixture。门禁使用当前源码构建的六进程、自有 PostgreSQL、目录和端口；
   缺依赖即失败，不再跳过栈后返回 PASS。

## 工作包

| 包                 | 状态 | 依赖     | 实现与证据                                                         |
| ------------------ | ---- | -------- | ------------------------------------------------------------------ |
| C00 宿主探针       | done | 无       | 保留实测矩阵；Docker 不声称 rootless/memory.high                   |
| C01 契约冻结       | done | C00      | 增量 Proto/Schema，迁移 066–072；生成与仓库检查见验证记录          |
| C02 包仓库与导入   | done | C01      | 真正流式 CLI 导入、PG 持久化、重启重算；损坏与重放拒绝测试         |
| C03 构建输出冻结   | done | C01、C02 | 真实 Docker 构建/测试、隔离输出与来源绑定；异常矩阵部分依赖单测    |
| C04 Core 版本绑定  | done | C01–C03  | Core 独立反查 Runtime 权威事实；修复候选真实 staged/published      |
| C05 正式 Workload  | done | C00–C04  | Docker inspect/只读包/网关 HTTP A、B、A；重启恢复                  |
| C06 发布与回滚裁决 | done | C04、C05 | 发布、人工回滚、启动失败自动恢复的真实端到端证据；精确代次拒绝单测 |
| C07 用户可见状态   | done | C04–C06  | Release 轮询测试，真实组件三尺寸 before/after/current              |
| C08 完整回归矩阵   | done | C02–C07  | F01–F27 全通过，最终六进程与浏览器证据见 09-20 记录。              |
| C09 P3 门禁        | done | C00–C07  | 六进程真实主链 + Chromium + 重启；按 namespace 清理                |
| C10 本次审查交付   | done | C09      | 修复、测试、文档同步；完整 P3 的遗留验收单独列出                   |

## 验收编号

PASS 只覆盖证据列明的场景；PARTIAL 表示该编号要求的完整矩阵尚未实测，不能折算为 PASS。

| 编号                    | 结果 | 实际证据与边界                                                                                                                              |
| ----------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| P3-A01 runner/保护能力  | PASS | C00 探针、真实 Docker adapter；rootless/memory.high unavailable                                                                             |
| P3-A02 契约与生成       | PASS | 增量 Proto、Schema、066–072；buf 检查、两次生成一致                                                                                         |
| P3-A03 导入/持久化      | PASS | 真实 CLI 分块导入→PG→安装；三进程重启后相同摘要 ready；损坏/缺失拒绝单测                                                                    |
| P3-A04 正式 A           | PASS | 隔离六进程 Gate 的真实 Docker A，Gateway Surface 返回 P3-VALUE-0                                                                            |
| P3-A05 作业/产物绑定    | PASS | Generic CLI 候选→真实 Docker Build/Test→Core staged，独立 HTTP B oracle                                                                     |
| P3-A06 失败零发布       | PASS | F03–F06 失败与取消零发布完整矩阵通过。                                                                                                      |
| P3-A07 Core B 绑定      | PASS | ready/provenance 校验、staged runtime.artifact，公开 B 摘要不同于 A                                                                         |
| P3-A08 并发/重放        | PASS | F07–F10 并发、漂移、重启、丢回复和跨 owner 来源完整矩阵通过。                                                                               |
| P3-A09 正式 B           | PASS | 真实 Docker adapter inspect 与只读 mount；六进程启动 B                                                                                      |
| P3-A10 Gateway/浏览器 B | PASS | Chromium 经真实 Gateway Surface 读 A→B→A；独立于候选测试的 HTML marker                                                                      |
| P3-A11 canary 事实      | PASS | 实际 HTTP 健康、Core pin、Runtime identity；保存代次并每轮验证；代次漂移拒绝单测                                                            |
| P3-A12 自动恢复 A       | PASS | B 启动退出42→rollback_pending→rolled_back，随后公开 Surface 实测 A                                                                          |
| P3-A13 人工 rollback    | PASS | owner 公开 RPC + revision CAS；实际 A→B→A，Chromium 同链验证                                                                                |
| P3-A14 崩溃恢复         | PASS | F11–F17 真实进程中断窗口全部通过。                                                                                                          |
| P3-A15 授权/用户改版    | PASS | F18–F20 用户改版、卸载/归档、grant epoch 与退役注册拒绝通过。                                                                               |
| P3-A16 损坏/安全边界    | PASS | F21–F25 运行身份、配额、来源和生命周期安全矩阵通过。                                                                                        |
| P3-A17 新旧回归         | PASS | 本轮 legacy、V2 Workspace/Task lease、grants、Web Bundle、owner rollback 集成和 E2E 全过，见 09-17 修复实测。                               |
| P3-A18 视觉证据         | PASS | [notes](../ui/desktop-web/changes/20260916-v2-p3-real-artifact-delivery/notes.md)，真实组件固定 fixture，三状态×三尺寸 before/after/current |
| P3-A19 门禁隔离         | PASS | 自有 DB/六进程/端口/namespace，成功及失败路径清理；缺依赖不返回成功                                                                         |
| P3-A20 仓库一致         | PASS | make generate 无新增差异、make check、race、diff --check，见验证记录                                                                        |

## 未决风险与下一步

- P3-A06/A08/A14/A15/A16 的缺项已于 09-20 补齐；历史接续记录保留原时点结论，当前结果以下方 F01–F27 表为准。
- P3-A17 的本轮兼容回归结果见[修复实测记录](evidence/20260917-v2-p3-closeout/repair-validation.md)。
- Release 投影仍未完整填充 AppID、旧包摘要和细粒度 failure category，保持空值，不虚构事实。
- 不包含 rootless、Docker memory.high、P2-A16 第二台物理 LAN 或 P4，边界不变。
- 合并本轮缺陷修复不等于完整 P3 验收完成；完整 P3 保持 scaffolded。

## 环境基线（2026-09-16 实测）

- 宿主 Linux 7.0.0-31-generic x64，cgroup v2（`/sys/fs/cgroup` 为 cgroup2fs）。
- Docker 客户端存在（`/usr/bin/docker`），`/var/run/docker.sock` 存在；当前用户在 `docker` 组。
- 无 Podman。P0–P2 的 rootless 缺口保持不变；P3 Docker profile 不声称 rootless。

## C00 能力矩阵（2026-09-16 实测，ADR-0033 的 profile 依据）

复现：`sh tools/v2-p3-delivery/probe.sh`；证据 [c00-probe.txt](evidence/20260916-v2-p3-delivery/c00-probe.txt)。

| 能力                                      | 结果                     | 实测事实                                                                                                                                |
| ----------------------------------------- | ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- |
| 引擎/内核                                 | 可用                     | docker 29.7.2（client/server），kernel 7.0.0-31-generic，cgroup v2（cgroup2fs），overlayfs                                              |
| 镜像固定 digest                           | 可用                     | `golang@sha256:e8c859f5…`；镜像 RepoDigest 解析为配置 ID，与容器 inspect 的 `.Image` 交叉核对                                           |
| 禁止隐式拉取                              | 可用                     | `--pull=never` + 不存在镜像 → 明确失败                                                                                                  |
| 网络隔离                                  | 可用（经 internal 网络） | `--internal` 网络：容器出口内核不可达（`Network is unreachable`）、外部 DNS 失败；**internal 网络不支持端口发布**（`-p` 被静默忽略）    |
| App 可达性模型                            | 容器桥 IP                | 宿主网络命名空间可直连容器桥 IP（on-link 路由）；无 DNAT/FORWARD，外部主机不可达。runtime-host 必须与宿主同网络命名空间（或加入该网络） |
| 只读包挂载                                | 可用                     | bind mount `/app:ro`，容器内写被拒；bundle A/B 内容不同 → HTTP 响应不同（`WORKOS-P3-PROBE-A` ≠ `WORKOS-P3-PROBE-B`）                    |
| memory.max / pids.max / cpu.max           | 可用                     | 读回 268435456 / 64 / `150000 100000`                                                                                                   |
| memory.high（软高水位）                   | **不可用**               | `--memory-reservation` 不映射到 memory.high（读回 `max`）；Docker profile 如实声明 unavailable，不冒充 Podman profile 的 memory.high    |
| cap-drop ALL / no-new-privileges / 只读根 | 可用                     | CapEff=0、NoNewPrivs=1、根文件系统写入被拒                                                                                              |
| 重启后容器归属识别                        | 可用                     | 按 `workos.owner`+probe 标签，新客户端可精确列出自己容器                                                                                |
| 容器内构建                                | 可用                     | `--network none` + GOPROXY=off + 挂载工作区，固定镜像内离线 `go build` 成功（C03 基础）                                                 |
| Docker rootless                           | 不声称                   | rootful Docker；与 Podman/rootless profile 分开声明                                                                                     |

## 2026-09-17 收尾（C08 故障/用户链）

### 2026-09-19 接续：完整 P3 可靠性验收

- 目标：补齐本任务 F01–F27 目前 PARTIAL 子场景，沿用 ADR-0033 的 bundle/Docker 方案；不扩展到 P2 物理 LAN 或 P4。
- 本轮基线：`feat/v2-p3-acceptance-complete`，起点 `e6698b3`；执行前工作树干净，唯一 worktree 为 `/home/aquatao/workos`。
- 2026-09-19 基线门禁：`sh tools/v2-p3-delivery/gate.sh` **PASS**，证据运行目录 `tmp/v2-p3-delivery.m1nm3G/`（临时日志，后续形成脱敏长期摘要）。实际覆盖当前源码构建的隔离六进程、PostgreSQL、真实 Docker 产物/运行、监督修复、F01–F25 既有子用例、两条 Chromium E2E、重启 replay；总耗时约 37 分钟。
- 基线期间观察到 Release 查询在状态尚未创建时返回 `NotFound`、服务重启期间返回 `Unavailable`；查询继续后分别收敛至已预期的 `published`、`rolled_back`，因此不据此记录产品失败。
- 基线通过不等于完整矩阵完成：待补场景集中于 F06 的执行/提交取消竞争，F07 的并发和字段漂移，F09/F10 的导入与跨作用域来源，F19 grant epoch，F23 配额/损坏去重，F24 全来源授权，F25 生命周期清理及 runner 对账，以及 F26 INT/TERM 清理与兼容矩阵。
- 真实 Provider 凭据和收费模型未使用；当前共享开发栈 PostgreSQL 停止，本轮只使用门禁自建资源，不触碰共享部署。
- 2026-09-19 接续实测：见 [本轮结果](evidence/20260919-v2-p3-acceptance/results.md)。修改后的完整 `sh tools/v2-p3-delivery/gate.sh` **PASS**，涵盖 F01–F25 完整既有矩阵、更新的 F06/F07/F09/F10/F13/F19/F23/F24/F25、所有 F11–F17 实际进程窗口、两条真实 Chromium 业务 E2E 与重启 replay。`sh tools/v2-p3-delivery/gate.sh legacy` **PASS**；`sh tools/v2-completion/gate.sh` 从宿主机执行 **PASS**（三个 viewport 视觉用例报告 skipped，不当作视觉通过）；SIGTERM 清理实测容器 namespace 归零。`make generate` 无生成差异，`make check` 全通过。
- 尚未完成的仍是矩阵行内的组合场景：F07 漂移跨重启、F09 所有结果查询响应丢失、F10 跨 owner 去重来源、F14 注册前中断、F16 Runtime 中途身份漂移、F23 不同工件生命周期的配额核算、F24 cross-project/build-import 冒用、F25 陈旧 stop/generation/异类容器清理，以及本轮跳过的固定 viewport 视觉验收。因此 P3 继续保持 scaffolded。
- 下阶段：按上述剩余组合补充独立、确定性的真实栈场景；视觉用例在具备可确定 fixture 的 gate 中单独启用；全部矩阵证据齐备后再评估状态提升。

#### 本轮验收边界

- grant 测试将请求 `project.read` 放入专用 P3 manifest，并经公开 SetAppGrants 更改 epoch；只断言旧 Bridge 会话失效及新 Surface 使用当前授权，不把 App grant 当作 owner 发布授权。
- 配额测试使用 Runtime 实际 2 GiB 上限，并验证 ready/临时/孤儿字节计入、磁盘去重不重复计费及损坏内容不能被当作有效去重命中。
- 清理测试只面向本轮 Compose project、固定 namespace 和识别出的 runtime-app 标签；不执行全局 prune 或操作共享开发栈。
- P3-A18 沿用 2026-09-16 真实组件视觉证据，只有发现并修复用户可见行为时才新增本轮截图。

### 修复接续与合并验收

- 接续基线：`21b16f6`，分支 `feat/v2-p3-closeout`，单一 worktree、单一写入智能体。
- 保留前序未提交的 P3 实现、测试和证据；`.zcode/plans/` 属于既有工作区资料，不纳入功能提交。
- 范围：复现并修复收尾门禁失败、审查故障注入与并发/恢复边界、补相应回归及事实记录。用户已明确授权验证后合并到本地 `main`。
- 依赖：宿主仅有 Docker，无 Go/Node/make；使用仓库固定工具链镜像及 `workos-make:local`。
- 验收：真实 bundle/legacy/兼容门禁、受影响 race、重复生成和 `make check`；未覆盖子场景保持 PARTIAL，禁止以单个用例通过扩大为整行 PASS。
- 修复与验收：构建批次预算、Workload 生命周期并发、陈旧修复注册、取消轮询及故障基建已修复；bundle/legacy/兼容回归、race 和全仓检查通过，见下方实测记录。

- 分支：`feat/v2-p3-closeout`；基线 HEAD 开始于 `21b16f6`（任务书），fork 自 `main@6e8bf37`。
- 唯一功能任务仍是本文；新证据目录：[20260917-v2-p3-closeout](evidence/20260917-v2-p3-closeout/README.md)。
- 不读取真实 Provider key，不跑 `make test-real-model-acceptance`。

### 工作包

| 包           | 状态 | 依赖    | 范围                                                                                           |
| ------------ | ---- | ------- | ---------------------------------------------------------------------------------------------- |
| D00 基线     | done | 无      | HEAD/工作树记录；主门禁入口 `make test-v2-p3-delivery`                                         |
| D01 故障注入 | done | D00     | `-tags faultinject` 测试构建中的文件屏障和提交后丢回复；生产构建不可启用                       |
| D02 F01/F02  | done | D01     | `TestP3Closeout/F01_*` Docker kill→Incident→发布；`e2e/p3-closeout.spec.ts` 桌面 Versions 回滚 |
| D03 F03–F10  | done | D01     | F03–F10 完整失败/并发/跨来源矩阵通过。                                                         |
| D04 F11–F17  | done | D01     | F11–F17 全部真实中断窗口通过。                                                                 |
| D05 F18–F25  | done | D01     | F18–F25 真实六进程授权/篡改/生命周期矩阵通过。                                                 |
| D06 F26      | done | D01     | bundle/legacy/V2 completion 与双信号清理全通过。                                               |
| D07 F27      | done | D00–D06 | `make generate`/整条 `make check`/受影响 race/buf breaking 通过                                |

### 前序收尾记录（接续修正见修复实测记录）

1. **陈旧容器阻塞重启收敛（F02 e2e 暴露，产品）**：SIGKILL 后 docker 对象处于 removal-finalizing 时 `StartContainer` 409 被归为可重试，回滚后的 A 重启退化为慢速 reconcile 退避。修复：`dockerapp` 将该 409 映射为 `ports.ErrContainerRemoving`，`driveLaunch` 对已认领对象就地收敛（remove→await→recreate）。
2. **并发启动无串行化（产品，健壮性）**：`driveLaunch` 无 per-workload 串行化，Surface 重放与 reconcile 重驱可并发驱动同一 workload，落败方日志出现 `finalize workload launch: workload is not available`（generation CAS 落败），并在需要收敛的场景互相删除对方容器。接续修复：生命周期共用可取消、可回收的实例锁；等锁后重读精确代次和状态，停止与重启也参与串行化。注：第三次门禁 F02 集成失败 90s 窗口的直接原因其实是下一条（测试自身未鉴权），本条是同期观测到的真实竞态。
3. **测试文件名 `_windows` 后缀触发 GOOS 约束（测试基建）**：`p3_closeout_windows_test.go` 在 linux 上被整体排除，F11–F17 此前从未真正编译进门禁。已更名 `p3_closeout_persistence_test.go` 并重跑。
4. **集成测试直连 runtime 未带可信身份（测试）**：F02/F15/matrix 的 Surface 客户端误用 `clients.runtimeURL`，runtime 依赖网关注入的 owner 身份 → 每次重开都被 `unauthenticated` 拒绝，窗口内从未真正发起 relaunch。修复：全部改走 gateway。
5. **窗口用例复用相同构建内容触发内容寻址去重（测试）**：F11（字节计数不加一）、F12（per-task preparing 行缺失）因与此前子测试同 digest 的文件/行已存在。修复：新增 `unique` 变体按 taskID 注入内容；F13 的跨任务同字节去重语义保留 `success`。
6. **F14 断言 SQL 列名错误（测试）**：`app_repair_candidate_versions` 无 `version` 列，改 join `app_versions`。
7. **faultinject 单测数据竞争（测试）**：handler goroutine 写布尔与断言读无同步，`-race` 报警；改 channel 关闭语义。
8. **WebBundle 垂直切片测试硬编码共享开发栈 owner（测试）**：直连设备身份固定 `0198d7ea-…`，隔离栈 owner 不同即 not_found；改为从 `WORKOS_TEST_OWNER_ID` 派生并保留默认值。
9. **F11/F12 窗口断言与既定晋升语义冲突（测试）**：F11 要求"仅权威查询才晋升"，但 reconcile 在持久 succeeded verdict + 已验证字节后即可合法晋升 preparing→ready；F12 把 preparing 工件对 `GetBuildArtifact` 的 `failed_precondition`（产品正确的未就绪信号）当作致命错误。修复：F11 删除该断言（verdict 门控已由崩溃窗口无行事实证明），F12 断言 pre-verdict 必须答 not-ready。
10. **F18 变体 manifest 缺 `build` 段（测试）**：schema `allOf` 规定 runtime 携带 artifact 时必须同时给出完整 build recipe（含真实 `sourceBundleId`）；变体注册补上传 source bundle 并附 build 段。
11. **F20 用例语义与 ADR-0016 §6 相反（测试）**：用例假设"旧 canary 先发布、新 offer 排队其后"；产品语义为 newest-wins 抢占——存在更新 incident 时旧行有界 `rolled_back`（放弃未晋升的 canary，不叠加自动回滚），新 offer 在旧行终态后被接纳（`ErrDeploymentActive` 拒绝-重试）。重写用例按真实语义断言，需先等 #1 进入 canary 再插入 #2（`new_incident` 判定要求 incident created_at 晚于 ledger 行）。
12. **generic-harness-fixture 对已修复目标非幂等（测试基建）**：同安装第二 incident 在 #1 canary 中到达时，repair 准入按当前 pin 快照目标——staging 已把 pin 翻到 #1 的 staged 候选版本，其源码已含修复（无 `return 0`），fixture 的 `!changed → fail()` 使 agent run 立即失败、repair 行进 terminal，第二 incident 永久卡死。修复：fixture 改为幂等修复（已修复目标原样回传文件），第二链路经 per-task artifact（070 已将 owner+digest 唯一改为主字节去重 + 每 task 唯一 provenance）收敛、staging 独立版本并串行晋升。此为前序记录：接续检查发现后续注册会错误刷新目标，现已修复为不可变 version/revision；旧目标失效后拒绝，新任务才可修复当前 A。

### F19 授权矩阵（ADR-0003/0012 派生，六进程实际执行点）

| 动作                                                                    | 所需授权                                                       | 撤销/越权后的预期                                                                                                                 |
| ----------------------------------------------------------------------- | -------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `InstallApp`/`TransitionAppVersion`/`RollbackAppVersion`/`UninstallApp` | Project owner 经 Gateway，`expected_project_revision` CAS 串行 | revision 不匹配 → `FailedPrecondition`，无部分写入                                                                                |
| `CreateSurface`（启动/换版后打开）                                      | installation active 且 project 未归档                          | 卸载后 `NotFound`；归档后拒开，绝无静默复活                                                                                       |
| 修复链 replay（offer/canary/publish 续驱）                              | 链条启动时的 installation/project 归属持续有效                 | canary 中卸载 → 回滚被 Core 拒绝 → `rollback_pending`/`failed` 有界终态，无新 Surface、无自动回滚历史                             |
| 同安装出现更新 incident（canary 中第二 Offer）                          | newest-wins 抢占（ADR-0016 §6）；同一安装同时仅一个在途部署    | 旧行有界 `rolled_back`（放弃未晋升 canary），新 offer 只在旧行终态且不可变目标前提仍有效时接入；旧 B 的修复不得重新绑定到已恢复 A |
| canary 中 owner 手动 transition/rollback                                | 同第一行（owner 命令永远优先）                                 | 旧自动流程 `superseded`，pin 只反映 owner 意图，历史不叠加自动回滚                                                                |
| `SetAppGrants`（grant epoch）                                           | Project owner；full replacement + 请求期 grant revision        | P3 fixture `permissions:[]`，epoch 语义属 grants 家族（`TestMutableProjectAppGrantsVerticalSlice` 等）；P3 侧只验安装/归档/所有权 |
| `workosctl runtime import-artifact`                                     | runtime-host 本地 admin socket（无网络面）                     | socket 属 runtime 私有；streaming 先验 digest+格式再落盘/写元数据                                                                 |
| App 侧（bridge）读取模型/外部服务                                       | capability 授权，无真实凭据入 App                              | epoch 失效后 bridge 方法 `PermissionDenied`（ADR-0003）                                                                           |

### F01–F27 映射

| 编号 | 结果 | 已有证据与未覆盖项                                                                                                                                                                                |
| ---- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F01  | PASS | 真实 Docker kill→监督 Incident→修复→B；同 occurrence 去重。最终轮次身份链见修复实测记录。                                                                                                         |
| F02  | PASS | `F02_manual_rollback_after_supervised_kill` 已通过；真实 Desktop Versions 回滚 E2E 通过，查询 unavailable 有现有组件回归；无新可见 UI。                                                           |
| F03  | PASS | `F03_docker_build_failure_zero_publish`：真实修复链 build 失败，A HTTP 可用，无 ready/部署/pin 变化。                                                                                             |
| F04  | PASS | 真实修复链 Docker test 非零，Core 无新版本、无部署台账、无 pin 变化，A HTTP 继续可用。 最终证据见 09-20 记录。                                                                                    |
| F05  | PASS | Runtime 缺输出代表例；`TestRealEngineFailureMatrix` 六种输出拒绝，限量用例带有效入口。                                                                                                            |
| F06  | PASS | 提交前取消、真实 Docker timeout/log-budget，以及 `artifact-bytes`、`artifact-preparing`、`before-verdict` 三个提交窗口取消均通过；见 2026-09-19 证据。                                            |
| F07  | PASS | 四路并发单胜，输入/配方逐字段漂移均拒绝；Runtime 重启后的全部漂移组合通过。 最终证据见 09-20 记录。                                                                                               |
| F08  | PASS | 两个真实 PG/Docker 执行者，过期 lease 接管；旧 renew/verdict 拒绝，成功包绑定唯一。                                                                                                               |
| F09  | PASS | Submit、注册/安装 CAS、admin import、GetBuildTest/GetBuildArtifact 丢回复后同身份重放，Runtime 重启后事实不变。 最终证据见 09-20 记录。                                                           |
| F10  | PASS | 不同 owner/key/app 的相同字节保留独立来源；一个 owner 的坏包不能污染另一个 owner 的去重。 最终证据见 09-20 记录。                                                                                 |
| F11  | PASS | 真实字节已落盘、metadata 未提交时 kill Runtime；无 ready，接管后同摘要单一包收敛。                                                                                                                |
| F12  | PASS | 真实 preparing/pre-verdict kill Runtime；提交前 not-ready，新 lease 成功后 ready。                                                                                                                |
| F13  | PASS | after-verdict 屏障后 kill/restart Runtime；允许并发 reconcile 已先晋升，重启后同 artifact/job/digest 身份唯一且 ready；定向真实栈通过。                                                           |
| F14  | PASS | 注册/安装丢回复与原始 revision 重放；真实 Reliability 在注册前 kill/start 后恰好收敛。 最终证据见 09-20 记录。                                                                                    |
| F15  | PASS | 真实容器已启动但 Workload 回执未落盘时 kill；重启 inspect 接管，无重复容器。                                                                                                                      |
| F16  | PASS | canary 进程重启和 Runtime 重启下 12 类真实身份/配置漂移均阻止误发布并恢复 A。 最终证据见 09-20 记录。                                                                                             |
| F17  | PASS | publish/rollback 丢回复，以及 starting/canary/promoted/rolled_back 台账提交前真实 kill/start 全通过。 最终证据见 09-20 记录。                                                                     |
| F18  | PASS | 新增注册前 owner 升级；canary 中公开升级 C/回滚 A，旧流程不得覆盖 pin。                                                                                                                           |
| F19  | PASS | 公开卸载/归档 + P3 `project.read` grant epoch 1→2→3；旧 token 撤权后及 regrant 后持续 PermissionDenied，新 Surface 仅获当前能力；真实 PG/Runtime 通过。                                           |
| F20  | PASS | 旧 canary 回滚，针对退役 B 的修复拒绝，新 A 任务接续；另一安装独立发布；重复 Offer 另见 legacy。                                                                                                  |
| F21  | PASS | 坏包/缺镜像，加 workload ID、generation、memory、CPU、PIDs、restart policy、mounted bytes、Entrypoint+Cmd、working directory、user、writable mount、image 12 类漂移通过。 最终证据见 09-20 记录。 |
| F22  | PASS | 真实启动失败/健康故障/新 Incident 恢复 A；旧 A 包缺失时只能有界失败。                                                                                                                             |
| F23  | PASS | 恶意 tar、损坏去重、orphan 配额，以及 ready/preparing/staging 生命周期配额分别实测拒绝。 最终证据见 09-20 记录。                                                                                  |
| F24  | PASS | 私有 RPC 隔离；foreign owner/app/project/installation/incident/source bundle 与 build/import 来源冒用拒绝。 最终证据见 09-20 记录。                                                               |
| F25  | PASS | 旧 Surface close、陈旧 stop/generation 不扰动新实例；Runtime 对账保留无关容器，测试资源按 namespace 清理。 最终证据见 09-20 记录。                                                                |
| F26  | PASS | 完整 bundle、legacy、V2 completion（含三个 viewport，无 skipped）通过；SIGINT 130/SIGTERM 143 后自有容器/网络/卷归零，无关 sentinel 保留。 最终证据见 09-20 记录。                                |
| F27  | PASS | 整条 make check、重复生成、buf breaking(21b16f6)、受影响 race 已过；最终格式/工作树复核见交付记录。                                                                                               |

修复接续记录：[代码修复与本轮实测](evidence/20260917-v2-p3-closeout/repair-validation.md)。
本轮不涉及可见 UI；沿用 [P3-A18 三尺寸视觉证据](../ui/desktop-web/changes/20260916-v2-p3-real-artifact-delivery/notes.md)，另跑真实桌面业务 E2E。

原 `p3StartRepair` SQL Incident 主链保留，证据范围仅限 Incident 之后的交付。

复现：`make test-v2-p3-delivery` 或 `sh tools/v2-p3-delivery/gate.sh`；故障专项 `make test-v2-p3-faults`。
保留边界：P2-A16 第二台物理 LAN、P4、rootless、Docker memory.high。

2026-09-20 最终结论：F01–F27 全部通过，完整运行证据、环境边界和清理结果见 [最终验收记录](evidence/20260920-v2-p3-final-matrix/results.md)。
