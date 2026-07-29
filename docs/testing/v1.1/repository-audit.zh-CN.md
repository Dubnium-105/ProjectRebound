# V1.1 仓库审计

[English](repository-audit.md) | 简体中文

审计基线：`af4343a4cdc0b60836a417c182d0c65c0917c197`（`main`）

审计日期：2026-07-21

范围：以 `Backend/` 中的 Go 控制面与 Edge Relay 为主，同时检查契约、部署、监控、测试和仓库中的旧后端实现。

## 1. 当前目录结构

```text
ProjectRebound/
├── .github/workflows/          # CI 构建、GHCR 镜像和分离部署
├── Backend/
│   ├── api/
│   │   ├── openapi/            # HTTP OpenAPI 与权限矩阵
│   │   ├── proto/              # Relay mTLS gRPC 控制流
│   │   └── relay-protocol.md   # 当前 UDP Relay v1
│   ├── cmd/
│   │   ├── control-plane/      # 生产控制面入口
│   │   ├── edge-relay/         # 最小 Edge Relay 入口
│   │   └── main.go             # 旧 SQLite/UDP 单体入口
│   ├── internal/
│   │   ├── auth, admin, player
│   │   ├── gameserver, p2proom, connection
│   │   ├── relayregistry       # 控制面 Relay 注册、调度、迁移和 mTLS
│   │   ├── relayruntime        # Edge UDP 数据面
│   │   ├── observability, health, cache, database
│   │   └── http, server, db, store, udp, matchmaking, models
│   │       # 尚未删除的旧单体实现
│   ├── migrations/             # PostgreSQL 000001～000008
│   ├── deployments/            # Compose、监控、边缘节点和公网网关
│   ├── scripts/                # 构建、部署、备份、验证与回滚
│   └── tests/                  # k6 HTTP 负载与 Relay netem 矩阵
├── docs/                       # 外部/内部 API、CI/CD 与 Debian 运维
├── Desktop/                    # 客户端浏览器工具
├── Payload/                    # 游戏 Payload
└── Tools/                      # 文档和 NAT 测试工具
```

生产镜像只构建 `cmd/control-plane` 和 `cmd/edge-relay`。旧 `cmd/main.go`、SQLite 数据库与相应包仍可编译，但不应作为 V1.1 权威实现继续扩展。

## 2. 当前公开 API

权威路由位于 `internal/controlplane/server.go`，Schema 位于 `api/openapi/openapi.yaml`。

| 领域 | 方法与路径 | 当前鉴权 |
| --- | --- | --- |
| 健康 | `GET /health/live`, `GET /health/ready` | 无 |
| 认证 | `POST /v1/auth/bind`, `POST /v1/auth/refresh` | 无；bind 仅有 IP 限流 |
| 当前玩家 | `POST /v1/auth/logout`, `GET /v1/users/me` | Player Access Token |
| Dedicated Server | `POST/GET /v1/game-servers`, `GET/DELETE /v1/game-servers/{id}`, `POST .../heartbeat` | 注册 Token、Server Token 或公开读 |
| P2P 房间 | `GET/POST /v1/p2p-rooms`，查询、加入、离开、心跳、开始、删除 | 公开读；写入要求 Active Player，房主操作另需 Host Token |
| 连接 | `POST /v1/connections`, `GET/DELETE /v1/connections/{id}` | 参与者 Access Token |
| 实时 | `GET /v1/realtime/connect` | Access Token + WebSocket Upgrade |
| 更新 | `/v1/updates/check`, `/v1/updates/{platform}/{version}/manifest`, `/v1/updates/files/{file_id}` | 无 |
| 客户端配置 | `GET /v1/client/config` | 无 |

当前缺少 V1.1 的玩家 Session 列表/撤销接口、邀请码接口和认证风险查询接口。`/auth/bind` 请求仅接受 `steam_id`、`persona_name`；未知字段会被拒绝，因此任务书中的可选字段尚不兼容。

## 3. 当前内部与管理 API

