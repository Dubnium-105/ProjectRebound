# ProjectRebound 控制面与边缘节点分离部署手册

本文档对应 `Backend/cmd/control-plane` 和 `Backend/cmd/edge-relay`。旧的 SQLite/systemd MatchServer 部署已经废弃；兼容入口 `Backend/deploy/deploy.sh` 会转到新的控制面部署脚本。

## 1. 部署拓扑

```text
客户端 / Dedicated Server
          |
       HTTPS/WSS
          v
  Control Plane 主机
  Caddy -> Go Control Plane -> PostgreSQL / Redis
                  |
          TLS 1.3 mTLS gRPC :9090
                  |
       +----------+----------+
       v                     v
 Edge Relay A           Edge Relay B
 UDP :8443              UDP :8443
```

控制面与边缘节点必须位于不同的 Compose 项目中，可以在不同主机、不同机房运行。边缘节点主动连接控制面，不连接 PostgreSQL、Redis 或 Grafana。

## 2. 主机与端口

实测 1 vCPU/1.9 GiB 主机可完成所有功能测试，但 100 VU 同机压测的 HTTP P95 为 941.62 ms，不能满足 200 ms 验收线。正式控制面建议从 4 vCPU、4 GiB 内存、SSD 起步，并在独立主机运行 k6。

控制面入站规则：

| 端口 | 来源 | 用途 |
| --- | --- | --- |
| TCP 22 | 运维网段 | SSH |
| TCP 80/443 | 公网 | Caddy HTTP/HTTPS、WebSocket、Relay 注册 |
| UDP 443 | 公网 | Caddy HTTP/3，可选 |
| TCP 9090 | 仅边缘节点网段/VPN | Relay TLS 1.3 mTLS gRPC 控制流 |
| TCP 18080 | 仅 `127.0.0.1` | 管理 API、直接健康检查、指标 |
| TCP 5432/6379/9091/3000 | 仅 `127.0.0.1` | PostgreSQL、Redis、Prometheus、Grafana |

边缘节点规则：

| 方向 | 端口 | 用途 |
| --- | --- | --- |
| 入站 UDP | 8443，或配置的游戏 Relay 端口 | Relay 数据面 |
| 入站 TCP | 22，仅运维网段 | SSH |
| 出站 TCP | 控制面 443 | 首次注册、证书续签 |
| 出站 TCP | 控制面 9090 | mTLS gRPC 控制流 |
| 本机 TCP | 127.0.0.1:9100 | Relay Prometheus 指标 |

优先通过 WireGuard、Tailscale 或私有网络开放 9090。必须使用公网时，只允许已知边缘节点地址；该端口本身仍强制双向证书认证。

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

## 4. 准备代码

两类主机均需要仓库源码用于本机构建：

```bash
git clone <PROJECT_REPOSITORY_URL> project-rebound
cd project-rebound/Backend
```

也可以在 CI 构建并将 `CONTROL_PLANE_IMAGE`、`EDGE_RELAY_IMAGE` 指向不可变镜像摘要；Compose 文件同时支持本地构建和预置镜像名。

## 5. 部署控制面

### 5.1 生成密钥与环境文件

```bash
cd project-rebound/Backend
chmod +x scripts/*.sh deploy/deploy.sh
./scripts/generate-control-plane-env.sh
chmod 600 deployments/control-plane/.env
```

生成器创建相互独立的 Ed25519 Access Token、Relay Token、更新签名密钥，以及十年期 Relay CA。它不会覆盖已有 `.env`，也不会输出密钥正文。

编辑 `deployments/control-plane/.env`：

- 将 `CORS_ALLOWED_ORIGINS` 改成真实客户端来源；多个来源用逗号分隔。
- 将 `UPDATE_CDN_BASE_URL`、`UPDATE_REALTIME_URL`、`UPDATE_STUN_SERVERS` 改成真实地址。
- 测试/IP 模式保留 `PUBLIC_API_SITE=http://:80` 和 `PUBLIC_API_HTTP_PORT=8080`。
- 域名生产模式设置 `PUBLIC_API_SITE=api.example.com`、`PUBLIC_API_HTTP_PORT=80`；DNS A/AAAA 指向控制面并开放 80/443，Caddy 自动申请证书。
- `RELAY_CONTROL_BIND_IP` 应绑定私网/VPN 地址；只有无法使用私网时才设为 `0.0.0.0`。
- 密钥 ID 在轮换时必须更新，不能在密钥变化后继续复用旧 ID。

