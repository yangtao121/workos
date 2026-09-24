# Task: V3 P0 浏览器原生桌面体验

- 状态：in_progress（实验引擎和真实 Code 保活已自测，不是完整 P0 验收）
- Owner/Agent：Grok 4.7
- 进程/模块：runtime-host nativehost、gateway、desktop-web；Core 只保存既有归属与共享桌面引用
- 分支/worktree：用户要求直接修改 `main` / `/home/aquatao/workos`，不创建分支或 worktree
- 基线：任务书编写基线 `e5e6d5c`；实际开工 HEAD `d345fc9732b13e8dffbda6bff72bfe4f1e886f7f`（`main` 比 origin 超前 1，仅含提示词文档提交）。开工时工作树干净。
- 依赖：V3 设计、ADR-0029/0032/0037、现有 Native/共享桌面/生命周期、Greenfield `6c578f4`

不修改提示词任务 [20260924-v3-p0-grok-prompt.md](20260924-v3-p0-grok-prompt.md)。

## 目标与范围

按 [执行任务书](../prompts/20260924-grok-4.7-v3-p0-native-experience.md) 交付接入现有 WorkOS 浏览器桌面的 Greenfield 实验路线：官方 Linux 桌面版 VS Code、输入、纯文本剪贴板、高分屏与同实例接续。默认 Native 引擎保持 Xvfb/WebRTC。不实施 P1–P3，不自动提交或推送。

## 协议/数据影响

实验 ADR 与 surface v1 追加字段/RPC（引擎、显示连接、尺寸/DPR、窗口身份、有界剪贴板）。不复用字段号，不把 Greenfield 连接伪装成 SDP。旧 `ConnectNativeSession` 行为保留。

## 工作包

| 包 | 状态 | 说明 |
| --- | --- | --- |
| W01 | done | 基线、验收表、before 视觉 |
| W02 | partial | 真实 Code 进程保活已测；窗口编辑未通 |
| W03 | partial | ADR-0038、Proto、greenfield 引擎、runtime 开关 |
| W04 | partial | GreenfieldApp 只报告连接状态，未嵌入合成画布 |
| W05 | partial | 引擎内有界剪贴板单测；没有真实双向粘贴 |
| W06 | partial | `make test-v3-p0-native-experience` 已添加 |
| W07 | partial | 运行说明和证据已写，完整门禁未跑 |

## 验收矩阵

旧 Native 证据只覆盖 Xvfb/VP8/WebRTC，不能代替本表。

| 编号 | 场景 | Grok 自测结果 | 证据 | 命令 | 缺项 |
| --- | --- | --- | --- | --- | --- |
| A01 | 固定版本启动真实 Code | PARTIAL | [results.md](evidence/20260924-v3-p0-native-experience/results.md) | launch-code.sh | 未通过 GUI 改写并保存 fixture |
| A02 | 多窗口、菜单和弹窗 | NOT_RUN | | | 浏览器合成器未编入 |
| A03 | 尺寸/DPR/清晰度 | NOT_RUN | | | 无画面 |
| A04 | 输入法和快捷键 | NOT_RUN | | | |
| A05 | 双向文本剪贴板 | PARTIAL | greenfield engine_test.go | go test | 只覆盖引擎内存边界，不是系统剪贴板 |
| A06 | 关闭所有客户端后恢复 | PARTIAL | results.md | launch-code.sh | 同一 pid 仍在；未证明未保存缓冲 |
| A07 | 断网、换设备与控制接管 | NOT_RUN | | | |
| A08 | Stop、退出、故障、Restart | PARTIAL | engine_test.go | go test | Stop 结束测试子进程；不是真实 Code |
| A09 | 授权、不可用和失败 | PARTIAL | engine_test.go | go test | 缺二进制和 SDP 伪装已拒绝 |
| A10 | 交互性能与资源 | NOT_RUN | | | |
| A11 | Mac 与物理客户端 | NOT_RUN | | | 本环境无 macOS / 第二台物理设备 |
| A12 | 回归与交付完整性 | NOT_RUN | | | make check 未作为本轮结果 |

## 验证记录

### W01 环境（2026-09-24）

- HEAD `d345fc9732b13e8dffbda6bff72bfe4f1e886f7f`，分支 `main`，工作树干净。
- 宿主没有 `make`、`go`、`node`；有 Docker。本地已有 `golang:1.26.7-bookworm`、`node:24.19.0-bookworm-slim`、`workos-native-runtime:dev`。
- GitHub 使用镜像 `https://ghfast.top/https://github.com/...`。固定源码包
  `greenfield-6c578f4db7ec027eb1d8a5f7ec6e09f7646dbb57.tar.gz` 已从该镜像下载，约 19 MiB。
- 受影响 Native 视觉基线 20 张已复制到
  [before/](../ui/desktop-web/changes/20260924-v3-p0-native-experience/before/)。
  来源是 `docs/ui/desktop-web/current/` 的 `native-window--*` 与 `agent-session-window--native-*`。
- 直接用 `golang:1.26.7-bookworm` 且不带模块缓存时，`go test ./internal/runtime/nativehost/...`
  失败，退出码 1：`proxy.golang.org` 连接超时，不是测试断言失败。
- 改用仓库既有 `workos-go-cache` 与 `GOPROXY=https://goproxy.cn,direct` 后同一命令通过，
  退出码 0：postgres、turnauth、xvfbengine、application 均为 ok。这些结果只覆盖现有
  Xvfb/WebRTC 路线，不代表 Greenfield。

## 交接

进行中。
