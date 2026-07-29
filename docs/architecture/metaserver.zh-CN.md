# MetaServer 架构

[English](metaserver.md) | 简体中文

## 范围与来源

Project Rebound 使用 Go 实现 Boundary MetaServer 协议。上游参考固定为
`Dubnium-105/BoundaryMetaServer master@d68e717`，生产环境不依赖 Node.js。41 个
protobuf 源文件、13 个内容定义、上游 commit、逐文件哈希、definitions 聚合哈希和
AGPL 声明固定保存在 `Backend/api/proto/metaserver` 与
`Backend/internal/metaserver/assets/definitions`。

生产代码使用静态生成的 Go protobuf 类型；definitions 被嵌入二进制并在启动时校验。
上游标记为 tentative 的字段在脱敏真实客户端抓包确认前，不进入会改变状态的匹配逻辑。

## 组件与信任边界

```text
浏览器/启动器 -- 匿名管道传 Access Token --> MetaTunnel
MetaTunnel -- HTTPS --> meta.dubnium.top -- FRP --> meta-server HTTP :8081
游戏 -- 本地 TCP --> MetaTunnel -- 验证证书的 TLS --> logic.dubnium.top:443
Logic 网关 -- TLS 终止 + 独立鉴权 FRP --> meta-server :6968

meta-server --> PostgreSQL meta_* 表和少量只读控制数据
meta-server --> Redis meta:* Gate Ticket
meta-server --> READY Game Server 与 Relay 注册表
Dedicated Server -- scoped token --> /internal/v1/meta/*
管理员 -- 可信网段 + 会话 + 权限 + Step-up --> /v1/admin/meta/*
```

`meta-server` 是独立进程和镜像，不与控制面共用 listener、数据库角色、Redis ACL
用户、FRP 用户、token、systemd unit 或回滚动作。客户端公网入口只有 443；6968、
6969、8000、8081 和 9000 只允许私网或回环访问。

## 身份流程

Browser 在现有控制面完成认证，通过 stdin 将 Access Token 交给 MetaTunnel。
MetaTunnel 不从命令行、环境变量、URL 或日志接收/输出 token，只绑定随机的
loopback HTTP/TCP 端口。

MetaTunnel 转发 `/connectServer` 时注入 bearer token。MetaServer 忽略旧字段
`playerId` 和 `loginToken`，从服务端会话获得玩家、认证会话、账号状态、客户端版本和
协议版本。服务端将以 SHA-256 为 Redis key 的 Gate 记录保存 60 秒，返回不透明的
256 位 Ticket。原生 Gate 握手通过 Redis `GETDEL` 原子消费；并发消费或重放会失败，
并记录重放指标和安全事件。

## 持久化与一致性

- 档案及所有玩家数据都引用 `players.id`。
- `(player_id, role_id)` 唯一标识角色配装。JSON 按固定 definitions 校验，并以
  `revision` compare-and-swap 更新。
- 武器档案同时保存原始 protobuf、解码 JSON 和 SHA-256，支持取证和迁移。
- 部分唯一索引保证一名玩家只能属于一个活动 Party 且只能有一个活动匹配 Ticket；
  Party 变更会锁定相关行。
- 调度器取得 PostgreSQL advisory transaction lock，以 `FOR UPDATE SKIP LOCKED`
  领取队列 Ticket 与 READY Game Server，并在同一事务中写入对局、名单、Ticket
  状态及 `READY -> RESERVED`。
- 普通回滚不回滚数据库；25–28 号迁移均为 additive。

## 可用性与动态发现

匹配只调度已注册 Dedicated Server，不隐式降级为玩家 P2P Host。无服务器时 Ticket
保持排队直至分配或超时；取消、超时、失败、服务器离线或连接期限届满会释放预留。

区域与 QoS 每次都从 Relay Registry 动态生成。只有心跳新鲜、READY 且接受新流量的
节点会返回。新增节点只需正常注册，不需要增加域名或修改 MetaServer 配置。

## 进程安全与可观测性

容器使用非 root 数字 UID、只读根文件系统、删除全部 capabilities、
`no-new-privileges`、tmpfs 及 CPU/内存/PID 限制。TCP 设置握手、读写和空闲超时，
最大帧 2 MiB，限制写队列、IP 连接数/建连速率和玩家/RPC 速率，并隔离 panic。
日志不记录 bearer token、Gate Ticket、完整 protobuf 帧或配装快照。

指标覆盖 HTTP、原生连接、RPC 延迟、畸形帧、Gate 签发/消费/重放、配装冲突、队列
深度、匹配耗时和 Relay QoS。另见[威胁模型](metaserver-threat-model.zh-CN.md)、
[原生协议](metaserver-native-protocol.zh-CN.md)和[部署手册](../operations/metaserver-deployment.zh-CN.md)。
