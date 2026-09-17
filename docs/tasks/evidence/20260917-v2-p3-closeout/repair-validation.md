# P3 收尾修复接续实测

代码基线 `21b16f6`，单一分支 `feat/v2-p3-closeout`。用户授权验证后合并本地 main。
保留此前未提交改动及历史记录；历史 PASS 不代表本次重跑。未使用真实模型或 API 密钥。

## 已复现问题与修复

1. `sh tools/v2-p3-delivery/gate.sh` 基线位于 `tmp/v2-p3-delivery.qKAL1q`。
   原主链两个用例通过，F01 发布通过，F02 在六分钟内未发布。
   确认失败后停止该轮测试 runner，门禁退出 2，隔离栈清理完成。
   日志索引：`tmp/p3-repair-baseline.log`，数据库可见后续作业长期 queued。
2. Runtime 用一个作业的 timeout 限制整个最多八个作业的批次，导致后续作业预算被耗尽，
   数据库操作使用取消后的上下文。改为每个作业独立预算，进程退出则不再领取后续作业。
   前序日志仅凭慢速就归因为资源争抢并不充分，本次发现此代码路径必须修复。
3. Workload 原 launch mutex 不可取消、永不回收，且等待后忽略数据库错误/终态，
   把更新 generation 的 running 当成旧请求成功。改用可取消、引用计数回收的实例锁；
   启动、停止、重启和对账共享该锁，重读精确 generation/state 后才执行引擎动作。
4. Docker create 重名不代表容器正在删除。完整验证现有对象后接管；仅明确的
   marked-for-removal 冲突允许删除后重建，其他 409 不被混同。
5. 故障注入仅靠环境变量关闭不满足生产隔离。新增 `faultinject` build tag 和生产 no-op
   实现；普通构建即使设置目录也不暂停、不改租约、不丢回复。
   HTTP/2 丢回复用 `http.ErrAbortHandler` 终止流，原子消费一次标记；撤除屏障释放等待者。
6. 故障用例没有退役自己的安装，故意损坏的 B 会在用例结束后继续触发修复并占用队列。
   按用例通过公开 API 退役安装并取消剩余构建；仅浏览器/重启用例明确保留的安装例外。
7. F08 原测试只有一个 Runtime 执行者，没有证明接管。补独立 PostgreSQL repository/
   Docker 执行者共享包库，验证第二次实际执行、唯一成功包，以及旧 token renew/verdict 拒绝。
8. 增加真实 Docker 输出/测试/timeout/log-budget 矩阵；配置要求实跑时缺 Docker 即失败。
   prepare-only 不再打印主门禁 PASS；必选用例有 skip 时门禁失败。
9. Reliability 对已 cancelled 的构建仍无限轮询。取消与失败同为终态，清退修复轮询且
   不注册版本、不 Offer 部署；新增回归通过。
10. 篡改测试原先复用相同 A/B 字节，会使同 owner 的其他安装一起失效。fixture 在健康响应
    头中携带每个 App 的独立标识，使破坏性用例只影响自身内容地址。
11. F07 的源码漂移曾在 Core source 幂等键上被提前拒绝，没有抵达 Runtime；改为
    独立 source 请求键，真正向同一 Runtime task 提交漂移输入。

12. Core 注册候选时重新读取 current pin/revision，使旧 A 修复在 B 或用户 C 已上线后被
    错当成新基线继续发布。复用持久修复任务的不可变 version/revision，首次注册校验当前
    前提；重放返回原始前提，Reliability 将 superseded 注册清退为终态。F14 增加发布后
    注册重放断言；F18 增加构建完成前 owner 换版；F20 验证旧 B 修复不覆盖已恢复 A，
    新 A 修复仍可接续发布。复用已有 Proto 快照，无新契约或迁移。

13. Docker 文件数/体积用例补有效且可执行的入口，确保不会因另一项「缺入口」检查
    提前失败。补强后的 9 项实际 Docker 重跑全部通过：`tmp/p3-engine-limits-final.log`。

## 最终验证结果

- 单元/race：`tmp/p3-race-all.log` 覆盖 workload、buildtest、artifactstore、Reliability；
  `tmp/p3-race-fixes.log` 覆盖新生命周期与 HTTP/2 注入回归，均退出 0；`tmp/p3-registration-race.log` 另覆盖 Core orchestration 与取消/superseded 交接。
- 中间诊断 `tmp/p3-repair-fixed.log` / `tmp/v2-p3-delivery.eBE8Zg`：原主链 2/2、
  Docker failure matrix 9/9、Closeout 10/10、Windows 9/9 通过。发现注册基线缺陷后，
  停止旧二进制的 Matrix runner；退出 2 并清理，不把该轮算总 PASS。
- 最终 `sh tools/v2-p3-delivery/gate.sh` 退出 0：`tmp/p3-repair-final.log` /
  `tmp/v2-p3-delivery.DZU1Lw`。原主链 2/2、Closeout 10/10、Windows 9/9、Matrix 13/13，
  两个真实 Chromium E2E 各 1/1、三进程重启后 replay 通过。无必选测试 skip。
  gate 内 Docker engine 矩阵 9/9；加强容量用例后独立重跑亦 9/9。
  此轮含不可变修复前提与取消轮询；结束后 compose 与 Runtime namespace 容器均为空。
