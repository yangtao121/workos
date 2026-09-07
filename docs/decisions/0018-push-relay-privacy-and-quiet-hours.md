# ADR-0018：推送唤醒——relay 白名单、免打扰与订阅生命周期

- 状态：Accepted
- 日期：2026-09-05
- 关系：扩展 ADR-0014（durable notification facts）；为 W5 提供推送隐私边界与
  软件链验收边界。

## 背景

推送唤醒链路涉及外部服务（APNs/FCM/relay）。Core 是 durable 通知事实的权威；
推送只是唤醒信号，绝不携带正文。真实 APNs/FCM 需要外部账号（本批不可用），
Web Push 使用独立 VAPID 身份和 RFC 8291 加密载荷。

## 决策

### 1. relay 载荷白名单

- relay 只接收 `{"notificationId": "<uuid>"}` 一个字段。标题、正文、项目名、
  代码、Agent 输出一律不出 Core。
- 白名单固定在 `domain.PushPayload`；新增字段必须显式进入该结构与其门禁断言。

### 2. 订阅模型

- 订阅 owner=workos-core：`(owner, device, platform)` 唯一；platform 仅
  `web-push` 与 `fixture`（APNs/FCM 待真实凭据后 additive）。
- 重复订阅幂等并复活 revoked；撤销幂等；撤销后不再投递。
- `web-push` 配置独立 owner-only P-256 key 与 VAPID subject 后启用；未配置时
  偏好 RPC 明确 unavailable，拒绝注册无效订阅。只向客户端公开订阅公钥。
- Gateway 设备撤销通过持久 outbox 通知 Core；永久 tombstone 拒绝迟到订阅。
  用户主动关闭单个平台则允许日后重新开启。

### 3. 免打扰（owner 级）

- `quiet_enabled + [start,end)` UTC "HH:MM"，支持跨午夜窗口；服务端裁决。
- 免打扰内事件不发生 relay 唤醒；durable 事实与前台补收链路不受影响。

### 4. 持久、有界重试

- 通知事实与 `push_deliveries` 同事务落盘，主键
  `(notification_id, device_id, platform)` 消除重复入队。
- 租约领取、token 确认、最多八次重试；只有 relay 接受后才记 delivered。
  接受后确认丢失可能重复投递，不能承诺恰一次网络唤醒。客户端按 notificationId
  去重，权威通知事实不依赖唤醒成功。
- 每次发送前重验订阅与 quiet；过期端点撤销、静默时段抑制。日志不输出端点或密钥。

### 5. Web Push / APNs / FCM 现状（诚实边界）

- Core Web Push adapter 使用 Go 标准密码学原语实现 RFC 8291 单记录加密与
  RFC 8292 VAPID。私钥仅 Core 持有，重试使用新盐/临时密钥；禁止跟随重定向。
- Worker 只展示固定标题/正文与 ID tag，不读取 Cookie/RPC，不存正文；唤醒
  shell 后通过现有认证与 NotificationService 补收事实。
- 本地 TLS receiver 与 Chromium push driver 验收不证明真实 APNs/FCM、厂商
  Push Service 或原生移动后台运行；这些能力仍未验证。

## 后果

- `make test-push-relay` 提供真实门禁证据后，status.json 才能升级推送切片。
- 白名单违规（payload 出现 notificationId 以外字段）是门禁失败。
- `make test-push-wake` 用独立数据库和新密钥串起实际 Core outbox、严格 TLS receiver、
  投递 503、Core 重启、加密重试及 Chromium 冻结页面唤醒/通知补收。测试只阻滞
  Watch 流，无成功 RPC mock；receiver 用 RFC 已知向量校验独立解密实现。
- Worker 顺序执行查重/展示，修复连续 push 时部分提醒未展示的问题；单次展示失败
  不阻断后续通知。系统文案、布局、权限和权威通知协议不变。

## 规范依据

- [RFC 8291：Web Push 内容加密](https://www.rfc-editor.org/rfc/rfc8291.html)
- [RFC 8292：VAPID](https://www.rfc-editor.org/rfc/rfc8292.html)
