# Native 网络连接

本模块采用 [ADR-0035](../decisions/0035-native-relay-connectivity.md)。当前验收进展以
[网络任务](../tasks/20260921-v2-network-continuity.md) 为准；不将 Docker 模拟拓扑等同物理设备或公网。

Runtime 保持 Native Workload 的程序生命周期。Gateway 继续终止 HTTPS、验证设备会话，
并转发 owner/device 身份。创建 peer 前，Desktop 用当前 session/control generation
请求 GetNativeConnectivity。Runtime 重新检查活动程序、工作区权限和当前控制权，
响应 `Cache-Control: no-store` 的短期连接配置。旧设备、旧 epoch、关闭/撤权后的程序
无法取得新凭证。Capability 只允许有界中继分配，不包含静态密钥，不授予程序控制权。

三种部署模式均由 `WORKOS_RUNTIME_NATIVE_CANDIDATES` 指定：

| 模式             | 网络策略                                 | 配置                                                               |
| ---------------- | ---------------------------------------- | ------------------------------------------------------------------ |
| loopback（默认） | 仅回环 host candidates                   | 不接入 TURN                                                        |
| lan              | 私有接口 host candidates                 | `WORKOS_NATIVE_LAN_CIDRS` 与 `WORKOS_NATIVE_UDP_PORTS` 必填        |
| relay            | 浏览器与 Pion 强制 TURN relay candidates | `WORKOS_NATIVE_TURN_URLS` 与 `WORKOS_NATIVE_TURN_SECRET_FILE` 必填 |

TURN URL 最多四个，显式端口，支持 `turn:host:port?transport=udp/tcp` 和
`turns:host:port?transport=tcp`。当前 Pion relay profile 使用 IPv4，IPv6 没有验收证据。
密钥文件为绝对路径、Runtime uid 所有、无组/其他用户权限、普通文件且不跟随叶子符号链接；
内容为 32–256 个可见 ASCII 字节，可有一个结尾换行。密钥只在 Runtime adapter 与受控
coturn 配置中出现，App/Harness 不读取。改变密钥或服务地址后重启 Runtime；现有程序
仍遵守 Runtime 重启的真实状态规则，不伪装为保留原实例。

用户名为到期 Unix 时间和随机 UUID，HMAC-SHA1/base64 遵循 coturn 的临时凭证协议，
最长 90 秒，不包含 owner、项目或设备资料。每次媒体重连获得新能力；媒体仍每 20 秒
重连、最多 30 秒授权，服务端每条输入检查当前 control generation。TURN 不可达、
配置缺失或没有收集到 relay candidate 会明确失败，不回退到直连。

90 秒是新 TURN 分配的凭证有效期，不保证既有 allocation 同时终止。验收所用 coturn
4.6.1 将 allocation lifetime 限制到至少 600 秒；部署模板据此设置 600 秒、配额与带宽。
媒体/输入撤权由 WorkOS 的 30 秒 peer 到期和逐条输入校验保证，不依赖 TURN 分配到期。
TURN TCP/TLS URL 的配置解析已覆盖，但本轮端到端网络门禁只验证 UDP relay。

部署示例：[Runtime overlay](../../deploy/compose.native-relay.yaml)、
[coturn 配置模板](../../deploy/turnserver.conf.example)。将 coturn 与 Runtime 配置到同一
静态密钥，设置实际可达的监听/relay/external 地址和受信 TLS 证书，开放 HTTPS、TURN
监听端口和有界 relay UDP 端口。模板默认拒绝所有 peer IP，操作者必须仅允许本部署
实际的 relay 地址；同时限制 allocation lifetime、用户/总配额和带宽，避免开放中继。
这些是部署参数，不是客户端可选的地址或权限。

## 可复现验收

`make test-network-continuity` 在独立六进程 fixture 中运行生产配对、临时受信 CA 和真实
Chromium。LAN 阶段两个独立容器直接连接私有 host candidate；NAT 阶段两个 internal
网络各自经 Linux router 做 source NAT，FORWARD 默认拒绝，只允许 HTTPS 与指定 TURN
端口。测试记录实际 ICE candidate pair、NAT 计数、真实 VP8 颜色变化、SCTP 输入、程序
内存变量和 Workload ID，覆盖续约、显式接管、重开、TURN 故障恢复及设备撤权。
测试控制通道只用于 Playwright 驱动，不代理浏览器的业务流量。

`TestRealCoturnCapabilityExpiryAndForgery` 直接分配中继，验证有效凭证、过期凭证与
伪造凭证。固定三尺寸截图只展示测试项目和确定性终端内容。所有网络、容器、测试证书
和密钥属于任务 namespace；信号退出清理此 namespace，不修改宿主的路由或防火墙。

本任务不包含公网部署、证书自动续签、TURN 高可用或 IPv6、物理网络性能指标；Android
平台信任与设备接续由后续移动任务提供独立证据。