- `make generate` 两遍退出 0；生成文件校验和无漂移，首次为零生成差异；随后为修复快照语义更新 Proto 注释，Go/TS 注释生成差异为预期。
- `make check` 修复前后各整条退出 0（`tmp/p3-make-check-final.log` 与合并前 `tmp/p3-make-check-closeout.log`；固定 `workos-make:local` 通过 Docker socket 调用 Makefile 镜像）。
- `sh tools/v2-p3-delivery/gate.sh legacy` 退出 0：`tmp/p3-legacy-final.log` /
  `tmp/v2-p3-delivery.N5giCh`。Chain 四子例、Runtime restart、fallback、awaiting-manual、
  Matrix seed 与三项真实持久部署/回滚用例均通过。
- `sh tools/v2-completion/gate.sh` 退出 0：`tmp/p3-v2-completion-final.log` /
  `tmp/v2-completion.RgqcwX`。连续会话重启、文件/Artifact/Preview/问答、PTY 控制与
  Runtime 重启 fencing、授权/Workspace adapter 回归及真实 Chromium 旅程通过。
  三个视觉采集 case 按原门禁设计 skip（本轮无可见 UI 改动，不属于必选业务测试）。
- `sh tools/v2-p3-delivery/f26-grants-webbundle.sh` 退出 0：`tmp/p3-f26-final.log` /
  `tmp/v2-p3-f26.1OmWuc`（隔离栈 `tmp/v2-completion.ZCgHou`）。regression.sh 退出 0；
  6 项 grants/Web Bundle/owner rollback/Unavailable 集成全过；Chromium 三项全过。
  prepare、Go、浏览器和清理均退出 0，没有必选测试 skip。

本轮不改可见 UI，沿用已有 P3-A18 视觉证据。完整 P3 状态仍为 scaffolded；
不能把部分子场景或一个测试方法通过写成 F01–F27 全部通过。
P2 第二台物理 LAN、P4、rootless、Docker memory.high 边界保持不变。

## 实际工具链命令

宿主无 Go/Node/make，仓库命令通过已有固定镜像执行（未升级依赖）：

```sh
docker run --rm --user "$(id -u):$(id -g)" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  -v "$PWD:$PWD" -w "$PWD" workos-make:local make generate
# 同一 wrapper 将最后两个参数改为 make check，实际整条退出 0。
docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$PWD:/workspace" -w /workspace bufbuild/buf:1.55.1 \
  breaking --against '.git#ref=21b16f6'
```

buf breaking 相对真实起点 `21b16f6` 退出 0，仅更新 Proto 注释，没有线协议变化。
第二遍生成对 153 个 Proto/SQL query/README 文件校验和比较无漂移，
日志 `tmp/p3-generate-final.log`、`tmp/p3-generate-final-repeat.log`、
`tmp/p3-generated-final-result.txt`。

## 最终轮次身份样本（真实监督 F01）

固定 owner fixture，HTTP oracle 为 A=`P3-VALUE-0`、B=`P3-VALUE-42`。
容器 kill 后，同一 Workload 的 `unexpected_exit` 与 `restart_limit_exhausted` 为不同
violation；不是同一 occurrence 重复。发布来自前者，后者的旧修复无第二次部署。

| 事实                    | 值                                                                        |
| ----------------------- | ------------------------------------------------------------------------- |
| 故障 A Workload         | `01a0b192-86ac-7115-8597-8b007a840f9a`                                    |
| 发布来源 Incident       | `01a0b192-8e3a-7d81-a6d0-77c77a4a02d6`                                    |
| Task                    | `01a0b192-8e57-7366-bd1b-4a8232e62025`                                    |
| Job                     | `01a0b192-9229-75d1-abb6-4f324b7e3c53`                                    |
| Artifact                | `01a0b193-9071-702e-a962-22a4a6a70356`                                    |
| 包摘要                  | `sha256:0408ccd6ab1afeabe9af073d66439fb38e828782fc16532942adbc3c058caa59` |
| Manifest                | `sha256:f6aad8073ef977f3efb541bb395425ea6ff625e3edbbb6feabc030acd318677a` |
| 固定发布前提            | `1.0.0` / revision `2`                                                    |
| B Workload / generation | `01a0b193-9831-7bc9-b518-272b786adef6` / `1`                              |
| 台账                    | `promoted`                                                                |

通过本次隔离 DB 的 deployment_ledger → build_jobs → artifacts 只读关联核对，
原始索引 `tmp/p3-final-provenance.json`；没有修改产品表来制造发布结果。

F20 补充只读核对：`P3 closeout F20 serialize` 的第二份任务原始目标为
`1.0.1-repair.01a0b1a5a966710eb8bb67507ba895d9`，Runtime build 为 `succeeded`，
repair ledger 为 `terminal`，Core candidate mapping 不存在；随后以 A 为目标的新任务成功发布。这证明拒绝的是已构建成功但
目标退役的候选，不是把 fixture/构建失败当作前提保护通过。

## 交付边界

本轮已发现并复现的缺陷修复及上述门禁全部通过，允许合并本地 main。
完整 P3 仍为 scaffolded：并发漂移、部分精确中断、P3 canary 撤权、跨来源授权、真实配额
等剩余子场景见总任务 F01–F27。成功/失败资源清理已有实跑，INT/TERM 专项尚未验证；
SIGKILL 无法执行 trap，孤立 namespace 必须按门禁日志中的 project 精确补清理。
原共享开发栈与 `.zcode/plans/` 工作区资料保留；不把本轮结果扩大到物理 LAN/P4/rootless/memory.high。

合并前再次生成：`tmp/p3-generate-closeout.log` 退出 0；扩大至全部带生成标记的文件、
Proto/TS 目录和 README 共 212 文件，校验和无漂移（`tmp/p3-generated-closeout-before.json`）。
