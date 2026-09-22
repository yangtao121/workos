# V2 已合并工作树清理

- 状态：in_progress
- 起点：`4786b8b`，主工作目录干净。
- Branch：`chore/v2-merged-worktree-cleanup`；Worktree：`/home/aquatao/workos`。
- 用户要求：核实全部功能已合并，清除旧副本。

## 范围与验收

1. 用祖先关系验证所有本地功能分支均已包含在最终集成树；旧工作树无未提交改动。
2. 将本地 main 快进到完整交付版本；清理三个 V2 工作树和已合并临时分支。
3. 保留已提交源码/协议/测试/截图/验收文档；将旧工作树中被验收记录引用的原始日志
   归档到主目录 ignored tmp，逐文件校验 SHA256，再删除其余构建缓存和测试 fixture。
4. 保留主目录 APK 与仓库外 API key/预算账本/Android SDK；不操作共享开发容器。
5. 最终仅保留主目录工作树和本地 main，工作树干净；不修改远端分支。

仅仓库整理和文档位置更新，无产品代码、协议、UI 或模块状态变化。
相关功能、完整 `make check`、生成一致性及 E2E 证据继承
[最终集成验收](evidence/20260922-v2-final-integration/results.md)。

## 起始核对

三个旧工作树 mobile/native/network 的 `git status --porcelain=v1 --untracked-files=all`
均为空，HEAD 分别为 `28dc6aa`、`2c3eb28`、`49227c9`；所有本地分支对 `4786b8b`
执行 `git merge-base --is-ancestor` 均成功。main 当前 `eaf6c4c`，允许安全快进。
