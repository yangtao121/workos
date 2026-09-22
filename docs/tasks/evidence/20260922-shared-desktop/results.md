# 共享桌面自动验收证据

日期：2026-09-22（UTC）。独立 PostgreSQL／六进程 fixture，固定本地 Provider 响应，未调用收费 Provider。

## 已验证的边界

- Core：事务序列化、20 个同 key／20 个独立 key 并发、重启、授权隔离、归档裁剪、游标 reset；Connect／Gateway 流撤权与 race 测试。
- Runtime：真实 PostgreSQL 策略/NULL roundtrip；shell、Xvfb；实际 Docker MANUAL_STOP 运行 1865 秒超过旧 30 分钟限制，创建请求取消后仍活，明确 Stop 回收，bounded 对照真实到期。
- 安装应用：实际容器与 HTTP 程序，legacy 30 秒 idle-stop 对照；MANUAL_STOP 保留进程 nonce、容器 ID、workload 和 Ensure 回执数。第二设备 attach-only，旧代次／不同安装／其他 owner 拒绝；停止后被动恢复不启动程序。
- 会话：提交前事务回执、多个视图不会擦除回执/草稿、未知响应只查原 key、离线不自动发送、卸载/Forget 之后旧 writer 失效、慢刷新合并、流与 cursor 上界。

## 可复现命令

```sh
make generate
make check
make test-shared-desktop
make test-v2-completion
```

独立 Runtime 31 分钟验证使用 `TestManualStopDockerLifetime`（详见 Runtime 任务）；不将缩短 TTL 的测试当成跨越真实旧期限证据。

集成浏览器、服务重启与全仓检查结果正在本轮收尾，最终结果写回此文件后才标记 done。

## 证据位置与限制

Core／Runtime 原始日志已归集到忽略目录 `tmp/shared-desktop-evidence/`；长期可审阅结论和命令保存在本文件及各子任务。UI 固定 viewport 为 1440×900、820×1180、390×844，见共享桌面与会话任务的 before/after/current。

[物理设备清单](physical-devices.md)为 pending。Linux WebKit／Chromium、独立浏览器进程与同宿主容器不代替 Android Chrome、iPhone Safari、iPad Safari 的真机验证。无公网部署、无任意嵌入 App 内部表单/滚动同步、无宿主重启后进程内存恢复声明。
