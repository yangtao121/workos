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
- 推送 token 注册只允许 https 或显式回环 relay 端点。
- 构建级门禁（typecheck/test/bundle/config）无原生 SDK 也可验收；原生
  sync/build 缺 SDK 时记录 BLOCKED-ENVIRONMENT，不伪造 PASS。

## 后果

- `make test-mdns-discovery` 在宿主多播可用时提供真实发现与指纹验收证据；
  宿主不支持多播时测试显式 skip 并记录。
- 指纹语法或广播字段的任何扩充都必须更新门禁断言。
