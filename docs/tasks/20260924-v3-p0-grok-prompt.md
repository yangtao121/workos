# Task: V3 P0 Grok 4.7 实现任务书

- 状态：done（仅提示词文档交付，不实施 P0 功能）
- Owner/Agent：Codex
- 进程/模块：文档；`docs/prompts`、`docs/tasks`
- 分支/worktree：用户明确要求直接修改 `main` / `/home/aquatao/workos`，不创建分支或 worktree。
- 基线：`main@e5e6d5c`，开始时工作树干净。
- 依赖：V3 设计、现有 Native/共享桌面/Runtime 生命周期、相关 Proto、测试及 UI 视觉约定。

## 目标与范围

编写适合 Grok 4.7 执行的中文 P0 任务书，一次只交付“原生体验验证”这一大类。
把内部工作拆成串行工作包，明确产品结果、代码入口、依赖、交付物、自测及停止条件。
Grok 负责实现、自测和交付；用户随后自行安排 Codex 独立测试。
Grok 不调用、安排或等待 Codex，也不把自己的交付等同于 P0 验收通过。

本任务仅新增 Markdown，不实施 Greenfield、VS Code、输入、剪贴板或其他产品功能；
不启动 P1–P3，不调用模型服务，不修改 UI，不自动提交或推送。

## 协议/数据影响

none。本轮不修改 Proto、manifest Schema、migration、公共接口或功能代码。
`docs/status.json` 的功能状态和 evidence 保持不变；生成 README 不应产生内容差异。
本轮不涉及可见 UI，无需制造新的视觉截图；功能任务书仍必须要求后续 UI 证据。

## 验收

- [x] 单份任务书覆盖 P0 产品、七个串行工作包、交付物和失败停止条件。
- [x] 明确直接修改 main；Grok 实现、自测后交接，由用户另行安排 Codex 测试。
- [x] 独立测试清单包含真实 Mac、同实例接续、输入/剪贴板、清晰度及性能证据边界。
- [x] Markdown 格式、本地链接、工作包/验收编号及 `git diff --check` 通过。
- [x] `make generate` 后无生成内容差异，`make check` 通过。
- [x] 交付仅含任务书与本任务记录，功能完成状态不变。

## 验证记录

- 已读 V3 设计、原设计相关章节、当前实现说明、进度事实和已有提示词格式。
- 已读 Native Proto、Native 引擎 port、应用/引擎测试、桌面 Native 实现和浏览器测试。
- 已读 ADR-0029、ADR-0032、ADR-0037、Runtime 生命周期及 UI 视觉约定。
- 已核对 `main@e5e6d5c` 和干净工作树；宿主没有 make/Go/Node，仓库固定 Docker 工具链可用。
- 静态检查通过：W01–W07、A01–A12 无缺号，36 个现有阅读入口和 5 个已有 make target
  存在；本地 Markdown 链接有效。拟新增测试命令和证据路径已明确标为后续功能任务产物。
- 两份 Markdown 经仓库 Prettier 格式化并检查。提示词引用的 Greenfield 两份接口源码
  可从官方 raw 地址读取；VS Code Linux 和 Clipboard API 阅读入口可访问。
- `make generate`：通过，退出码 0；生成后 `git diff --exit-code` 通过，既有 tracked 文件无差异。
- `make check`：通过，退出码 0。包括 Proto/sqlc、Go 格式/vet/测试、TypeScript 架构检查、
  ESLint/Prettier、各 workspace 类型检查/测试、桌面构建及状态生成一致性。
  desktop-web 为 31 个测试文件、213 个测试通过；构建保留已有大于 500 kB 的 chunk 提示。
- 本次没有实施 P0，也没有运行 P0／Mac／图形性能／跨设备产品验收；不把仓库门禁当作
  V3 产品验收。不修改 UI 或采集截图，不调用真实模型。

仓库门禁使用以下命令，分别执行成功（本地日志不纳入版本库）：

```sh
docker run --rm --user "$(id -u):$(id -g)" \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  -v /usr/bin/docker:/usr/bin/docker:ro \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$PWD:$PWD" -w "$PWD" \
  golang:1.26.7-bookworm make generate > tmp/v3-p0-prompt-generate.log 2>&1

docker run --rm --user "$(id -u):$(id -g)" \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  -v /usr/bin/docker:/usr/bin/docker:ro \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$PWD:$PWD" -w "$PWD" \
  golang:1.26.7-bookworm make check > tmp/v3-p0-prompt-check.log 2>&1
```

最终更新仅补充本记录中的验证结果；随后重新检查两份 Markdown 的格式、链接和空白。
`git status --short --branch` 仅列两份新增文档，分支保持 main；未自动提交或推送。

## 交接

交付：[Grok 4.7 V3 P0 执行任务书](../prompts/20260924-grok-4.7-v3-p0-native-experience.md)。
本记录是提示词编写任务；后续 Grok 应另建任务书指定的唯一 P0 功能记录，不覆盖本记录。
P0 功能和 Mac 验收均未在本次文档工作中执行。
