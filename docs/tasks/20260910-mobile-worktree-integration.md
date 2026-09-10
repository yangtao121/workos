# Task: 未提交移动壳改动合入 main

- 状态：done
- Owner/Agent：Codex
- 进程/模块：mobile-shell、仓库构建门禁
- 依赖：当前工作树移动壳改动；main 9a2af49

## 目标与范围

按用户要求提交全部现有未提交改动并合入本地 main，补齐合并所需格式、验证、
模块文档、状态和首次移动壳视觉记录。原生设备能力继续如实保持 scaffolded。
不推送远端。

## 协议/数据影响

不新增 Proto 或 migration；移动壳消费已有 ProjectService。

## 验收

- [x] make generate 无额外生成漂移
- [x] make check 与 make test-mobile-wrappers
- [x] 首次移动壳 before/after/current 视觉证据
- [x] 所有改动已提交并合入 main，当前工作树干净

## 交接

`make test-mobile-wrappers` PASS：单元/类型检查、Web 构建、平台文件检查、
Android sync、Chromium 挂载及确定性 unavailable 场景。
`make generate` PASS：仅按 docs/status.json 渲染 README 状态区块，协议/SQL 无漂移。
最终 `make check` PASS（含 Go vet/test、Proto/SQL、ESLint/Prettier、
TypeScript、单元测试、桌面构建及状态校验）；桌面 144 个测试通过。
初轮暴露移动壳 lint、E2E tsconfig 与 Capacitor 生成资源扫描问题，已修复。
中间一轮 genericcli 取消测试读取已退出进程 /proc/stat 失败，最终完整重跑通过；
本次未改动该既有测试。

视觉证据：[before](../ui/mobile-shell/changes/20260910-mobile-worktree-integration/before/)、
[after](../ui/mobile-shell/changes/20260910-mobile-worktree-integration/after/)、
[current](../ui/mobile-shell/current/)、
[采集说明](../ui/mobile-shell/changes/20260910-mobile-worktree-integration/notes.md)。
首次 client 基线按 UI 约定使用相同初始 before/current。

原生 Android/iOS 二进制、真实配对和安全存储尚未验收；产品仍为 scaffolded。
