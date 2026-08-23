# 下一位智能体 Prompt：Harness Binding UX 第二轮审核修复

> 将本文件完整交给执行智能体。当前分支仍未达到合并条件；只修复本文列出的第二轮审核问题。

## 你的角色与当前状态

你是 WorkOS `feat/harness-catalog-binding-ux` 分支的第二轮修复智能体。仓库位于
`/home/aquatao/workos`。第一轮修复提交为 `205f118 fix: isolate harness settings state by project`，其
父提交包含原始功能和第一轮审核 Prompt。开始时必须重新检查分支和提交，不得盲信上述哈希。

审核者已重新运行 `make check`：Go、Proto、架构、TypeScript、Desktop production build 和现有
desktop-web 15 tests 全部通过；`git diff --check main...HEAD` 也通过。但静态审核确认第一轮实现没有
满足原 Prompt 的 Project 切换语义，现有测试遗漏了关键失败路径，因此当前分支不得合并。

继续使用现有任务记录 `docs/tasks/20260823-harness-catalog-binding-ux.md`。不要创建新功能任务，不要修改
Proto、migration、Core、Harness、Gateway、SDK 或产品范围，不要实现任何后续模块。

## 开始前

1. 完整阅读：
   - `AGENTS.md`
   - `docs/prompts/20260823-review-harness-catalog-binding-ux.md`
   - `docs/tasks/20260823-harness-catalog-binding-ux.md`
   - `apps/desktop-web/src/Desktop.tsx`
   - `apps/desktop-web/src/Desktop.test.tsx`
   - `apps/desktop-web/src/HarnessSettings.tsx`
   - `apps/desktop-web/src/HarnessSettings.test.tsx`
2. 运行 `git status --short --branch` 和 `git log --oneline --decorate -6`；保留所有已有改动。
3. 将现有任务状态从 `done` 改回 `active`，记录第二轮审核结论。只有完成全部修复和验收后才能恢复
   `done`。

## 阻断项一：切换回来会恢复未保存 draft

当前 `Desktop.tsx` 使用长期存在的 `bindingDrafts: Record<project_id, selection>`。active Project 的
draft 取值为：

```text
bindingDrafts[activeProject.id] ?? selectionFromProject(activeProject)
```

因此以下时序仍然失败：

```text
Project A 的持久化 binding = Global Default
  → 用户在 A 选择 Fake，但不保存
  → 切换到 Project B
  → 再切回 Project A
  → UI 仍选择未保存的 Fake，而不是 A 持久化的 Global Default
```

第一轮 Prompt 明确要求切换 Project 后以目标 Project 当前持久化 binding 初始化，不得保留该 Project
上一次未保存的 draft。当前测试只覆盖不同 Project 的初始已保存 binding，没有覆盖切走再切回。

### 必须达到的行为

- active Project ID 每次变化时，编辑器 draft 必须从该 Project 当前缓存的持久化 binding 初始化。
- 离开 Project 时丢弃它的未保存 selection 和旧 feedback；切回时不得恢复。
- 不得出现一帧可交互的旧 draft。状态模型应把“当前编辑器”与后台每 Project pending operation 分开，
  或用等价的确定性方式保证切换是原子语义。
- Project 的服务端缓存、每 Project pending/save token 可以继续按 ID 隔离；不能用长期 draft map 代替
  持久化事实。

## 阻断项二：inactive Project 的响应仍写入编辑器状态

第一轮实现只检查 operation token，没有检查响应到达时用户是否仍在查看该 Project。成功、普通失败和
revision conflict refresh 都会把 draft/feedback 写入对应 map；这些内容虽然不会立即显示在另一个
Project，但用户稍后切回时会看到过期的“saved”或 conflict 提示。

现有 conflict 测试甚至明确断言切回 A 后显示 `changed elsewhere`，这与第一轮 Prompt 的条件相反：只有
响应到达时仍在查看该 Project，才允许重置当前编辑器并显示该次操作的 feedback。

### 必须达到的行为

- 每个请求继续捕获不可变的 `project_id` 与单调 operation token。
- token 已被新操作取代时，整个旧响应都必须忽略；在调用 `replaceProject` 之前就检查 token，不能让旧
  Project 响应覆盖较新的缓存。
