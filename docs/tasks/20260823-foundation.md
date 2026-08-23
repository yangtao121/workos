# Task: foundation architecture and vertical slice

- 状态：done
- Owner/Agent：foundation builder
- 进程/模块：all stable processes; Project/Task/Harness vertical slice
- 依赖：Docker, PostgreSQL 18, Go 1.26, Node 24, Buf 1.55

## 目标与范围

建立可持续开发的 monorepo、稳定协议与六进程边界，并用 Project → durable Task → Harness → persisted Event Stream 证明边界可运行。范围不包含 DeepSeek 产品 adapter、生产认证、App Registry 实现、workload runner、repair 或 RAG。

## 协议/数据影响

- 新增 `workos.*.v1` Proto 与 App manifest v1 JSON Schema。
- 新增 Core-owned Project/Task 表和 append-only event/outbox schema。
- migration 只允许前向追加且记录 checksum。

## 验收

- [x] Go/TypeScript/Proto 构建与单元检查
- [x] PostgreSQL 跨进程集成链路
- [x] Desktop 浏览器 E2E
- [x] 完整 `make check` 与生成漂移检查
- [x] 六进程探活、镜像和部署资产验证
- [x] README、架构边界与机器可读状态同步

## 交接

已执行并通过：

- `make generate`；生成目录复跑前后内容哈希一致。
- `make check`；包含 Buf/SQLC、Go vet/test、Go/TypeScript 架构守卫、ESLint、Prettier、TypeScript/Vitest 与 Desktop production build。
- `make test-integration`；Project → durable Task → Fake Harness → persisted event stream 通过，Core/Harness 重启后任务状态与 event cursor 恢复通过。
- `make test-e2e`；Chromium 中通过 Gateway 创建 Project、执行 Task 并观察 Fake Harness terminal event。
- `make dev`、六个 `/readyz` 与 `workosctl doctor`；六进程和 PostgreSQL 均可用。

国内开发默认使用 goproxy.cn、npmmirror 与阿里云 Debian 镜像；所有地址均可覆盖。Docker Hub 基础镜像加速由机器自己的 Docker daemon 或 `*_IMAGE` 构建参数负责，禁止提交个人阿里云加速地址。

仍属后续范围：生产认证与设备注册、DeepSeek/Codex 产品 adapter、App Registry/Artifact 实现、rootless Podman workload runner、Reliability enforcement、Indexer/RAG。当前 `workosctl doctor` 如实报告 `rootlessPodmanAvailable=false`；不要把 scaffolded/contract-only 能力升级为 working。

下一阶段应从独立任务记录开始，优先在生产认证、首个真实 Harness adapter、rootless Runtime runner 三条主线中选一条；涉及 v1 协议或进程边界时先写 ADR。当前能力事实仍以 `docs/status.json` 为准，不要从聊天记录推断状态。
