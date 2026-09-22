# V2 最终集成验收（2026-09-22）

最终功能树 `eb69a24` 合入移动 `28dc6aa`、网络 `49227c9`、原生 `2c3eb28` 和 P3
`b232993`。本任务之后仅更新交付文档，不再修改产品代码。主工作目录 `/home/aquatao/workos`，
分支 `feat/v2-final-integration`。

合并中的三个文档冲突来自 P3 同一补丁的不同提交：逐字节确认 `b232993` 与 `8d507cc`
的原 README/status/implementation 完全相同，保留后续功能事实并用工具重新生成 README。
没有丢弃 P3 事实或其他工作树变更。最终产品源码和移动验收分支一致。

| 检查         | 最终结果                                                                                                                                          | 本机记录                                                   |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| 依赖         | frozen lockfile 安装通过，无依赖升级                                                                                                              | `tmp/integration-pnpm-install.log`                         |
| 生成         | `make generate` 前后 212 个 Proto/sqlc/README 文件哈希一致                                                                                        | `tmp/integration-generate.log`                             |
| 全仓         | `make check` PASS；Proto/sqlc、Go vet/tests、13 个 TS workspace 架构、lint/format/typecheck/unit/build/status；desktop 173 tests、mobile 12 tests | `tmp/integration-make-check.log`                           |
| 六进程       | 从最终源码重新编译二进制及桌面，`sh tools/v2-completion/gate.sh` PASS                                                                             | `tmp/integration-v2-gate.log` / `tmp/v2-completion.w9RP68` |
| 开发／持久化 | 同一会话两轮真实文件执行、Core/Harness/Gateway 重启恢复、成果/预览/提问、PTY 后台状态及控制代次、Runtime 重启拒绝旧程序身份                       | 同上                                                       |
| 权限／运行   | App artifacts/approval/拒绝不执行、项目 grant、容器 PTY/Native/预览、真实工作区边界、长路径预览等集成测试 PASS                                    | 同上                                                       |
| 浏览器       | 4 passed，0 skipped/flaky/unexpected，30.6 秒；1440×900、820×1180、390×844 与真实开发/原生显示续接                                                | [浏览器摘要](browser-summary.json)                         |
| 子工作树     | 最终树单独设置真实宿主挂载路径运行 `TestDelegatedWorktreesAgainstDocker` PASS；隔离父/子/兄弟、diff 新文件、拒绝 dirty/重复准备与 hook 隔离       | `tmp/integration-worktree-probe.log`                       |
| 清理         | 本次 `v2-completion-w9rp68` 容器和网络归零；未操作共享开发栈或其他旧任务容器                                                                      | 命名空间查询为空                                           |
| 密钥         | 2260 个 Git 候选文件和所有分支可达历史的 5045 个 blob，精确字节扫描无真实 key；仓库外目录 0700/文件 0600                                          | `tmp/integration-secret-audit.log`                         |
| UI／APK      | 移动任务三个 client 的 after/current 同名文件逐字节一致，截图均小于 2 MiB；普通 debug APK 拷入本机 deliverables 后摘要一致                        | [移动结果](../20260921-v2-mobile-android/results.md)       |

默认 V2 gate 因未设置 `WORKOS_WORKTREE_PROBE_ROOT` 会跳过该独立 Docker 子树探针；上表
已在最终源码补跑并通过，没有将 skip 算作通过。重现命令：以 Docker socket、宿主同路径
工作树和测试目录挂载 Go 容器，设置 `WORKOS_WORKTREE_PROBE_ROOT=$PWD/tmp/integration-worktree-probe`，
运行 `go test -count=1 -run '^TestDelegatedWorktreesAgainstDocker$' -v ./internal/runtime/workspacehost/adapters/dockerexec`。

本次没有新增付费模型调用。真实 DeepSeek、P3 全矩阵、LAN/NAT/TURN 和真实 APK 的专项
证据均随其功能提交保留，入口见 [交付索引](../../../architecture/v2-delivery-status.md)。
网络是 Docker 拓扑，Android 是 KVM 模拟器；物理/公网、iOS、后台推送、商店发布等边界不变。

## 本机日志 SHA256

- `tmp/integration-pnpm-install.log`：`dcc10c7627f3dc6b0f6be07fdfb2ea49e10545b3de5e0b71c8ae94b1d9fc2f7c`
- `tmp/integration-generate.log`：`049add0d0f4de83f2fa1188381288b266a93615ec67a8d9e603f2bc1d7e79ee0`
- `tmp/integration-make-check.log`：`0a8a538a0bc05ae4515040dec24b4556ff07869e839e0295502d60eb3dda5420`
- `tmp/integration-v2-gate.log`：`b6950df012a380c331e4abe30b21922ce74998962c7363e9221616d1488c42b5`
- `tmp/integration-worktree-probe.log`：`e0febf4a31f67daf10ea844c8bffe8717a57f5b00bc41fed653ab782a482d7b5`
- `tmp/integration-secret-audit.log`：`aae26dd91c6cfeb4433152f70e1daae62da32a4e1b12490b1968f77f7b46a836`
