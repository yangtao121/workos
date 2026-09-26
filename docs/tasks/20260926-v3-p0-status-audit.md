# V3 P0 A01–A12 状态与证据审计

- 状态：done（仅文档审计完成；P0 功能仍未通过验收）
- Owner/branch/worktree：Codex，docs/v3-p0-status-audit，/home/aquatao/workos-v3-p0-status-audit
- 基线：main@192e44b；独立 worktree 开始时干净，主工作树已有其他任务的未提交改动，未触碰。
- 范围：对照 [V3 结构方案](../structure-v3.md) 的 P0 门禁、[原始 A01–A12 任务](20260924-v3-p0-native-experience.md)、已合并实现和测试记录，校准 [状态事实源](../status.json)。只改状态事实源及本任务记录。
- 依赖：[P0 集成任务](20260926-lan-p0-integration.md)、[常驻窗口 ADR](../decisions/0040-resident-greenfield-window-media.md)、[owner 浏览器门禁](../runbooks/lan-code-p0-browser-e2e.md)。集成任务负责将最终浏览器阶段合入主线；owner 保留密码并自行运行该门禁。
- 验收：A01–A12 每项有已合并证据和未完成边界；LAN 密码入口与 V3 常驻 Greenfield 保持 scaffolded；明确 owner 认证 HTTPS 浏览器门禁未运行；JSON 与状态生成器校验通过，交接生成 README 与全仓检查。

## 判定口径

以下 PARTIAL 表示实现、单测、固定 fixture 或隔离真实进程探针已有证据，**不是** P0 集成 PASS。隔离 child 的私有 broker 和直接浏览器实验均不能代替 owner 认证、CA 验证的 Gateway → Runtime → 浏览器用户链路。A01–A12 在本次审计中没有一项被提升为完整集成 PASS。原始任务表保留当时的 Grok 自测历史，本表记录 main@192e44b 的后续进展。

