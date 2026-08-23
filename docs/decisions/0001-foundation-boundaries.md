# ADR-0001：Foundation 技术栈与稳定边界

- 状态：Accepted
- 日期：2026-08-23

## 决策

- 服务端使用 Go，客户端与 SDK 使用 React/TypeScript。
- Protobuf/Connect 是跨语言 RPC 与事件的事实源，Buf 阻止 v1 破坏性变更。
- 保留 Gateway、Core、Harness、Runtime、Reliability、Indexer 六个进程边界。
- PostgreSQL append-only event log 与 transactional outbox 是首个 durable event backbone。
- 当前单 owner，但 identity/capability/audit 全链路显式建模。

## 理由

这些边界直接对应产品架构中的信任、故障与替换边界。Foundation 可以少实现功能，但不能通过
临时合并隐藏边界，否则后续需要迁移数据、权限和协议。
