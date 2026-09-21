# V2 HTTPS、模拟 LAN/NAT 与 TURN 接续

- 状态：done
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

## 实现进展

- 契约已提交 `83e76cd`。Runtime 派生最长 90 秒 TURN capability，静态 key 文件
  校验 owner/0600/无符号链接，模式由操作者选择；客户端只能在当前控制权下获取配置。
- relay 模式在 Pion 和浏览器均使用 relay-only，配置缺失/过期不能回退直连；保留
  30 秒媒体认证、设备/control generation 校验和私有 LAN CIDR/UDP 配置。
- Runtime 单元与 race、NativeApp 七个组件用例通过；真实 coturn 与独立浏览器网络
  容器、NAT router、防火墙、受信 CA 测试工具已实现，完整 E2E 正在执行。
- 首轮 LAN 已证明受信 HTTPS、真实 host 候选对、画面/输入、续约、跨设备显式接管，
  并从第二端读回程序内存变量。重开窗口时测试误以为新 attachment 自动取得控制；
  当前协议要求显式恢复控制，测试已补齐此动作，未放宽服务端授权。
- 状态仍 in_progress；尚无完整 NAT/TURN/故障链、最终视觉或全仓 check 的通过结论。

## 收口进展

- 完整 LAN/NAT 门禁 `tmp/network-full-final.log`、`tmp/v2-completion.1xxYUs` 全部通过；coturn 增加双向数据与 peer IP 拒绝探针，真实候选分别为 host/host 和 relay/relay。
- coturn 移入独立网络命名空间，修复宿主 Docker MASQUERADE 改写 relay 间回送的问题；未放宽网络策略。
- 旧链回归暴露预览 Unix socket 长路径问题，已用目录 fd 连接修复；加长路径的真实 Docker 测试通过。
- 最终源码重建后的 V2 completion 全链通过：`tmp/network-v2-regression-fixed.log`、`tmp/v2-completion.TWsfc5`；四个浏览器用例含三尺寸全部执行。
- `make check` 已通过一次（`tmp/network-check-final.log`）；新增路径回归与文档后再做最终检查。
- 视觉重新使用基线 Desktop 与同一真实 fixture 采集；发现响应式切换可先读到旧帧，现要求连续三个 streaming/绿色解码帧才截图，正在重采集稳定版本。
- [视觉 before](../ui/desktop-web/changes/20260921-v2-network-continuity/before/)、[after](../ui/desktop-web/changes/20260921-v2-network-continuity/after/)、[说明](../ui/desktop-web/changes/20260921-v2-network-continuity/notes.md)；[长期证据](evidence/20260921-v2-network-continuity/results.md)。

## 最终验收

最终完整网络门禁 `tmp/network-full-visual-final.log` / `tmp/v2-completion.0RRDDo` PASS，稳定 baseline `tmp/v2-completion.oi7j7p` PASS。三尺寸均确认 streaming 和实际绿色解码帧，已更新 before/after/current。

最终 `make check`、全链 V2 completion、受影响 race 通过；`make generate` 无协议/SQL 生成差异，README 状态由工具更新。已验证命令、历史缺陷与证据边界见 [长期记录](evidence/20260921-v2-network-continuity/results.md)。下一任务为移动浏览器与 Android APK/模拟器；网络部分不遗留实现工作，不扩大为公网/物理设备结论。
