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