| 接口 | 当前保护和用途 |
| --- | --- |
| `/v1/admin/players*` | Admin Token + trusted CIDR；查询、修改玩家和撤销全部 Session |
| `POST /internal/v1/relay-nodes/enroll` | 一次性 Bootstrap Token；签发 Node Token 与证书 |
| `POST /internal/v1/relay-nodes/{id}/certificate/renew` | Node Token；换新 CSR、证书和 Keyset |
| `GET /internal/v1/relay-nodes[/...]` | Admin Token + trusted CIDR；动态 Relay 清单 |
| `POST .../{id}/drain`, `resume`, `revoke` | Admin Token + trusted CIDR；节点状态迁移 |
| `GET /internal/metrics` | trusted CIDR；Prometheus 抓取 |
| RelayControl `Connect` | TLS 1.3 mTLS 双向流，TCP 9090（生产经 FRP/SNI 网关暴露） |
| Edge `/metrics` | 默认 `127.0.0.1:9100`，仅本地监控 agent 抓取 |

控制流采用 `google.protobuf.Struct` envelope。Edge 上报 `Hello`、`Heartbeat`/`CapacityReport`、`TrafficReport`、allocation 开关事件和运行错误；控制面下发配置、Keyset、Drain、allocation 撤销、证书轮换提示与 Shutdown。

## 4. 当前数据库表和迁移

迁移器使用 PostgreSQL advisory lock、每迁移事务、SHA-256 checksum 与 `schema_migrations`，已应用迁移不可静默修改。当前迁移：

| 版本 | 主要表/变更 |
| --- | --- |
| `000001_baseline` | 版本基线 |
| `000002_auth` | `players`, `auth_sessions`, `auth_login_audit_logs` |
| `000003_admin` | `admin_audit_logs` |
| `000004_game_servers` | `game_servers` |
| `000005_p2p_rooms` | `p2p_rooms`, `p2p_room_members` |
| `000006_connections` | `connections`, `connection_candidates`, `connection_path_checks` |
| `000007_relay_registry` | `relay_bootstrap_tokens`, `relay_nodes`, `relay_allocations`, `relay_node_audit_logs` |
| `000008_relay_migrations` | `MIGRATING_RELAY` 状态、allocation 失败原因、`relay_migrations` |

尚无 `auth_risk_events`、规范化的 `auth_login_events`、独立 `auth_refresh_tokens`、`invite_codes`、`invite_code_uses`、`relay_node_credentials`、`relay_signing_keys`。`relay_allocations` 没有 bound 时间、close reason 和双向字节持久化字段。更新 Manifest 当前来自部署目录，不存在任务书列出的 `update_releases`/`update_files` 数据库表。

## 5. 当前认证和 Session 流程

1. Bind 校验 16～20 位数字 SteamID，规范化 persona name。
2. 在事务内按唯一 SteamID upsert 玩家；并发 bind 由唯一约束保证只创建一个玩家。
3. 创建 `auth_sessions` 行，Refresh Token 使用 48 字节安全随机数并只保存 SHA-256。
4. Access Token 为 Ed25519 JWT，带 player、session、provider、auth level 和 token version。
5. Refresh 使用 `SELECT ... FOR UPDATE`，创建新 Session 行并将旧行标记 `ROTATED`。
6. 再用旧 Refresh Token 会撤销整个 `token_family_id`，记录 `REFRESH_TOKEN_REUSE`，而每次 Access 鉴权都会查询 Session，故撤销可立即生效。
7. Logout 撤销当前 Session；管理员可撤销玩家的全部活动 Session。

已实现 Refresh rotation/reuse 的核心安全语义。缺口包括：只按 IP 对 bind 使用进程内令牌桶；Redis 未用于认证风控；Device ID 仅从 `X-Device-Id` 读取并以明文存入 Session；没有设备/SteamID/组合维度限流、邀请码、风险事件、失败登录规范表、玩家 Session 管理接口和 IP 脱敏展示。

## 6. 当前 Relay BIND 协议

当前标记为 `ProtocolVersion=1`，但已经实现任务书中多项安全机制：

```text
BIND(token) -> CHALLENGE(cookie) -> BIND_PROOF(cookie, token) -> BIND_OK(handle, role)
```

- Cookie 为 HMAC，绑定源 IP/端口、Token hash 与当前/上一时间桶；Relay 不保存未验证 challenge 状态。
- Challenge 固定 38 字节，代码保证不大于 BIND 请求。
- HOST 与 PEER 都完成 bind 后才转发。
- DATA 包含随机 64 位 handle、role、64 位 sequence、16 字节 HMAC tag 和不透明 payload。
- 每端维护 64 位 replay window；重复或窗口外包丢弃。
- 数据包不携带任意目标地址，无法把 Relay 用作通用 UDP 转发器。

