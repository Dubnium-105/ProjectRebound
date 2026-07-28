# MetaServer 原生协议

[English](metaserver-native-protocol.md) | 简体中文

## 传输层

客户端只允许经 MetaTunnel 连接 `logic.dubnium.top:443`。MetaTunnel 使用 Windows
系统信任库并以 `logic.dubnium.top` 为服务器名称校验证书。公网网关终止 TLS，再将
字节流经独立、启用 TLS 且使用 token 鉴权的 Meta FRP 通道转发。公网没有明文原生
listener。

每个帧的结构为：

```text
uint32_be payload_length | protobuf RequestWrapper payload
```

解析器支持 TCP 拆包与粘包。payload 长度必须为 1 字节至 2 MiB。连接设有握手、帧
读、帧写和空闲超时；响应通过单一有界写队列串行发送。

## Gate

第一个会改变状态的请求必须携带已认证 HTTP 会话接口返回的单次 Gate Ticket。服务端
原子消费 Ticket，将连接绑定到其中的玩家 ID、认证会话 ID、客户端版本、协议版本和
签发时间。以下情况会断开连接：

- Ticket 过期、未知、并发消费或重放；
- 协议版本不匹配；
- 旧消息中的身份与连接身份不一致；
- 连续畸形帧或非法 protobuf；
- 触发速率限制或写队列滥用。

## 已确认的 RPC 行为

生产 wrapper 只映射已审查的 RPC 标识。Gate/status、Party 创建/准备/Presence、区域
发现、playlist 发现、匹配开始及匹配状态/停止兼容响应均使用静态生成的消息。未知 RPC
返回兼容错误，不会使连接崩溃。

上游字段编号仍为 tentative 的 `QueryUnityMatchmakingRes` 不用于发布匹配 endpoint。
在脱敏抓包确认原生字段映射前，权威 endpoint 仅从已认证 HTTP Ticket 资源获得，避免
猜测字段进入生产状态机。

## 数据包处理规则

- 不记录完整帧或解码后的配装。
- 不把未知字段回显到另一玩家会话。
- Keepalive 不延长 Access Token 或 Gate Ticket 有效期。
- 在高成本 protobuf 或数据库工作前执行帧和 RPC 限制。
- `Backend/internal/metaserver/testdata/golden` 的样本均已脱敏，不含生产身份或凭据。

协议来源及精确上游 commit 记录在
`Backend/api/proto/metaserver/UPSTREAM.zh-CN.md`。
