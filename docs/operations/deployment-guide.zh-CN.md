# ProjectRebound 控制面与边缘节点分离部署手册

[English](deployment-guide.md) | 简体中文

本文档对应 `Backend/cmd/control-plane` 和 `Backend/cmd/edge-relay`。

## 1. 部署拓扑

```text
客户端 / Dedicated Server --HTTPS/WSS--> Cloudflare Tunnel --> Control Plane
                                                            Caddy / Go / DB
                                                                    ^
                                                                    |
Edge Relay A/B --TLS 1.3 mTLS gRPC--> relay.example.com:443（灰云 DNS）
                                                                    |
                                                               FRPS 公网网关
                                                                    |
                                                          FRPC 独立实例（控制面）
                                                                    |
                                                               127.0.0.1:9090
```

控制面与边缘节点必须位于不同的 Compose 项目中，可以在不同主机、不同机房运行。边缘节点主动连接控制面，不连接 PostgreSQL、Redis 或 Grafana。控制面没有公网 IPv4 时，Cloudflare Tunnel 继续负责 HTTP API；独立 FRP 网关只做 TCP 字节转发，不终止 mTLS。

## 2. 主机与端口

实测 1 vCPU/1.9 GiB 主机可完成所有功能测试，但 100 VU 同机压测的 HTTP P95 为 941.62 ms，不能满足 200 ms 验收线。正式控制面建议从 4 vCPU、4 GiB 内存、SSD 起步，并在独立主机运行 k6。

### 2.1 Cloudflare Tunnel 传输调优

使用包管理器安装并保持 `cloudflared` 更新。至少使用 `2026.5.2`，使启动流程自动检查 DNS、UDP/QUIC 7844、TCP/HTTP2 7844 和 Cloudflare API；检查结果存在失败时不要继续压测。查看版本和最近一次预检：

```bash
cloudflared --version
sudo journalctl -u cloudflared -b --no-pager |
  grep -Ei 'CONNECTIVITY PRE-CHECKS|precheck|Registered tunnel connection'
```

Cloudflare 通常推荐 `--protocol auto`，让 connector 优先使用 QUIC 并在 UDP 不可用时回退 HTTP/2。但协议可连接不代表链路质量足够稳定：若日志反复出现 `no recent network activity`、QUIC stream timeout 或 HA 连接数下降，应通过 `systemctl edit --full cloudflared.service` 固定为 HTTP/2，再进行同源 A/B 测试。当前生产控制面经实测使用以下参数：

```text
cloudflared --no-autoupdate tunnel --protocol http2 --edge-ip-version 4 run --token <TUNNEL_TOKEN>
```

这里显式使用 IPv4，是因为当前控制面自动 IPv4/IPv6 选路会把连接分散到 LAX/SJC，而 IPv4 能稳定保持四条 LAX HTTP/2 连接。该选择是部署点相关的，迁移网络或机房后必须重新测试，不应直接复制为所有环境的默认值。

修改后至少验证：

```bash
sudo systemctl daemon-reload
sudo systemctl restart cloudflared
sudo systemctl is-active cloudflared
curl -fsS http://127.0.0.1:20241/metrics |
  grep -E '^cloudflared_tunnel_(ha_connections|request_errors|server_locations)'
curl -fsS https://<PUBLIC_API_HOST>/health/ready
```

`cloudflared_tunnel_ha_connections` 应为 `4`，请求错误应保持为 `0`。从固定的外部负载机重复相同 k6 场景，比较 P50、P95、错误率和 RPS；不要用控制面本机生成的流量代替公网验收。若 connector 长期只能连接远端 PoP，物理 RTT 会成为 Cloudflare Tunnel 参数无法消除的下限，此时应增加更近的 connector/origin，或改用带公网地址的 HTTP 网关。

