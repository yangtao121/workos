# V3 P0 Greenfield 实验运行说明

默认 Native 引擎仍是 Xvfb/WebRTC。只有显式设置下面的变量才走 Greenfield，失败不会切回旧引擎。

```sh
WORKOS_RUNTIME_NATIVE_ENGINE=greenfield
WORKOS_RUNTIME_NATIVE_GREENFIELD_PROXY=/opt/greenfield/packages/compositor-proxy-cli/dist/main.js
WORKOS_RUNTIME_NATIVE_GREENFIELD_APP=/usr/share/code/code
WORKOS_RUNTIME_NATIVE_SCRATCH=/var/lib/workos/greenfield
```

代理必须监听 `127.0.0.1`，并设置 `RENDERER_ALLOW_SOFTWARE=1`。
`OpenGreenfieldDisplay` 返回 `/native/greenfield/{session}/code`。Gateway 在现有会话鉴权后把该路径转到 Runtime，Runtime 再转到本机会话端口，并把响应里的 `127.0.0.1` 改写成浏览器可访问的地址。

构建实验镜像：

```sh
docker build -t workos-greenfield-runtime:p0 -f deploy/greenfield-runtime.Dockerfile .
```

GitHub 用 `https://ghfast.top/https://github.com/...`，Debian 用华为云，npm 用 npmmirror。版本见
[versions.md](../tasks/evidence/20260924-v3-p0-native-experience/versions.md)。

停止实验进程使用现有 Surface Stop。关闭窗口只 Detach。清理本轮镜像：

```sh
docker rmi workos-greenfield-runtime:p0
```

浏览器合成画布还没有链进桌面。`GreenfieldApp` 只显示连接事实，不把空画面当成应用。