- token 最新的服务端成功或 conflict refresh 响应可以更新同 ID 的 Project 缓存。
- 只有响应到达时该 Project 仍为 active Project，才能更新当前 editor draft/feedback。
- 响应在 Project inactive 时到达：
  - 不影响当前 Project 的 draft、feedback 或 saving；
  - 不为 inactive Project 保存稍后显示的 feedback/draft；
  - 稍后切回时，从已经更新的 Project 缓存初始化，因此能看到正确 revision/binding，但没有过期提示。
- 普通失败在 Project inactive 时同样不得留下稍后显示的错误。
- 组件卸载时使所有 binding operation token 失效；pending Promise 随后完成不得调用任何 binding 相关
  state setter，也不得产生 React act/state-update 警告。

推荐维护一个同步的 active Project ID ref 和 mounted/operation generation，并把 Project 缓存更新与当前
editor 更新分成两个明确步骤。不要用 timeout、sleep、隐藏 Project 切换或吞掉所有响应来规避。

## 小型门禁修正

根目录 `.gitattributes` 当前最后一行没有 POSIX newline。只补文件末尾 newline，不改变其唯一例外规则：

```gitattributes
sdk/protocol/src/gen/** whitespace=-blank-at-eof
```

禁止修改任何生成文件，禁止放宽其他路径的 whitespace 检查。

## 必须新增或修正的确定性测试

使用 deferred Promise，不使用随机 sleep：

1. **未保存 draft 丢弃**：A 持久化为 Global，选择 Fake 不保存，切到 B 再切回 A；A 必须重新选中
   Global，保存按钮保持 disabled，旧 feedback 不存在。
2. **inactive success**：A 保存 pending 时切到 B，A 成功响应后 B 完全不变；再切回 A 时显示响应中的
   revision/binding，但不显示 `Harness setting saved.`。
3. **inactive conflict refresh**：A conflict refresh pending 时切到 B；refresh 返回后 B 不变；再切回 A
   时显示刷新后的 revision/binding，但不显示 `changed elsewhere`。修正当前相反的断言。
4. **inactive ordinary failure**：A 请求失败前切到 B；切回 A 后不得显示该次过期错误，A draft 来自持久化
   binding。
5. **unmount**：binding Promise pending 时 unmount Desktop，再 resolve/reject；不得产生 binding state
   update 或 act warning。
6. 保留第一轮成功串线、Catalog non-ready、Global Default、unknown/unavailable binding、credential 不渲染
   与 provider snapshot 覆盖。

如果允许不同 Project 并发保存，再增加反序完成测试，证明 A/B 的 pending token 和缓存不会互相清除；
如果产品选择一次只允许一个保存，必须在 UI 中明确 busy 且仍满足上述切换行为。

## 任务记录修正

当前任务记录声称“切换后以持久化 binding 初始化”，但代码实际保留未保存 draft；还声称 inactive
conflict 会在切回后显示提示，而第一轮 Prompt 禁止这种过期 feedback。先删除或更正这些不实证据，
再记录第二轮测试覆盖和最终行为。

`docs/status.json`、README 状态和架构文档不需要改变。

## 验收

完成后实际运行并记录：

```bash
make generate
make generate
make check
make test-integration
make test-deepseek-fixture
make test-e2e
buf breaking --against '.git#branch=main'
git diff --check
git diff --check main...HEAD
```

确认生成树第二次无漂移、`docs/structure.md` 未变化、没有 root-owned 文件、真实 secret、测试产物或新增
migration。不得使用真实 DeepSeek Key，不得删除 PostgreSQL volume。

## 提交与交接

- 全部验收通过后把任务状态恢复为 `done`。
- 使用 Conventional Commit 提交到当前功能分支，建议：

```text
fix: reset harness editor on project changes
```

- 提交后 `git status --short` 必须为空。
- 不要 merge、push、rebase、force、删除分支或执行 `docker compose down -v`。
- 最终报告说明状态模型、五类新测试、完整命令结果、提交 ID，并明确未 merge、未 push、未使用真实 Key、
  未删除 volume。由审核者完成下一轮复审和 `--ff-only` 合并。
