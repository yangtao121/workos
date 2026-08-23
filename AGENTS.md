# WorkOS Agent Rules

本文对所有人类与智能体生效。`docs/structure.md` 是产品架构主线，
`docs/architecture/implementation.md` 是当前代码边界，`docs/status.json` 是进度事实源。

## 开始前

1. 阅读目标模块文档、相关 Proto 和已有测试。
2. 在 `docs/tasks/` 建立或认领单一任务记录，写清范围、依赖和验收。
3. 检查工作树，保留不属于当前任务的改动。

## 不可违反的边界

- 六个进程边界固定：gateway、core、harness-host、runtime-host、reliability-host、indexer。
- 模块依赖方向固定为 `domain → application → ports ← adapters`。
- Domain 禁止导入数据库、Connect、HTTP、厂商 SDK、文件系统或其他模块 adapter。
- 跨模块禁止直接 SQL、共享可变 entity 或引用对方的 internal package。
- DeepSeek、Codex 等 Provider 类型只能出现在对应 adapter；Core 只认识 canonical protocol。
- App 不得读取模型或外部服务真实凭据，只能请求 capability。
- 安全保护不能依赖 Harness；未实现的保护必须明确报告 unavailable。
- `gen/`、`src/gen/`、README 状态区块均由工具生成，禁止手改。
- 不得以 TODO、固定成功响应或空 adapter 冒充 working 功能。

## 协议与数据

- 跨进程、Go/TypeScript、SDK 的契约必须先修改 `api/proto`，再运行 `make generate`。
- v1 Proto 字段号不得复用；删除字段/枚举值必须 reserved；破坏性变更需要新版本与 ADR。
- App manifest 是版本化 JSON Schema；不得再手写一套同义 DTO。
- 每张表和 migration 只能由一个进程拥有；其他进程通过 port、RPC 或事件访问。
- 事件按 at-least-once 设计，所有 consumer 必须幂等并持久化 cursor。
- 所有时间使用 UTC，资源 ID 使用 UUIDv7，外部写操作提供 idempotency key 或 etag。

## 完成定义

- 实现、测试、模块文档、任务记录和 `docs/status.json` 同步更新。
- `make generate` 后工作树无生成差异，`make check` 通过。
- 新公共行为有单元/集成测试；跨进程用户链路有 E2E 或明确的测试任务。
- 日志不包含 secret、provider raw credential 或用户内容全文。
- 如果功能没有端到端证据，状态最高只能是 scaffolded。

## 并行协作

- 一个任务对应一个 branch/worktree；不要让两个智能体同时修改同一 migration 或 Proto package。
- 先合并契约任务，再并行实现 producer/consumer。
- 交接时在任务记录中写明已验证命令、未决风险与下一步，不以聊天记录代替仓库事实。
