# 共享桌面：Runtime 主动停止生命周期

- 状态：进行中
- 分支：feat/shared-desktop-runtime
- 依赖：bca25f8 生命周期协议；ADR-0037
- 范围：PTY、Native、Preview 的真实进程生命周期、持久策略与幂等重启；连接/媒体期限保持独立。
- 验收：旧调用保持 bounded；显式 manual-stop 无时间到期；重启策略漂移拒绝；停止、撤权、配额和宿主重启真实状态；单元、真实 PostgreSQL 与容器引擎验证。
- UI 与全链路证据由共享桌面主任务集成。

## 已实现

- PTY／Native／Preview 的显式 MANUAL_STOP、SQL NULL expiry、实际容器／Xvfb／shell 生命周期；旧调用维持 bounded。
- 安装应用持久 MANUAL_STOP，跳过无窗口空闲回收；主动停止／重启、generation 和策略漂移回执。
- 精确 `GetSurfaceWorkload` 与安装应用运行列表；App `attach_only` 核对 owner／project／installation／version／generation，不启动替代程序。
- PTY／Native attach generation 前置条件；终态、自然进程退出、权限、控制租约及资源限额保持真实。
- [模块文档](../architecture/runtime-lifecycle.md)。UI／全链路由[主任务](20260922-shared-desktop.md)验收。

## 已验证

- Go 1.26.7 容器：`go vet ./... && go test ./...` 通过，见 `tmp/shared-desktop-runtime/lifecycle-go-all.log`。
- 独立 PostgreSQL 18（`workos-lifecycle-pg`，非开发栈）：真实迁移、策略／NULL roundtrip、模拟一小时后 bounded 回收／manual 仍活、策略漂移与重启回执、真实 PTY shell 输入／退出、安装应用策略持久化；Surface attachment／控制状态机回归通过。见 `tmp/shared-desktop-runtime/lifecycle-postgres.log`。
- `workos-native-runtime:dev` 执行 Xvfb 编译测试：实际 manual display 无 deadline，请求取消后继续，主动停止清理；旧 bounded display 仍有 30 分钟 deadline。见 `tmp/shared-desktop-runtime/lifecycle-native-engine.log`。
- SQLC 1.30.0 vet 通过，重新生成 Runtime SQLC 文件无变化；`git diff --check` 通过。

## 待集成

- 真实 Docker MANUAL_STOP 31 分钟门禁正在独立运行；完整结果后续补入本记录。
- 主任务负责完整 `make generate`／`make check`、多设备 E2E 和 UI 截图；本子任务不单独宣称整个共享桌面已验收。
- 未改动共享部署栈；未读取或调用任何真实 Provider 凭据。