若采用“公网网关运行 connector、私网控制面运行 origin”的结构，应为 `boundary.<DOMAIN>` 创建独立 named tunnel，并只发布这个 hostname。不要把承载其他域名的共享 tunnel token 直接部署到网关：同一 tunnel 的 replicas 没有固定流量导向保证，新请求可能进入任意就近 replica，且远程 ingress 配置会随 tunnel 一起下发。建议拓扑为：

```text
Client -> Cloudflare edge -> gateway cloudflared
       -> gateway loopback-only HTTP origin
       -> isolated QUIC FRP/WireGuard/Tailscale path
       -> control-plane 127.0.0.1:18081
```

回源端口必须只绑定网关回环地址；回源 FRPS/FRPC 要使用独立用户、配置目录、token、systemd unit 和控制端口，不得复用下方 mTLS FRP 实例。先用临时 hostname 做同源 A/B，确认性能和健康检查后再切换 `boundary.<DOMAIN>` 的 published application route。切换后保留旧 connector 至少一个观察窗口作为回退，但不要让共享 tunnel 与专用 tunnel 同时宣告同一个 hostname。

当前生产网络的实测结论是：控制面 connector 的 10 VU/1 分钟 P95 为 1.05 s；LAX 网关 connector 加独立 QUIC 回源降至 531 ms，错误率均为 0。网关方案改善约 49%，但仍未达到 200 ms 验收线，因此更近的 origin/connector 或控制面迁移仍是最终性能整改项。Quick Tunnel 只用于 A/B，不得作为生产入口。

若 Cloudflare Zero Trust 因账户或支付方式无法开通，可使用普通橙云 HTTP 代理加自建 SNI 网关，不需要 Tunnel。公网网关由 HAProxy 接管 443：`boundary.<DOMAIN>` 终止 HTTPS 后经独立 FRP QUIC 回源，Relay mTLS hostname 则以原始 TLS 透传到回环 FRPS。API origin 只允许 Cloudflare 官方地址段，mTLS 域名保持灰云且允许合法 Relay 直连。Cloudflare SSL 模式必须为 Full (strict)，不得长期使用 Flexible。完整配置和证书续期流程见 `Backend/deployments/public-http-gateway/README.md`。

控制面入站规则：

| 端口 | 来源 | 用途 |
| --- | --- | --- |
| TCP 22 | 运维网段 | SSH |
| TCP 80/443 | 公网或 Cloudflare Tunnel | Caddy HTTP/HTTPS、WebSocket、Relay 注册 |
| UDP 443 | 公网 | Caddy HTTP/3，可选 |
| TCP 9090 | 仅 `127.0.0.1` | FRPC 到 Relay TLS 1.3 mTLS gRPC 控制流 |
| TCP 18080 | 仅 `127.0.0.1` | 管理 API、直接健康检查、指标 |
| TCP 5432/6379/9091/3000 | 仅 `127.0.0.1` | PostgreSQL、Redis、Prometheus、Grafana |

边缘节点规则：

| 方向 | 端口 | 用途 |
| --- | --- | --- |
| 入站 UDP | 8443，或配置的游戏 Relay 端口 | Relay 数据面 |
| 入站 TCP | 22，仅运维网段 | SSH |
| 出站 TCP | 控制面 443 | 首次注册、证书续签 |
| 出站 TCP | mTLS 网关 443 | mTLS gRPC 控制流 |
| 本机 TCP | 127.0.0.1:9100 | Relay Prometheus 指标 |

公网 mTLS 网关额外开放 TCP 443 给边缘节点，并将 FRPS 控制端口 TCP/UDP 7000 限制为仅控制面出口地址或两机 VPN 地址可达。mTLS 域名必须使用 Cloudflare DNS Only（灰云）；橙云代理不支持此任意 TCP mTLS 通道。完整配置、systemd 隔离和验收命令见 `Backend/deployments/public-mtls-gateway/README.md`。

## 3. Debian 前置准备

