# MetaServer 部署

[English](metaserver-deployment.md) | 简体中文

本清单将控制面 MetaServer、公网网关和 Relay 节点作为三个独立角色部署。生产环境使用 CI 产物。以下命令假设仓库位于 `/opt/projectrebound/current`，FRP/HAProxy 已按现有网关版本安装。

最终生产 DNS：

- `project-rebound.space`：公网 API，Cloudflare 橙云，SSL 模式 Full (strict)；
- `meta.project-rebound.space`：MetaServer HTTP，Cloudflare 橙云，SSL 模式 Full (strict)；
- `logic.project-rebound.space`：MetaServer 原生 TLS 端点，灰云/DNS only，使普通 TLS TCP 客户端可以直连；
- `relay.project-rebound.space`：Relay mTLS 端点；若经同一网关分流则使用灰云/DNS only。

`dubnium.top` 已弃用，不得继续出现在生产默认值、SNI 规则、证书路径、监控或客户端配置中。域名切换必须在同一发布中同步更新客户端默认值、Control Plane/MetaServer 配置、OpenAPI 示例、网关 SNI 规则、证书与监控。不得只在一侧修改 Logic 域名，因为 MetaTunnel 会严格验证对应 TLS server name。

不得将 6968、6969、8000、8081、9000、16968、16969 或 18082 暴露到公网。

## 1. CI 产物与发布输入

CI 生成：

- `ghcr.io/<owner>/projectrebound-meta-server:sha-<40位commit>`；
- Windows `meta-tunnel.exe` artifact；
- 镜像 SBOM、漏洞结果和 provenance；
- 包含协议版本 `1`、数据库迁移 `40`、definitions 哈希 `20393e344e14935535c0eac6815ad82ca051f33caf199281ace4d4bb58391c49` 及上游 commit `d68e717267abf14e32d4e39618f9b7680ed93046` 的发布元数据。

只提升完整通过 CI 的同一 SHA。生产使用 `production-meta-server` GitHub Environment，staging 使用 `staging-meta-server`。`meta-server` target 只部署或回滚该镜像，不重启 `control-plane`。

## 2. 控制面主机

准备现有 `.env`：

```bash
cd /opt/projectrebound/current/Backend
sudo ./scripts/generate-control-plane-env.sh deployments/control-plane/.env
sudoedit deployments/control-plane/.env
```

为 `META_POSTGRES_PASSWORD` 和 `META_REDIS_PASSWORD` 设置互不相同的高熵值。保持 `META_POSTGRES_USER=projectrebound_meta`、`META_REDIS_USERNAME=projectrebound-meta`、回环端口 18082/16968 及最终公网域名。新生成的环境已经包含 `ACCESS_TOKEN_PUBLIC_KEY_BASE64` 和 `ADMIN_ACCESS_TOKEN_PUBLIC_KEY_BASE64`，以及 Control Plane 使用的独立 `GAME_SERVER_CA_CERT_PEM_BASE64` 与 `GAME_SERVER_CA_KEY_PEM_BASE64`。即使只部署 MetaServer，Compose 插值也要求该 CA 对存在。旧环境应先按 [Dedicated Server 注册手册](dedicated-server-registration.zh-CN.md)补齐 Game Server CA，再使用以下方式派生公钥，且不输出私有 seed。

若其他本地服务已占用 18082，设置 `META_SERVER_HTTP_PORT=18083`。随后只把 Meta FRPC HTTP 代理的 `localPort` 改为 18083，网关 `remotePort` 仍保持 18082。这样可隔离同机的 AdminWeb 与 MetaServer 监听，同时不改变公网路由。

```bash
printf '%s\n' "$ACCESS_TOKEN_PRIVATE_KEY_BASE64" |
  ./scripts/derive-ed25519-public.sh
printf '%s\n' "$ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64" |
  ./scripts/derive-ed25519-public.sh
```

将输出写入对应 public-key 变量。MetaServer 只接收验证公钥，不得接收玩家/管理员签名私钥或管理员 MFA 加密密钥；任何 token key 都不得复制到网关。

部署不可变 CI 镜像：

