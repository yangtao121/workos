# ADR-0039：局域网单 owner 密码认证

- 状态：已采纳，验收进行中
- 日期：2026-09-26
- 关系：修订 ADR-0007 的局域网入口和 `structure-v3` 的“不新增登录”选择；配对模式保留兼容。

## 裁决

Gateway 新增互斥的 `WORKOS_AUTH_MODE=password|pairing`，默认 `pairing`。局域网 HTTPS
部署显式选择 `password`。密码模式不开放旧设备配对端点，也不接受旧配对会话；两种模式
均经 Gateway 逐请求验证，并将可信 owner/device 身份注入既有下游连接。开发旁路仅用于
回环地址的本地环境，不是局域网入口。

操作者通过 Gateway 独占的 0600 Unix 管理 socket 设置用户名和密码。CLI 从交互终端
读取密码，不接受命令行参数或环境变量中的密码。Gateway 仅保存 Argon2id 哈希和随机盐；
每次设置密码都在同一数据库事务中撤销所有密码会话。`GetMode` 允许浏览器选择登录页；
`Login` 在 TLS、同源检查、请求体预算、每地址与全局限流及 Argon2id 并发上限下工作。

每次成功登录生成独立的 UUIDv7 设备与仅保存在 `__Host-` Secure、HttpOnly、
SameSite=Strict Cookie 中的不透明会话。会话具有绝对到期时间。设备列表、撤销、退出
沿用 `DeviceService` 契约；服务端撤销立即阻止下一次请求，持久流按既有重验间隔终止。
密码设备、会话与撤销幂等事实归 Gateway 自有表，其他进程不直接查询。

## 后果与验收

浏览器不需要扩展、客户端证书或设备密钥。每次重新输入密码会建立新的设备身份；
设备列表保留旧记录供审计和撤销。密码尚未设置时登录失败且 Gateway readiness 不通过，
管理 socket 仍可供首次设置。真正的局域网 HTTPS 与跨设备实测证据由 P0 任务记录承接。
