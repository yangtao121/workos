# ADR-0026：Runtime Build/Test 执行与候选发布

- 状态：Accepted
- 日期：2026-09-08
- 关系：ADR-0012 版本历史、ADR-0016 §5/§6、ADR-0024 固定构建输入、ADR-0025 租约候选。

## 裁决

Build/Test 是 Repair 候选成为可部署版本的唯一验证段。进程边界固定：Core 拥有输入、
源码、版本与授权事实；Runtime（runtime-host）拥有实际构建/测试执行、作业台账与资源
限制；Reliability 编排轮询，不直接写 Core/Runtime 的表。跨进程契约先 Proto（新增
`workos.taskexecution.v1` 的 `BuildTestService` 与 `RepairVersionService`，均为私有
listener，Gateway 不路由）。

## 作业与失败契约

Runtime `BuildTestService.SubmitBuildTest` 携带 Reliability 从 Core 私有
`RepairCandidateService` 读取的完整 `RepairBuildInput` 与候选文件（受 ADR-0024 的
128 文件/512 KiB 限制约束）。作业以 task_id 幂等：同 task 同输入摘要精确重放，不同
输入 Aborted；持久于 `workos_runtime.build_jobs`（migration 051，owner runtime-host），
状态机 queued→running→succeeded/failed/cancelled，重试仅限 transient 引擎错误且至多
3 次，重启后 reconcile 重驱 queued/过期租约的 running 作业。

以下任一情形作业终态 failed，绝不产生可部署版本：构建非零退出、测试非零退出、超时、
进程组清理失败、输出预算超限、输入摘要漂移、引擎崩溃。终态记录到达阶段、退出码、
引擎能力事实与有界日志尾部（64 KiB，逐行 4 KiB）。

## 引擎分级

`BuildEngine` 是中立端口，执行器从可信固定输入运行 build/test argv，绝不执行候选自带
的 Containerfile、网络配置、挂载或自选测试命令，也不隐式拉取镜像。

- **rootless 容器引擎（Podman）**：固定 manifest 镜像、`--network=none`、cgroup v2
  CPU/内存/PID 限制与磁盘/输出预算。本验收宿主无 podman，保持既有 BLOCKED 记录；
  该引擎在满足前提的宿主上才允许把容器级隔离声明为 enforced。
- **进程沙箱引擎（process）**：0700 私有工作目录、候选文件物化、bash `ulimit` 内核
  强制的 CPU 秒/地址空间/文件大小/进程数/打开文件上限、墙钟 deadline + 进程组
  SIGKILL、字节封顶的 stdout/stderr、最小环境（无代理变量、`GOPROXY=off`）。
  能力事实如实记录 `network_namespace=false`、`image_pinned=false`；验收级容器隔离
  不因进程引擎存在而声明。

作业终态持久记录引擎名称与 enforced 事实；发布裁决不因引擎降级放宽失败语义。

## 候选暂存与发布（migration 050，owner workos-core）

`app_versions` 增加 `state`（`staged|published`，存量行 published）。默认版本选择与
owner 驱动的 TransitionAppVersion 只允许 published；staged 版本不可被 owner UI 选中。

Core 私有 `RepairVersionService`（Reliability-facing listener）：

- `RegisterRepairCandidateVersion`：Reliability 提交作业成功事实。Core 在一个事务内
  重验 task completed、候选映射、输入摘要、installation 仍处于期望版本/revision，然后
  从原版本 manifest 派生新 manifest（仅替换 build.sourceBundleId/sourceDigest，其余
  逐字节保留），经既有 schema 校验后创建 staged 版本，版本号确定性派生为
  `{patch+1}-repair.{task 前 8 hex}`。同 task 幂等重放第一个结果；installation 已变
  更时拒绝（调用方走放弃路径）。
- `TransitionCandidateVersion`：canary 启动专用。语义同 ADR-0012 的精确版本切换
  （server 解析 registry 目标、grants 覆盖 fail-closed、幂等键），但允许 staged 目标
  并要求期望 revision 前置；该 RPC 只在私有 listener 上。
- `PublishRepairCandidateVersion`：canary 通过后把 staged 转 published。前置：版本仍
  staged、digest 匹配、installation 未被用户改走；重复 promote 幂等且不覆盖用户变更。

DeploymentController.Offer 只接受带 staged 事实的候选：Transition 走
TransitionCandidateVersion，Promoted 终态驱动 Publish；启动失败/canary 新 incident 走
既有回滚（回到上一精确 pin）。任务完成本身永远不是 promote；L5 数据迁移/凭据/权限/资
源提升保持人工，不进入本链。

## 验收

新增 `make test-repair-buildtest` 独立门禁：真实 Gateway/Core/Runtime/Reliability/
PostgreSQL + golang 工具链镜像中的进程引擎，覆盖成功、测试失败、构建失败、启动故障、
canary 故障回滚、回滚失败重试、进程重启恢复、重复提交/丢响应幂等、用户版本变更拒绝，
断言实际版本、Surface 与持久台账。容器级隔离在本宿主维持 BLOCKED 记录，不因进程引擎
证据升级。