```bash
cd /opt/projectrebound/current/Backend
sudo env \
  DEPLOY_SOURCE=ci \
  META_SERVER_IMAGE=ghcr.io/<owner>/projectrebound-meta-server:sha-<commit> \
  ./scripts/deploy-meta-server.sh
```

脚本只构建/拉取 MetaServer，等待当前的 40 号迁移，幂等创建受限 PostgreSQL 角色和仅限 `meta:*` 的 Redis ACL 用户，再执行 `up -d --no-deps meta-server`。

`META_MATCH_RESERVATION_TTL_SECONDS` 默认为 90。玩家未在此期限内连接到已分配 Dedicated Server 时，调度器会把预留标记为失败，将健康服务器恢复为 `READY`，并释放 Party。生产环境还必须设置 `META_LOGIC_PROXY_PROTOCOL=true`。可信 HAProxy/FRP 链路提供该头部，使每 IP 限制使用真实客户端地址；不得把启用 PROXY 的 Logic listener 暴露给不可信网络。

验证：

```bash
curl -fsS http://127.0.0.1:18082/health/ready
curl -fsS http://127.0.0.1:18082/v1/meta/regions
sudo docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml --profile meta ps meta-server
sudo ss -lntp | grep -E '127.0.0.1:(18082|16968)'
```

安装隔离 FRPC 身份，不得修改或复用其他服务的 FRPC 配置/unit：

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-meta-frpc || true
sudo install -d -o root -g projectrebound-meta-frpc -m 0750 /etc/projectrebound-meta-frpc
sudo install -o root -g projectrebound-meta-frpc -m 0640 \
  deployments/public-http-gateway/frpc-meta.toml.example \
  /etc/projectrebound-meta-frpc/frpc.toml
sudo install -o root -g root -m 0644 \
  deployments/public-http-gateway/projectrebound-meta-frpc.service \
  /etc/systemd/system/
```

只替换 `GATEWAY_IPV4`；通过现有安全运维渠道把 Meta FRP token 传到 `/etc/projectrebound-meta-frpc/token`，所有者 `root:projectrebound-meta-frpc`，权限 0640。启用独立 unit：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-meta-frpc
```

## 3. 公网网关

新建第三套 FRP 服务，与现有 HTTP 和 Relay mTLS 服务完全分离：

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-meta-frps || true
sudo install -d -o root -g projectrebound-meta-frps -m 0750 /etc/projectrebound-meta-frps
openssl rand -hex 32 | sudo tee /etc/projectrebound-meta-frps/token >/dev/null
sudo chown root:projectrebound-meta-frps /etc/projectrebound-meta-frps/token
sudo chmod 0640 /etc/projectrebound-meta-frps/token
sudo install -o root -g projectrebound-meta-frps -m 0640 \
  Backend/deployments/public-http-gateway/frps-meta.toml.example \
  /etc/projectrebound-meta-frps/frps.toml
sudo install -o root -g root -m 0644 \
  Backend/deployments/public-http-gateway/projectrebound-meta-frps.service \
  /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-meta-frps
```

FRPS 控制端口 7002 只允许回环 remote port 18082 和 16969。防火墙仅允许控制面出口来源访问 7002；不得对公网打开代理端口。

为两个最终 Meta 域名取得证书。`meta.project-rebound.space` 接收 Cloudflare origin 流量；`logic.project-rebound.space` 必须向 MetaTunnel 提供公有信任证书：

```bash
sudo certbot certonly --standalone --preferred-challenges http \
  --http-01-address 127.0.0.1 --http-01-port 18888 \
  -d meta.project-rebound.space
sudo certbot certonly --standalone --preferred-challenges http \
  --http-01-address 127.0.0.1 --http-01-port 18888 \
  -d logic.project-rebound.space
```

设置 root-only 续期 defaults，运行自带的 HAProxy 原子部署 hook：

```bash
printf '%s\n' \
  'META_HTTP_HOST=meta.project-rebound.space' \
  'META_LOGIC_HOST=logic.project-rebound.space' | \
  sudo tee -a /etc/default/projectrebound-http-gateway >/dev/null
