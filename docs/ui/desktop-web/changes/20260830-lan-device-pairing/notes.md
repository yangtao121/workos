# notes — 20260830-lan-device-pairing

## 任务

- 任务记录：`docs/tasks/20260830-lan-device-pairing.md`；ADR：
  `docs/decisions/0007-lan-device-pairing-and-gateway-session.md`。
- 受影响界面：应用入口新增 Auth Gate（unpaired/pairing/authenticated/unavailable 状态机）；
  dock 新增 🔑 Device Center 普通窗口（配对设备列表、会话到期、Pair another device、
  revoke/logout）。Desktop 其余像素与交互不变。

## 采集环境与口径

- 浏览器/视口：Playwright Chromium，1440×900，deviceScaleFactor 1。
- 栈：compose `workos-gateway-tls`（profile lan-pairing）：真实 TLS 1.3 网关（每次临时生成的
  leaf 证书，SAN localhost/127.0.0.1）+ 真实 PostgreSQL + admin Unix socket。
- 采集命令：`make capture-lan-pairing-visual`（执行
  `e2e/lan-pairing-visual.spec.ts` 并同步 `after/` → `current/`）。Auth Gate 由临时 TLS fixture
  Gateway 提供；Device Center 的 DeviceService 响应在导航前由 Playwright 固定为两条 Connect
  fixture，locale=`en-US`、timezone=`UTC`，同时隐藏无关的动态 Project/Agent 背景。
- 截图只含 fixture 数据；无任何真实凭据。

## 文件

### before/

- 全部 17 张从任务开始时的 `docs/ui/desktop-web/current/` 复制（任务基准：无 Auth Gate、
  无 Device Center、dock 无 🔑 按钮；dev bypass 部署在入口直接挂载 Desktop）。

### after/（同时已更新 `docs/ui/desktop-web/current/`）

- `auth-gate--unpaired--1440x900.png`：无 Cookie、无本地设备密钥的浏览器访问生产模式网关时
  的 bounded unpaired 界面（文案与 unavailable 状态明确区分）。
- `auth-gate--pairing--1440x900.png`：打开 pairing URL 后的配对界面。**fragment 是明确无效的
  deterministic fixture**（43 个 'A' 的 secret + 全 0 的 sha256 指纹），不可被扫描后授权；
  地址栏已在渲染前被 `history.replaceState` 擦除（截图与 URL 均不含 ticket）。
- `device-center--paired-devices--1440x900.png`：真实 Desktop window manager 中的 Device Center
  组件，数据固定为 `Fixture Desktop` / `Fixture Phone`、revision 3/2 与 UTC session expiry；无 Cookie、
  IndexedDB key、live ticket、持久卷历史或 wall-clock 输入。短期 QR 的 server expiry 自动清内存行为
  由 `DeviceCenter.test.tsx` 的定时器测试覆盖。

## 有意差异

- before/ 中不存在这三张图对应的界面（本任务新增），故以整套 current/ 基线作为 before。
- Device Center 只保留可重复的固定 Connect fixture；截图不再继承持久验收卷历史。
- `ignoreHTTPSErrors` 仅用于临时自签测试 CA；它不证明浏览器信任链或 native certificate
  pinning（ADR-0007 §7）。