与目标 V2 的差异：BIND_INIT 没有 client nonce 和 requested MTU；Cookie 间接绑定 Token 而不是显式 allocation/client nonce；没有 v1 兼容开关；默认最大 datagram 为 1280 而不是目标默认 1200；协议名称、数据密钥派生标签和文档均仍为 v1。

## 7. 当前 Relay Token 格式

Ed25519 `relay+jwt` 已包含并校验：`iss`、`aud`、`kid`、`jti`、`relay_node_id`、`allocation_id`、`connection_id`、`room_id`、`endpoint_role`、`protocol`、`nbf`、`exp`、`allocation_expires_at`、`max_bps`、`max_pps`、`max_total_bytes`。

Edge 按 `jti` 缓存 allocation、role、源 endpoint 和过期时间；同 endpoint 重试幂等，不同 endpoint 重用会拒绝。当前不支持 NAT 端口变化后的受控重新 challenge 更新；Keyset 只是无版本、无整体签名的公钥数组。

## 8. 当前 Relay 节点注册和控制通道

- Bootstrap Token 只保存 hash，并在成功注册事务中消费。
- Edge 本地生成 Ed25519 私钥和 CSR；`identity.json` 以 0600 原子保存 Node Token、私钥、证书、CA 和 Keyset。
- 生产必须显式配置持久 Relay CA、Relay Token 私钥与 Bootstrap Token。
- Node 证书当前默认有效 24 小时；进程启动时剩余不足 1 小时尝试续签。
- mTLS 服务校验证书 CA，并在 Hello 时按 fingerprint、有效期和节点状态绑定数据库记录。
- Revoke 将节点设为 `REVOKED` 并下发 Shutdown；随后续签与 Hello 均被拒绝。

缺口：数据库只在 `relay_nodes` 保存当前 fingerprint/expiry，没有证书序列历史、撤销原因和轮换审计表；证书只在 Edge 启动时续签，没有运行中自动续签调度；没有按证书有效期比例配置阈值。

## 9. 当前故障迁移能力

节点 sweeper 按心跳/证书期限把节点转为 `UNHEALTHY`/`OFFLINE`，Scheduler 只选择 `READY` 且容量低于阈值的节点。迁移 sweeper 会：

1. 找到故障节点上的活动 allocation；
2. 使用数据库唯一索引保证每条 connection 最多一个 `BINDING` migration；
3. 选择另一 READY 节点并创建新 allocation；
4. 标记旧 allocation failed，连接进入 `MIGRATING_RELAY`；
5. 下发旧 allocation 撤销和新的 Relay Token/WebSocket 事件；
6. 新节点报告 `AllocationOpened` 后完成 migration，并发出 migrated 事件。

当前缺少 migration deadline、尝试次数、候选节点历史、最大重试、超时后的下一节点选择和无节点时的明确最终失败闭环。Drain API 请求体不支持自定义 deadline 或 `migrate_existing`，自然 Drain 不主动迁移；Revoke/故障会触发已有迁移 sweeper。

## 10. 当前指标和日志

控制面已有 HTTP counter/histogram、auth bind 成败、Refresh reuse、房间、服务器、Relay 状态/allocation、数据库池、Go goroutine/内存和 Relay 集中遥测。Edge 已有 allocation、收包/转发/丢包、转发字节、bind 成败、非法 Token、限流丢包和控制通道指标。

主要缺口：没有 `http_active_requests`、分维度 auth 限流、风险/邀请码/刷新总量、WebSocket 重连、迁移成败、Redis 延迟、后台任务耗时/失败；Edge 缺少 bind init/challenge、Cookie/Token replay、认证失败、超大包、replay drop、入口字节、节点负载、goroutine 和内存等细分指标。

日志使用结构化 `slog`，认证错误只记录错误码/内部 error，未发现主动输出完整 Token 或私钥的代码。仍需在 V1.1 增加专门的秘密扫描测试和日志契约测试。

## 11. 当前部署方式

