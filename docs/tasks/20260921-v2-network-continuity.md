# V2 HTTPS、模拟 LAN/NAT 与 TURN 接续

- 状态：in_progress
- Branch：`feat/v2-network-continuity`
- Worktree：`/home/aquatao/workos-v2-network`
- 基线：`2c3eb28`，P3 和原生目标/技能/子 Agent 已验收。
- 设计：[V2](../structure-v2.md)、[ADR-0031](../decisions/0031-continuous-apps-and-device-takeover.md)、[ADR-0032](../decisions/0032-runtime-isolated-development.md)。

## 范围与依赖

用户接受 Docker 多网络模拟本轮 LAN/NAT 验收。继续使用 WebRTC，接入 coturn，
不引入 KasmVNC、VPN 或新 WorkOS 进程。网络 fixture 使用生产配对和受信 HTTPS，
独立浏览器 profile/容器，明确区别于物理设备、真实公网与证书自动化运维。

先定义 canonical 短期连接配置 RPC，再实现 Runtime TURN 凭证派生、媒体配置和
桌面连接；TURN 共享密钥仅 Runtime/受控 coturn 配置可读，客户端只获短期 capability。
原生媒体 30 秒重新认证及 device/control generation 边界保持有效。现有 LAN 私有
CIDR/UDP 约束保留；relay 模式不可悄悄退回 host candidate。

## 验收

1. Proto additive、生成一致性；短期配置所有权/控制权/结束状态拒绝测试。
2. 真实 coturn，短期凭证、错误/过期凭证拒绝、relay-only ICE，敏感配置不进日志。
3. 模拟 LAN 两个独立客户端：真实 TLS 信任、配对、同一程序状态、显式接管与旧端失效。
4. 隔离网络/NAT 与直连阻断：选中的候选对为 relay，真实画面、输入、续租、断线重连，
   TURN 停止后诚实失败、恢复后接续同一 Workload；撤权拒绝新信令。
5. 确定性三尺寸 UI before/after/current；六进程旧链回归、`make generate`、`make check`。
6. 文档/status 与长期证据；仅清理本任务命名空间，不动共享开发栈。

## 进展

- 起始工作树干净。现有 Native 的 `loopback`/`lan` 只收集 host candidate；桌面
  创建空 ICE 配置的 peer，尚无 TURN 凭证入口。已读取 Proto、Runtime service/engine、
  NativeApp、现有 LAN TLS fixture 与上游 coturn/Pion 文档。
- 下一步：契约与 ADR，然后顺序实现。

- 契约阶段：ADR-0035 与 GetNativeConnectivity additive Proto 已生成，buf lint/breaking
  相对 `2c3eb28` 通过，Native 全模块单测通过。新入口暂时明确 unavailable，尚不宣称 TURN working。
