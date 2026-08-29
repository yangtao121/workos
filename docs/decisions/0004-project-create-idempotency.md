# ADR-0004：ProjectCreate 的持久精确幂等与首次响应快照

- 状态：Accepted
- 日期：2026-08-29
- 关系：落实 docs/prompts/20260829-next-agent-project-service-contract-hardening.md 的
  CreateProject 幂等要求；沿用 003（App Registry 注册幂等）与
  `project_app_installation_requests` 已确立的“mapping 表是唯一幂等事实源”原则，
  不引入新的语义家族。

## 背景

`workos_core.projects` 自 001 起把 `idempotency_key` 放在可变 Project row 上
（UNIQUE (owner_user_id, idempotency_key)）。该设计有两个无法修复的结构性缺陷：

1. 它只能证明“同 owner 同 key 曾创建过 Project”，不能证明“同 key 的本次请求与第一次
   请求相同”——相同 key 的不同请求会静默返回旧 Project；
2. Project row 是可变聚合（name/icon/refs/binding/revision/archived_at 随 Update 与
   Archive 变化），重放 key 时返回的是当前可变状态，不是第一次 Create 的精确响应。
   任何“从当前 row 反推首次响应”的方案都是在伪造历史。

在引入审批、预算策略与更多 Project mutation 之前必须修正，否则后续功能会继承错误的
幂等基础。

## 决策

### 1. 新 authority 表是 Create 幂等的唯一事实源

migration `013`（owner：workos-core Project）新增：

```sql
CREATE TABLE workos_core.project_create_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object' AND result -> 'result_version' IS NOT DISTINCT FROM '"1"'::jsonb),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);
```

- `result` 列的版本谓词用 jsonb 等值 `IS NOT DISTINCT FROM '"1"'` 判定：普通等值在
  `result_version` 缺失时结果是 NULL——PostgreSQL CHECK 视 NULL 为通过；`->>` 文本比较
  还会把数值 `1` 误判为合法。缺失键与错误类型都必须落库前拒绝，重放层再以 Go 侧
  fail-closed 校验兜底。
- `projects` 的既有数据与约束（含 `UNIQUE (owner_user_id, idempotency_key)` 与
  `idempotency_key` 列）全部保留：该唯一索引继续作为并发插入的物理仲裁，不重建历史表、
  不做 broad rewrite。可变 row 不再是幂等裁决依据；mapping row 才是。
- 两个进程并发同 key Create：两个事务同时 `INSERT projects ON CONFLICT DO NOTHING`，
  PostgreSQL 唯一索引让后到者等待先到者提交并以 `rows == 0` 落败；落败者重读 mapping：
  digest 相同 → 精确重放首次快照，不同 → Aborted。裁决完全在数据库，不依赖进程内
  mutex、sleep 或“查询后再无约束 insert”。最终恰有一个 winner：一个 Project、一个
  `project.created.v1` event、一个 outbox entry、一条 consumed key 记录。

### 2. canonical request digest

digest 在 application 完成语义规范化/验证后计算，canonical encoding 是确定性 JSON
（struct 字段按字母序、无歧义转义，禁止裸字符串拼接），版本标记 `project.create/v1`。
覆盖且仅覆盖影响首次 Project 内容的客户端输入：

```text
command           = "project.create/v1"
name              = 规范化（trim）后的 name
icon              = 提交的 icon
workspace_refs    = 提交顺序保留的数组，每项含 id/kind/uri/logical_mount/read_only
harness_binding   = 可选，presence 即语义；每项含 provider/instance policy/profile/
                    credential_ref/resource_policy
```

不混入 owner（owner + key 是命名空间，不是请求内容）、服务端生成的 ID、时间、
revision、当前数据库状态。same digest 因此意味着“规范化后逐字段相同的请求”。

### 3. 版本化首次响应快照

winner 在 Create 事务内把第一次成功响应的精确 Project 投影写入 `result`
（`result_version = 1`，字段：id、owner、name、icon、workspace_refs、harness_binding
presence、installed_app_ids、default_agent_role、knowledge/artifact collection id、
revision、created_at/updated_at UTC）。它是内部持久模型，不与 Proto 竞争 public DTO，
也不含 secret。重放只读 mapping row 的快照，绝不读当前 projects row——因此 Project
后来被 Update（revision/内容变化）或 Archive 后，Create replay 仍返回第一次响应的
revision、字段与 timestamps。

### 4. 语义矩阵

| 场景                                | 语义                                                                     |
| ----------------------------------- | ------------------------------------------------------------------------ |
| same owner + key + digest           | 精确重放第一次 CreateProjectResponse 的 Project 快照（跨请求/进程/重启） |
| same owner + key + different digest | 稳定 Aborted（净化固定消息，不泄漏原请求或结果）                         |
| 不同 owner 的相同 key               | 互不冲突，互不可见（PK 以 owner 为前缀）                                 |
| validation 失败                     | InvalidArgument，不消费 key                                              |
| DB / event / outbox / commit 失败   | 事务回滚，mapping 未写入 → key 未消费                                    |
| 高并发同 key                        | 恰一个 winner；loser 按 digest replay 或 Aborted                         |

### 5. legacy key：诚实且 fail closed

013 之前创建的 Project 只留下 `projects.idempotency_key`，原始 canonical 请求与首次
响应不可恢复。迁移不伪造 digest、不从（可能已被 Update/Archive 的）当前 row 伪造
“首次快照”。对无 mapping 记录的既有 key 的任何 Create 重放（无论请求内容）统一
fail closed：事务内发现 `projects` 唯一索引冲突但 mapping 缺失 → Aborted（与
digest conflict 相同的净化消息，避免存在性 oracle 的双消息侧信道）。此决定牺牲了
legacy key 的重放能力，换取“绝不把 different request 误判为 replay、绝不把后来状态
冒充首次结果”的硬保证。

### 6. 错误与事务边界

- Create 的 project insert、mapping insert（含 result 快照）、`project.created.v1`
  event、outbox 写入在同一个 Core-owned PostgreSQL transaction 内提交；这是唯一
  线性化点。
- 失败（validation 之后任何一步）回滚全部，key 不被消费；transaction 内的回滚不留
  局部 Project/mapping/event/outbox。
- 数据库 I/O 失败统一走与 installation 共享的 `storeError` 分类（transient →
  Unavailable，其余 → 净化 Internal）；JSON 编解码、UUID 生成等程序错误不标
  Unavailable。
- Update/Archive 的 optimistic concurrency（owner + project + expected revision）、
  event sequence = Project revision、单事务 event/outbox 语义保持不变。

## 后果

- migration `013` 新表一个；001–012 逐字节不变（checksum 测试钉住）。
- 既有“同 key 不同 name 仍成功”的 foundation integration 断言改为 Aborted；
  补齐 same-request replay、Update/Archive 后 replay、重启后 replay、真实双 pool
  并发与 legacy fail-closed fixture 测试。
- 客户端以相同 key 微调请求（例如改名）从“静默返回旧 Project”变为显式 Aborted——
  这是契约收紧，桌面与 SDK 需要换新 key 重试（与 installation 命令既有语义一致）。
- `projects.idempotency_key` 列保留但降级为物理仲裁与历史记录；未来如需移除该冗余，
  需要新的 ADR 与 additive migration。
