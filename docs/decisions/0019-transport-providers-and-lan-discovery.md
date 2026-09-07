# ADR-0019：传输提供方与局域网发现——mDNS、指纹信任链与移动封装

- 状态：Accepted
- 日期：2026-09-05
- 关系：扩展配对信任模型（pairing ticket 的 TLS 叶指纹固定）；为 W5 局域网
  链路与移动封装提供边界。

## 背景

设备在局域网内发现 WorkOS 实例并完成配对。发现层不能成为新的信任锚：
冒名的 mDNS 广播绝不能把实例引向伪造网关。Relay/Overlay 传输的基础设施
不存在。

## 决策

### 1. TransportProvider 抽象

- `Discovery`（Browse）与 `Announcer` 是传输提供方的最小接口；提供方状态
  只有 available/unavailable 两个诚实值。
- LanDirect（mDNS）为 working；Relay 与 Overlay 仅保留接口与 unavailable
  状态，不实现、不伪造。

### 2. 广播事实白名单

- mDNS TXT 仅广播 `origin`（公共 https 源）与 `fp`（配对 ticket 固定的
  TLS 叶指纹）。密钥、token、配对 URL 不进入广播。
- 发现客户端只返回语法合法（https 源 + `sha256:<64hex>` 指纹）且指纹与
  配对期望常量时间匹配的实例；伪造广播不触达调用方，错误净化。

### 3. 移动封装信任链

- Capacitor 封装共享 adaptive-shell；设备密钥走原生安全存储，插件缺失时
  回退临时内存并如实上报 insecure 状态，绝不静默。
- 原生推送通过 canonical notification 契约接入，禁止另造直接发送 token 的 JSON relay API；
  现有未接入的 registerPushToken 已删除，APNs/FCM 仍 unavailable。
- library bundle 不能证明可启动封装。门禁必须检查 HTML 入口及 Android/iOS 工程，
  再执行 Android sync；任何软件/同步错误直接失败，不能统一归为缺 SDK。原生二进制
  构建与设备验收单列，只有实际缺失的宿主前提可记 BLOCKED-ENVIRONMENT。

## 后果

- `make test-mdns-discovery` 在宿主多播可用时提供真实发现与指纹验收证据；
  宿主不支持多播时测试显式 skip 并记录。
- 指纹语法或广播字段的任何扩充都必须更新门禁断言。

## 2026-09-07 实现核查

真实 `cap sync android` 返回 platform has not been added yet；mobile-shell 的 library build
只有 main.js，没有 index.html，也没有 Android/iOS 工程。当前包保持 scaffolded，已验证的
响应式 UI 属于 desktop-web。`make test-mobile-wrappers` 因明确的软件缺口失败，不表示 SDK
已经成为唯一阻塞。

依据：[Capacitor v6 sync](https://capacitorjs.com/docs/v6/cli/commands/sync) 组合 copy/update，
[官方工作流](https://capacitorjs.com/docs/v6/basics/workflow) 将资源同步与原生二进制构建分开。