控制面和每台边缘节点都执行：

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl git jq openssl docker.io docker-compose
sudo systemctl enable --now docker
sudo docker version
sudo docker compose version
```

如果安装被异常中断，先确认卡住的 PID 确实属于 apt/dpkg，再依次执行：

```bash
ps -ef | grep -E 'apt|dpkg'
sudo dpkg --configure -a
sudo apt-get -f install
sudo dpkg --audit
```

不得在未核对 PID 的情况下批量终止进程。

网络无法访问 Docker Hub 时，可选配置可信镜像代理。镜像代理可以看到请求的镜像名和来源 IP，生产环境应使用自建缓存；测试环境若使用第三方代理，需先接受该隐私边界。例如：

```json
{
  "registry-mirrors": ["https://docker.m.daocloud.io"]
}
```

保存到 `/etc/docker/daemon.json` 后执行 `sudo systemctl restart docker`。Go 模块下载的官方默认值是 `https://proxy.golang.org,direct` 和 `sum.golang.org`；官方端点不可达时，可在 `.env` 中启用已经验证过的备用值：

```text
GOPROXY=https://goproxy.cn,direct
GOSUMDB=sum.golang.org https://sum.golang.google.cn
```

## 4. 选择发布来源

生产环境推荐使用 GitHub Actions 产出的不可变 GHCR 镜像。CI 为 Control Plane、MetaServer 和 Edge Relay 发布 `sha-<40 位提交>` 镜像，Deploy 工作流只向目标机传输 Compose、验证及回滚脚本的小型 release bundle，然后拉取选定镜像；目标机不需要 Go、编译缓存或永久保存完整 Git 仓库。

各部署入口都支持 `DEPLOY_SOURCE`：

- `ci`：要求对应的 `CONTROL_PLANE_IMAGE`、`META_SERVER_IMAGE` 或 `EDGE_RELAY_IMAGE` 是 `ghcr.io/...:sha-<40 位提交>`，只拉取 CI 镜像；
- `source`：使用当前检出的源码执行 Docker Compose/BuildKit 本机构建；
- `auto`（默认）：检测到合法的 GHCR SHA 镜像时使用 `ci`，否则使用 `source`。

自动 CD 始终显式设置 `DEPLOY_SOURCE=ci`。只在离线开发或排障时使用源码模式；手工源码模式需要检出仓库：

```bash
git clone <PROJECT_REPOSITORY_URL> project-rebound
cd project-rebound/Backend
```

手工使用私有 GHCR 镜像前先执行 `docker login ghcr.io`。部署账号只需要目标 package 的 `read:packages` 权限。

## 5. 部署控制面

### 5.1 生成密钥与环境文件

```bash
cd project-rebound/Backend
chmod +x scripts/*.sh deploy/deploy.sh
./scripts/generate-control-plane-env.sh
chmod 600 deployments/control-plane/.env
```

生成器创建相互独立的 Ed25519 Access Token、Relay Token、更新签名密钥、设备指纹 HMAC 密钥、32 字节 VNT 房间秘密加密密钥、MinIO root/应用凭据，以及彼此独立的十年期 Relay CA 与 Game Server CA。它不会覆盖已有 `.env`，也不会输出密钥正文。重建时必须保留 `GAME_SERVER_CA_*` 和 MinIO 凭据；直接替换会使现有 Dedicated Server 证书无法正常续期或使下载对象失去管理访问。

部署支持证书身份的 Dedicated Server 版本前，使用下列检查确认旧环境已经包含两个值，同时不输出秘密正文：

```bash
env_file=deployments/control-plane/.env
test "$(grep -Ec '^GAME_SERVER_CA_(CERT|KEY)_PEM_BASE64=[A-Za-z0-9+/=]+$' "$env_file")" -eq 2
```

