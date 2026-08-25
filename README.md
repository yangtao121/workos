# WorkOS

WorkOS 是运行在 Linux 之上的个人工作操作环境。人、Agent 与长期运行的软件围绕
Project 协作；Harness、App、Surface、Workload 与 Incident 均通过稳定协议接入。

> 当前处于 foundation 阶段。产品愿景见 [docs/structure.md](docs/structure.md)，当前可运行
> 能力以本文的状态表和 [docs/status.json](docs/status.json) 为准。

## 当前状态

<!-- status:start -->

最后更新：2026-08-25

<!-- prettier-ignore -->
| 模块 | 进程 | 状态 | 证据 |
| --- | --- | --- | --- |
| Access Gateway | workos-gateway | `scaffolded` | health/config boundary |
| Project | workos-core | `working` | revision-safe server-preset binding integration + browser E2E |
| Harness Provider Catalog | workos-core | `working` | public Catalog integration + default/DeepSeek fixture browser E2E |
| Event Backbone | workos-core | `working` | persisted ordered stream + resume integration |
| Agent Task Router | workos-core | `working` | Project binding snapshot + idempotency integration |
| Harness Broker | harness-host | `working` | Fake, Generic CLI, and typed provider execution tests |
| DeepSeek Harness Adapter | harness-host | `working` | official runtime + keyless streaming fixture integration and browser E2E |
| Desktop Shell | desktop-web | `working` | foundation + DeepSeek fixture Catalog/binding E2E + App Library install/remove E2E |
| App Registry | workos-core | `working` | schema-backed immutable registration + durable idempotency + bounded paging/read + credential-shaped key rejection + restart persistence |
| Project App Installation | workos-core | `working` | pinned version install/uninstall + revision/event/outbox transaction + durable idempotency + restart persistence + Desktop App Library browser E2E |
| Artifact | workos-core | `contract-only` | workos.artifact.v1 |
| Runtime / Surface | runtime-host | `scaffolded` | capability probe; runners unavailable |
| Reliability | reliability-host | `scaffolded` | health; enforcement unavailable |
| Indexer | indexer | `scaffolded` | health; indexing unavailable |
| Mobile Shell | mobile-shell | `contract-only` | device-class contract |

<!-- status:end -->

状态含义：`contract-only` 只有稳定契约；`scaffolded` 可构建和探活；`working` 有真实链路；
`verified` 具有持续运行的集成/E2E 证据。任何占位实现都不能标记为 working。

## 架构

```text
Desktop / SDK
      │
workos-gateway ── identity / capability / public API
      │
workos-core ───── Project / Agent API / Event Backbone
      ├────────── harness-host
      ├────────── runtime-host
      ├────────── reliability-host
      └────────── indexer
                    │
                PostgreSQL
```

六个宿主是稳定进程边界，不能为了方便把它们合并成单进程。详细所有权与依赖规则见
[当前实现架构](docs/architecture/implementation.md)。

## 快速开始

前置条件：Docker 29+、GNU Make 4+。本机不需要预装 Go、Node、Buf 或 PostgreSQL。
Go、npm/pnpm、Debian 与 Playwright 下载默认使用国内镜像，且均可通过环境变量覆盖；详见
[部署说明](deploy/README.md)。

```bash
make bootstrap
make generate
make dev
```

打开 <http://127.0.0.1:8080>。开发模式仅允许绑定 loopback，并注入固定的 owner/device
身份；它不能用于局域网部署。

常用命令：

```bash
make check             # 与 CI 相同的静态检查和单元测试
make test-integration  # PostgreSQL、事件与进程集成测试
make test-deepseek-fixture # 官方 DeepSeek Harness + 本地无密钥 API fixture
make test-e2e          # Desktop → Project → Agent Task 浏览器链路
make logs              # 查看开发栈日志
make down              # 停止开发栈
```

## 配置与运行

