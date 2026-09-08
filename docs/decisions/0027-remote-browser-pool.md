# ADR-0027：Remote Browser Pool（真实 Chromium worker）

- 状态：Accepted
- 日期：2026-09-08
- 关系：structure §8（Remote Browser 执行方式）、ADR-0026 进程沙箱纪律。

## 裁决

Browser Pool 归 runtime-host：每个会话是一个真实的 headless Chromium 子进程
（既有进程管理的子进程，不是第七个常驻服务）。启动参数固定：`--headless
--remote-debugging-port=0 --user-data-dir=<私有 0700 临时目录>`，拒绝用户提供的
任何其他 Chromium flag。进程组 + Pdeathsig + 墙钟看门狗与会话上限（默认 4 并发/
owner）约束资源；宿主无 cgroup 委派时如实记录无内核内存上限。

控制面 `workos.surface.v1.BrowserSessionService`（Gateway 路由、owner 身份）：
Create（幂等、http(s) 校验）、Navigate、Close、Get、Watch（有界 JPEG screencast
服务端流）。画面经 CDP `Page.captureScreenshot` 轮询产生（默认 2fps、单帧 ≤256
KiB、1280×800 内），不执行页面内脚本注入。会话持久于 workos_runtime
.browser_sessions（migration 054，owner runtime-host）：崩溃检测后有限重启
（默认 3 次）后终态 failed；空闲 TTL 与关闭都会回收进程与 profile 目录。

桌面 Browser 系统窗口从沙箱 iframe 升级为 Pool 消费者：地址栏驱动 Navigate，
canvas 渲染 Watch 帧；iframe 路径保留为 Pool 不可用时的诚实降级（显式
"browser pool unavailable" 文案，不伪装）。

## 验收

`make test-browser-pool`：工具链镜像 runtime（含 Playwright Chromium）跑真实
页面——初始导航出真实画面（帧序列增长、非空 JPEG）、二次导航内容变化、进程
崩溃后自动重启恢复帧、Close 后进程与目录回收、会话上限拒绝、非法 scheme 拒
绝、跨 owner 会话隔离。桌面 E2E 断言 Browser 窗口渲染 Pool 帧并保留地址校验。
宿主无真实容器引擎：Chromium 进程级证据成立，容器级隔离如实 unavailable。
