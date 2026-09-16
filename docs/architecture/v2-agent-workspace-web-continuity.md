# V2 持续开发会话与 Web 应用接续

[任务与验收](../tasks/20260915-v2-agent-workspace-web-continuity.md) ·
[ADR-0032](../decisions/0032-runtime-isolated-development.md) ·
[V2 架构](../structure-v2.md)

## 用户操作

1. 创建 Project。操作员将该项目目录注册到 Runtime；在 Project settings 的 Project
   workspace 选择 Connect workspace。浏览器不能提交宿主任意路径。
2. 在同一设置页选择已配置 Vault 凭据的 DeepSeek Harness。打开 Agent Sessions，新建
   会话并提出开发目标。一次输入对应一个 Task；运行中追加的要求按顺序排队。
3. Files 的 Current files 直接读取同一目录；编辑保存校验 etag 和工作区版本。冲突保留
   草稿供对比。Indexed search 是派生搜索，不代表当前磁盘内容。
4. Terminal 和 Native 打开隔离环境中的相同目录。Development previews 可用已批准
   工具链的启动命令启动真实服务，或打开 Agent 工具已启动的预览。
5. 关闭窗口、刷新或换项目只断开界面。Home 的 Running apps 可发现、打开、Stop 或
   Restart 原程序；预览列表独立提供对应操作。Take control 是显式设备接管。
6. needs review 表示执行失败、被取消或结果未知。先核对文件和工具结果，再建新会话；
   原队列不会自动执行。取消执行与关闭会话分开，历史仍可读取。

原生问题显示在执行窗口中，答案只作用于该次执行，两分钟过期。拒绝与超时不会被解释
为授权。权限升级审批不可用；已有 App 调用 Agent 的 Core 事前审批仍有效。

## 部署

需要 Linux Docker Engine，以及固定 Node/Go/X11 工具链镜像。先构建主镜像和 Runtime
镜像：`make build && make build-workspace-runtime`。操作员目录必须可由指定 Runtime uid
读写；禁止将含模型 key、Docker socket 或宿主敏感文件的目录登记为项目。

```sh
export WORKOS_UID="$(id -u)" WORKOS_GID="$(id -g)"
export WORKOS_DOCKER_GID="$(stat -c %g /var/run/docker.sock)"
export WORKOS_WORKSPACE_ROOTS=/srv/workos/projects
export WORKOS_PREVIEW_BRIDGE_ROOT=/srv/workos/preview-bridges
export WORKOS_NATIVE_X11_HOST_DIRECTORY=/srv/workos/x11
export WORKOS_RUNTIME_CONTAINER_NAMESPACE=workos
mkdir -p "$WORKOS_WORKSPACE_ROOTS" "$WORKOS_PREVIEW_BRIDGE_ROOT" "$WORKOS_NATIVE_X11_HOST_DIRECTORY"
chmod 700 "$WORKOS_PREVIEW_BRIDGE_ROOT"
chmod 1777 "$WORKOS_NATIVE_X11_HOST_DIRECTORY"
# 从 Project 界面取得项目 ID；owner 为部署的单 owner。
export WORKOS_RUNTIME_WORKSPACE_MOUNTS='OWNER_UUID:PROJECT_UUID:/srv/workos/projects/example'
docker compose -f compose.yaml -f deploy/compose.workspace-runtime.yaml up -d --no-deps runtime-host harness-host
```

Core/Gateway/PostgreSQL 应已按现有部署说明初始化；这里不重跑 bootstrap，不改 Vault。
`WORKOS_RUNTIME_WORKSPACE_MOUNTS` 多项用分号分隔，末尾 `:ro` 强制只读。挂载只登记来源，
用户仍须在项目设置中建立 Core binding。Docker daemon 与 Runtime 需要看见相同绝对
目录；Runtime 挂载目录和自己的 X11/bridge，用户容器只得到项目与其专属 socket。
Harness 的原生会话目录用 `workos-harness-sessions` volume 持久化。

DeepSeek 设置 `WORKOS_DEEPSEEK_ENABLED=true`；真实 key 仅通过 `workosctl credential put`
的标准输入进入已有 Vault，不能写 Compose、命令参数、截图或仓库。每轮重新取得任务
凭据租约，App、Terminal、Native 和预览无权读取它。

LAN 显式设置：

```sh
export WORKOS_RUNTIME_NATIVE_CANDIDATES=lan
export WORKOS_NATIVE_LAN_CIDRS=192.168.1.0/24
export WORKOS_NATIVE_UDP_PORTS=52000-52100
```

按实际 LAN 网段修改，并仅对该网段开放配置的 UDP 端口。Runtime 必须使用 host 网络。
Gateway 继续使用既有可信 TLS origin、配对与设备撤销流程；不要将 Core/Runtime 私有
HTTP 端口暴露到 LAN。默认 loopback，无公网/TURN 支持。

## 预览与运行限制

预览命令在 `/workspace` 运行，服务监听 `$PORT`，资源路径使用 `$WORKOS_PREVIEW_BASE`。
依赖须预装；容器不能访问外部网络。Node 示例为 `node preview.cjs`，Vite 示例为
`npm run dev -- --host 127.0.0.1 --port "$PORT" --base "$WORKOS_PREVIEW_BASE"`。
当前代理仅支持普通 HTTP，不支持 WebSocket/HMR；修改后手动刷新或 Restart。

| 对象                | 限制与恢复                                                          |
| ------------------- | ------------------------------------------------------------------- |
| 文件工具            | 文本 256 KiB、受控路径、链接拒绝、etag 原子写；普通 App 仍为 32 KiB |
| 工作区命令          | 全局 4 并发、最长 5 分钟、输出 256 KiB；未知结果不重放              |
| PTY / Native / 预览 | PTY/预览各 4 个、Native 2 个，程序最长 30 分钟，窗口关闭不续期      |
| 容器                | 禁网、只读根、1 GiB、2 CPU、128 pids、256 MiB tmp                   |
| 控制权              | 单设备，显式接管，旧代次输入与 resize 无效                          |
| Native peer         | 30 秒重新鉴权，前端 20 秒刷新；观察者先点击接管                     |
| Runtime 重启        | 清理本部署容器，旧程序转 failed；显式 restart 建立新 generation     |
| Harness 重启        | 已完成轮原生恢复；执行中断转 needs_review，不自动再次执行副作用     |

Core 授权或 workspace revision 改变时停止旧程序；Core 不可达也停止。关闭/重启不是
删除文件。Native 只提供当前控制端屏幕，不承诺多个观察者同步观看；Web 应用自行保存
业务状态。本轮不交付 P3 构建发布/回滚、更多 Provider 或公网部署。

## 验证

`make test-v2-completion` 启动独立六进程 fixture，模拟的只有模型响应。它不启动或重置
共享开发数据库/Vault。Go 容器矩阵和浏览器断言详见任务记录；三尺寸视觉证据在
[UI 记录](../ui/desktop-web/changes/20260916-v2-completion/notes.md)。`make generate` 与
`make check` 是最终一致性门禁。

真实模型验收仅在明确费用额度内、通过 Vault 对新建测试项目执行；物理第二台 LAN
设备不能由两个浏览器 context 代替。当前执行结果以任务 A15/A16 行为准。
