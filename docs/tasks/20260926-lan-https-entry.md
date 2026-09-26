# Task: V3 P0 局域网 HTTPS 入口

- 状态：active（本分支实现与离线验证完成，待密码模式合并后的真实浏览器验收）
- Owner/Agent：Codex（LAN HTTPS 独立工作树）
- 进程/模块：Gateway 部署配置、主机证书工具、部署说明
- 分支/worktree：`feat/lan-https-setup` / `/home/aquatao/workos-lan-https`
- 基线：`dc6aa20`；开工时工作树干净
- 依赖：Gateway 密码模式任务提供 `WORKOS_AUTH_MODE=password` 与 `workosctl auth set-password`

## 目标与范围

为当前主机 IP `192.168.5.5`（可覆盖）提供可重复的 HTTPS :8443 启动入口。
首次运行生成持久本地 CA 和含 IP SAN 的服务端证书，证书临近到期或 IP 变化时仅续签叶证书；
CA 私钥留在主机 owner-only 目录，Gateway 只挂载叶证书和叶私钥。
局域网 Gateway 使用密码模式，关闭免认证 HTTP；PostgreSQL 和可选 OTLP collector 仅监听回环。
不修改 Gateway 认证实现、Proto、migration、Greenfield 或 UI。

## 协议/数据影响

无 Proto、event、migration 或 capability 变更。新增部署环境接口：`WORKOS_LAN_IP`、
`WORKOS_LAN_TLS_DIR`，以及与 Gateway 密码模式共用的 `WORKOS_AUTH_MODE=password`。

## 验收

- [x] 本地证书生成、复用、按 IP 变化续签和权限测试
- [x] Compose 配置断言仅 Gateway HTTPS 对局域网开放；PostgreSQL/OTLP 回环
- [ ] 与密码模式合并后跑真实 Gateway TLS/登录链路
- [ ] `make generate`、`make check` 与文档、`docs/status.json` 同步（集成阶段）

## 交接

- `sh tools/lan/test.sh`：PASS，包含 CA/叶证书、IP SAN 匹配、错误 IP 拒绝、私钥权限、
  续签和 Compose 配置。
- `sh tools/lan/start.sh config`、`git diff --check`：PASS。
- Go 1.26.7 容器运行 `go test ./internal/platform/config ./internal/gateway/...`：PASS，包含旧免认证 LAN 开关失效的回归。
- Prettier 3.6.2 `--check`（改动的 Markdown/YAML）：PASS。
- `workos:dev` 隔离容器以 uid 1000 挂载 mode 0600 叶私钥及 uid 1000 tmpfs：PASS；
  容器内可读叶证书/私钥、可写管理 socket 目录、不可见 CA 私钥。
- 当前主工作树启动的旧 `workos` Compose 栈仍监听 `*:8080` 和 `*:4318`；本分支未重启共享栈，
  以免打断并行任务。合并后必须运行 `./tools/lan/start.sh`、设置密码并复查宿主监听地址。
- 待集成：密码模式 CLI/登录链路、浏览器 CA 信任与多设备测试；`make generate`、`make check`
  和 `docs/status.json` 由集成任务完成。本任务不声称已完成端到端验收。