- 默认开发配置在 `deploy/config/dev.yaml`，可复制后通过 `WORKOS_CONFIG_FILE` 指定。
- Secret 只能通过环境变量或受限文件引用注入，禁止写入 YAML、日志和 App 响应。
- `deploy/systemd` 提供本机服务管理骨架；设备注册完成前仍只允许 loopback 部署。
- 自定义 workload 的 rootless Podman runner 尚未实现，Runtime 目前只做只读 capability probe。
- `workosctl doctor` 检查配置、PostgreSQL、cgroup v2 和内部服务连接。
- 设置 `OTEL_EXPORTER_OTLP_ENDPOINT` 即可启用六个进程的 OTLP/HTTP trace；示例见
  [部署说明](deploy/README.md)。

### Project Harness 设置

Desktop 的 Project settings 从 Gateway 查询 canonical Provider Catalog，并显示 provider stable ID、
adapter version、真实 health 与 capability。浏览器只调用 Core 的只读 `HarnessCatalogService`；包含
`ExecuteTask` / `CancelRun` 的 `HarnessHostService` 仍是进程间私有接口，Gateway 不转发它。

Project 可以选择 healthy 或 degraded provider，也可以清除 binding 以使用 Core 的 global default。
starting、unavailable 与 unknown provider 不允许新绑定；已经存在但后来不可用或不再出现在 Catalog
中的 binding 仍会显示并可解除。保存使用 Project revision 做乐观并发控制，Task 页面显示 Submit 时
持久化的 `provider_id` 快照，Project 后续改绑不会改变旧 Task。

客户端不提供 Key、credential ref 或内部 policy 输入。Core 为新 binding 注入非 secret 的
`instance_policy` / `profile_id` / `resource_policy_id` preset；当前 resource policy 只是 binding reference，
尚不代表 cgroup 或其他资源强制已经实现。Catalog 是可选能力，harness-host 暂时不可达不会影响
Project CRUD、Core readiness 或已有 Task。

### DeepSeek Harness

DeepSeek Provider 默认关闭；有 Key 也不会隐式启用。启用时只从
`DEEPSEEK_API_KEY` 读取凭据，非 secret 参数使用 `WORKOS_DEEPSEEK_*` 环境变量。
容器固定并校验官方 Harness runtime，常规 CI 只运行本地 fixture，不访问 DeepSeek 网络。
`make test-deepseek-fixture` 还会从浏览器选择 DeepSeek、验证 Project revision、Task provider snapshot、
ordered events 与改绑语义；它只使用 loopback API fixture 和明确的假 credential。
配置、输入限制、测试方法和真实 API smoke 的限制见
[adapter 说明](internal/harness/adapters/deepseek/README.md)。

## 仓库地图

| 路径                       | 责任                                       |
| -------------------------- | ------------------------------------------ |
| `api/proto`                | 跨进程、Go/TypeScript 的 v1 事实契约       |
| `internal/core`            | Project、Catalog、binding、Task 与事件主干 |
| `internal/harness`         | Broker 与 provider adapters                |
| `internal/platform`        | identity、配置、迁移、日志、遥测等共享机制 |
| `apps` / `clients` / `sdk` | Shell、可复用客户端模块与公共 SDK          |
| `deploy`                   | Compose、OTel 与 systemd 资产              |
| `docs/tasks`               | 可跨智能体交接的任务事实记录               |

新增业务模块时不要手抄目录：

```bash
make scaffold-module PROCESS=workos-core NAME=calendar
```

脚手架只建立 `domain/application/ports/adapters/transport` 边界，不会生成虚假的业务实现，也不会
覆盖已有目录。

## 参与开发

开始任何工作前必须阅读 [AGENTS.md](AGENTS.md) 与
[CONTRIBUTING.md](CONTRIBUTING.md)。新任务从 [任务模板](docs/tasks/TEMPLATE.md) 建立独立
记录；改变进程边界、模块所有权或 v1 协议必须新增 ADR。

本项目使用 [Apache License 2.0](LICENSE)。
