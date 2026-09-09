# ADR-0028：受监督的真实终端会话（PTY）

- 状态：Accepted
- 日期：2026-09-09
- 关系：structure §8 Human Native Workspace / Native Runner；ADR-0027 进程纪律。

## 裁决

Terminal 是 runtime-host 拥有的受监督真实登录 shell：每个会话一个真实
`/bin/sh` 子进程运行在 pty 上（migration 055 持久 owner-scoped 会话行）。
控制面 `workos.surface.v1.PtySessionService`（Create/Write/Read/Resize/Close，
Gateway 路由 + owner 身份）：输入有界（16 KiB/写，NUL 拒绝）、输出为有界
ring（256 KiB）+ 严格递增 cursor 读取、窗口 resize 经 TIOCSWINSZ+SIGWINCH、
Close/Sweep 杀进程组并回收。子进程 Setsid（pty 库要求）+ Pdeathsig；不加
Setpgid（与 Setsid 冲突导致 EPERM——子进程先成为组长后 setsid 必败）。
并发会话上限 4/owner，TTL 30 分钟。native-runner 能力按配置如实报告；
虚拟显示/WebRTC Native Runner 仍未实现，不在本 ADR 声明范围。

桌面 Terminal 系统窗口（原 unavailable 入口）消费该服务：真实 shell 输出
滚动区、键盘输入经串行化写入链（逐字符 RPC 竞态会转置字符——
"echo"曾变"echwo"）、窗口关闭即 Close 会话。

## 验收

`make test-terminal-sessions`：真实栈上 Create 幂等/漂移 Aborted、真实
`echo` 往返（marker 回读）、cursor 严格递增 + 从零重读有界、resize、
外 owner 直连 runtime 404（Gateway 注入身份不可伪造，故隔离检查在可信
listener 上做）、尺寸/超限输入 fail-closed、会话上限（恰好 4，第 5 拒绝）、
Close 后写入 404；Chromium E2E：桌面 Terminal 窗口打开真实 shell、
`$` 提示符出现、键入 `echo workos-terminal-proof` 回显结果。
