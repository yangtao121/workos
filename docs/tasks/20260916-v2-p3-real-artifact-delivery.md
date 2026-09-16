# V2 P3 真实产物、运行版本与回滚（C00–C10）

- 范围：[任务书](../prompts/20260916-glm-5.3-v2-p3-real-delivery-goal.md) 的 P3 小型单机 App 发布链；六进程边界不变。
- 基线：本地 main `3820447`（2026-09-16），任务分支 `feat/v2-p3-real-artifact-delivery`。
- 目标：`app-bundle.v1` 不可变发布包 → Core 精确绑定 → Runtime Docker 正式 Workload →
  Reliability 凭真实运行事实 canary/发布/回滚 → 用户经正式 Surface 看到 A→B→A 真实 HTTP 行为。
- 决策：[ADR-0033](../decisions/0033-app-bundle-v1-artifact-delivery.md)。
- 上轮记录：[P0–P2 最终记录](20260915-v2-agent-workspace-web-continuity.md)（A16 仍缺第二台物理设备，P3 不借该缺口宣称完成）。

## 工作包

| 包                     | 状态 | 依赖       | 实现/验收入口                                                       | 验收                              | 结果与未决项 |
| ---------------------- | ---- | ---------- | ------------------------------------------------------------------- | --------------------------------- | ------------ |
| C00 宿主探针           | done | 无         | tools/v2-p3-delivery/probe.sh                                       | A01                               | 20/20 检查通过；矩阵见下 |
| C01 契约冻结           | done | C00        | ADR-0033、manifest Schema、Proto、迁移 066–068                      | A02                               | 生成一致、buf breaking 无破坏、go build/vet/test 通过 |
| C02 包仓库与导入       | todo | C01        | internal/runtime/artifactstore/、workosctl import、admin socket     | A03                               |              |
| C03 构建输出冻结       | todo | C01、C02   | internal/runtime/buildtest/adapters/dockerbuild/                    | A05、A06、A08                     |              |
| C04 Core 版本绑定      | todo | C01–C03    | appregistry staging/resolver、Runtime 反查                          | A04、A07                          |              |
| C05 正式 Workload      | todo | C00–C04    | internal/runtime/workload/adapters/dockerapp/                       | A09、A10、A16                     |              |
| C06 发布与回滚裁决     | todo | C04、C05   | internal/reliability/application/、deployment transport             | A11、A12、A13、A15                |              |
| C07 用户可见状态       | todo | C04–C06    | apps/desktop-web/src/VersionDialog.tsx、ReleaseService 投影         | A18                               |              |
| C08 回归矩阵           | todo | C02–C07    | tests/integration + 门禁矩阵                                        | A06、A08、A14、A15、A16、A17      |              |
| C09 P3 门禁            | todo | C00–C08    | tools/v2-p3-delivery/、make test-v2-p3-delivery                     | A19、A04、A09–A13                 |              |
| C10 文档与交付         | todo | C09        | docs/、status.json、make check                                      | A20                               |              |

## 验收编号

