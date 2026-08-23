# 下一位智能体 Prompt：修复 Harness Catalog / Binding UX 合并阻断项

> 将本文件完整交给执行智能体。目标是修复当前功能分支的审核阻断项并提交，不能扩展成新功能。

## 你的角色

你是 WorkOS `feat/harness-catalog-binding-ux` 分支的修复智能体。仓库位于
`/home/aquatao/workos`。Harness Provider Catalog 与 Project Binding UX 的主链路已经实现并通过现有
单元、集成和浏览器测试，但代码审核发现 Project 切换期间的异步状态串线，以及一个生成文件
whitespace 门禁问题。

只修复本文列出的审核项、补测试、更新现有任务记录并提交到当前功能分支。不要实现 App Registry、
Runtime、Credential Vault、其他 Provider 或任何新的产品能力；不要 merge 或 push。

## 当前仓库事实

- 当前功能分支应为 `feat/harness-catalog-binding-ux`，审核时 tip 为 `899dded`，本地 `main` 为其直接
  祖先 `8ccf7cf`。开始时必须重新核对，不能假定提交号仍未变化。
- 当前工作树在审核结束时 clean；如果开始时已有改动，先识别并保留，不得覆盖。
- 现有任务记录是 `docs/tasks/20260823-harness-catalog-binding-ux.md`，不要新建另一个功能任务。
- 当前实现的公共 Proto、Core Catalog、binding orchestration、Gateway allowlist、SDK、默认 Fake E2E
  和 DeepSeek 本地 fixture E2E 均已存在。本修复不需要修改 Proto、migration 或服务端接口。
- 审核基线已实际通过：
  - `make check`
  - `make test-integration`
  - `make test-e2e`（默认 spec 通过，fixture-only spec 按设计 skip）
  - `make test-deepseek-fixture`（Go fixture、重启验证、浏览器 fixture 均通过）
  - `buf breaking --against '.git#branch=main'`
- `git diff --check main...HEAD` 当前只报告两个由 `protoc-gen-es` 新生成文件造成的
  `new blank line at EOF`；仓库现有全部生成 TypeScript 文件都使用相同结尾，禁止手改生成文件规避。
- 测试使用本地假 credential，不需要、也不得查找或使用真实 DeepSeek Key。
- PostgreSQL volume 含验收数据，禁止 `docker compose down -v`。

## 开始前必须完成

1. 完整阅读：
   - `AGENTS.md`
   - `docs/architecture/implementation.md`
   - `docs/status.json`
   - `docs/tasks/20260823-harness-catalog-binding-ux.md`
   - `apps/desktop-web/src/Desktop.tsx`
   - `apps/desktop-web/src/Desktop.test.tsx`
   - `apps/desktop-web/src/HarnessSettings.tsx`
   - `apps/desktop-web/src/HarnessSettings.test.tsx`
   - `apps/desktop-web/src/model.ts`
2. 运行 `git status --short --branch`、`git log --oneline --decorate -5`，确认正在现有功能分支并保留所有
   已有改动。
3. 将现有任务记录状态从 `done` 改回 `active`，在交接部分写明本次审核阻断项。修复和完整验收结束后
   才能重新标记 `done`。
4. 不要改写或删除当前已通过的 Catalog、binding、Gateway、DeepSeek fixture 实现。

## 合并阻断项一：Project 切换时异步状态串线

当前 `Desktop.saveHarnessBinding()` 捕获调用时的 `activeProject`，但成功、revision conflict refresh、
失败和 `finally` 分支会直接更新全局的 `bindingDraft`、`bindingFeedback`、`bindingFeedbackIsError` 与
`bindingSaving`。

可复现时序：

```text
Project A 选择 provider 并开始保存
  → SetProjectHarnessBinding 尚未返回
  → 用户切换到 Project B
  → B 的 draft 从 B 当前 binding 初始化
  → A 的请求成功或进入 conflict refresh
  → A 的异步结果覆盖当前正在显示的 B draft / feedback
```

这会让 B 显示 A 的 provider，用户随后可能把错误 provider 保存到 B。成功响应和 revision conflict
后的 `GetProject` 响应都必须修复，普通失败提示也不能显示在另一个 Project 的编辑器中。

### 必须达到的行为

- 每次 binding 操作必须带有不可变的 `project_id` 和 operation token；异步 continuation 不得依赖后来
  变化的 active Project。
- 服务端返回的 Project 可以更新列表中同 ID 的缓存项，但只有以下条件同时成立时才能修改当前编辑器
  的 draft、feedback 或 saving 状态：
  - 当前 active Project ID 仍等于该操作的 Project ID；
  - 该 token 仍是这个 Project 的最新操作。
- pending/saving 状态必须按 Project 或 operation 隔离。切换到 B 后，A 的完成不能把 B 标成保存成功、
  冲突或失败，也不能解除 B 自己的 pending 状态。
- 切换 Project 后，编辑器立即以目标 Project 当前持久化 binding 初始化；不得沿用前一个 Project 的
  未保存 draft。
