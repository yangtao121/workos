# 当前实现架构

本文把 `docs/structure.md` 的产品愿景映射为可执行边界。愿景与实现冲突时必须先写 ADR，不能在
业务提交中顺手改变主线。

## 进程所有权

| 进程             | 当前所有权                                                   | 不拥有                     |
| ---------------- | ------------------------------------------------------------ | -------------------------- |
| workos-gateway   | TLS、identity、capability、公开 API、静态 Shell              | Project/Harness 状态       |
| workos-core      | Project、Task Router、Event Backbone、App/Artifact contracts | Provider 进程、cgroup      |
| harness-host     | Broker、Provider Adapter、run execution                      | Project 数据、公开 API     |
| runtime-host     | Workload、runner、Surface                                    | Incident 决策、业务数据    |
| reliability-host | Supervisor、Incident、Repair/Deploy ports                    | App 业务逻辑、Harness 路由 |
| indexer          | Archive/RAG/indexing                                         | 原始业务表写权限           |

服务之间使用版本化 Connect API 与 durable event，不共享 internal package 或直接查询对方 schema。

## 请求与事件流

```text
Desktop → Gateway → Core: SubmitTask
Core transaction: task + outbox(agent.task.requested.v1)
Harness Host: claim event → Provider → append canonical events
Desktop → Gateway → Core: WatchTaskEvents(after_sequence)
```

断线不会取消任务；事件流可从持久化 sequence 恢复。取消命令是幂等状态转换，不依赖客户端连接。

## 状态与失败

- liveness 表示进程事件循环存活，readiness 表示必需依赖可用。
- capability discovery 表示可选功能是否真实可用。
- 未实现能力返回明确的 Unimplemented；依赖暂时失效返回 Unavailable。
- 所有跨进程 consumer 按 at-least-once 处理，不假设 exactly-once。

## 工程守卫

- Go 架构测试拒绝 Domain 导入数据库、HTTP、生成协议或 adapter，并拒绝进程 internal 互相导入。
- TypeScript 架构检查验证 workspace 分层、依赖声明、边界逃逸和依赖环。
- Buf 负责 Proto lint/生成与 CI breaking check；README 状态表只从 `docs/status.json` 生成。
- 所有入站和内部 HTTP/Connect 调用支持 W3C trace propagation；只有配置 OTLP endpoint 时才启动
  exporter，因此本地运行没有隐含可观测性依赖。