| 编号 | 验收事实                                 | 当前结果 | 实际证据（命令/路径/摘要） |
| ---- | ---------------------------------------- | -------- | -------------------------- |
| A01  | 可行正式 runner 与安全能力矩阵           | PASS      | [c00-probe.txt](evidence/20260916-v2-p3-delivery/c00-probe.txt)：20 项检查通过 |
| A02  | Schema/Proto/ADR 向前兼容，生成一致      | PASS      | `make proto-check` 通过；`buf breaking <current> --against <main 基线>` 0 差异；迁移 066–068 仅增量（build_jobs 加列、state CHECK 扩展）；`make generate` 后仅预期生成物变化 |
| A03  | Runtime 导入与包保存真实持久             | todo     |                            |
| A04  | 旧版 A 经正式安装/Surface 运行           | todo     |                            |
| A05  | build/test/output 单 job 绑定            | todo     |                            |
| A06  | 构建/测试/输出失败零发布                 | todo     |                            |
| A07  | Core staged manifest 绑定 B 包           | todo     |                            |
| A08  | 重放与并发安全                           | todo     |                            |
| A09  | B 使用正式 runner 启动                   | todo     |                            |
| A10  | 网关到浏览器真实服务 B                   | todo     |                            |
| A11  | canary 只有健康、身份相符才发布          | todo     |                            |
| A12  | B 故障恢复旧 A                           | todo     |                            |
| A13  | 人工 rollback 恢复旧 A                   | todo     |                            |
| A14  | 崩溃/重启恢复                            | todo     |                            |
| A15  | 用户换版/撤权/卸载拒绝旧流程             | todo     |                            |
| A16  | 镜像/包损坏和安全边界拒绝                | todo     |                            |
| A17  | 新旧链路回归                             | todo     |                            |
| A18  | 浏览器交互与三尺寸视觉证据               | todo     |                            |
| A19  | 六进程新门禁隔离与清理                   | todo     |                            |
| A20  | 最终仓库一致                             | todo     |                            |

## 环境基线（2026-09-16 实测）

- 宿主 Linux 7.0.0-31-generic x64，cgroup v2（`/sys/fs/cgroup` 为 cgroup2fs）。
- Docker 客户端存在（`/usr/bin/docker`），`/var/run/docker.sock` 存在；当前用户在 `docker` 组。
- 无 Podman。P0–P2 的 rootless 缺口保持不变；P3 Docker profile 不声称 rootless。

## C00 能力矩阵（2026-09-16 实测，ADR-0033 的 profile 依据）

复现：`sh tools/v2-p3-delivery/probe.sh`；证据 [c00-probe.txt](evidence/20260916-v2-p3-delivery/c00-probe.txt)。

| 能力 | 结果 | 实测事实 |
| --- | --- | --- |
| 引擎/内核 | 可用 | docker 29.7.2（client/server），kernel 7.0.0-31-generic，cgroup v2（cgroup2fs），overlayfs |
| 镜像固定 digest | 可用 | `golang@sha256:e8c859f5…`；容器 inspect 的 `.Image` 返回同一 digest，可在启动后核对 |
| 禁止隐式拉取 | 可用 | `--pull=never` + 不存在镜像 → 明确失败 |
| 网络隔离 | 可用（经 internal 网络） | `--internal` 网络：容器出口内核不可达（`Network is unreachable`）、外部 DNS 失败；**internal 网络不支持端口发布**（`-p` 被静默忽略） |
| App 可达性模型 | 容器桥 IP | 宿主网络命名空间可直连容器桥 IP（on-link 路由）；无 DNAT/FORWARD，外部主机不可达。runtime-host 必须与宿主同网络命名空间（或加入该网络） |
| 只读包挂载 | 可用 | bind mount `/app:ro`，容器内写被拒；bundle A/B 内容不同 → HTTP 响应不同（`WORKOS-P3-PROBE-A` ≠ `WORKOS-P3-PROBE-B`） |
| memory.max / pids.max / cpu.max | 可用 | 读回 268435456 / 64 / `150000 100000` |
| memory.high（软高水位） | **不可用** | `--memory-reservation` 不映射到 memory.high（读回 `max`）；Docker profile 如实声明 unavailable，不冒充 Podman profile 的 memory.high |
| cap-drop ALL / no-new-privileges / 只读根 | 可用 | CapEff=0、NoNewPrivs=1、根文件系统写入被拒 |
| 重启后容器归属识别 | 可用 | 按 `workos.owner`+probe 标签，新客户端可精确列出自己容器 |
| 容器内构建 | 可用 | `--network none` + GOPROXY=off + 挂载工作区，固定镜像内离线 `go build` 成功（C03 基础） |
| Docker rootless | 不声称 | rootful Docker；与 Podman/rootless profile 分开声明 |

## 执行记录

（按包追加；每包写实际命令、结果、证据路径、风险与下一步。）
