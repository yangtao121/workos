# Project Knowledge Search 视觉证据（2026-09-01）

- 任务：`docs/tasks/20260901-v1-project-knowledge-search.md`
- ADR：`docs/decisions/0013-project-knowledge-search.md`
- 实现依据：`docs/prompts/20260901-next-agent-project-knowledge-search.md`
- 采集命令：`make capture-knowledge-visual`（真实 PostgreSQL + Core + harness-host +
  indexer + Gateway + Chromium；fixed viewport，确定性 fixture，无真实用户内容/
  随机 UUID/时间/credential 入镜）

## Surfaces 与状态

| after/current 文件                          | viewport          | 状态                                                                                            |
| ------------------------------------------- | ----------------- | ----------------------------------------------------------------------------------------------- |
| knowledge-center--results--1440x900.png     | 1440×900 Expanded | Knowledge Center 窗口：固定 fixture 查询命中 "Fake Harness Review Document"，有界 inert excerpt |
| knowledge-center--results--390x844.png      | 390×844 Compact   | Compact 单主内容pane内同一结果 + Use as Agent context 可直接触达                                |
| agent-center--context-chip--1440x900.png    | 1440×900 Expanded | 命中已通过 canonical chip 流程固定为 Agent context（复用既有 4-chip 上限/幂等）                 |
| app-knowledge-search--results--1440x900.png | 1440×900 Expanded | 获得 knowledge.read grant 的 opaque Web Bundle App 内 knowledge.search 命中同一投影             |

## before/ 说明

本批次为 Knowledge Center 首次建立视觉记录。before/ 取自实现前（base commit
`d785414` 时期）已存在的 `current/` 证据：

| before 文件                                 | 复制自                                                                                               |
| ------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| knowledge-center--results--1440x900.png     | current/artifact-center--project-list--1440x900.png（桌面上尚无 Knowledge Center 入口/窗口）         |
| knowledge-center--results--390x844.png      | current/compact--home--390x844.png（compact 首页无 Knowledge Center 入口）                           |
| agent-center--context-chip--1440x900.png    | current/agent-center--context-chip--1440x900.png（chip 流程本身为既有能力，base 时期已存在）         |
| app-knowledge-search--results--1440x900.png | current/app-surface--bridge-result--1440x900.png（App surface 仅有 agent 能力，无 knowledge.search） |

before/after 均来自真实不同状态：before 中无 Knowledge Center 入口/结果/app
knowledge 方法；after 中三者皆真实存在并有门禁（`make test-project-knowledge-search`、
`make test-app-knowledge-search`）与确定性断言支撑。

## 刻意差异（intentional diffs）

- Dock 新增 ✦ "Open Knowledge Center" 按钮；Adaptive home/medium dock 新增
  Knowledge Center 快捷入口。
- Agent composer 的 chip 由 Knowledge Center 的 Use as Agent context 触发，
  语义与既有 Artifact Center 入口一致（duplicate 幂等、≤4 上限）。
- App surface（knowledge.read grant）hello methods 仅含 knowledge.search。

## 同步

after/ 中四个文件已按约定复制到 `docs/ui/desktop-web/current/`（对应文件 hash 相等）。
