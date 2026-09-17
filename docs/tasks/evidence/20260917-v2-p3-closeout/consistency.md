# F27 一致性检查记录（2026-09-17）

接续后的整条 `make check` 已退出 0：`tmp/p3-make-check-final.log`。以下为前序记录；
当前实际命令和生成一致性见 [repair-validation.md](repair-validation.md)。

宿主无 `make`，以下按 Makefile 定义翻译为 docker 命令执行（镜像与参数与
Makefile 完全一致）。

| 项              | 等价 make 目标                                                                                                                           | 结果                                                                                                     |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| 代码生成幂等    | `make generate` ×2（buf 1.55.1 + sqlc 1.30.0）                                                                                           | PASS：两遍后 `git status gen/ sdk/` 无差异                                                               |
| proto 检查      | `make proto-check`（buf format --diff --exit-code / buf lint / sqlc vet）                                                                | PASS                                                                                                     |
| Go 检查         | `make go-check`（gofmt -l cmd internal tests 为空 + `go vet ./...` + `go test ./...`）                                                   | PASS：80 包 ok，0 FAIL                                                                                   |
| Web 检查        | `make web-check`（architecture + eslint + prettier + pnpm -r check + desktop-web build）                                                 | PASS（对本任务新增 markdown 先执行 prettier --write 后通过）                                             |
| README 状态区块 | `node tools/status/render.mjs --check`                                                                                                   | PASS（render 后过；status.json 先前更新未重渲染，本轮补齐）                                              |
| 竞争检测        | `go test -race`：buildtest、artifactstore、reliability(application+transport)、workload(dockerapp+application+…)、faultinject、appbundle | PASS（修复 faultinject 测试自身的数据竞争：handler goroutine 写布尔与断言读无同步，改 channel 关闭语义） |
| 空白检查        | `git diff --check`                                                                                                                       | PASS（修复任务记录 EOF 多余空行）                                                                        |

限制：`make check` 未整条跑（等价子项已分别全过）；race 未覆盖全部仓库包，
仅按任务书列出的受影响面。
