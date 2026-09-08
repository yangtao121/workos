# ADR-0025：租约绑定的修复源码候选

- 状态：Accepted
- 日期：2026-09-08
- 关系：ADR-0022 的入队版本快照、ADR-0024 的不可变构建输入。

Core 在既有 Harness mTLS execution listener 上提供 RepairExecutionService。请求只携带
lease/worker，不接受 owner、project、installation 或版本选择。Agent 自有 port 锁定有效、
未过期、非终态的任务，orchestration 从任务 RepairTarget 读取 Registry 的固定 manifest
和源码，核验版本摘要、源码归属与摘要。没有 build 配置明确返回 FailedPrecondition。
安装后续升级不改写已入队输入；该输入也不授权部署到已变化的安装。

候选提交只含普通文件，使用 ADR-0024 的相同路径和字节限制。Core 在同一事务内重新验证
租约与原始输入，通过 Registry 自有 port 保存不可变源码和每 task 至多一个候选映射。
相同文件重放第一次 ID/摘要/时间，不同文件冲突；失效租约不可读、不可写或重放。候选
映射归 Registry，Agent 表只由 Agent adapter 访问。普通任务不能调用这些修复接口。

候选是待验证源码，不是 app_versions 中的可安装版本。它不能指定 manifest、测试命令、
权限、资源或部署版本，也不会成为默认版本。后续 Runtime Build/Test 必须使用原版本的
固定配置；Reliability 只有在验证构建结果和当前安装前置条件后才能发起 canary。
本阶段不声明 Recovery 入队治理、构建执行或自动发布完成。

## Harness producer 与后续消费者读取

CLI v2 通过 repair 输入与 repair_source 输出扩展；候选能力单独声明，Core 在新 Repair
入队前校验。Worker 与 adapter 都验证输入绑定，完整成功流且子进程 exit 0 后才保存候选，
成功回执先于 task completed。候选不与 review artifact 混合发布。Fake 候选是明确的
合成说明，不作为实际修复结果。

普通 Core listener 上的私有 RepairCandidateService 只允许 owner-scoped completed task
读取原始构建输入与候选；Gateway 不路由此服务。运行中、失败、取消、外 owner 均不可
作为可部署候选读取。消费者必须继续证明 Runtime Build/Test 和当前安装版本前置条件。