检查失败时，通过批准的秘密生成流程单独创建 Game Server CA，并在部署前补齐两个值；不得替换整个环境文件，也不得复用 Relay CA。缺少任一值时，生产 Compose 会在更改运行版本前拒绝部署。当前代码已不读取旧的 `GAME_SERVER_REGISTRATION_TOKENS`；确认全部节点使用数据库中的实例绑定 Registration Token 后，应从旧环境删除该变量。

向已有生产环境首次引入 VNT 支持前，执行以下检查且不打印密钥：

```bash
env_file=deployments/control-plane/.env
vnt_key="$(sed -n 's/^VNT_SECRET_ENCRYPTION_KEY_BASE64=//p' "$env_file")"
test "$(printf '%s' "$vnt_key" | base64 -d | wc -c)" -eq 32
unset vnt_key
```

若缺失，先为现有环境文件创建权限为 `600` 的备份，再通过批准的秘密管理器生成正好 32 个随机字节，把标准 Base64 结果保存为 `VNT_SECRET_ENCRYPTION_KEY_BASE64`，然后重新检查。生产 Compose 会拒绝空值，应用也会在对外服务前拒绝缺失或格式错误的值。该密钥用于加密每个房间的 VNT network token、E2E password 和幂等 Host Token 恢复材料，必须保持稳定并单独备份。若所有仍被引用的密钥副本都丢失，数据库中的 VNT 房间秘密将无法解密。

VNT 秘密密钥轮换使用显式 keyring：设置新的唯一 `VNT_SECRET_ENCRYPTION_KEY_ID`，把 `VNT_SECRET_ENCRYPTION_KEY_BASE64` 替换为新的 32 字节密钥，并把旧密钥按 `key_id=base64_key` 加入分号分隔的 `VNT_SECRET_DECRYPTION_KEYS`。重启 Control Plane 后，必须验证旧幂等建房和旧 VNT 房间 bootstrap，才能移除历史密钥。新建/rebind 房间只使用当前活动 key ID；旧密钥保持只读，直到 `p2p_rooms.host_token_key_id` 与 `p2p_vnt_sessions.secret_key_id` 都不再引用它。重复/非法 key ID 或错误密钥会导致启动失败。

编辑 `deployments/control-plane/.env`：

