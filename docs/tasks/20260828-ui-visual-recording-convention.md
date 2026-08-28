# Task: UI visual recording convention

- 状态：done
- Owner/Agent：Codex
- 进程/模块：repository documentation / UI clients
- 依赖：none

## 目标与范围

建立用户可见 UI 变更的截图留档约定，让后续智能体能够从仓库直接确认当前界面，
并在每次可见 UI 变更中查看同一任务的前后对比。

包含：

- 在根目录 `AGENTS.md` 中增加对所有智能体生效的完成要求。
- 在 `docs/ui/` 中记录目录、命名、采集、隐私和体积约定。
- 明确稳定截图使用 fixture 数据和固定 viewport，不依赖真实外部凭据。

不包含：

- 本任务不改变任何产品 UI。
- 本任务不引入视觉回归测试或截图生成脚本。
- 本任务不采集现有页面截图；首个后续 UI 任务负责建立对应 client 的基线。

## 协议/数据影响

none。无 Proto、event、migration、capability 或运行时数据变更。

## 验收

- [x] `AGENTS.md` 要求所有用户可见 UI 变更同步更新视觉记录。
- [x] `docs/ui/README.md` 定义 `current/` 和任务级 `changes/` 结构。
- [x] 约定包含固定 viewport、确定性 fixture、敏感信息和文件体积限制。
- [x] 文档格式检查通过；`docs/status.json` 无产品状态变化，无需修改。

## 交接

已验证：

- `make check`
- `make generate`
- `git diff --check`

`make generate` 后没有生成文件差异。本任务不改变产品能力或模块成熟度，因此
`docs/status.json` 保持不变。

未决风险：当前没有统一的截图采集命令，也没有已有 `desktop-web` 视觉基线；这是本任务
明确排除的实现范围。首个后续 UI 任务必须按照 `docs/ui/README.md` 从其基准提交采集
`before/`，并建立 `after/` 和 `current/`。后续可单独增加 `make capture-ui` 自动化任务。
