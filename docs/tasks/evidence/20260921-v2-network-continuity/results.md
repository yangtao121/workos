# HTTPS、LAN/NAT 与 TURN 验收（2026-09-21）

2026-09-22 清理更新：旧工作树已删除；下文引用的现有 `tmp/*.log` 等根目录日志已按原字节
归档至主工作目录的 `tmp/archived-worktree-evidence/20260922/workos-v2-network/tmp/`。
完整清单、SHA256 与范围见 [清理记录](../../20260922-v2-merged-worktree-cleanup.md)。
临时测试运行目录已清理，已提交的结果、截图与原日志摘要保留。

契约 `83e76cd`，Native 基线 `2c3eb28`。本轮 Docker 模拟网络，不宣称物理设备或公网验收。

| 范围         | 结果                                                                                                | 证据                                                                                                                                                    |
| ------------ | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 完整网络门禁 | PASS，LAN 56.3 秒、NAT 约 1.1 分钟，各 1 个用例，无 skipped/flaky                                   | `tmp/network-full-visual-final.log`、`tmp/v2-completion.0RRDDo`                                                                                         |
| TLS / 设备   | 真实 CA 信任、HTTPS secure context、HttpOnly Secure cookie、生产配对；未忽略证书错误                | 两个 Chromium 独立容器及 profile                                                                                                                        |
| LAN / relay  | 选中 host/host 与 relay/relay；私有客户端经独立 router source NAT                                   | [LAN](selected-pair-lan.json)、[relay](selected-pair-relay.json)、[拓扑](network-topology.json)、[A 计数](router-a-nat.txt)、[B 计数](router-b-nat.txt) |
| coturn       | 有效分配、过期/伪造拒绝、双向数据、任意目标 IP 拒绝                                                 | `TestRealCoturnCapabilityExpiryAndForgery` 四个子用例 PASS                                                                                              |
| 程序与控制   | 真实 VP8 红/绿像素、SCTP 输入、20 秒媒体续约；接管/窗口重开保留内存变量和 Workload ID；旧 epoch 403 | `network-continuity.spec.ts`                                                                                                                            |
| 故障 / 撤权  | 停 TURN 后 ended，恢复后同程序；撤设备后新信令 401、全部 peer 在期限内关闭；最终主动停止程序        | 同一完整门禁                                                                                                                                            |
| UI           | 同 fixture 基线/当前桌面，三尺寸 before/after/current                                               | [说明](../../../ui/desktop-web/changes/20260921-v2-network-continuity/notes.md)                                                                         |

## 修复与证据边界

- coturn 绑定宿主 bridge 时，Docker MASQUERADE 改写中继间回送源地址；分配成功却无法交换数据。独立 coturn 网络命名空间修复，双向探针与实际 ICE/媒体均通过，未放宽 peer IP 策略或直连限制。
- 90 秒凭证限制新分配；既有 TURN allocation 不等于媒体授权，coturn 4.6.1 的 allocation 实际最短 600 秒。WorkOS 独立保证每条输入的控制权和最长 30 秒媒体授权。
- 全链回归发现 worktree 长路径超出 Unix socket 上限；开发预览改用打开的目录 fd 连接，真实 Docker 回归固定构造长路径，防止只依赖随机短路径通过。
- 仅 UDP relay、IPv4、Docker LAN/NAT；无物理设备、公网、TURN TCP/TLS 或 HA 的通过声明。所有真实模型调用均未参与本门禁。

最终 V2 六进程回归 PASS：`tmp/network-v2-regression-fixed.log`、`tmp/v2-completion.TWsfc5`，真实长路径预览 PASS（2.03 秒），四个浏览器用例含三尺寸全部通过，无跳过。

`make check` PASS：`tmp/network-make-check-final-complete.log`，Proto/sqlc、全 Go vet/tests、TypeScript 边界/lint/format/typecheck/unit/build 与状态生成检查。受影响模块 race PASS：`tmp/network-race-final.log`。

`make generate` 已执行；Proto 注释及对应生成注释随 ADR-0035 更新，无协议结构或 SQL 生成差异；状态 README 由工具更新。重复生成哈希和凭据/namespace 检查见 `tmp/network-final-integrity.log`。所有网络 fixture 容器/网络已清空；未操作共享开发栈。
