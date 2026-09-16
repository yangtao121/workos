# V2 P3 真实产物、运行版本与回滚（C00–C10）

- 范围：[任务书](../prompts/20260916-glm-5.3-v2-p3-real-delivery-goal.md) 的 P3 单机 App 发布链；六进程边界不变。
- 基线：本地 main `3820447`；接管 `feat/v2-p3-real-artifact-delivery@df47ecf` 与同任务未提交实现。
- 用户已授权：审查、修复、验证并合并本地 main；不推送。无关 `.zcode/plans/` 文件保留。
- 决策：[ADR-0033](../decisions/0033-app-bundle-v1-artifact-delivery.md)。
- 当前结论：真实发布与回滚主链已具备端到端证据；完整 P3 验收矩阵仍有缺项，整体保持 **scaffolded**，不能宣称全部 V2 完成。
- [审查验证记录](evidence/20260916-v2-p3-delivery/review-validation.md) 为本次合并证据入口。

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

| 包                 | 状态       | 依赖     | 实现与证据                                                                 |
| ------------------ | ---------- | -------- | -------------------------------------------------------------------------- |
| C00 宿主探针       | done       | 无       | 保留实测矩阵；Docker 不声称 rootless/memory.high                           |
| C01 契约冻结       | done       | C00      | 增量 Proto/Schema，迁移 066–072；生成与仓库检查见验证记录                  |
| C02 包仓库与导入   | done       | C01      | 真正流式 CLI 导入、PG 持久化、重启重算；损坏与重放拒绝测试                 |
| C03 构建输出冻结   | done       | C01、C02 | 真实 Docker 构建/测试、隔离输出与来源绑定；异常矩阵部分依赖单测            |
| C04 Core 版本绑定  | done       | C01–C03  | Core 独立反查 Runtime 权威事实；修复候选真实 staged/published              |
| C05 正式 Workload  | done       | C00–C04  | Docker inspect/只读包/网关 HTTP A、B、A；重启恢复                          |
| C06 发布与回滚裁决 | done       | C04、C05 | 发布、人工回滚、启动失败自动恢复的真实端到端证据；精确代次拒绝单测         |
| C07 用户可见状态   | done       | C04–C06  | Release 轮询测试，真实组件三尺寸 before/after/current                      |
| C08 完整回归矩阵   | scaffolded | C02–C07  | 旧修复门禁全组通过；并发崩溃与全部授权/Web Bundle/Workspace 组合未全部重跑 |
| C09 P3 门禁        | done       | C00–C07  | 六进程真实主链 + Chromium + 重启；按 namespace 清理                        |
| C10 本次审查交付   | done       | C09      | 修复、测试、文档同步；完整 P3 的遗留验收单独列出                           |

## 验收编号

PASS 只覆盖证据列明的场景；PARTIAL 表示该编号要求的完整矩阵尚未实测，不能折算为 PASS。

| 编号                 | 结果    | 实际证据与边界                                                                                                                              |
| -------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| A01 runner/保护能力  | PASS    | C00 探针、真实 Docker adapter；rootless/memory.high unavailable                                                                             |
| A02 契约与生成       | PASS    | 增量 Proto、Schema、066–072；buf 检查、两次生成一致                                                                                         |
| A03 导入/持久化      | PASS    | 真实 CLI 分块导入→PG→安装；三进程重启后相同摘要 ready；损坏/缺失拒绝单测                                                                    |
| A04 正式 A           | PASS    | 隔离六进程 Gate 的真实 Docker A，Gateway Surface 返回 P3-VALUE-0                                                                            |
| A05 作业/产物绑定    | PASS    | Generic CLI 候选→真实 Docker Build/Test→Core staged，独立 HTTP B oracle                                                                     |
| A06 失败零发布       | PARTIAL | 旧链 build/test 失败无部署；Docker build 失败真实容器；缺输出/超额/入口拒绝为单测，全部六进程失败组合未跑                                   |
| A07 Core B 绑定      | PASS    | ready/provenance 校验、staged runtime.artifact，公开 B 摘要不同于 A                                                                         |
| A08 并发/重放        | PARTIAL | 内容/来源漂移、lease 到期与 token SQL 守卫、重启恢复；各提交边界丢回包/双 Runtime 换手未全面注入                                            |
| A09 正式 B           | PASS    | 真实 Docker adapter inspect 与只读 mount；六进程启动 B                                                                                      |
| A10 Gateway/浏览器 B | PASS    | Chromium 经真实 Gateway Surface 读 A→B→A；独立于候选测试的 HTML marker                                                                      |
| A11 canary 事实      | PASS    | 实际 HTTP 健康、Core pin、Runtime identity；保存代次并每轮验证；代次漂移拒绝单测                                                            |
| A12 自动恢复 A       | PASS    | B 启动退出42→rollback_pending→rolled_back，随后公开 Surface 实测 A                                                                          |
| A13 人工 rollback    | PASS    | owner 公开 RPC + revision CAS；实际 A→B→A，Chromium 同链验证                                                                                |
| A14 崩溃恢复         | PARTIAL | Core/Runtime/Reliability 重启后 A/ready 包不变；旧链 running job 重启恢复；每个部分提交窗口未全面注入                                       |
| A15 授权/用户改版    | PARTIAL | 旧链 UserVersionChange、真实 Connect stale pin 拒绝、grant 单测；bundle canary 期间撤权/卸载组合未跑                                        |
| A16 损坏/安全边界    | PARTIAL | 无隐式拉取、坏包、链接、配额、来源与身份拒绝测试；正式容器隔离实测；全部跨 owner/运行期篡改六进程矩阵未跑                                   |
| A17 新旧回归         | PARTIAL | 原 repair-buildtest 的修复/恢复/回滚测试全组通过，全仓 Go/TS 测试；App grant/Workspace/Web Bundle 独立 E2E 未重跑                           |
| A18 视觉证据         | PASS    | [notes](../ui/desktop-web/changes/20260916-v2-p3-real-artifact-delivery/notes.md)，真实组件固定 fixture，三状态×三尺寸 before/after/current |
| A19 门禁隔离         | PASS    | 自有 DB/六进程/端口/namespace，成功及失败路径清理；缺依赖不返回成功                                                                         |
| A20 仓库一致         | PASS    | make generate 无新增差异、make check、race、diff --check，见验证记录                                                                        |

## 未决风险与下一步

- 按 A06/A08/A14/A15/A16 增加可控故障注入：双 Runtime lease 换手、提交后丢回复、
  preparing 与成功 verdict 之间重启，以及 canary 撤权/卸载/包篡改；必须断言无越权发布。
- 补跑 A17 的 App grants、Workspace、Web Bundle 独立门禁；当前只对旧修复发布组提供新鲜 E2E。
- Release 投影尚未填充 AppID、旧包摘要和细粒度 failure category；缺失字段保持空，
  UI 不据此虚构旧包或失败原因。若需展示，必须补充权威快照和契约证据。
- 不包含 rootless、Docker memory.high、P2 A16 的第二台物理 LAN 设备或 P4；不支持的保护仍 unavailable。
- 合并完成不等于完整 P3 验收完成；上述缺项留在本任务，避免下轮从聊天反推。

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
