# UI Visual Records

本目录保存用户可见 UI 的版本化视觉证据。它服务于人工审核和后续智能体理解当前界面，
不替代单元测试、E2E 或视觉回归断言。

Playwright 的 `test-results/`、`playwright-report/`、trace 和视频属于临时测试产物，
不得作为本目录的长期视觉记录。

## 目录结构

每个 UI client 独立维护当前基线和任务级前后对比：

```text
docs/ui/
└── <client>/
    ├── current/
    │   └── <surface>--<state>--<width>x<height>.png
    └── changes/
        └── <YYYYMMDD-task-slug>/
            ├── before/
            │   └── <surface>--<state>--<width>x<height>.png
            ├── after/
            │   └── <surface>--<state>--<width>x<height>.png
            └── notes.md
```

当前 Web client 使用 `desktop-web` 作为 `<client>`。新增 client 时使用仓库内稳定、
小写的目录名，不使用智能体名称或临时分支名。

## UI 任务工作流

任何会改变用户可见像素、布局或交互状态的任务都必须执行以下步骤：

1. 开始修改前，查看对应 `current/`；把受影响界面的当前基线复制到本任务的
   `changes/<YYYYMMDD-task-slug>/before/`。如果尚无基线，从任务基准提交运行 UI 并采集。
2. 完成实现和测试后，以相同 fixture、viewport、路由和交互状态采集 `after/`。
3. 用 `after/` 中相同文件更新 `current/`，确保它代表包含本任务的代码状态。
4. 在 `notes.md` 记录任务文件、受影响界面、路由、状态、fixture、viewport、浏览器、
   采集命令和任何有意差异。
5. 在 `docs/tasks/<task>.md` 链接 `before/`、`after/` 和 `notes.md`，记录验证命令。

首次为某个 client 建立视觉记录时，可以让 `before/` 与初始 `current/` 相同；不得以
“没有旧截图”为由省略本任务完成后的 `after/` 和 `current/`。

如果任务只改变不可见实现且渲染结果确实不变，应在任务记录中说明“不涉及可见 UI”，
不需要制造无差异截图。无法启动或稳定复现受影响 UI 时，记录阻塞原因，任务不得标记 done。

## 采集约定

- 优先使用已有 Playwright E2E 和仓库内 fixture 自动截图，避免人工构造不可复现状态。
- 同一组前后截图必须使用相同浏览器、viewport、device scale factor、路由和交互状态。
- `desktop-web` 默认使用 Chromium、`1440x900` viewport 和 `deviceScaleFactor: 1`；
  需要其他尺寸时把尺寸写入文件名和 `notes.md`。
- 只截取完成审核所需的稳定界面。加载动画、随机时间、随机 ID、光标和系统通知应固定或隐藏。
- 文件名采用 `<surface>--<state>--<width>x<height>.png`，例如
  `app-library--installed--1440x900.png`；名称使用小写 kebab-case。
- 默认提交 PNG。单张图片应小于 2 MiB；超过时应裁剪到相关 viewport 或无损优化，
  不得直接提交大型视频、trace 或无关页面长图。

## 安全与内容边界

- 只能使用 fixture、seed 或专门的测试账号数据。
- 禁止出现 API key、token、cookie、Authorization header、provider raw credential、
  真实用户内容或其他敏感信息。
- 禁止为了截图调用真实收费 Provider 或生产外部服务。
- 如果界面包含敏感字段，必须在 fixture 层使用明显的假值；不能仅在截图后手工涂抹。
- 截图是文档证据，不得放进应用静态资源目录，也不得被运行时代码加载。

## 与视觉回归测试的关系

本目录中的图片允许随经过审核的 UI 变更更新，主要供人查看。若增加像素级视觉回归测试，
测试基线应由对应测试框架管理，并与这里的文档截图分开存放；任务记录应分别链接两类证据。
