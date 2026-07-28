# MetaServer 故障 Runbook

[English](metaserver.md) | 简体中文

## 首次响应

1. 记录告警时间、环境、部署镜像 digest、request ID 及受影响玩家/对局/服务器 ID；
   不复制凭据或 protobuf body。
2. 依次检查 `meta-server` readiness、容器状态、PostgreSQL/Redis dependency、
   Meta FRPC/FRPS、HAProxy 和证书有效期。
3. 对比 Gate 重放、畸形帧、队列深度、调度、HTTP/RPC 延迟、Relay readiness 和
   Game Server 可用性指标。
4. 重启任何组件前保留日志和审计记录。MetaServer 可独立重启；有活动 allocation 的
   Relay 不得重启。

## Readiness 失败

- PostgreSQL：确认 28 号迁移已应用且受限 Meta 角色权限仍在；不得通过授予全 schema
  写权限绕过。
- Redis：确认 `projectrebound-meta` ACL 用户启用且只能访问 `meta:*`。Redis 重启会
  使未消费 Gate Ticket 失效，客户端需要重新申请会话。
- Schema/config：对比镜像发布记录中的协议/DB/definitions 版本，检查启动哈希校验。
- 若新镜像引发失败，只回滚 MetaServer 镜像；additive 迁移继续保留。

## HTTP 正常但 Logic 失败

1. 确认 `logic.dubnium.top` 是灰云/DNS only 且解析到网关。
2. 使用 SNI 和证书验证运行 `openssl s_client`。
3. 依次检查 HAProxy SNI 路由、回环 16969、Meta FRPS 7002、控制面 Meta FRPC 和
   回环 16968。
4. 确认其他 FRP 服务没有复用 Meta 用户、token、配置目录、端口或 unit。
5. 不得把 6968/6969 暴露公网作为临时绕过。

## Gate 重放或畸形帧突增

先排除客户端发布缺陷，仅对确认的攻击来源在网关阻断。采集计数和短元数据，不采集
Ticket 或完整帧。疑似玩家凭据被盗时撤销相关认证会话。只有 tunnel 身份可能失陷时
才轮换 FRP token；轮换 FRP 不会撤销玩家会话。

## 匹配队列堆积

检查调度 leader 指标和 PostgreSQL lock，再按状态、mode、region、version、容量、
token 过期和心跳年龄列出 Game Server。没有兼容 READY 服务器时等待是预期行为，
不得开启 P2P fallback。预留卡住时使用有审计的管理取消接口，使对局取消和
`RESERVED -> READY` 在同一事务完成。

## QoS 或 Relay 发现故障

检查 Relay Registry state、心跳新鲜度、load state 和公网 UDP endpoint。
DRAINING、REJECT_NEW、UNHEALTHY 或 OFFLINE 节点会被有意排除。使用精确且有界的
`0x59` 请求测试，不发送超大探测包。QoS 限流不得改变正常的认证 Relay 流量。逐台
Relay 滚动，且必须先 Drain allocation。

## 凭据或数据泄漏

在各自所属系统撤销泄漏的玩家/管理员/Game Server 凭据；Meta FRP token 泄漏时单独
轮换。Gate Ticket 在 60 秒 TTL 后无需清数据库，但应检查重放证据。若日志包含凭据或
完整快照，先限制日志存储访问、保留证据并启动安全事故，再做脱敏/保留期清理。

## 恢复验收

要求 readiness 正常、HTTP 和 Logic TLS 路径有效、新 Gate 完成一次签发和单次消费、
配装读写/冲突测试、一次单人/Party 匹配、Dedicated Server 鉴权、动态 Relay/QoS
测试、错误率稳定且无新增 FRP 重连。先运行 canary，再恢复常规长稳门禁。
