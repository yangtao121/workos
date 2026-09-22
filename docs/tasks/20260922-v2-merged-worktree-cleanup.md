# V2 已合并工作树清理

- 状态：done
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

## 清理结果

- 本地 main 已从 `eaf6c4c` 快进到 `4786b8b`，覆盖全部确认功能；清理文档再通过快进合入。
- 删除三个旧工作树：`workos-v2-mobile`、`workos-v2-native`、`workos-v2-network`；
  每个删除前再次确认状态干净、HEAD 为 main 祖先，且无运行中容器或进程依赖其路径。
- 删除六个已合并功能分支：`feat/v2-mobile-android`、`feat/v2-native-goals-skills-subagents`、
  `feat/v2-network-continuity`、`feat/v2-p3-closeout`、`feat/v2-p3-final-matrix`、`feat/v2-final-integration`。
  使用 `git branch -d`，没有强制删除独有提交；本清理分支在文档合入 main 后同样删除。
- 旧工作树包含约 10.8 GiB 数据，已清理其依赖、编译产物、模拟器镜像副本和一次性测试目录。
  仅处理这三个指定旧目录，没有清空主目录 tmp 或共享开发栈。
- 验收引用的 28 个现有根目录日志/记录，共 237873 字节，按原字节保存在
  `tmp/archived-worktree-evidence/20260922/<旧工作树>/tmp/`。逐文件复制前后 SHA256 一致；
  `manifest.json` 记录原位置、归档位置、长度和摘要。归档为 ignored，本地目录 0700/文件 0600。
- 已提交的源码、协议、测试、截图、证据保留完整；交付 APK 校验摘要一致，仓库外 key
  权限仍为 0600；预算账本和 Android SDK/cache 保留。没有删除或推送远端分支。

归档分布：

- `workos-v2-mobile`：9 个文件。
- `workos-v2-native`：12 个文件。
- `workos-v2-network`：7 个文件。

`manifest.json` SHA256：`bb1079d8c8a4873fdb91df47a050a2ffd739962581dad17a3ac0daa9325a3c12`。

## 最终验证

- 清理后 `make generate` 前后 212 个生成文件哈希一致；`make check` 全部通过，
  包括 Go/Proto/sqlc、13 个 TS workspace 架构、lint/format/typecheck/unit/build/status。
  Desktop 173 tests、Mobile 12 tests；工作树移除没有破坏主目录构建依赖。
- 与已验收集成提交相比，产品代码无差异，只有清理记录及历史日志位置说明；
  docs/status.json 的模块状态和 UI 截图无需变更。
- 本清理文档提交后 main 快进包含清理记录，临时清理分支删除；最终只保留
  `/home/aquatao/workos` 工作树与本地 main，状态干净。远端未修改。

本机检查日志 SHA256：

- `tmp/v2-cleanup-generate.log`：`049add0d0f4de83f2fa1188381288b266a93615ec67a8d9e603f2bc1d7e79ee0`
- `tmp/v2-cleanup-make-check.log`：`2dc7458ef300c2ec6b02619a1b698759ebddc4d72acf486a7db4d8af61e476d4`
