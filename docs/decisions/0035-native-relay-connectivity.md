# ADR-0035：原生 Surface 的短期 TURN 连接能力

- 状态：accepted
- 日期：2026-09-21
- 任务：[网络接续](../tasks/20260921-v2-network-continuity.md)
- 前序：ADR-0029、0031、0032

## 决策

NativeSessionService 新增 GetNativeConnectivity，必须先验证 owner、真实 Gateway
device、活动 session 与当前 control generation。返回部署的连接模式、ICE server、
relay-only 标记和 UTC 到期时间。服务端派生一份短期随机标识的 TURN capability，
不返回 coturn 共享密钥。配置无持久写操作，不改变 Workload 身份或控制权。

Runtime nativehost 通过 port 取得连接配置；coturn 的 HMAC-SHA1 临时凭证实现留在
对应 adapter。共享密钥来自 owner-only 无符号链接文件，不能从浏览器/App/Harness
传入。凭证最长 90 秒，用户名只含到期时间和随机 ID，不携带用户或项目资料。

部署模式仍由操作者选择：loopback 默认；lan 继续要求私有 CIDR 与有界 UDP；
新增 relay 强制双方仅使用 TURN candidate。relay 缺失配置时启动失败，TURN 故障
不退回直连，也不声明媒体连接成功。桌面每次续约前重新获取配置；仍每 20 秒重新
信令，Runtime peer 最长 30 秒，旧控制端的队列与输入继续通过现有 generation gate。
短期 TURN capability 本身不授予解密媒体、Workload 访问或控制权。

coturn 为部署依赖，不是新的 WorkOS 进程边界。部署示例要求有界 relay 端口、
allocation lifetime、用户和总配额，并限制 peer IP 至本部署允许的 relay 范围；
共享密钥由操作者配置/轮换。协议不绑定 coturn 厂商品牌。

## 验证与证据边界

网络测试使用临时 CA 加入浏览器真实信任库，禁止 ignoreHTTPSErrors/证书错误绕过。
Docker 多网络的 LAN/NAT、实际选中 relay 候选对、真实 VP8 解码与 SCTP 输入分别
留证；原生程序内存状态和 Workload 身份验证接续。物理设备、真实公网、证书自动
续签和 TURN 高可用不由模拟网络结果替代。Android 在后续独立任务验收。

参考（2026-09-21）：[coturn 凭证与配置](https://github.com/coturn/coturn/blob/master/README.turnserver)、
[Pion ICE policy](https://github.com/pion/webrtc/blob/main/icetransportpolicy.go)。
