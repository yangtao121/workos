# ADR-0020：能力修复与桌面收口

- 状态：Accepted
- 日期：2026-09-06
- 依据：用户批准的全面修复计划；修正 ADR-0016 的实际部署实现。

## 部署

候选必须包含明确 Registry version、owner/project/installation/incident 和操作前项目 revision。
先持久化 candidate，再通过 Core 版本命令切换并创建真实 Surface；成功启动后才开始观察窗。
观察到新 incident 时回滚，否则到期 promote。窗口不能早于版本切换，空目标不得成功。

Reliability 持有台账与事务行锁；Core 持有版本事实。每条外部命令的 idempotency key 和
expected revision 均来自持久记录，不在重试时改写。Core 的 revision 冲突阻止覆盖用户后续操作。
台账终态不再进入协调循环；候选/回滚失败有界重试。原 observation-only canary 保留记录并标记
failed，不冒充实际部署证据。修复任务完成本身不是候选，完整 Build/Test 产物交接仍是验收必需项。

## 桌面与契约

保留自由窗口桌面和现有 responsive 状态模型；统一深色控件、图标、工作区几何及入口注册。
公共协议先修改 Proto 再生成，现有数据保留，migration forward-only。删除失效 UI/重复代码，
不添加旧实现兼容分支。所有像素变化按 docs/ui/README.md 留确定性前后证据。

## 验收事实

静态页面只证明 Web Bundle 能渲染；不证明 Browser Pool、Native Runner 或 WebRTC。
词项哈希仅证明本地确定性词项相似度。每项能力声明按实际跨进程证据描述；代码缺失不能记成
环境阻塞。具体进度由原总攻任务记录的 2026-09-06 修复验收表持有。

## 通知投递

通知与设备待发送记录在 Core 同一事务创建，覆盖系统与 App 全部来源。
免打扰按创建时间决定永久 suppressed；发送前再次检查偏好及订阅撤销。
发送记录由有界租约协调，成功后才标记 delivered；失败指数退避，最多八次。
过期租约可接管，确认必须匹配 claim token。远端接受后本地确认前崩溃可能重复唤醒，
因此是 at-least-once，接收方按 notification id 去重，不承诺 exactly-once。
迁移 042 保留旧记录为 unknown；旧发送时间不能证明投递成功。

Web Push 使用 [RFC 8291](https://www.rfc-editor.org/rfc/rfc8291) 单记录加密与
[RFC 8292](https://www.rfc-editor.org/rfc/rfc8292) VAPID；认证标识与内容加密使用不同 P-256 密钥。
持久专用密钥只存在于 Core 的 owner-only 文件，浏览器只接收公开订阅密钥。
relay 不接受重定向，404/410 撤销失效订阅，其他失败回到 outbox 重试。
免打扰设置以 migration 043 的 revision 实施并发控制；stale 更新 Aborted，避免多设备静默覆盖。

## App shell 调用

`AuthorizeShellAction` 只接受固定方法名。Runtime 每次从 token 查有效 owner/device session，
经 Core resolver 重读活动安装，比较 grant revision、App ID、版本、manifest digest；
`project.current` 还需 `project.read`。未配置 resolver 或 Core 不可用不执行动作。
Shell 的 title/badge/maximize/minimize/close 只绑定自己的窗口；授权超时后的迟到结果丢弃。
项目摘要读取原 surface 项目，禁止回退活动项目；徽标是可清除的 0..9999 整数。
此 RPC 不表示 files/artifacts 能力已实现，完整 Bridge 验收仍由任务表追踪。
