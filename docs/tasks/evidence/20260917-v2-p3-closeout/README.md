# 2026-09-17 P3 closeout 证据索引

本目录记录 C08 收尾的命令与结果。历史 09-16 日志不得冒充本次重跑。

## 环境

- 分支 `feat/v2-p3-closeout`，开始 HEAD `21b16f6`，产品基线 `6e8bf37`。
- 隔离门禁：自有 PostgreSQL、当前源码六进程、`WORKOS_FAULT_DIR` 文件屏障。
- 未使用真实模型 API key。

## 入口

| 命令                                                                | 覆盖                                                               |
| ------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `make test-v2-p3-delivery` / `sh tools/v2-p3-delivery/gate.sh`      | 原主链 + `TestP3Closeout` + `p3-delivery`/`p3-closeout` E2E + 重启 |
| `make test-v2-p3-faults` / `sh tools/v2-p3-delivery/gate.sh faults` | 同一栈的 faults 配置名                                             |
| `sh tools/v2-p3-delivery/gate.sh legacy`                            | 旧 image-only/process 链                                           |

原始门禁日志在每次运行的 `tmp/v2-p3-delivery.*`；成功运行会按 namespace 清理容器。将脱敏摘要回填本目录后再把对应用例标 PASS。

修复接续以 [repair-validation.md](repair-validation.md) 为当前结果索引；下列旧轮次记录保留为历史。

## 当前结论

本轮缺陷修复已通过 bundle/legacy、V2 连续工作区与 grants/Web Bundle/owner rollback
专项门禁，并获用户授权合并本地 main。完整 P3 保持 scaffolded，逐项范围以
[总任务 F01–F27](../../20260916-v2-p3-real-artifact-delivery.md) 为准。

专项兼容入口：`sh tools/v2-p3-delivery/f26-grants-webbundle.sh`；
连续工作区入口：`sh tools/v2-completion/gate.sh`。
