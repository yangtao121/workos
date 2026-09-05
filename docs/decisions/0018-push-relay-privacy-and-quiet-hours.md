# ADR-0018：推送唤醒——relay 白名单、免打扰与订阅生命周期

- 状态：Accepted
- 日期：2026-09-05
- 关系：扩展 ADR-0014（durable notification facts）；为 W5 提供推送隐私边界与
  软件链验收边界。

## 背景

推送唤醒链路涉及外部服务（APNs/FCM/relay）。Core 是 durable 通知事实的权威；
推送只是唤醒信号，绝不携带正文。真实 APNs/FCM 需要外部账号（本批不可用），
Web Push 的 RFC 8291 加密载荷需要独立的密码学实现范围。

## 决策

### 1. relay 载荷白名单

- relay 只接收 `{"notificationId": "<uuid>"}` 一个字段。标题、正文、项目名、
  代码、Agent 输出一律不出 Core。
- 白名单固定在 `domain.PushPayload`；新增字段必须显式进入该结构与其门禁断言。

### 2. 订阅模型

- 订阅 owner=workos-core：`(owner, device, platform)` 唯一；platform 仅
  `web-push` 与 `fixture`（APNs/FCM 待真实凭据后 additive）。
- 重复订阅幂等并复活 revoked；撤销幂等；撤销后不再投递。
- `web-push` 平台的发送器当前为 unavailable 诚实实现（可订阅、投递明确
  失败）；fixture relay 为 working 全链路。

### 3. 免打扰（owner 级）

- `quiet_enabled + [start,end)` UTC "HH:MM"，支持跨午夜窗口；服务端裁决。
- 免打扰内事件不发生 relay 唤醒；durable 事实与前台补收链路不受影响。

### 4. 恰一次投递

- `push_deliveries` 主键 `(notification_id, device_id, platform)` 是投递
  幂等账本：consumer at-least-once 重放不会双重唤醒。
- 单设备投递失败只记录日志，不阻塞其他设备、不影响事实。

### 5. Web Push / APNs / FCM 现状（诚实边界）

- VAPID 本地生成/轮换与 RFC 8291 载荷加密、Service Worker 展示、真实
  APNs/FCM 属后续范围；当前 `web-push`/`apns`/`fcm` 均如实 unavailable。
- 订阅注册、偏好、quiet 裁决、fixture relay 软件链全部 working。

## 后果

- `make test-push-relay` 提供真实门禁证据后，status.json 才能升级推送切片。
- 白名单违规（payload 出现 notificationId 以外字段）是门禁失败。
