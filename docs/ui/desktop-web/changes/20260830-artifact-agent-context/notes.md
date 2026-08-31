# 20260830 — Review Artifact 作为 Agent Context（ADR-0010）

- 客户端：desktop-web；viewport：Chromium 1440x900，deviceScaleFactor 1；deterministic synthetic
  fixture `Context Visual Fixture`。采集时隐藏不属于本视觉契约的历史 Project cards 与
  server-minted task UUID，避免截图依赖本地数据库先前状态。
- before/：变更前 current 基线（Artifact Center 行无 context 操作、composer 无 context chip、
  以目标状态文件名保存，便于与 after/ 逐张比较）。
  - `artifact-center--use-as-context--1440x900.png`
  - `agent-center--context-chip--1440x900.png`
- after/：同一 fixture/viewport 下的新状态。
  - `artifact-center--use-as-context--1440x900.png`：Artifact Center 行内 "Use as Agent
    context" 操作与已 pin 的 "Pinned as Agent context." 状态。
  - `agent-center--context-chip--1440x900.png`：composer 中的可移除 context chip（只显示
    title 与 Markdown/Diff 类型；digest/内部 id 不进 UI）。
- current/：已同步为 after/ 的两张新基线（markdown/diff viewer 与 composer 无 chip 的其余
  表面无像素变化，沿用既有 current 基线）。
- 采集命令：`make capture-artifact-context-visual`。容器内固定输出到 `/workspace/docs/ui/...`；
  禁止传入 host absolute path，避免在仓库内生成 `home/...` 镜像目录。该门禁在第二张截图前
  关闭 Artifact Center，确保真正展示 composer context chip，而不是重复截取覆盖窗口。
- 提交请求携带 exact id+digest（contextRefs）；测试证明 digest/ID 不进 DOM/截图。