- 将 `CORS_ALLOWED_ORIGINS` 改成真实客户端来源；多个来源用逗号分隔。
- 将 `UPDATE_CDN_BASE_URL`、`UPDATE_REALTIME_URL`、`UPDATE_STUN_SERVERS` 改成真实地址。
- 测试/IP 模式保留 `PUBLIC_API_SITE=http://:80` 和 `PUBLIC_API_HTTP_PORT=8080`。
- 域名生产模式设置 `PUBLIC_API_SITE=api.example.com`、`PUBLIC_API_HTTP_PORT=80`；DNS A/AAAA 指向控制面并开放 80/443，Caddy 自动申请证书。
- 默认下载存储使用同机 MinIO。把 `MINIO_S3_SITE`、`DOWNLOADS_SITE`、`DOWNLOAD_S3_ENDPOINT`、`DOWNLOAD_PUBLIC_BASE_URL` 与 `MINIO_CORS_ALLOWED_ORIGINS` 中的示例域名一起替换；S3 和下载域名的 DNS A/AAAA 同样指向控制面。公开基址必须包含桶名，MinIO Console 只允许通过 `127.0.0.1:MINIO_CONSOLE_PORT` 的 SSH 隧道访问。
- 当 FRPC 与控制面同机部署时，`RELAY_CONTROL_BIND_IP` 必须保持为 `127.0.0.1`；只有 FRPC 位于另一台可信私网/VPN 主机时才改为对应私网地址，不应直接绑定 `0.0.0.0`。
- `RELAY_CONTROL_SERVER_NAMES` 必须包含边缘节点使用的 `control_server_name`，例如 `control-plane,localhost,relay.example.com`。
- 签名密钥 ID 在轮换时必须更新，不能在密钥变化后继续复用旧 ID。
- `DEVICE_FINGERPRINT_HMAC_KEY_BASE64` 必须保持稳定并单独备份；生产环境缺失时会拒绝启动。服务端不保存硬件因子原文，因此该密钥丢失后无法重算已有设备摘要。在多密钥迁移流程可用前，不得变更它或 `DEVICE_FINGERPRINT_KEY_ID`。
- 当前 `VNT_SECRET_ENCRYPTION_KEY_BASE64` 与全部 `VNT_SECRET_DECRYPTION_KEYS` 必须采用同等级备份保护。开发环境在活动密钥为空时可以创建临时密钥，但生产环境绝不会这样做；临时密钥会在重启后使已有 VNT 房间秘密失效，也不适合共享 staging。不得把同一个 `VNT_SECRET_ENCRYPTION_KEY_ID` 复用于不同密钥字节。
- `STEAM_APP_ID` 与票龄设置仅为旧配置和测试夹具兼容而保留，不参与真实 ticket 准入。镜像内置独立的 Go `/usr/local/bin/decrypt-ticket` verifier，但官方 Steamworks Linux `libsdkencryptedappticket.so` 与该应用的 32 字节 encrypted-ticket key 必须分别通过 `STEAM_ENCRYPTED_APP_TICKET_LIBRARY_HOST_PATH` 和 `STEAM_ENCRYPTED_APP_TICKET_KEY_HOST_PATH` 提供。两者均只读挂载，绝不打入镜像。key 文件可以是正好 32 个原始字节或 64 个十六进制字符。宿主机上应保持 root 所有者，将文件组设置为容器 `app` 的 GID（固定为 `999`），权限设置为 `0440`；包含它的宿主目录保持 root 所有且权限为 `0700`。root 所有的 `0600` key 无法被容器内非 root 进程读取。部署脚本会在替换现有容器前以 `app` 身份执行无效密文探针；只有 key 与原生库均可加载时才允许发布。verifier 仅从 stdin 接收 ticket，并在 stdout 输出受限 JSON；控制面不包含、也不会回退到进程内 Steam 解密算法。
- 将规范的 ToolBox 证书放到 `TOOLBOX_PUBKEY_HOST_PATH`，并只读挂载到 `TOOLBOX_PUBKEY_PATH`。每次完整性 proof 都会哈希 PEM 的精确字节（包括换行符），不得重新排版或转换成 base64；生产环境缺少该设置时拒绝启动。`INTEGRITY_CHALLENGE_TTL_SECONDS` 默认为 120，除非客户端和事件响应策略同时更新，否则 `INTEGRITY_MAXIMUM_FAILURES` 必须保持为 3。

`.env` 必须保留在主机秘密存储中，权限必须为 `600`，不得提交 Git、复制进镜像或写入工单。
`minio-data` volume 必须进入独立的异机备份计划；它不是 PostgreSQL 备份的替代品，也不能只依赖单机单盘副本。

同机 MinIO 必须保持 `DOWNLOAD_S3_ENDPOINT=http://minio:9000`，并将公网 S3
域名配置到 `DOWNLOAD_S3_UPLOAD_ENDPOINT`；前者供控制面校验和清理，后者只供浏览器预签名上传。

### 5.2 更新描述符

按照 `Backend/deployments/updates/README.md` 把非秘密发布描述符放到 `Backend/deployments/updates`。生产模式缺少有效发布描述符或安全更新 URL 时会拒绝启动。大文件必须放在对象存储/CDN，API 仅返回下载元数据。

### 5.3 启动与验证

```bash
DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh
./scripts/verify-control-plane.sh
```

部署脚本会：

1. 拒绝包含 `CHANGE_ME` 或 `example.com` 的环境文件；
2. 强制 `.env` 权限为 `600`；
3. 校验 Compose；
4. 拉取 CI 控制面镜像，并启动 PostgreSQL、Redis、控制面、Caddy、Prometheus、Grafana；
5. 应用当前数据库迁移并等待 `/health/ready`；
6. 失败时输出受限的末尾日志并返回非零状态。