- CI 在 Linux 上执行 gofmt、模块校验、vet、race tests、构建、OpenAPI/Compose/脚本/文档校验，并发布按完整 commit SHA 固定的 control-plane/edge-relay GHCR 镜像和 provenance attestation。
- CD 可分别部署控制面与边缘节点；使用独立 GitHub Environment、固定 SSH host key 和不可变镜像引用。
- 控制面 Compose 包含 PostgreSQL、Redis，可选 Prometheus/Grafana；不挂载 Docker Socket。
- Edge 使用独立 Compose/raw Docker、host networking 和持久 identity volume，不连接 PostgreSQL/Redis。
- `remote-deploy.sh` 在控制面发布前备份，健康检查失败会尝试上一 release/image。
- 公网由 Cloudflare HTTP 代理 + HAProxy SNI 网关 + 独立 FRP QUIC 回源；Relay mTLS 使用独立 FRP，配置和服务隔离。

缺口：应用启动时自动迁移，没有独立 Expand/Migrate/Contract gate；没有统一 release preflight、数据库 schema/协议/build metadata；镜像仍产生 `main` 浮动标签（部署虽不使用）；备份没有加密、分层保留、异地上传和周期恢复演练；Prometheus 没有版本控制的告警规则。

## 12. 与 V1.1 冲突或重叠的旧实现

1. `cmd/main.go` 及 `internal/http|server|db|store|udp|matchmaking|models` 是 SQLite/旧 UDP 单体，与 PostgreSQL 控制面和 Edge Relay 并存；V1.1 不应在两套实现中重复开发。
2. 根 `config.yaml`、`matchserver.db`、`deploy/` 属于旧部署路径，容易被误认为生产权威入口。
3. Relay 已具有 V2 目标的大部分包格式和安全语义，却仍公开命名为 v1；直接改版本会破坏已部署客户端，必须用兼容开关和明确迁移策略。
4. Refresh Token 已实现 rotation/reuse，但数据模型是“每个 refresh 一条 Session”，与计划中的独立 refresh-token 表不同；迁移时应保持现有 Token 立即撤销语义并采用 expand-first。
5. 当前认证 bind IP 限流位于通用中间件且为单进程内存状态，不能满足多维、Redis 一致和风险审计要求。
6. 现有迁移能完成单次 Relay 替换，但不满足重试、超时和 Drain 强迁移的完整状态机；不得把现状误报为 Release Gate 已通过。

## 13. 拟修改和新增的文件

以下为当前审计后的预计范围；每个 Milestone 会保持独立提交，并在实现中细化：

- 认证与数据库：新增 `migrations/000009_auth_security.sql`，修改 `internal/auth/*`、`internal/config/config.go`、`internal/controlplane/server.go`、`internal/observability/metrics.go`；新增 invite、risk、session、Redis/local limiter 领域文件与测试。
- API：修改 `api/openapi/openapi.yaml`、权限矩阵、`docs/api/external.md` 和 `docs/api/internal.md`。
- Relay V2：修改 `internal/relayruntime/{protocol,cookie,token,runtime,config,metrics}.go`、对应测试、`api/relay-protocol.md` 和 edge 示例配置；保留受控 v1 兼容路径。
- Relay 资源与迁移：新增后续 expand migration，修改 `internal/relayregistry/{model,repository,service,http,migration_sweeper,token,authority,control}.go`、connection 实时事件和集成测试。
- 密钥/证书：新增 signing key、node credential repository/service、轮换后台任务、Keyset 版本/签名和证书自动续签测试。
- 测试工具：新增 `cmd/load-bot/`、`internal/loadbot/`、标准场景与报告 Schema；扩展 `tests/load/`。
- 弱网/故障：新增 `scripts/netem/`、`scripts/chaos/`、`tests/netem/` 场景和 `docs/operations/runbooks/chaos-testing.md`。
- 备份恢复：新增 Linux `scripts/backup/postgres-{backup,restore}.sh`、校验/保留脚本、systemd timer 示例和恢复演练报告。
- 监控：拆分/扩展 Grafana dashboards，新增 Prometheus rule files、配置校验与告警测试。
- 发布：新增 `scripts/release/preflight.sh`、版本/build metadata、迁移 gate、回滚 runbook 和发布清单；调整 CI 镜像/测试矩阵。
- V1.1 文档：持续维护 `docs/testing/v1.1/` 中的基线、协议、迁移、密钥、备份、测试、发布和最终验收报告。

## 审计结论

当前实现已覆盖 V1.1 目标中较难的部分基础能力，但尚未达到任务书的完整 Release Gate。实施应复用现有 PostgreSQL 事务、Relay 数据面和迁移骨架，避免重写；优先补齐认证滥用面，再将现有 Relay v1 安全协议以兼容方式升级并补全可观测性、生命周期和运维证据。
