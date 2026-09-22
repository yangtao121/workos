# 物理设备局域网验收

状态：pending。自动化 Chromium/WebKit 不能代替下列真机结果。

在同一可信 HTTPS 部署上配对电脑、Android Chrome、iPhone Safari、iPad Safari；只使用测试项目与内容。
记录各设备 OS/浏览器版本、部署版本、网络和操作结果，不记录凭据或用户内容。

| 场景                                                 | Android Chrome | iPhone Safari | iPad Safari |
| ---------------------------------------------------- | -------------- | ------------- | ----------- |
| 进入后恢复电脑当前项目、窗口和会话                   | pending        | pending       | pending     |
| 开关窗口、切项目和焦点双向同步                       | pending        | pending       | pending     |
| 手机输入法、横竖屏、平板分屏下聊天与审批可操作       | pending        | pending       | pending     |
| 切后台/锁屏后回前台补齐消息，无重复发送              | pending        | pending       | pending     |
| 断网草稿保留，恢复后不自动发送                       | pending        | pending       | pending     |
| 精确接回原应用；Native/PTY 显式接管后旧设备不能输入  | pending        | pending       | pending     |
| 所有浏览器关闭再打开，程序仍运行；主动停止后各端一致 | pending        | pending       | pending     |
| Forget/撤权后清理本地状态并停止访问                  | pending        | pending       | pending     |

任何失败记录复现步骤和净化后的截图；未执行不记为通过。