不需要本机监控栈时：

```bash
ENABLE_MONITORING=0 DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh
```

仅在需要从当前检出源码构建时运行 `DEPLOY_SOURCE=source ./scripts/deploy-control-plane.sh`。

查看状态：

```bash
sudo docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml --profile monitoring ps
curl -fsS http://127.0.0.1:18080/health/ready
```

部署包含 VNT 支持的版本时，先保持 `VNT_ROOMS_ENABLED=false`，直到 migration 已执行到 `000038_vnt_security_audit.sql`，`player_feature_grants`、`vnt_nodes`、`p2p_vnt_sessions`、`vnt_security_audit_logs` 均存在，至少一个兼容节点健康，且 ToolBox 运行时门控通过。必须先设置精确且区分大小写的 CSV 白名单 `VNT_ALLOWED_VNTS_VERSIONS` 与 `VNT_ALLOWED_WRAPPER_VERSIONS`；任一列表为空或格式错误时，启用 VNT 会导致启动配置校验失败，房间创建/rebind 也会拒绝版本不在白名单中的节点。`VNT_CREDENTIAL_ROTATION_GRACE_SECONDS` 控制旧 Node Token 仅用于 heartbeat 的轮换重叠期，默认 60 秒，允许范围为 `1..600`；它不是旧 Token 的管理权限延长期。`VNT_MAX_NODES_PER_PLAYER` 默认为每位玩家最多 3 个非 `RETIRED` 节点，允许 `1..100`。独立限流默认值为每玩家每小时 5 次 enrollment、每来源 IP 每分钟 120 次目录查询、每玩家每分钟 30 次房间 bootstrap、每凭据每分钟 120 次 heartbeat、每凭据每小时 10 次 rotate/retire；分别通过 `VNT_ENROLLMENT_REQUESTS_PER_PLAYER_PER_HOUR`、`VNT_DIRECTORY_REQUESTS_PER_IP_PER_MINUTE`、`VNT_BOOTSTRAP_REQUESTS_PER_PLAYER_PER_MINUTE`、`VNT_HEARTBEAT_REQUESTS_PER_CREDENTIAL_PER_MINUTE`、`VNT_MANAGEMENT_REQUESTS_PER_CREDENTIAL_PER_HOUR` 调整。不能只凭 HTTP liveness 推断迁移成功；`/health/ready` 与 migration/table 检查必须同时通过。然后设置 `VNT_ROOMS_ENABLED=true`，只重建 Control Plane 容器，并在开放客户端流量前验证 `/v1/client/config` 返回 `features.vnt_rooms=true`。重新设为 `false` 会阻止新的 VNT 房间创建/rebind，同时允许已有会话排空。

启用 VNT 前还要确认 `/internal/metrics` 中 `vnt_nodes_compatible_online >= 1`、`vnt_node_credentials_expired == 0`，且没有活动的 `NoCompatibleVNTNodeAvailable` 或 `VNTNodeCredentialExpired` 告警。`VNTNodeCredentialExpiringSoon` 应立即转为凭据轮换工单，不能等到运行时鉴权失败。

从运维机访问监控：

```bash
ssh -L 9091:127.0.0.1:9091 -L 3000:127.0.0.1:3000 user@CONTROL_HOST
```

## 6. 部署第一台边缘节点

### 6.1 在控制面准备一次性凭据

`RELAY_BOOTSTRAP_TOKENS` 的格式为 `credential_id=token`，多个凭据用分号分隔。生成器已经创建第一条。通过秘密管理器把等号右侧 token 值传给对应边缘节点，不要在聊天、日志或命令输出中展示。

每台新边缘节点使用不同 credential ID 和不同随机 token。旧 token 一经注册即在数据库中标记为已消费，不能复用。

