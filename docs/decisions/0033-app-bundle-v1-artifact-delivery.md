# ADR-0033：app-bundle.v1 不可变发布包与正式 Docker App 运行

- 状态：Accepted
- 日期：2026-09-16
- 范围：V2 P3 小型单机 App 发布链；六进程边界不变。
- 依据：[P3 任务书](../prompts/20260916-glm-5.3-v2-p3-real-delivery-goal.md)、
  ADR-0006/0012/0016/0024/0025/0026/0032、[C00 探针矩阵](../tasks/evidence/20260916-v2-p3-delivery/c00-probe.txt)。
- 探针结论（2026-09-16，docker 29.7.2 / kernel 7.0.0-31 / cgroup v2）：internal 网络容器出口
  内核不可达、外部 DNS 失败、宿主可直连容器桥 IP；internal 网络不支持端口发布；
  `--memory-reservation` 不映射 memory.high（软高水位不可用）；cap-drop/no-new-privileges/
  只读根/memory.max/pids.max/cpu.max 均可实施并读回。

## 1. 发布包格式 `app-bundle.v1`

确定性 USTAR tar 字节流：

- 条目仅目录（typeflag `5`）与普通文件（typeflag `0`）；拒绝 symlink、hardlink、设备、
  socket、fifo、绝对路径、`..` 段、空段、重复路径与任何 pax/GNU 扩展头。
- 路径按字节序排序；uid=gid=0、uname/gname 空、mtime=0。
- 权限规则固定：目录 0755；文件按源可执行位映射 0755 或 0644。
- 流以两个 512 字节全零块结束，无额外 record 填充。
- **内容摘要（content digest）** = 整个 tar 字节流的 sha256（`sha256:<64hex>`）。
  它与 manifest 摘要是不同事实：前者证明包字节，后者证明版本声明。写入与读取前后都
  必须重算并比较内容摘要；不得以目录名、DB 字段或用户声明值代替实测。
- 上限（fail closed）：普通文件 ≤1024 个、单文件 ≤32 MiB、内容总量 ≤128 MiB、
  每 owner 的 ready+preparing 编码字节数合计 ≤2 GiB。超限在采集/解包流式阶段即拒绝，
  不是事后压缩时才发现。
- 磁盘仓库由 Runtime 独占拥有：0700 根目录、owner 隔离子目录、内容按摘要寻址
  （`<owner>/<digest>.bundle`），`tmp → fsync → rename → dir fsync` 原子写入；只能经
  Runtime 的受控 descriptor 打开。不做 ready 包自动 GC；配额到顶拒绝新构建。

## 2. Manifest 契约（Schema v1 增量）

- `build.output`：`{"format":"app-bundle.v1","directory":"<相对目录>"}`。directory 为
  规范化相对路径（`[A-Za-z0-9._-]+` 段、无 `..`、不以 `/` 开头、总长 1–64）；格式常量
  只有 `app-bundle.v1`。它是**输入配方的一部分**：候选不能改写，浏览器与模型运行时都
  不能提交。
- `runtime.artifact`：`{"id":<UUIDv7>,"digest":<sha256>,"format":"app-bundle.v1"}`。
  它是**部署事实**：只有可信 Runtime 完成的 ready 包（build_job 来源）或本地管理员受控
  导入（operator_import 来源）可以填入。出现该字段要求 `runtime.type=container` 且
  manifest 同时含 `build` 与 `build.output`（bundle profile）；旧 image-only 容器
  manifest 不受影响，Core 绝不从旧字段猜测或自动补 artifact。
- staged 派生只新增 `runtime.artifact` 并替换 `build.source*`/`version`；其余授权字段
  与基础镜像逐字节保留，canonical manifest 摘要因此改变。

## 3. 正式 Docker App runner profile

- 引擎身份：`docker`（runtime-host 受监督 adapter，直接使用 Docker Engine API）。
  **不声称 rootless**；Podman/rootless profile 的既有保证不变。能力矩阵以 C00 实测为准，
  `memory.high` 软高水位在 Docker profile 明确 unavailable，不映射成其他保护冒充等价。
- 网络：`workos-app-internal` internal 网络。容器无端口发布；endpoint 是容器桥 IP，
  仅宿主网络命名空间可达（runtime-host 必须与宿主同 netns 或加入该网络）。容器出口
  内核不可达、外部 DNS 失败。
