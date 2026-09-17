# F26 新旧回归记录（2026-09-17）

以下为接续前历史轮次，不代表最终修复后的重跑。当前结果见
[repair-validation.md](repair-validation.md)。

## v2-completion 全量门禁

命令：`sh tools/v2-completion/gate.sh`（等价 `make test-v2-completion`）

- 退出码 0；证据 tmp/v2-completion.1SdXPj。
- 覆盖：隔离六进程栈上 regression.go 集成（含 grants 垂直切片、会话工具授权/撤销、
  App 审批链）、workspacehost adapters 单测（dockerexec 真容器隔离、localfs 原子写）、
  `e2e/v2-completion.spec.ts`（共享文件、恢复的 native display、真实开发 preview，
  1 passed；3 个视觉快照 spec 按设计跳过）。

## legacy 门禁

命令：`sh tools/v2-p3-delivery/gate.sh legacy`（等价故障名 `make test-v2-p3-faults`
之外的 legacy profile；本宿主无 `make`，直接执行脚本）

- 退出码 0；`v2-p3-delivery: PASS (legacy profile)`。
- 运行：TestRepairBuildTestChain、TestRepairBuildTestRestartSeed、
  （compose restart runtime）TestRepairBuildTestRestartRestore、
  TestRepairRecoveryFallback、TestRepairRecoveryAwaitingManual、
  TestRepairBuildTestMatrixSeed、（compose stop reliability）
  TestRepairBuildTestDeploymentMatrix。
- 原始日志：tmp/v2-p3-delivery.J6pJaF（运行后已清理容器，日志保留）。

## grants / Web Bundle（隔离 v2-completion 栈）

命令：`sh tools/v2-p3-delivery/f26-grants-webbundle.sh`（固化脚本；过程
prepare-only 起隔离六进程栈 → regression.sh → 定向 go 测试 → playwright →
私有栈拆除，全部退出码落 results.txt）

- 最终退出码 0，步骤记录（tmp/v2-p3-f26.3MjNZV/results.txt）：
  prepare-only exit=0；regression.sh exit=0（含
  TestMutableProjectAppGrantsVerticalSlice、TestSessionToolAuthorizationAndRevocation
  等 3 段 ok）；grants-webbundle-go exit=0（TestMutableGrantRevocationChain、
  TestWebBundleSurfaceVerticalSlice 均 `--- PASS`）；playwright exit=0
  （mutable-grants.spec.ts + web-bundle-surface.spec.ts，2 passed）。
- 失败重跑记录（诚实保留）：
  1. 第一轮 tmp/v2-p3-f26.Djgnxg：grants-go 因未传
     `WORKOS_TEST_RUNTIME_URL` 直连 127.0.0.1:8083 拒绝；playwright 步骤因
     脚本续行错误未执行。修复脚本后重跑。
  2. 第二轮 tmp/v2-p3-f26.fRuT7U：playwright 已过；
     TestWebBundleSurfaceVerticalSlice/SurfaceIdempotencyBindsTrustedDevice
     失败——该测试此前从未接入任何门禁，直连设备身份硬编码共享开发栈
     owner（0198d7ea-…），与隔离栈 owner 不匹配。已改为从
     `WORKOS_TEST_OWNER_ID` 派生（保留开发栈默认值）。
  3. 第三轮 prepare 因与主门禁同时起栈资源竞争超时（tmp/v2-p3-f26.Glp23K），
     重试成功。
- 共享开发栈未被触碰（隔离 namespace + 独立端口 + 私有 PostgreSQL）。
