# ADR-0024：App 构建输入与候选发布边界

- 状态：Accepted
- 日期：2026-09-08
- 关系：补齐 ADR-0016 的 Build/Test 输入，衔接 ADR-0022 的入队目标。

镜像与故障摘要不足以重建 App。Core App Registry 新增 owner-scoped、不可变的源码包，
由版本化 Proto 提交普通文件清单，不接受 ZIP/TAR、链接或宿主路径。清单经路径、体积和
文件/目录冲突检查后排序，摘要覆盖路径、内容与执行位；同请求键重放首个 ID/摘要/时间，
不同内容返回 Aborted。源码包最多 128 个文件、总计 512 KiB、单文件 256 KiB，创建和
读取不经过 App Bridge，也不授予 App 文件系统或凭据访问能力。

Manifest 的可选 `build` 配置以既有版本化 JSON Schema 为唯一语法来源，固定源码包
ID/digest、不可变工具链镜像及 build/test argv。Registry 在注册时验证源码包属于该 owner
且摘要完全一致。未提供此配置的版本明确不能自动构建；不猜测旧 maintainer.sourceRef 或
maintainer.tests 的语义，也不从已安装镜像倒推源码。配置变化产生新 manifest digest。

后续 Repair 候选只能提议新的普通文件内容。Core 保留入队时的固定构建/测试配置，Runtime
拥有构建执行和资源限制，Reliability 只编排。候选不能选择自己的测试命令、Containerfile、
网络或挂载权限。构建和测试都成功后，Core 才能产生不可变候选版本；候选尚未通过 canary
时不能成为 Registry 默认版本。权限、凭据、资源提升和数据迁移不属于自动发布范围。

Runtime 的具体 rootless 构建、候选凭证和恢复治理将分别以后续实现/门禁证明；源码包 API
本身不宣称 Build/Test 已完成。执行器必须关闭构建网络并禁止隐式拉取；Podman 官方说明
区分 RUN 的网络隔离与镜像拉取策略，两者不能互相代替。
参考：[Podman build](https://docs.podman.io/en/stable/markdown/podman-build.1.html)。