`.env` 必须保留在主机秘密存储中，权限必须为 `600`，不得提交 Git、复制进镜像或写入工单。

### 5.2 更新描述符

按照 `Backend/deployments/updates/README.md` 把非秘密发布描述符放到 `Backend/deployments/updates`。生产模式缺少有效发布描述符或安全更新 URL 时会拒绝启动。大文件必须放在对象存储/CDN，API 仅返回下载元数据。

### 5.3 启动与验证

```bash
./scripts/deploy-control-plane.sh
./scripts/verify-control-plane.sh
```

部署脚本会：

1. 拒绝包含 `CHANGE_ME` 或 `example.com` 的环境文件；
2. 强制 `.env` 权限为 `600`；
3. 校验 Compose；
4. 构建并启动 PostgreSQL、Redis、控制面、Caddy、Prometheus、Grafana；
5. 等待 `/health/ready`；
6. 失败时输出受限的末尾日志并返回非零状态。

不需要本机监控栈时：

```bash
ENABLE_MONITORING=0 ./scripts/deploy-control-plane.sh
```

查看状态：

```bash
sudo docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml --profile monitoring ps
curl -fsS http://127.0.0.1:18080/health/ready
```

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
- `control_addr`：控制面 9090 的私网/VPN地址，例如 `10.20.0.10:9090`。
- `control_server_name`：保持 `control-plane`。当前 mTLS 服务证书固定包含此 SAN，不应改成 IP。
- `advertised_endpoints[].host`：客户端实际可达的公网 IP 或域名。
- `advertised_endpoints[].port`：公网映射后的 UDP 端口。
- `region`、`zone`、`provider`、容量和带宽：填写该节点真实信息。

分离式边缘 Compose 使用 Linux host networking，因此 `127.0.0.1:9100` 指标可由主机上的 Prometheus/agent 抓取，同时不会暴露到公网。

### 6.3 首次启动

```bash
chmod +x scripts/deploy-edge-relay.sh
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

升级：

```bash
./scripts/backup-control-plane.sh /srv/project-rebound-backups
git fetch --all --prune
git switch <RELEASE_BRANCH_OR_TAG>
./scripts/deploy-control-plane.sh
./scripts/verify-control-plane.sh
```

边缘节点可逐台 drain、更新源码并运行 `deploy-edge-relay.sh`。回滚应用时切回上一个已验证 tag 并重新部署；只有在迁移不向后兼容且已有恢复计划时才回滚数据库。

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
- 更新 Manifest 签名和文件 SHA-256；
- 备份恢复；
- 独立 network namespace 中的 `tests/netem/run-relay-matrix.sh`；
- 独立负载机上的 `tests/load/control-plane.js`。

性能验收要求 HTTP P95 `< 200 ms`、HTTP 失败率 `< 1%`、检查成功率 `> 99%`、WebSocket upgrade P95 `< 1 s`。功能成功不等同于性能门槛通过，报告中必须分别给出结果。

## 10. 安全不变量

- PostgreSQL、Redis、Grafana、Prometheus和直接控制面 HTTP 端口只能绑定回环地址。
- 公网 Caddy 对 `/v1/admin*` 和 `/internal/*` 返回 404，仅放行 Relay 注册和证书续签。
- Admin Token 与玩家 Token、Game Server Token、Relay Token 不可互换。
- Access、Refresh、Admin、Bootstrap、Relay、Game Server Token 和私钥不得进入日志。
- 边缘节点不拥有数据库或 Redis 凭据，不解析游戏 Payload。
- 不在生产物理网卡运行 netem；只使用隔离 namespace/veth。
- 不把 `.env`、`identity.json`、数据库备份或签名私钥提交 Git。

外部 API 见 `docs/control-plane-external-api.md`，内部与 Relay API 见 `docs/control-plane-internal-api.md`，机器可读契约见 `Backend/api/openapi/openapi.yaml`。
