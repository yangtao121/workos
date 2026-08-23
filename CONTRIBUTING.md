# 贡献指南

## 工作流

1. 从 `docs/tasks/TEMPLATE.md` 创建任务文件，文件名使用 `YYYYMMDD-short-name.md`。
2. 将任务范围限制在一个明确的领域或一条纵向链路；跨协议变更先提交 ADR。
3. 使用 Conventional Commits：`feat:`、`fix:`、`refactor:`、`docs:`、`test:`、`build:`。
4. 提交前运行 `make check`；涉及数据库或进程交互时再运行 `make test-integration`。
5. 更新任务记录和 `docs/status.json`，然后运行 `make docs`。

新模块使用统一脚手架，例如
`make scaffold-module PROCESS=workos-core NAME=calendar`。允许的进程名就是六个稳定二进制名；
命令遇到已有目录会直接失败。

## 代码约定

- Go package 小写单词；公开符号必须有用途清晰的注释。
- 错误应携带操作上下文，但不得重复记录或泄漏 secret。
- TypeScript 开启严格模式，不允许 `any` 绕开生成协议。
- SQL 使用显式列名；migration 向前兼容，禁止服务启动时自动 migration。
- 配置启动即校验，安全相关配置不允许静默 fallback。

## Pull Request 门禁

- 生成代码无漂移。
- Proto 无破坏性变更。
- Go、TypeScript、SQL 和架构依赖检查通过。
- 新 migration 能从空库执行。
- README 展示的状态与机器可读状态一致。
