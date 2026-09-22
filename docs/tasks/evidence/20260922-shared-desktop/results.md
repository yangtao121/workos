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

完整新 namespace 门禁 `WORKOS_V2_SKIP_BUILD=1 sh tools/shared-desktop/gate.sh` 通过，使用本轮已构建的 Go 二进制和 Web dist：

- Chromium10项、WebKit10项，共20 passed，0 skipped／unexpected／flaky（56.6秒）。三独立浏览器进程验证会话输入和完成结果同步、重载/离线草稿与不重复提交；精确终端恢复、窗口操作和确定性视觉同时覆盖。
- 安装应用真实栈37.44秒通过，Core/Gateway实际重启后完整 Desktop 投影相等。
- 自动清理后该 namespace 没有所属应用容器/网络；验收仅使用独立 namespace，未重置默认开发栈数据卷；任务开始时恢复了默认栈停止的依赖服务。
- 对应原始日志：`tmp/shared-desktop-clean-gate.log`、`tmp/v2-completion.gJ04Af/shared-browser-results.json`、`shared-desktop-apps.log`；另已归集到 `tmp/shared-desktop-evidence/integration/`。
- `make generate` 重跑后217个生成文件（Go/TS/SQLC/README）哈希完全一致；Proto lint、SQLC vet、全Go测试/架构约束、TypeScript架构/ESLint/Prettier/各workspace测试和Web构建已分别通过。完整 `make check` 已通过（`tmp/shared-make-check-final.log`），Desktop共213项测试通过；旧V2新环境完整门禁已通过，12项主要跨进程回归及工作区容器/文件测试、4项浏览器与三尺寸视觉通过（32.5秒，0 skipped／unexpected／flaky）。原始日志：`tmp/shared-v2-final-regression.log`，fixture：`tmp/v2-completion.nIxSgM`。独立授权工作树探针仍按既有门禁配置跳过，不将其计入本次新增证据。

浏览器真实栈使用固定loopback开发身份；其他owner/设备拒绝由独立PostgreSQL、Gateway鉴权与安装应用第二设备RPC测试验证。本轮不替代已有生产HTTPS配对专项或真机试用。

## 证据位置与限制

Core／Runtime 原始日志已归集到忽略目录 `tmp/shared-desktop-evidence/`；长期可审阅结论和命令保存在本文件及各子任务。UI 固定 viewport 为 1440×900、820×1180、390×844，见共享桌面与会话任务的 before/after/current。

[物理设备清单](physical-devices.md)为 pending。Linux WebKit／Chromium、独立浏览器进程与同宿主容器不代替 Android Chrome、iPhone Safari、iPad Safari 的真机验证。无公网部署、无任意嵌入 App 内部表单/滚动同步、无宿主重启后进程内存恢复声明。
