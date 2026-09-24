# Task: WorkOS V3 浏览器原生桌面与 Harness 协作架构

- 状态：done（仅文档交付；V3 功能尚未实施）
- Owner/Agent：Codex
- 进程/模块：架构与进度文档；无功能代码变更
- 分支/worktree：`docs/architecture-v3-native-workspace` / `/home/aquatao/workos`
- 基线：`main@39f584b`，开始时工作树干净。
- 依赖：V2 交付索引、ADR-0032 至 ADR-0037、当前 Proto、Native/工作区/会话实现与既有测试。

## 目标与范围

把已确认的讨论写入 [structure-v3.md](../structure-v3.md)，明确浏览器唯一客户端主线、
局域网、持久项目环境、原生 WorkOS Code、SSH 远程开发和人与 Harness 共同操作。
浏览器合成器优先，以 Greenfield 为首个验证实现；Mac Chrome/Edge 的输入、双向文本
剪贴板、高分屏和同实例接续是 P0 门禁。IDE 服务于人的工作，Harness 直接操作相同环境。

同步原结构文档、V2 和实现说明的入口，以及状态事实中的设计/实现边界；保留 V2 历史。
不实施 P0–P3，不安装图形组件，不调用收费模型，不新增登录、配对、授权或审批工作流。
不扩展公网或 Mobile/Android 原生客户端；已有代码和交付证据保留。

## 协议/数据影响

none。本次只记录后续接口方向，不修改 Proto、manifest Schema、migration 或应用代码。
V3 设计不替代已接受 ADR；行为变更由后续专项任务采用 ADR 并先修改契约。
模块实现状态不升级，README 状态区只通过生成工具更新。
不涉及可见 UI，按 UI 文档约定无需生成新的 before/after/current 截图。

## 验收

- [x] V3 文档覆盖已确认范围、当前事实、核心架构、接口方向、P0–P3 和验收。
- [x] Greenfield 上游能力、待验证限制与 WorkOS 当前实现明确区分；官方来源可追溯。
- [x] 原方案/V2 正文与原有交付证据保留，相对链接有效。
- [x] `docs/status.json` 同步边界，所有模块实现状态保持不变。
- [x] README 由工具生成，`make generate` 不产生额外生成差异。
- [x] `make check` 通过，变更仅含本任务文档及工具生成的 README 状态区。

## 验证记录（2026-09-24 UTC）

- Python 检查：56 个本地文档链接有效；23 个模块的名称、进程和状态未变，原 evidence
  全部保留，只为 5 个模块追加 V3 设计边界；原方案、V2 和实现说明仅增加导航文字。
- `make generate`：通过。执行前后的 Git diff 摘要与文件清单一致；Proto/SQL 生成文件
  无差异，README 状态区由 `tools/status/render.mjs` 生成且重复生成稳定。
- `make check`：通过。覆盖 Proto/SQL 校验、Go 格式与 vet/测试、TypeScript 架构与
  ESLint/Prettier、各 workspace 类型检查和测试、桌面生产构建、状态生成一致性。
  构建保留已有的单包超过 500 kB 提示，退出码为 0。
- `git diff --check`：通过。没有应用代码、Proto、依赖或 UI 变更。

本机通过仓库固定版本的 Docker 工具链运行检查；等价复现命令：

```sh
for gate in generate check; do
  docker run --rm --user "$(id -u):$(id -g)" \
    --group-add "$(stat -c %g /var/run/docker.sock)" \
    -v /usr/bin/docker:/usr/bin/docker:ro \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$PWD:$PWD" -w "$PWD" \
    golang:1.26.7-bookworm make "$gate" || exit
done
```

本地检查日志：`tmp/architecture-v3-generate.log`、`tmp/architecture-v3-check.log`。
日志不纳入版本库，以上结论及复现命令作为文档任务的持久记录。

## 交接

已阅读目标文档、相关 Proto 和既有测试，确认基线工作树干净，建立唯一文档分支。
既有 V2 测试结果来自仓库证据，不算本次重新执行的产品验收。
Greenfield 的同实例接续、真实 VS Code 体验、Mac 输入/剪贴板及图形性能仍待 P0 实测。
用户明确要求本次只交付设计并合入 `main` 后结束，不启动 P0–P3 功能实施。
