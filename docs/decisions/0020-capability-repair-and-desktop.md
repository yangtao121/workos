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

## 项目文件 Bridge

Runtime 配置 `runtime.workspace_mounts` 显式绑定 owner UUID、project UUID、本地绝对目录与
read_only；最多 32 项，禁止根目录、重复/重叠路径、符号链接及非受控目录。原 Indexer
注册保持只读索引用途，不成为写入授权。安装新增 files.read/files.write grant，未绑定目录
不协商文件能力；files.pick 来自 files.read，写能力还受 read_only 限制。

FileRef 的 project_id 只用于拒绝错项目引用，真实 owner/project 始终来自有效 session。
相对 POSIX 路径最长 1024 字节、最多 8 层；文件最多 32 KiB，目录最多 1000 项，
每页最多 20 项。列表是实时目录分页，etag 为内容 SHA-256；写入必须匹配旧 etag，
空 etag 仅创建不存在的文件。目录绑定由 Runtime 独占写入，其他进程只读挂载；
写操作按项目串行化，临时文件 fsync 后原子替换，错误不截断旧内容。

Linux adapter 使用内核 [openat2](https://pkg.go.dev/golang.org/x/sys/unix#Openat2) 的
BENEATH/NO_SYMLINKS 解析；不把先检查路径再普通 open 当作安全边界，
见 [Go 路径遍历说明](https://go.dev/blog/osroot)。缺少内核支持时不协商文件能力。
选择器由 shell 渲染，用户可取消；App 只拿 FileRef 与有界文件内容，不能指定宿主目录。

## App 产物

App 的 artifacts.create/open 使用 artifact.write/read grant；只支持既有 Markdown/diff
不可变 review 内容与系统查看器。公开 Bridge 不接收 owner、project 或来源字段；Runtime
从 session 派生，Core 私有服务在事务内锁定安装并重验 epoch。App 创建产物记录
source_app_instance_id，与 source_task_id 恰好二选一；不制造 Agent 任务或 timeline 事件。
迁移 044 由 Core Artifact 独占，新增 App 幂等映射；安装内 key 采用既有 output-key 语法。
同 key 同内容重放首个产物，不同内容 Aborted；单安装最多 100 个产物。
产物、索引 publication、通知在一个事务提交。open 只返回当前安装项目内经授权重读的
review 元数据，由 shell 打开既有查看器，禁止 App 指定 URL 或任意窗口目标。

推送偏好额外返回当前认证设备 active endpoint 的 SHA-256；不返回 endpoint 或订阅密钥。
订阅/撤订阅请求中的 device_id 必须等于可信身份，不允许一个设备替另一个设备写入。
Shell 将 Core digest、浏览器订阅与当前 VAPID public key 一并核对；不自动恢复撤销订阅，
需要用户点击重新连接。公钥变化时先撤销旧 Core 注册，再清理浏览器旧订阅并注册新密钥。
Core 撤销成功而浏览器清理失败时明确显示未完成的清理，禁止宣称完全成功。

## 设备撤销与后台提醒

Gateway 在撤销 credential/session 的同一事务写 Core 通知义务（migration 045，Gateway 独占）。
私有 DevicePushService 以可信 owner/device 身份和原始 revoked_at 消费；Core 同事务保存永久
设备 tombstone 与撤销全部平台注册（migration 046，Core 独占）。稳定幂等键为设备 UUID，
与可信身份绑定；同键 revoked_at 漂移返回 Aborted。Subscribe 与该消费共用设备锁，
防止已通过旧 session gate 的迟到请求恢复推送。设备 UUID 不复用，单平台手动停用仍可重连。

Gateway 批量租约领取，成功 RPC 后按 claim token 确认；丢失确认可重放，失败/重启后恢复。
每次最多 32 项、30 秒租约，安全撤销义务持续重试，不以固定失败次数放弃。
会话撤销立即生效；Core 可达时后台提醒在下次协调后停止。已在途/已被 relay 接收的通用提醒
无法撤回。跨进程不读对方 SQL；未同步期间不声称已完成后台推送撤销。
