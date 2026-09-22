# V2 最终集成与交付

- 状态：in_progress
- Branch：`feat/v2-final-integration`
- Worktree：`/home/aquatao/workos`
- 起点：`b232993`，工作树干净。
- 范围：按 [已接受执行顺序](20260920-v2-p3-final-matrix.md)，合入原生 goals/skills/subagents、
  LAN/NAT/TURN 与移动浏览器/Android 成果，完成综合回归和设计状态收口。
- 依赖：P3 完整矩阵、原生与网络已完成；移动任务最后归档中。各功能任务保留独立分支，
  已先合入契约再实现 producer/consumer。

## 验收

1. 主工作目录包含所有已验证成果；不覆盖其他任务改动，不操作共享开发栈。
2. 最终集成树 `make generate` 前后无生成差异、`make check` 通过。
3. 在最终源码运行独立六进程 V2 用户链路与固定三尺寸 UI 回归，核对移动、网络与真实模型的专项证据。
4. 更新设计的实现入口、模块事实、任务记录、status 与自动生成 README；保留未验收边界。
5. 验证凭据在仓库外且不被 Git 跟踪；清理本轮自己的测试 namespace，交付可安装 debug APK。

本任务不增加可见 UI；使用移动任务的最终 before/after/current 记录。Docker LAN/NAT、
KVM Android 与真实物理网络/手机明确区分；iOS/后台推送/商店发布不在已接受范围。
真实 DeepSeek 验收已通过且预算内，不为最终集成重复产生付费调用。
