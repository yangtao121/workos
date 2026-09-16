# P3 合并审查验证（2026-09-16 UTC）

范围：`feat/v2-p3-real-artifact-delivery`，本地 main 基线 `3820447`。接管 GLM 的已提交与
未提交实现后修复；保留无关 `.zcode/plans/` 文件。完整验收状态见
[任务记录](../../20260916-v2-p3-real-artifact-delivery.md)，以下 PASS 不代表未覆盖的验收组合已完成。

## 可复现命令与结果

| 命令                                                                                                                                                              | 结果与证据                                                                                                                                               |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `sh tools/v2-p3-delivery/gate.sh`（即 `make test-v2-p3-delivery`）                                                                                                | PASS；[bundle gate 原始测试输出](bundle-gate.txt)                                                                                                        |
| `sh tools/v2-p3-delivery/gate.sh legacy`                                                                                                                          | PASS；[legacy gate 原始测试输出](legacy-gate.txt)                                                                                                        |
| `make check`                                                                                                                                                      | PASS；Proto format/lint、sqlc vet、Go format/vet/test、TS architecture/lint/format/check、desktop build、生成状态检查；[检查摘要](repository-checks.txt) |
| `make generate` 两次生成比较                                                                                                                                      | PASS；191 个生成文件（Proto Go/TS、SQL bindings、README）逐字节一致；提交后再次确认无新增 diff                                                           |
| `buf breaking --against '.git#ref=main'`                                                                                                                          | PASS，退出 0，无输出；比较基线 3820447                                                                                                                   |
| `go test -race ./internal/runtime/buildtest/application ./internal/runtime/artifactstore/... ./internal/reliability/application ./internal/reliability/transport` | PASS；[race 输出](race.txt)                                                                                                                              |
| `go test -race ./internal/runtime/workload/adapters/dockerapp ./internal/runtime/workload/application`                                                            | PASS；含 12 并发解包、启动前无 IP 接管；[并发回归](concurrent-unpack.txt)                                                                                |
| `WORKOS_TEST_DOCKER_BUILD=1 go test -count=1 -run TestReal -v ./internal/runtime/buildtest/adapters/dockerbuild ./internal/runtime/workload/adapters/dockerapp`   | PASS，真实 Docker 构建冻结、失败无输出、独立 A/B 服务；[容器测试](real-containers.txt)                                                                   |
| `playwright test e2e/version-dialog-visual.spec.ts --workers=1`                                                                                                   | before/after 各 3 tests PASS；9 个状态/尺寸截图各一组；[视觉说明](../../../ui/desktop-web/changes/20260916-v2-p3-real-artifact-delivery/notes.md)        |
| `git diff --check`                                                                                                                                                | PASS                                                                                                                                                     |

宿主没有 Go/node/pnpm/make，使用仓库固定 Docker 工具链执行上述命令。make 本身在
`workos-make:local` 中运行，仓库以同一绝对路径挂载，并映射 Docker socket；不改变
Makefile 的校验步骤。Go 1.26.7、Node 24.19.0、buf 1.55.1、sqlc 1.30.0、Playwright 1.62.1。

## 真实发布主链

新门禁为每次运行建立自有 pgvector PostgreSQL、随机端口、私有目录及 namespace；
使用当前源码编译的六进程二进制，镜像只提供运行依赖。缺少镜像/数据库/真实 runner
即失败，不存在 skip 后 PASS。Generic CLI 仅确定性提交修复候选，构建、测试、
产物仓库、Docker App、Gateway HTTP、Chromium 和回滚均真实运行。

- 管理员经 Unix socket 流式导入 A，Core 注册/安装，公开 Surface 返回 `P3-VALUE-0`。
- 候选把服务行为改为 42，固定 build/test 配方在 Docker 中执行。B 的包摘要必须不同于 A。
- canary/发布台账保存的 Workload ID/generation 必须与实际 B 及其 artifact digest 相符。
- owner 公开 rollback RPC 使用当前 revision，公开 Surface 返回 A；正常替换不得产生额外 incident。
- 独立候选在启动时退出 42，自动流程经历 rollback_pending/rolled_back，实际恢复 A。
- Chromium 经真实 Gateway 再次读取 A→B→A；随后重启 Core/Runtime/Reliability，
  再验证 A 的 HTTP 行为和导入包的 ready/摘要。

成功运行的固定镜像为 `golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514`。
B 摘要为 `sha256:6dadca44cb70c96c33acf8566a69a50d3d779a77a0edc0a6bea6c64617d7da81`；
此次发布的 Workload 为 `01a0ab18-7f9c-7d9b-8b6e-fd5e40a343ba`，generation=1，台账与运行行相符。
版本替换创建新的 Workload 身份，因此不能只比较 generation 数字。

原始临时诊断目录为 `tmp/v2-p3-delivery.lwhkEN/`；成功的门禁会自动清理其容器、网络、volume
及数据库，保留本地诊断。上面的仓库证据仅含确定性 fixture，不含真实凭据或用户数据。

## 审查中定位的失败

原实现存在流式 chunk 重读、同字节来源混淆、成功 verdict 前过早 ready、宿主可写构建目录、
租约失效仍提交、回滚复用 B 摘要、Workload 缺产物持久列/网桥约束、canary 未固定代次等问题。
已修复并由上列单测、数据库主链与真实容器/浏览器验证覆盖。

浏览器门禁首次揭示并发解包被对账误判，导致额外修复把人工回滚后的 A 再改回 B；
修复为临时目录完成后原子发布，并允许校验通过的容器在启动前无 IP。最终门禁加入
无额外 incident 的断言并通过。曾失败的尝试保留在 `tmp/p3-review/`，不冒充成功证据。

限制：完整故障注入/授权矩阵以及 App grant、Workspace、Web Bundle 独立门禁未全部重跑；
Release 的旧包摘要、AppID、细粒度 failure category 仍为空。rootless/memory.high、
第二台物理 LAN 设备、P4 均不在本次完成声明内。
