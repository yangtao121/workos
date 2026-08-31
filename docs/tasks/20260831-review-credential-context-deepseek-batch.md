# Task: Credential / Context / DeepSeek 批次实现复核与修复

- 状态：active
- Owner/Agent：WorkOS 复核智能体
- 进程/模块：workos-core、harness-host、desktop-web、部署与批次文档
- 依赖：ADR-0009、ADR-0010、ADR-0011；阶段 A/B/C 已实现提交

## 目标与范围

复核 `docs/prompts/20260830-next-agent-credential-context-deepseek-batch.md` 的三阶段实现，
修复会导致安全边界、幂等协议、仓库卫生或完成证据不成立的问题。保持既有 additive Proto 与
forward-only migration，不接触真实 Provider，不使用真实凭据。

## 验收

- [x] 凭据管理写入的物理幂等仲裁在 put/rotate/revoke 三条路径一致，冲突不会继续写入。
- [x] master key 以无 symlink 跟随的单次打开读取，严格验证 owner/mode/恰好 32 bytes，并为
      AEAD 与 request digest 派生不同子密钥。
- [x] dev/CI fixture 挂载满足最小权限：Core 看不到 Harness 私钥，Harness 看不到 Core 私钥与
      vault master key，其他常驻进程看不到以上材料。
- [x] 删除误提交编译产物与错误路径视觉证据，视觉输出路径不会再次生成仓库根路径镜像。
- [x] 任务记录、架构文档与 `docs/status.json` 如实同步；生成文件只通过生成器更新。
- [x] 相关单元/集成门禁、`make generate` clean check 与 `make check` 通过。

## 执行记录

- 复核基线：分支 `feat/deepseek-structured-review`，HEAD `4594551`，工作树干净。
- 不使用用户曾提供的 Provider API key；所有 Provider 验收仅使用 loopback fixture。
- 修复 Credential rotate/revoke 已消费 key 后仍继续写入的问题；补充 exact replay、冲突不改变 revision、
  持久化 metadata 与 lease fact fail-closed 覆盖。
- master key 与 mTLS private material 改为 `O_NOFOLLOW` 单 descriptor 有界读取；Core/Harness 使用精确且
  role-specific 的 URI SAN/EKU，AEAD 与 request digest 使用独立派生子密钥。
- Compose fixture 拆为 Core identity、Harness identity、vault key 三个 volume；systemd skeleton 使用独立
  service account、`LoadCredential` 与 Core-only admin socket runtime directory。
- 删除根目录编译产物 `devauth` / `deepseekapi` 与错误的绝对路径镜像截图；新增 ignore 防回归。
- 修复 Artifact Context chip 样式、视觉用例未关闭 Artifact Center 的遮挡问题，并使用固定 Project fixture、
  viewport 与容器内输出路径重建 before/after/current 证据：
  [notes](../ui/desktop-web/changes/20260830-artifact-agent-context/notes.md)。
- 验证通过：
  - `make generate` 连续两次结果一致（首次远程 Buf 插件连接重置，重试后通过）；
  - `make check`（首次报告新增视觉用例需 Prettier，格式化后全量通过）；
  - `make test-integration`；
  - `make test-credential-vault`；
  - `make test-artifact-context`；
  - `make test-deepseek-structured-review`；
  - `make capture-artifact-context-visual`；
  - `go test -race ./internal/core/credential/... ./internal/core/orchestration ./internal/platform/privatetls ./tests/devauth`
    （通过仓库固定 Go 容器执行）；
  - `docker compose config --quiet`、`git diff --check` 与新增 diff credential-token 扫描。
- 已知限制：本地 Compose/mTLS/restart 链路已验收；systemd drop-in 仅完成静态语法与权限模型复核，仍需在
  真实生产发行版主机上完成 provisioning/启动验收。此限制不降低当前 dev/CI working 证据。

## 交接

- 待修复提交创建后，补记 commit hash 并关闭本任务。