- 启动：固定 digest 基础镜像（`repo@sha256:…`，pull=never，启动前 /images 校验）+
  从已验证 ready 包解包的只读 `/app` bind mount（解包后重算内容摘要）。
  只读根、cap-drop ALL、no-new-privileges、pids/memory/cpu 上限、`/tmp` tmpfs、
  labels 携带 owner/app/version/artifact digest/generation 与 purpose `runtime-app`。
- 启动后 inspect 逐项核对 image digest、mount 源与只读、argv、labels、资源上限；
  任一不符即停止容器并返回失败。Runtime 重启仅按完整标签对账自己拥有的容器；
  与 workspace/interactive purpose 互不清理。
- 保护不能实施时该 profile 返回 unavailable，绝不降级为宿主进程或 fake engine。

## 4. 进程职责与数据所有权

- Core：原始 manifest、不可变源码包、版本注册与安装授权；staged/published 版本表持久化
  artifact 绑定（可审计列 + 索引，canonical manifest 本身携带完整事实）。注册前必须
  亲自向 Runtime 查询 job+ready 包权威事实并比对 task/owner/来源摘要/manifest 摘要/
  配方/格式/内容摘要；Reliability 传的 ID/digest 字符串只是对账线索，不是证明。
- Runtime：构建作业、发布包字节、容器与 Workload 事实。artifacts 表（066）与磁盘仓库
  由 Runtime 拥有；包内容不进 Core SQL、不落 Harness/Reliability 目录；日志不含包全文。
- Reliability：deployment ledger 持久化 artifact 快照（068）；发布/回滚裁决只凭
  精确候选身份 + 实际 Workload/Surface/健康事实。
- 跨进程只用版本化 RPC；v1 字段号只增不复用，破坏性变更须新版本 + ADR。

## 5. 本地管理员导入（operator_import）

- 管理员把按 `app-bundle.v1` 备好的旧应用包经 Runtime admin Unix socket（0600 权限，
  文件系统即身份）用 `workosctl runtime import-artifact` 流式导入；Runtime 以同一套
  上限/路径/摘要校验后生成自己的 UUIDv7 与 origin=`operator_import`。
- 同键同内容幂等一份；换内容稳定冲突；不接受浏览器或 Gateway 调用，不接受用户提供的
  本机绝对路径作为执行/部署权限（CLI 在本地读文件、以字节流上传）。
- 该来源**没有"已通过本轮 Build/Test"的证明**；Core 注册初始版本时仍向 Runtime 查询
  owner/app/ready 身份，来源在查询投影中如实显示为导入。

## 6. 失败与回滚事实

- ready 的前提：构建与测试都成功、输出完整冻结、内容摘要校验通过、且作业 lease/代次
  仍拥有提交权。取消/超时/重试输家/崩溃半包绝不 ready。重启对账：preparing 且实测摘要
  匹配 → ready；损坏/不完整 → failed；ready 元数据但文件缺失 → unavailable（不可启动）。
- 构建/测试/输出任一失败：无 ready 包、无 staged 版本、无 canary、无安装 pin 副作用。
- 发布裁决（Reliability）：StartSurface 成功 = Core pin 精确匹配候选 + Runtime 有
  running Workload 且 image/artifact/generation 精确匹配 + Surface 绑定该 generation +
  健康端点真实通过。canary 期间身份漂移/进程死亡/包不可用/用户新 incident → 停止自动
  推广；用户主动改版 → 终态 `superseded`，不被自动流程覆盖。
- 自动回滚：Core 精确 previous pin CAS → 终结/隔离失败容器 → 启动旧 version+artifact
  新 generation → Surface 重建 → 实测读到旧服务健康/行为，仅此时记 `rolled_back`；
  否则 `rollback_pending`（可重试，不更深回滚）。重复回滚/丢响应按台账与精确产物收敛。
- 停止、重启与版本切换产生新 generation；旧 Surface、控制 token 与后台输入不能操作
  新 generation。

## 7. 兼容性

- 旧 `web-bundle` 路径、image-only 容器 manifest、项目开发预览（ADR-0032）、旧
  Task/App→Agent 与 owner 手动版本切换全部保持原语义；新发布能力不扩大 App grant
  或凭据能力。bundle mount 不是普通 App 的文件 Bridge，不向 App/容器/环境/日志暴露
  模型凭据。
