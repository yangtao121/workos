# 安全策略

WorkOS 尚处于 foundation 阶段，不应暴露到公网。

## 当前安全边界

- 开发认证绕过只允许 `127.0.0.1`/`::1`。
- 非 loopback 绑定必须配置 TLS；但生产 device session 尚未实现，所以 foundation 仍不得用于
  LAN 或公网服务。没有开发绕过时 Gateway 明确拒绝请求。
- Generic CLI 只能运行服务端 allowlist 中配置的 executable，不接收用户提供的 shell 命令。
- v1 契约只允许传 credential reference；lease/broker 尚未实现，App 与 Agent Task API 不返回真实值。

请不要在公开 Issue 中提交 credential、日志原文、项目文件或模型对话。安全问题通过仓库所有者的
私密安全报告渠道提交。
