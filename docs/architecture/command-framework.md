# ProjectRebound 运行时指令框架

CommandFramework 是 Windows 桌面浏览器与游戏进程内 Payload 之间的本地命名管道协议。当前实现由 C++ 管道服务端和 .NET 管道客户端组成，不再使用旧 Python `PipeClient`。

## 实现位置

- Payload 服务端：`Payload/Communication/CommandFramework.h`、`CommandFramework.cpp`；
- Payload 接线：`Payload/dllmain.cpp`；
- .NET 客户端：`Desktop/ProjectRebound.Browser/Services/PipeClient.cs`；
- 启动与调用：`Desktop/ProjectRebound.Browser/Services/GameLauncher.cs`、`ViewModels/MainViewModel.cs`。

浏览器为每次运行生成管道名，通过 `-pipe=<name>` 传给游戏。Payload 创建 `\\.\pipe\<name>`，浏览器随后连接。

## 帧格式

每条消息是一行 UTF-8 文本：

```text
<command>\t<json>\n
```

- 命令和 JSON 之间是一个 Tab；
- 每帧以 LF 结束；
- JSON 必须是对象；
- 单帧最大长度由 Payload 当前的 64 KiB 读取缓冲限制。

示例：

```text
ping\t{}
join\t{"ip":"203.0.113.10:7777","token":"..."}
debug\t{"action":"status"}
```

## 当前命令

| 方向 | 命令 | 响应 | 说明 |
| --- | --- | --- | --- |
| Browser → Payload | `ping` | `pong` | 连接探测 |
| Browser → Payload | `join` | `join_ack` | 请求切换到 `ip` 指定的比赛；`token` 当前为预留字段 |
| Browser → Payload | `debug` | `debug_ack` | 执行 Payload 注册的调试回调 |
| Payload → Browser | — | `error` | JSON 无效、字段缺失或命令未知 |

`join` 要求非空字符串 `ip`。Payload 在监听线程中调用 Join 回调，并返回是否接受命令；实际加入结果仍由游戏连接流程决定。

## 生命周期与并发

- Payload 使用单个监听线程和 Overlapped I/O；
- 默认 30 秒没有读到数据会断开当前客户端并重建管道；
- 同一时刻只服务一个浏览器连接，断线后允许重连；
- `SendResponse` 使用互斥锁保护，可从其他线程调用；
- `Stop()` 会取消 I/O 并等待监听线程退出。

.NET `PipeClient` 当前采用严格的一问一答调用方式：发送一条命令后读取一行响应。调用方不得并行复用同一个实例发送多条请求，否则响应对应关系无法保证。

## 安全边界

命名管道只用于同一 Windows 主机上的浏览器和游戏进程，不应承载长期凭据或服务端管理密钥。当前 Payload 管道安全描述符允许本机任意进程连接，因此协议参数仍必须视为不可信输入；若未来传输敏感信息，应改为限定用户 SID 的 ACL，并增加消息级身份校验。

修改协议时必须同时更新 C++ 分发逻辑、.NET 客户端调用和本文命令表。
