# W02 / W03 自测记录

## 真实桌面 Code 进程

命令：`docker run workos-greenfield-runtime:p0 sh tools/v3-p0-native-experience/launch-code.sh`

- HTTP 201，spawn pid 的 exe 是 `/usr/share/code/code`
- 参数包含官方 Electron 二进制，不是 code-server
- 没有任何浏览器连接时，数秒后同一 pid 仍在，子进程是 zygote
- Xwayland 已监听 `:1`，但没有 `/dev/dri/renderD128`，EGL 未初始化
- `xdotool search` 会卡住，因为浏览器侧的 X 窗口管理器没有连上
- 因此这次没有通过桌面 Code 把 `note.txt` 改写并保存

这证明进程在浏览器消失后还在，不证明窗口可操作或未保存缓冲可恢复。

## 引擎单测

`go test ./internal/runtime/nativehost/adapters/greenfield/` 通过。

- 缺二进制时 Available 失败
- Detach 后子进程仍在，并写过标记文件
- Connect(SDP) 返回 `ErrWrongEngine`
- 剪贴板在未连接时不返回旧值；超限拒绝；Stop 后读失败
- Stop 把显示标成已退出

- 浏览器合成器使用 npmmirror 上已发布的 `@gfld/compositor@1.0.0-rc1` 预编译包（含 wasm）。
  在镜像里用 Emscripten 从源码编译 xkbcommon 会长时间停在交叉编译，已停止该路径。
- Gateway 把 `/native/greenfield/` 在会话鉴权后转到 Runtime；Runtime 再转到 `127.0.0.1` 上的代理，并改写响应里的回环地址。
- 桌面 Native 窗口在 `session.engine === "greenfield"` 时挂合成画布。xvfb 仍走原来的视频路径。

## 尚未完成

## 浏览器合成器连接（2026-09-24）

源码里的 Emscripten 交叉编译停在 xkbcommon，日志长时间不前进。该构建已停止。
浏览器侧改用 npmmirror 上的 `@gfld/compositor@1.0.0-rc1` 预编译包。

同一网络里的 Chromium 打开 `greenfield-proof.html` 后：

- 控制台有 `Session created` 和 `[ProtocolChannel] - open`
- Code 进程仍是原来的 pid 922，exe 为 `/usr/share/code/code`
- 画布是白的，没有窗口像素。容器没有 `renderD128`，EGL 未初始化，所以没有帧
- `/fixture/note.txt` 仍是 `line-one`，没有完成编辑保存

截图：`proof-canvas.png`。它只说明画面没有出来，不能当作清晰度通过。
