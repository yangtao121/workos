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

## 原生自动目标、项目 Skills 与隔离子 Agent（ADR-0034）

Core 通过 additive SessionDirective 接收目标创建/暂停/恢复，并保存带 ref/revision 的
目标投影。运行中暂停经独立幂等控制请求送达原生 step 边界；原生 goal-round-driver
拥有自动轮次。取消或失租约会撤销自动执行投影，恢复原生日志不自动重跑副作用。
Desktop 会话展示目标、轮数、暂停状态以及子任务的状态和成果差异。

DeepSeek adapter 使用锁定 runtime 的官方 skill registry，只从授权项目 `.dsh/skills`
与 `.agents/skills` 加载；不读宿主 HOME，不启用 watcher，不添加权限。官方原生子
session 不继承父对话；最多同时两个、深度一，不能调用提问、目标创建或再派生工具。
父子请求与自动轮次共同预留 Task 输出预算，未知 usage 阻止继续请求，沿用硬截止时间。

Runtime 为每个 delegation 持久化 scope 和准备回执，以只读项目 HEAD 创建专属 bare
repository 与 Git worktree。拒绝 dirty 项目；子命令只挂载自己的工作树和 Git 元数据，
无网络、无凭据并受既有资源限制。Core 在长操作期间持续复核父租约和项目/工作区授权。
成功结果生成含新增文件的有界 diff；取消/撤权清理运行容器，未知文件效果保留待核对。
父工作区不自动合并；已保存差异可由用户审阅。准备或中断后未生成差异的工作树须由
操作者在 Runtime 的 delegation 存储中核对，不伪报已回滚。

Core Agent 拥有 migration 073，Runtime WorkspaceHost 拥有 074，Core Artifact 拥有 075。
第三项保留普通 Task 的单类型成果限制，并给经 Core 授权的每个子任务独立成果名额。
生产 workspace overlay 在 preview bridge root 下配置独立 delegation 根；未配置或
缺少干净 Git 基线时该次子任务执行明确 unavailable。

完整验收入口 `make test-native-automation` 启动隔离六进程，使用固定模型响应驱动
真实锁定 Harness、Docker worktree、Core 授权、成果审阅与浏览器恢复；包含取消/撤权
回收和三尺寸视觉用例。真实外部模型单独经 Vault 与持久费用预留代理验收。
当前进度及证据见 [任务记录](../tasks/20260920-v2-native-goals-skills-subagents.md)。
