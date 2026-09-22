# V2 最终集成与交付

- 状态：done
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

## 最终结果

集成提交 `eb69a24` 包含全部确认范围的功能，之后仅补齐文档。
`make generate`（212 文件无差异）、`make check`、从源码重建的完整六进程 V2 gate
全部通过；浏览器 4 passed、无 skipped/flaky，独立 Docker 子工作树探针补跑通过。
详细命令、范围、日志摘要和清理结果见 [最终验收](evidence/20260922-v2-final-integration/results.md)。

- [交付索引](../architecture/v2-delivery-status.md)汇总 P0–P3、原生目标/Skills/子 Agent、
  Docker LAN/NAT/TURN、触控浏览器和 Android 的当前行为与边界。
- [设计文档](../structure-v2.md)保留原始设计时点，已加入当前交付入口；
  status 的 22 个模块均为 working，含各模块明确的证据范围，不扩张为所有未来能力已完成。
- P3、原生、网络与移动专项均有端到端证据；不重复收费调用。原生累计 38 次真实请求，
  保守预留 6.055478 元，小于用户授权上限。
- 不涉及新的可见 UI；移动任务 [桌面](../ui/desktop-web/changes/20260921-v2-mobile-android/notes.md)、
  [Web 壳](../ui/mobile-shell/changes/20260921-v2-mobile-android/notes.md)、
  [Android](../ui/android-shell/changes/20260921-v2-mobile-android/notes.md) 的 before/after/current
  已归档，最终核对 current 与 after 一致。
- 本机普通 Android debug 包：`tmp/deliverables/workos-android-debug.apk`，
  旁边 `SHA256SUMS` 与移动验收摘要一致；APK 和 SDK/cache 不提交 Git。
- 真实 DeepSeek key 只保存在仓库外，权限和全部可达 Git blob 精确扫描通过。
  自有集成 namespace 清空，共享开发栈未操作。

确认范围内无剩余实施项。物理设备/公网、iOS/后台推送、商店签名、更多 Harness、
rootless/memory.high 等未纳入或未验收能力仍按交付索引明确标注，不作为已通过承诺。

2026-09-22 后续整理：全部功能已快进合入本地 main，旧功能分支与工作树已删除。
原始日志归档与保留清单见 [清理记录](20260922-v2-merged-worktree-cleanup.md)；
本页原 Branch/Worktree 与验收目录保持历史执行时点，不再表示仍存在的分支或旧工作树。