sudo chmod 0600 /etc/default/projectrebound-http-gateway
sudo install -o root -g root -m 0755 \
  Backend/deployments/public-http-gateway/deploy-haproxy-cert.sh \
  /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
```

把 `haproxy.cfg.example` 的 Meta SNI 段合并到已部署配置。公网 443 上：

- `meta.project-rebound.space` 只接受 Cloudflare 来源，终止 TLS 后转到 `127.0.0.1:18082`；
- `logic.project-rebound.space` 终止普通 TLS，字节流转到 `127.0.0.1:16969`；
- Meta Logic TLS 使用私有监听端口 `10446`；`10444` 继续保留给现有 Admin HTTPS 监听；
- 两段 HAProxy Logic 路径使用 PROXY protocol v1 保留客户端地址，FRP 将其原样转发到控制面 listener；
- 现有 API、Admin 和 Relay SNI 路由保持不变。

重载前验证：

```bash
sudo haproxy -c -f /etc/haproxy/haproxy.cfg
sudo systemctl reload haproxy
sudo systemctl is-active haproxy projectrebound-meta-frps
sudo ss -lntp | grep -E ':(443|7002)|127.0.0.1:(18082|16969)'
```

## 4. Relay 节点

不需要 Meta 专用域名或手工节点列表。将每个现有 `edge-relay` 升级到不可变 CI 镜像，并保持当前 Registry 身份。现有 UDP listener 会识别 QoS，不影响已认证 Relay 流量。默认值：

```yaml
qos_enabled: true
qos_packets_per_second: 32
qos_max_request_bytes: 256
```

等价环境变量为 `EDGE_RELAY_QOS_ENABLED`、`EDGE_RELAY_QOS_PACKETS_PER_SECOND` 和 `EDGE_RELAY_QOS_MAX_REQUEST_BYTES`。请求至少 11 字节并以 `0x59` 开头；畸形包静默丢弃，响应绝不大于请求。继续使用现有公网 UDP Relay 端口，不开放 8000 或第二个 QoS 端口。

逐节点发布：先停止接收新 allocation，等待活动 allocation 归零，部署，确认心跳新鲜且为 READY，再处理下一台。禁止按小时重启 Relay。

## 5. 客户端接入

只分发 CI 构建的 Windows `meta-tunnel.exe`。Browser 通过匿名 stdin 传 Access Token，读取 readiness JSON，并在游戏退出时结束 tunnel。listener 只绑定随机 `127.0.0.1` 端口，因此不应触发公网防火墙监听提示。无法启用证书校验或 MetaTunnel 时不得发布客户端。

生产默认值为 `https://meta.project-rebound.space` 与 `logic.project-rebound.space:443`；公网 API 统一使用 `project-rebound.space` 域名体系。不得再发布任何 `dubnium.top` fallback。

## 6. 验收与回滚

使用当前网关地址验证公网路由，但不要把该地址提交到仓库：

```bash
GATEWAY_IPV4='<current-gateway-ipv4>'
curl --resolve meta.project-rebound.space:443:${GATEWAY_IPV4} \
  -fsS https://meta.project-rebound.space/health/ready
openssl s_client -connect ${GATEWAY_IPV4}:443 \
  -servername logic.project-rebound.space \
  -verify_hostname logic.project-rebound.space -verify_return_error </dev/null
```

随后验证已认证会话、Gate 单次消费/重放拒绝、档案/配装 round trip 与 revision 冲突、Party、单人/Party 匹配、Dedicated Server scope/IDOR、Relay 动态发现和 QoS、指标/告警以及小范围 canary，最后才进入长稳。

监控 profile 会运行加固的 Blackbox Exporter。确认 `probe_success{job="project-rebound-meta-public"}` 和 `probe_success{job="project-rebound-logic-public"}` 均为 1；这些探测覆盖公网 TLS、HAProxy 分流和隔离 FRP 链路，而不只是本机容器状态。

失败时使用部署 workflow 的 MetaServer rollback，或重部署上一不可变 digest。不得重启 control-plane，不回滚已经应用的 25–40 号迁移，普通镜像回滚不恢复 PostgreSQL。网关/FRP 回滚是独立配置变更，必须分别通过 `haproxy -c` 和 FRP 配置校验。
