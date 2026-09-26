# Task: 局域网免认证开发访问（dev-lan）

- 状态：done
- Owner/Agent：ZCode（feat/lan-dev-access）
- 进程/模块：workos-gateway、internal/platform/config、compose 开发环境
- 依赖：无

## 目标与范围

包含：

- 新增显式开发开关 `auth.dev_bypass_allow_lan` / `WORKOS_DEV_AUTH_BYPASS_ALLOW_LAN`（默认关闭）：
  仅当 DevBypass 开启且该开关为 true 时，允许 gateway 绑定非 loopback 地址（如
  `0.0.0.0:8080`）并以纯 HTTP 服务——局域网设备免认证访问开发环境。
- `deploy/compose.dev-lan.yaml` 开发 overlay 与 `make dev-lan` 目标。
- `apps/desktop-web` 新增 `dev:lan` 脚本（Vite 绑定 0.0.0.0，供局域网热更新调试）。

不包含：

- 生产暴露路径的任何变化：DevBypass=false 时校验完全不变（TLS + canonical
  origin + 设备配对），生产暴露仍走 lan-pairing profile / systemd :443。
- 防火墙/安全组配置（主机外部依赖）。

## 协议/数据影响

none（无 Proto、event、migration、capability 变更）。

## 验收

- [x] `internal/platform/config` 单元测试：不开开关仍拒绝公网绑定；开开关 +
      DevBypass + `0.0.0.0` 无 TLS 通过；仅开开关（DevBypass=false）仍拒绝；
      环境变量解析覆盖。
- [x] `make check`（本机未安装 make，已逐项执行 Makefile 等效 docker 命令）：
      proto-check（buf format/lint + sqlc vet）、go-check（gofmt + vet + 全部
      `go test ./...`）、web-check（architecture + eslint + prettier + 全部
      workspace check + desktop-web build + status render --check）全部通过。
- [x] `docker compose -f compose.yaml -f deploy/compose.dev-lan.yaml up -d --build`
      启动后 `curl http://127.0.0.1:8080/` 返回 SPA（HTTP 200）。
- [x] 局域网 IP `curl http://192.168.5.5:8080/` 返回 SPA（HTTP 200）；经 gateway
      POST `/workos.project.v1.ProjectService/ListProjects` 免认证成功，响应中
      owner 为固定注入的 `0198d7ea-2110-7c42-b659-c5e4d73bc337`。
- [x] 容器内生效环境变量确认：`WORKOS_HTTP_ADDRESS=0.0.0.0:8080`、
      `WORKOS_DEV_AUTH_BYPASS_ALLOW_LAN=true`；gateway 监听 `*:8080`，日志无
      校验报错。

## 安全说明

开关开启后，局域网内任何设备都以固定 owner 身份获得完整无认证访问，流量为明文
HTTP。仅限可信开发局域网使用；默认关闭，所有既有 fail-closed 行为不变。

注意：compose 栈使用 host 网络，`postgres`（pgvector 镜像默认行为）同样监听
`0.0.0.0:5432`（开发凭据 workos/workos）。这是既有行为，与本任务无关；如需
收紧可在 compose 中给 postgres 加 `command: postgres -c listen_addresses=127.0.0.1`。

## 附带修复（既有失败，非本任务引入）

main 分支上一提交遗留的 `make check` 失败，已在分支内修复（均为机械性修复，
无行为变化）：

- `cmd/runtime-host/main.go`、`internal/runtime/nativehost/domain/session.go`：
  gofmt（import 排序、var 对齐）。
- `internal/runtime/nativehost/adapters/greenfield/proxy.go`：删除
  `req.URL.RawQuery` 自赋值死代码（go vet 报错）。
- `apps/desktop-web/src/main.tsx`：去掉两处非空断言（`?? 0` 等价替换）。
- `apps/desktop-web/src/GreenfieldApp.tsx`：取消守卫改为闭包内读取（TS 流分析
  误报恒真）、两处 void 箭头简写加花括号。
- `apps/desktop-web/src/GreenfieldApp.test.tsx`：三处 void 箭头简写加花括号。
- `docs/tasks/20260924-v3-p0-native-experience.md`、
  `docs/tasks/evidence/20260924-v3-p0-native-experience/versions.md`：prettier。

## 交接

已验证命令与结果：

- `go test ./internal/platform/config/` → ok（新增 4 个用例全过）。
- buf format --diff --exit-code / buf lint / sqlc vet → 通过。
- `gofmt -l cmd internal tests` → 空；`go vet ./...`、`go test ./...` → 通过。
- eslint . / prettier --check . / `pnpm -r check` / desktop-web build /
  `node tools/status/render.mjs --check` → 通过。
- `docker compose -f compose.yaml -f deploy/compose.dev-lan.yaml up -d --build`
  → postgres healthy、bootstrap 完成、六进程全部 Up（修复了之前 postgres 停止
  导致的 core/runtime/reliability/indexer 崩溃重启循环）。
- `curl http://127.0.0.1:8080/` 与 `curl http://192.168.5.5:8080/` → 200 SPA。
- 免认证 Connect RPC（ListProjects）经 192.168.5.5 成功返回真实数据。

未决风险：

- 局域网可达性依赖主机所在网络放行 8080（本机 ens18 = 192.168.5.5/24）。
- 本机未安装 GNU Make（README 前置条件要求 4+，无 sudo 无法安装）；本次用
  Makefile 等效 docker 命令执行，建议装 make 后用 `make dev-lan` 日常操作。
- compose host 网络下 postgres 监听 0.0.0.0:5432（见安全说明）。

下一步（可选）：

- 已合并至 main（04851a5，fast-forward）；日常局域网调试用 `make dev-lan`。
  如需 TLS 的局域网暴露，走既有 lan-pairing profile（ADR-0007）。
