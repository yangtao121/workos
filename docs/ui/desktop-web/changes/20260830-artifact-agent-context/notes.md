# 20260830 — Review Artifact 作为 Agent Context（ADR-0010）

- 客户端：desktop-web；viewport：Chromium 1440x900，deviceScaleFactor 1；synthetic fixtures。
- before/：变更前 current 基线（Artifact Center 行无 context 操作、composer 无 context chip、
  viewer 无 pin 操作）。
  - `artifact-center--project-list--1440x900.png`
  - `agent-center--artifact-created--1440x900.png`
  - `artifact-viewer--markdown-review--1440x900.png`
- after/：同一 fixture/viewport 下的新状态。
  - `artifact-center--use-as-context--1440x900.png`：Artifact Center 行内 "Use as Agent
    context" 操作与已 pin 的 "Pinned as Agent context." 状态。
  - `agent-center--context-chip--1440x900.png`：composer 中的可移除 context chip（只显示
    title 与 Markdown/Diff 类型；digest/内部 id 不进 UI）。
- current/：已同步为 after/ 的两张新基线（markdown/diff viewer 与 composer 无 chip 的其余
  表面无像素变化，沿用既有 current 基线）。
- 提交请求携带 exact id+digest（contextRefs）；测试证明 digest/ID 不进 DOM/截图。
