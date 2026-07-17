# 游戏元服务器仓库审计

审计基线：`merge-upstream` 分支，提交 `4a5741e`。审计完成时工作树无本地改动。

## 1. 当前目录与模块

仓库同时包含 Unreal/C++ Payload、桌面服务器浏览器、服务器启动器、共享 C# 合约以及 `Backend/` Go 服务。现有 Go 后端是单进程：`cmd/main.go` 同时启动 HTTP、UDP rendezvous、UDP relay、UDP QoS、房间清理器和两套 matchmaking。

后端现有模块：

- `internal/config`：YAML 配置和少量环境变量覆盖。
- `internal/db`：SQLite 打开逻辑、代码内建表和数据库实体。
- `internal/http`：路由、鉴权以及直接访问 SQL 的 handler。
- `internal/lifecycle`：房间、探测、匹配票据和内存 Relay 的过期清理。
- `internal/matchmaking`：P2P 和 metaserver 匹配器。
- `internal/store`：NAT、Relay、Token 内存状态。
- `internal/udp`：rendezvous、probe、QoS 和内嵌 UDP Relay。

## 2. 当前已有 API

- 健康：`GET /health`。
- 认证：`POST /v1/auth/guest`。
- 主机探测：`POST /v1/host-probes`、`POST /v1/host-probes/{probeId}/confirm`。
- NAT：`POST /v1/nat/bindings`、`POST /v1/nat/bindings/{bindingToken}/confirm`。
- 房间：`POST/GET /v1/rooms`、`GET /v1/rooms/{roomId}`、join、leave、heartbeat、start、end。
- 打洞：创建、查询和完成 punch ticket。
- Relay：`POST /v1/relay/allocations`。
- P2P 匹配：创建、查询、删除 matchmaking ticket。
- 兼容接口：`POST /server/status`、`GET /v1/servers`、`/matchmaking/*`。
- 未实现 stub：`POST /v1/rooms/{roomId}/host-migration/`。

任务书所列 Steam bind/session、Admin、Dedicated Server、`/v1/p2p-rooms`、connection/realtime、update/client config API 均不存在。

## 3. 当前数据库表

当前数据库为 SQLite，启动时以 `CREATE TABLE IF NOT EXISTS` 建表，没有版本化迁移：

- `players`
- `host_probes`
- `rooms`
- `room_players`
- `match_tickets`
- `legacy_servers`

## 4. 当前认证流程

`POST /v1/auth/guest` 接受或生成 device token，将 SHA-256 保存为 `players.device_token_hash`，并把原 token 返回客户端。Bearer 中间件再次哈希 token 后直接查 `players`。当前没有 SteamID、独立 Access Token、Refresh Token、session family、rotation、reuse detection、logout 或管理员撤销。非 `Active` 玩家被所有受保护接口直接拒绝。

## 5. 当前 Relay 代码

控制面进程直接监听 UDP。HTTP 分配 room-scoped 随机 secret，客户端向 Relay 发送包含 secret 的 JSON 注册包；Relay 在进程内记录 endpoint，再做 host 与同房间 client 间的 UDP 转发。已有 UDP socket、内存路由、过期清理和压缩原型，但没有独立边缘二进制、签名 Token、cookie challenge、重放窗口、数据面认证标签、PPS/带宽限制、节点注册/调度/Drain、mTLS/gRPC 或故障迁移。

## 6. 与任务冲突的旧实现

- SQLite 与 PostgreSQL 目标冲突，代码内建表也不是可回滚的版本化迁移。
- guest/device token 模型与 Steam bind、Access/Refresh session 模型冲突。
- HTTP 响应缺少统一 `data/error + request_id` envelope。
- 健康检查只有 `/health`，没有依赖就绪检查。
- handler 混合 SQL、鉴权和业务逻辑。
- 玩家、房间和连接状态枚举及 API 路径与目标模型不同。
- `match_tickets` 保存明文 host/join ticket。
- 内嵌 Relay 与独立最小边缘运行时冲突。
- 仓库中不存在 Steam OpenID 三接口，因此无需删除不存在的 OpenID 代码。

## 7. 可复用代码

可复用的基础包括 YAML/环境变量配置加载、`slog`、Request ID/recovery 中间件骨架、安全随机 token 与定时比较、HTTP 优雅关闭、NAT 探测思路、房间/匹配生命周期经验、UDP socket/worker/压缩实现。

## 8. 拟删除或替换文件

按 Milestone 渐进替换 `cmd/main.go`、`internal/db/sqlite.go`、代码内迁移、guest auth 与旧 Bearer 模型。内嵌 `internal/udp/relay.go` 和旧 relay store 在独立 Edge Relay 可用后退出主路径。旧 room/matchmaking/legacy handler 先作为兼容层隔离，待新 API 验证后再移除，避免一次性破坏当前客户端。

## 9. 拟新增文件

新增 `cmd/control-plane`、`cmd/worker`、`cmd/edge-relay`，以及 auth/player/admin/gameserver/p2proom/connection/realtime/relayregistry/relayscheduler/relaytoken/relayruntime/update/database/cache/middleware/observability/jobs 等模块；同时新增版本化 SQL migrations、OpenAPI/proto、Compose/Caddy/monitoring、unit/integration/contract/load/netem 测试和运维脚本。

## 10. Milestone 修改范围

1. 基础工程：新控制面入口、配置、PostgreSQL、Redis、迁移、HTTP 基础设施、健康检查、OpenAPI 和 Compose。
2. 认证：Steam bind、Access/Refresh session、rotation/reuse detection、logout 和 me。
3. 管理：玩家状态/VIP、管理员鉴权、审计和 session 撤销。
4. Dedicated Server：注册、心跳、公开查询、注销和超时任务。
5. P2P：房间生命周期、目录、成员与封禁权限。
6. Connection：候选交换、状态机和 WebSocket。
7. Relay Registry：节点注册、证书、心跳、调度和 Drain。
8. Edge Relay：独立 UDP runtime、Token、challenge、转发和速率限制。
9. Relay 迁移：故障检测、重新调度和客户端事件。
10. Update：版本检查、确定性 Manifest、Ed25519 和下载跳转。
11. 上线：指标、日志、压测、弱网、备份和部署文档。