- revision conflict 仍需 owner-scoped 地刷新发生冲突的原 Project。刷新结果更新对应缓存；只有用户仍在
  查看该 Project 时才重置该编辑器并显示 conflict 提示。
- 同一 Project 保存期间继续阻止重复提交。可以选择允许不同 Project 并发保存，或对其他 Project 显示
  明确 busy 状态，但绝不能用一个无归属的全局 boolean 造成跨 Project 状态覆盖。
- 组件卸载或请求被新 token 取代后，旧响应必须被忽略，不能产生 React state update 警告。

优先使用小型 reducer 或带 Project ID/token 的显式状态，不要通过增加任意 timeout、隐藏 Project
切换按钮或依赖响应先后顺序来掩盖竞态。

## 合并阻断项二：Catalog 非 ready 时仍可能保存旧 provider

`HarnessSettings` 的 `draftCanSave` 当前只查询仍保留在内存中的 `catalog`。调用 retry 后
`catalogState="loading"`，旧 Catalog 和旧 provider draft 仍可能让保存按钮保持可用。

必须调整为：

- 保存具体 provider 只在 `catalogState === "ready"`、当前 Catalog 仍包含该 ID，且 health 为
  healthy/degraded 时允许。
- Catalog loading、error 或 provider 已消失时禁止提交具体 provider。
- `Use Global Default` 不依赖 Catalog；Catalog 不可用时仍可解除 binding。
- 已保存但当前 unknown/unavailable 的 binding 仍必须显示且可解除，不能静默清除或自动回退。

## 合并门禁项：生成文件 whitespace

不要修改 `sdk/protocol/src/gen` 中的任何文件。创建或更新仓库根目录 `.gitattributes`，只为生成目录
关闭 Git 的 `blank-at-eof` 检查：

```gitattributes
sdk/protocol/src/gen/** whitespace=-blank-at-eof
```

不得全局关闭 trailing whitespace 检查，也不得对非生成文件放宽规则。完成后
`git diff --check main...HEAD` 必须通过。

## 必须新增的测试

使用 deferred Promise 或等价的确定性控制，不要依赖真实计时器或随机 sleep：

1. Project A 保存未完成时切换到 B；A 成功返回后：
   - A 的列表项可以更新；
   - B 的选中 provider、revision、feedback 均不被 A 覆盖；
   - B 仍可按自己的状态保存。
2. Project A 保存返回 `Code.Aborted`，A 的 `GetProject` refresh 未完成时切换到 B；refresh 返回后：
   - 只更新 A 的缓存；
   - B 的 draft 和提示不变；
   - 再切回 A 时显示刷新的 revision/binding。
3. Catalog 从 ready 进入 loading/error 时，之前选中的具体 provider 不能保存；Global Default 仍可保存。
4. 保留并通过现有 idle Project 切换、revision conflict、unknown binding、credential 不渲染和 provider
   snapshot 测试。

如果测试暴露同一状态模型中的直接变体，可以一并修复；不得借机重做 Desktop Window Manager 或其他
UI。

## 明确禁止

- 不修改 `api/proto`、migration、Core/Harness/Gateway 行为或公开 SDK 接口。
- 不手改 `gen/`、`sdk/protocol/src/gen/` 或 README 自动生成状态区块。
- 不新增 Provider、credential 字段、Key UI、真实网络 smoke、App Registry 或 Runtime 功能。
- 不读取 shell history、聊天历史、进程环境或其他本机文件寻找真实 Key。
- 不执行 force push、rebase、merge、分支删除、`git reset --hard` 或 `docker compose down -v`。
- 不删除现有 PostgreSQL volume，不清理与本任务无关的容器或用户改动。

## 验收命令

完成代码和测试后至少执行并记录：

```bash
make generate
make check
make test-integration
make test-deepseek-fixture
make test-e2e
buf breaking --against '.git#branch=main'
git diff --check main...HEAD
```

`make generate` 之后立即再次运行一次，确认第二次无生成漂移；确认 `docs/structure.md` 未变化、工作树中
没有 root-owned 文件、真实 secret 或测试产物。

## 任务记录与提交

- 在 `docs/tasks/20260823-harness-catalog-binding-ux.md` 交接部分记录：
  - 两个异步竞态测试覆盖的时序；
  - Catalog non-ready 保存门禁；
  - `.gitattributes` 的生成器例外范围；
  - 每条实际运行的验收命令及结果。
- 只有全部验收通过后才把任务状态恢复为 `done`。
- 使用 Conventional Commit 提交到当前功能分支，建议提交信息：

```text
fix: isolate harness settings state by project
```

- 提交后确认 `git status --short` 为空。不要 merge 或 push；由审核者复审后执行 fast-forward 合并。

## 最终交接格式

完成后向用户简洁报告：

1. Project/operation 状态如何隔离，为什么旧响应不再污染当前 Project。
2. Catalog loading/error 与 Global Default 的保存规则。
3. 新增了哪些确定性竞态测试。
4. 完整验收命令结果和生成漂移结果。
5. 提交 ID、工作树状态，并明确说明未 merge、未 push、未使用真实 Key、未删除 volume。