### 6.2 配置边缘节点

在边缘主机：

```bash
cd project-rebound/Backend
cp deployments/edge-relay/.env.example deployments/edge-relay/.env
cp deployments/edge-relay/config.edge-relay.yaml.example \
   deployments/edge-relay/config.edge-relay.yaml
chmod 600 deployments/edge-relay/.env
```

编辑 `.env`，只在首次注册时设置 `EDGE_RELAY_BOOTSTRAP_TOKEN`。编辑 YAML：

- `control_plane_url`：公网 HTTPS API，例如 `https://api.example.com`。
- `control_addr`：稳定的 mTLS 网关地址，例如 `relay.example.com:443`；使用私网/VPN直连时可填写 `10.20.0.10:9090`。
- `control_server_name`：必须与 `control_addr` 使用的证书域名及控制面 `RELAY_CONTROL_SERVER_NAMES` 一致，例如 `relay.example.com`，不应改成 IP。
- `advertised_endpoints[].host`：客户端实际可达的公网 IP 或域名。
- `advertised_endpoints[].port`：公网映射后的 UDP 端口。
- `region`、`zone`、`provider`、容量和带宽：填写该节点真实信息。

分离式边缘 Compose 使用 Linux host networking，因此 `127.0.0.1:9100` 指标可由主机上的 Prometheus/agent 抓取，同时不会暴露到公网。常规监控不要求为每台新节点配置独立抓取链路：Relay 会复用 mTLS 控制流上报累计遥测，控制面的 `/internal/metrics` 统一输出全部注册节点。节点本地 9100 抓取只作为可选的故障诊断增强。

### 6.3 首次启动

```bash
chmod +x scripts/deploy-edge-relay.sh
DEPLOY_SOURCE=ci \
EDGE_RELAY_IMAGE=ghcr.io/<owner>/projectrebound-edge-relay:sha-<40-char-commit> \
  ./scripts/deploy-edge-relay.sh
```

脚本等待 `relay control connected`，随后自动把边缘 `.env` 中的一次性 token 清空，并强制重建容器再次连接。这一步同时验证 `/edge-relay-data/identity.json` 已持久化。不要删除 `project-rebound-edge-relay_edge-relay-data` 卷，否则必须签发新的 Bootstrap Token 重新注册。

确认监听和本地指标：

```bash
sudo ss -lunp | grep ':8443'
curl -fsS http://127.0.0.1:9100/metrics
sudo docker compose --env-file deployments/edge-relay/.env \
  -f deployments/edge-relay/docker-compose.yaml logs --tail=50 edge-relay
```

在控制面通过回环管理端口查询节点：

```bash
curl -fsS 'http://127.0.0.1:18080/internal/v1/relay-nodes?limit=100' \
  -H 'Authorization: Bearer ADMIN_TOKEN'
curl -fsS http://127.0.0.1:18080/internal/v1/relay-nodes/RELAY_NODE_ID \
  -H 'Authorization: Bearer ADMIN_TOKEN'
```

期望状态为 `READY`。

## 7. 添加、下线和恢复边缘节点

添加节点时生成新的高熵 token，将新的 `id=token` 追加到控制面 `RELAY_BOOTSTRAP_TOKENS`，重部署控制面，再按第 6 节部署边缘节点。

计划维护：

```bash
curl -X POST http://127.0.0.1:18080/internal/v1/relay-nodes/NODE_ID/drain \
  -H 'Authorization: Bearer ADMIN_TOKEN'
```

确认现有 allocation 排空后停止边缘容器。恢复后调用 `/resume`。证书或节点凭据泄漏时调用 `/revoke`，该操作不可逆；被撤销节点必须使用新身份重新注册。

控制面重建、升级或重启不得更换 `RELAY_CA_*`。只要 CA 和边缘身份卷保持不变，边缘节点会自动重连。Relay CA 轮换需要双 CA/双证书迁移方案，当前版本不能通过直接替换完成无中断轮换。

