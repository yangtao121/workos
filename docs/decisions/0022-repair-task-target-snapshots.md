# ADR-0022：Repair 入队来源与安装版本快照

- 状态：Accepted
- 日期：2026-09-08
- 关系：细化 ADR-0016 的任务入队和 ADR-0020 的候选部署前置条件。

修复任务必须引用入队时该安装的明确版本，不能等任务完成后再读取已变化的当前安装。
私有 CreateRepairTask 携带 incident/project/installation 和固定摘要；Core 从 Project
模块的同一数据库读取快照得到 installation/App/version/manifest digest/project revision，
通过 canonical RepairTarget 写入 AgentTaskInput。公开 SubmitTask 不接受 incident_id
或 repair_target，App 也不能自行制造 repair 来源。

重放按 owner/project/incident/installation/摘要判断调用者请求是否相同，并复用首次任务
及其版本目标。读取重放不重新解析活动安装，因而版本切换、归档或 Provider 绑定变化不会
改写原目标；不同请求使用同键返回 Aborted。数据库创建结果显式区分新建和重放，queued
任务的重放也返回 replay=true。普通任务仍按原始输入裁决，不能冒用 repair 键。

安装/项目事实仍归 Core Project，任务及其目标快照归 Core Agent，Reliability 只保存
incident/task 关联并通过 RPC 传递事实，不跨模块或跨进程访问 SQL。目标快照只作为后续
Build/Test/部署校验依据，不授予文件、凭据或权限，也不表示修复完成后可以直接部署。
尚无固定目标的历史任务不猜测回填，不能进入自动候选发布。快照只描述入队时的安装，
不能证明 incident 对应的原 workload 版本；后者目前没有版本事实，不能倒推。
