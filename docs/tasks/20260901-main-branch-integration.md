# Task: Main branch integration and local branch cleanup

- 状态：done
- Owner/Agent：Codex
- 进程/模块：repository-wide Git integration；不改变既有六进程边界
- 依赖：`main` at `12a53ab`；各功能分支任务记录与验收证据；干净工作树

## 目标与范围

盘点全部本地与远端分支，用提交可达性和任务记录确认功能是否已进入 `main`；将仍缺失的
功能分支按依赖顺序合并到 `main`，验证合并后的生成结果与门禁，并删除已合并的本地分支。

范围包括：

- fetch/prune 后的本地与远端分支拓扑、ahead/behind、merge-base 和任务交接核对；
- 合并 `feat/v1-runtime-reliability-adaptive-closeout`；
- 核对非祖先旧分支是否已被干净候选分支功能等价替代；
- 合并后删除所有已进入 `main` 或经仓库任务记录明确认定已被替代的本地分支；
- `make generate` 幂等性、`make check` 和与合并功能相称的集成/E2E 门禁。

不包括：推送远端、删除远端分支、为未完成能力补写实现、放宽宿主安全能力，或把明确
blocked/scaffolded 的能力伪报为 working。

## 协议/数据影响

本整合任务本身不新增 Proto、event、migration 或 capability。被合并分支已有的 additive
Proto、migration 025、ADR-0012 和状态更新保持其原任务边界与证据。

## 验收

- [x] 全部分支均被分类为 `main` 可达、待合并，或有仓库证据证明被后续分支替代
- [x] 缺失功能分支合并到 `main`，无未解决冲突
- [x] `make generate` 连续两次无生成漂移，`make check` 通过
- [x] 合并功能的集成/E2E 专项门禁通过，已知宿主 blocker 保持诚实记录
- [x] 已合并/已替代的本地分支删除，最终仅保留 `main`
- [x] 任务记录与 `docs/status.json` 一致，工作树干净

## 交接

### 分支结论

- `feat/v1-runtime-reliability-adaptive-closeout` 是唯一真正领先整合前 `main` 且尚未交付的功能
  分支；其 5 个提交以 merge commit `da8adee` 无冲突合入 `main`。
- 其余 16 个功能/修复/文档分支的 tip 均已是 `main` 祖先。
- `feat/minimal-web-bundle-surface` 的 4 个提交不是 `main` 祖先，但
  `docs/tasks/20260825-minimal-web-bundle-surface.md` 明确认定它是含大二进制历史的污染分支，
  并指定已进入 `main` 的 `feat/web-bundle-surface-merge-candidate` 为权威交付。树级核对确认候选
  保留其功能并额外包含 transient 分类/测试等加固，因此未把旧实现再次合入。
- fetch/prune 后远端只有 `origin/main`；没有远端功能分支。本地 16 个祖先分支以 `git branch -d`
  删除，已替代污染分支在上述证据核对后以 `git branch -D` 删除，最终仅保留 `main`。

### 已验证命令

| 命令                                                         | 结果                                                                               |
| ------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| `git fetch --all --prune` + merge-base/ahead/behind/祖先核对 | PASS                                                                               |
| `make generate` 连续两次 + 每次 `git diff --exit-code`       | PASS，无生成漂移（期间 Buf 远端短暂不可用，恢复后完整复跑）                        |
| `make check`                                                 | PASS（Go/Proto/architecture/TypeScript/单元测试/生产构建/status render）           |
| `make test-integration`                                      | PASS（真实 PostgreSQL + restart battery，包括 version transition/rollback replay） |
| `make test-e2e`                                              | PASS（14 passed，11 skipped）                                                      |
| `make test-adaptive-shell`                                   | PASS（4 passed）                                                                   |
| `make test-app-version-rollback`                             | PASS（1 passed）                                                                   |
| `make test-podman-fixture`                                   | BLOCKED as designed：宿主没有 Podman                                               |

### 未决风险 / 下一步

- `docs/status.json` 保持合并分支的真实裁决：Runtime container 与 Reliability 真实链路仍受
  rootless Podman acceptance host 阻塞，不升级 capability。
- 本任务不含远端写操作；本地 `main` 尚需由仓库维护者推送到 `origin/main`。