| 项目                             | 当前审计                | 已合并证据                                                                                                                                                                                                                                       | 仍需验收                                                                                                                 |
| -------------------------------- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------ |
| A01 真实 Code 启动和保存         | PARTIAL                 | [直接路径技术门禁](evidence/20260924-v3-p0-native-experience/greenfield-gate.md)通过真实 Code GUI 修改并保存隔离 fixture；LAN 常驻镜像已构建和启动。                                                                                             | owner 认证的 WorkOS Code 窗口实际编辑、保存与仓库字节核对。                                                              |
| A02 窗口、菜单、弹窗             | PARTIAL                 | [常驻 child](20260926-greenfield-resident-child.md)显示真实 Code File 菜单及带 parent ID 的 Open File 窗口；[精确 Close](20260926-greenfield-native-window-close.md)的隔离探针和[桌面层叠测试](20260926-p0-native-window-a02.md)覆盖局部行为。   | owner 浏览器中真实对话框层叠、焦点、拖动、缩放与只关闭子窗。                                                             |
| A03 尺寸、DPR、清晰度            | PARTIAL                 | [常驻 child](20260926-greenfield-resident-child.md)的真实 Code DPR 1→2 探针收到新原生帧；浏览器固定 fixture 有画面记录。                                                                                                                         | 1440×900、DPR 1/2 的真实浏览器绘制、缩放后文字可读性和无损刷新。                                                         |
| A04 输入法、快捷键、滚动         | PARTIAL                 | [浏览器输入任务](20260926-v3-p0-browser-input.md)与[常驻 viewer](20260926-greenfield-window-viewer.md)有组件、fixture 和 sequenced input 测试。                                                                                                  | owner 浏览器中真实 Code 的 composition、快捷键、滚动与焦点丢失释放；自动派发 composition 不等于物理输入法。              |
| A05 双向纯文本剪贴板             | PARTIAL                 | 直接路径探针只验证 Code X11→浏览器 selection；[Terminal Clipboard](20260926-p0-terminal-clipboard.md)在隔离 Chromium fixture 通过有序 UTF-8 输入、复制、observer 拒绝；[Mousepad child](20260926-p0-auxiliary-editor.md)已有真实像素与焦点探针。 | Code、真实 PTY Terminal、独立 Mousepad 与浏览器的双向中文、emoji、tab、多行粘贴及复制，均需 owner HTTPS 浏览器运行。     |
| A06 全部 viewer 关闭后同实例接续 | PARTIAL                 | 直接路径的同 PID 新浏览器画布为空，明确失败；[常驻 child](20260926-greenfield-resident-child.md)在私有 broker 重连后恢复同窗口和完整帧。                                                                                                         | 全部浏览器关闭后未保存 Code 缓冲、同一 container/PID/starttime、generation 和窗口仍可在新 profile 观察、编辑。           |
| A07 多设备、控制接管与续租       | PARTIAL                 | Runtime/Gateway 隔离门禁和[真实续租浏览器阶段](20260926-p0-a07-real-control-renewal-gate.md)已编写并静态检查。                                                                                                                                   | 两个不同 Gateway device ID 的 owner 浏览器同时观察/接管，旧输入被拒；30 分钟真实续租阶段尚未运行。                       |
| A08 Stop、退出、故障、Restart    | PARTIAL                 | [应用退出任务](20260926-p0-native-application-exit.md)记录隔离真实 Code File→Exit 与 SIGKILL 的 STOPPED/FAILED 消息，Runtime 持久化路径有聚焦测试。                                                                                              | owner 浏览器经 Gateway 验证真实终态、运行列表、Stop/Restart 新 generation 与 child 身份；隔离探针未证明持久化全链。      |
| A09 授权、不可用、失败           | PARTIAL                 | Gateway/Runtime 拒绝直连 raw proxy、旧 attachment 和控制代次；[失败门禁](20260926-lan-p0-failure-gate.md)的浏览器脚本已静态检查。                                                                                                                | owner 认证浏览器中运行 live media 撤销、Clipboard API 拒绝/超限、可见失败与登录/登出/撤销。                              |
| A10 交互性能与资源               | NOT_RUN                 | [集成任务](20260926-lan-p0-integration.md)的合成 PNG 编解码探针仅供容量参考。                                                                                                                                                                    | 真实 Code 至少 30 个 keydown→可见像素样本、最近秩 p95、RTT、浏览器、viewport/DPR、CPU/GPU/容器资源；≤100 ms 目标未验证。 |
| A11 Mac 与物理客户端             | SCOPE_UPDATED / NOT_RUN | [V3 方案](../structure-v3.md)改为标准 Chromium 内核验收，Mac 专项已退出 P0；Docker bridge 访问 LAN IP 不是第二物理设备。                                                                                                                         | 两个隔离浏览器 profile 仍需 owner gate；第二物理设备未测试，不能从 profile 推断。                                        |
| A12 回归与交付完整性             | PARTIAL                 | V2 兼容门禁曾通过，合并中 Web 检查阶段和生成检查曾通过；局部任务保留视觉 before/after/current 证据。                                                                                                                                             | 最终集成主线的 make generate 无差异、make check、完整阶段结果和任务矩阵回填；已有单项通过记录不能代替最终检查。          |

## 状态事实源

[docs/status.json](../status.json) 中 LAN HTTPS Password Entry 和 V3 Resident Greenfield Surface 均保持 scaffolded。更新的证据文字明确区分直接路径保存但重连失败、常驻 child 私有 broker 探针、真实 Code Exit/SIGKILL、Mousepad 帧和 Terminal fixture 测试，并列出 owner 门禁仍未运行。其余模块的 working 状态只指各自既有交付范围，不能外推为 V3 P0。

## 验证与交接

- 本 worktree 仅包含 docs/status.json 与本任务记录的源文件变更；主工作树的未提交浏览器门禁和截图由集成任务保留。
- 状态生成器会把 evidence 逐字写入 README 状态区块。按本任务的文件所有权约束，本分支不提交工具生成的 README；集成 owner 合并后运行 make generate，并提交工具生成差异。
- owner 认证的 LAN 浏览器门禁尚未运行，也没有真实 A01–A10 完整验收结果。集成 owner 收到本审计后负责最终 make check、浏览器阶段和按实际结果回填状态，不可依据脚本存在或隔离探针宣布 P0 完成。

## 本任务验证结果

- python3 -m json.tool docs/status.json 通过；两条目标模块仍为 scaffolded，证据均明确 owner-authenticated gate has not run。
- 本记录的 22 个本地链接均存在；git diff --check 通过。
- 在固定 Node 24.19.0 容器内对 docs/status.json 和本记录运行 Prettier --check 通过。
- 状态生成器 node tools/status/render.mjs 生成 README 状态区块后，--check 通过；只有对应两条 evidence 产生生成差异。根据本任务文件范围，已恢复本分支的 README，待集成 owner 在主线运行 make generate 并提交生成结果。
- 本分支未运行 owner 认证浏览器门禁，也未对正在集成的主线运行 make check；这些仍属于 P0 集成验收。