## 8. 备份、恢复与升级

创建并校验 PostgreSQL custom-format 备份：

```bash
./scripts/backup-control-plane.sh /srv/project-rebound-backups
```

备份目录权限为 `700`，备份文件为 `600`。将备份加密复制到另一主机/区域，并定期恢复到隔离数据库验证。生产恢复步骤：停止控制面写入、保留当前数据库备份、使用 `pg_restore --single-transaction --clean --if-exists` 恢复到明确数据库、重新运行迁移，然后执行冒烟测试。恢复是破坏性操作，不由自动部署脚本执行。

升级推荐通过 GitHub Actions 的 Deploy 工作流完成：选择目标环境和节点，填写已经通过 CI 且仍存在于 GHCR 的完整 commit SHA。工作流会先备份控制面数据库，再拉取同一 SHA 的镜像、执行健康检查，并在失败时恢复上一 release。

必须在主机上手工升级控制面时：

```bash
./scripts/backup-control-plane.sh /srv/project-rebound-backups
DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh
./scripts/verify-control-plane.sh
```

边缘节点可逐台 drain，再使用同一 CI commit SHA 运行 Deploy，或按第 6.3 节以 `DEPLOY_SOURCE=ci` 运行 `./scripts/deploy-edge-relay.sh`。应用回滚时部署仍存在于 GHCR 的上一个已验证 SHA；只有在迁移不向后兼容且已有恢复计划时才回滚数据库。

## 9. 完整验收

发布前至少执行：

```bash
cd Backend
go vet ./...
go test ./... -count=1
./scripts/verify-control-plane.sh
```

然后执行：

- 真实 PostgreSQL 集成测试和迁移测试；
- Auth bind/refresh/logout、封禁权限；
- Dedicated Server 注册/心跳/注销；
- P2P 创建/加入/离开/启动；
- WebSocket candidate 交换和 Relay fallback；
- Relay drain/resume、控制面重建后重连；
- 公共 DNS 灰云解析、无客户端证书被拒绝、有效中继证书正向 mTLS 握手和服务端证书自动轮换；
- 更新 Manifest 签名和文件 SHA-256；
- 备份恢复；
- 独立 network namespace 中的 `tests/netem/run-relay-matrix.sh`；
- 独立负载机上的 `tests/load/control-plane.js`。

性能验收要求 HTTP P95 `< 200 ms`、HTTP 失败率 `< 1%`、检查成功率 `> 99%`、WebSocket upgrade P95 `< 1 s`。功能成功不等同于性能门槛通过，报告中必须分别给出结果。

## 10. 安全不变量

- PostgreSQL、Redis、Grafana、Prometheus、直接控制面 HTTP 和 Relay mTLS 后端端口只能绑定回环地址。
- FRPS 只允许远端代理 TCP 443；控制端口 7000 不向任意公网来源开放，FRPC 必须与已有面板/应用 FRPC 隔离。
- 公网 Caddy 对 `/v1/admin*` 和 `/internal/*` 返回 404，仅放行 Relay 注册和证书续签。
- Admin Token 与玩家 Token、Game Server Token、Relay Token 不可互换。
- Access、Refresh、Admin、Bootstrap、Relay、Game Server Token 和私钥不得进入日志。
- 边缘节点不拥有数据库或 Redis 凭据，不解析游戏 Payload。
- 不在生产物理网卡运行 netem；只使用隔离 namespace/veth。
- 不把 `.env`、`identity.json`、数据库备份或签名私钥提交 Git。

外部 API 见 `docs/api/external.md`，内部与 Relay API 见 `docs/api/internal.md`，机器可读契约见 `Backend/api/openapi/openapi.yaml`。

通过 GitHub Actions 构建 GHCR 镜像、配置 staging/production Environment、自动备份部署和回滚的方法见 `docs/operations/ci-cd.md`。
