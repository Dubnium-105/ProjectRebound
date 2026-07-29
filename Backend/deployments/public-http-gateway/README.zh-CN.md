# 公网 HTTP 网关（无需 Cloudflare Zero Trust）

[English](README.md) | 简体中文

当控制面没有公网 IPv4、Cloudflare Tunnel/Zero Trust 无法开通时，可使用已有公网网关承接普通 Cloudflare HTTP 代理流量。HAProxy 在公网 `443` 读取 TLS SNI，将 API HTTPS/WSS 与 Relay mTLS 分流；API 再经独立 FRP QUIC 通道回源。该 HTTP FRP 与 mTLS FRP 必须使用不同的用户、token、配置目录、控制端口和 systemd unit。

```text
boundary.example.com（橙云） -> HAProxy :443 -> TLS :10443
  -> HTTP FRPS 127.0.0.1:18081 -> HTTP FRPC -> control 127.0.0.1:18081

relay.example.com（灰云） -> HAProxy :443 -> mTLS FRPS 127.0.0.1:9443
  -> mTLS FRPC -> control 127.0.0.1:19090
```

## DNS 与 Cloudflare

- API hostname 创建指向网关 IPv4 的橙云 A 记录。
- Relay mTLS hostname 继续使用指向同一 IPv4 的灰云 A 记录。
- Cloudflare `SSL/TLS > Overview` 必须选择 **Full (strict)**；不得长期使用 Flexible。
- 网关证书可使用 Let's Encrypt：HAProxy 将 ACME HTTP-01 路径转发给本机 `127.0.0.1:18888` 的 Certbot standalone listener。
- API 的 80/443 origin 请求仅允许 Cloudflare 官方地址段；Relay mTLS SNI 不受该来源限制。

## 独立 HTTP FRP

网关安装 `frps.toml.example`、`projectrebound-http-frps.service` 到 `/etc/projectrebound-http-frps` 与 `/etc/systemd/system`。生成独立 token：

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-http-frps
sudo install -d -o root -g projectrebound-http-frps -m 0750 /etc/projectrebound-http-frps
openssl rand -hex 32 | sudo tee /etc/projectrebound-http-frps/token >/dev/null
sudo chown root:projectrebound-http-frps /etc/projectrebound-http-frps/token
sudo chmod 0640 /etc/projectrebound-http-frps/token
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-http-frps
```

通过秘密通道将相同 token 放到控制面 `/etc/projectrebound-http-frpc/token`，安装 FRPC 模板和 unit。网关只允许控制面出口 IP 访问 TCP/UDP 7001；远端代理端口通过 `proxyBindAddr = "127.0.0.1"` 强制绑定回环。

## 443 SNI 迁移

现有 mTLS FRPS 必须改为：

```toml
proxyBindAddr = "127.0.0.1"
allowPorts = [{ single = 9443 }]
```

控制面 mTLS FRPC 的代理改为 `remotePort = 9443`。先验证两端 FRP 配置，再让 HAProxy 接管公网 443；最终 Relay 客户端仍连接 `relay.example.com:443`，无需修改。

将 `haproxy.cfg.example` 中的 `PUBLIC_API_HOST`、`RELAY_MTLS_HOST` 替换为实际域名。安装 Cloudflare 地址刷新脚本、service 和 timer：

```bash
sudo install -o root -g root -m 0755 refresh-cloudflare-ips.sh /usr/local/sbin/projectrebound-refresh-cloudflare-ips
sudo install -o root -g root -m 0644 projectrebound-cloudflare-ips.service projectrebound-cloudflare-ips.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-cloudflare-ips.timer
sudo systemctl start projectrebound-cloudflare-ips.service
```

## TLS 证书

```bash
sudo certbot certonly --standalone --non-interactive --agree-tos \
  --register-unsafely-without-email --preferred-challenges http \
  --http-01-address 127.0.0.1 --http-01-port 18888 \
  -d boundary.example.com
```

用仅允许 root 修改的 defaults 文件配置证书域名，再安装 deploy hook。若 Admin Web 使用独立域名，还要设置 `ADMIN_WEB_HOST`；Hook 会先安装两条续期证书链，再校验并重载 HAProxy：

```bash
printf '%s\n' \
  'PUBLIC_API_HOST=boundary.example.com' \
  'ADMIN_WEB_HOST=admin.boundary.example.com' | \
  sudo tee /etc/default/projectrebound-http-gateway >/dev/null
sudo chown root:root /etc/default/projectrebound-http-gateway
sudo chmod 0600 /etc/default/projectrebound-http-gateway
sudo install -o root -g root -m 0755 deploy-haproxy-cert.sh \
  /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo certbot renew --dry-run --no-random-sleep-on-renew
```

deploy hook 会将 `fullchain.pem` 与 `privkey.pem` 合并为 HAProxy PEM，并在续期后原子替换、校验配置、热重载 HAProxy。公网 80 端口除 ACME HTTP-01 外只返回 HTTPS 重定向；如果 Cloudflare 被误改为 Flexible，该配置会暴露重定向异常，避免静默使用明文 API 回源。

## 隔离的 MetaServer HTTP 与 Logic 路由

最终 Meta 路径使用第三套 FRP 身份，不修改现有 HTTP 或 Relay mTLS FRP 服务：

```text
meta.dubnium.top（橙云） -> HAProxy :443 -> TLS :10445
  -> Meta FRPS 127.0.0.1:18082 -> Meta FRPC -> 控制面 127.0.0.1:18082

logic.dubnium.top（灰云） -> HAProxy :443 -> TLS :10446
  -> Meta FRPS 127.0.0.1:16969 -> Meta FRPC -> 控制面 127.0.0.1:16968
```

两段 HAProxy Logic 路径使用 PROXY protocol v1，使 MetaServer 经 FRP 后仍能看到
真实客户端地址。10446、16969 和控制面的 PROXY listener 必须保持私有；从不可信
来源接受该头部会允许 IP 欺骗。

10446 特意与已部署的 Admin HTTPS 监听 10444 分离。不得让 Meta Logic 复用 Admin
监听或证书。

网关将 `frps-meta.toml.example` 与 `projectrebound-meta-frps.service` 安装到
`/etc/projectrebound-meta-frps`；控制面将 `frpc-meta.toml.example` 与
`projectrebound-meta-frpc.service` 安装到 `/etc/projectrebound-meta-frpc`。
必须使用唯一 token 和 FRP user，7002 控制端口只允许控制面来源。两个证书都在网关
终止。Meta HTTP 只接受 Cloudflare 来源；Logic 需要普通 TLS 客户端直连，因此不限制
为 Cloudflare 来源。Meta HTTPS frontend 对 `/internal/*` 和 `/v1/admin*` 返回
404；管理员继续使用已有隔离 Admin origin。

FRP 控制连接使用 Tailscale 等双机 VPN 时，应让 FRPS 绑定 VPN 地址而不是
`0.0.0.0`。若叠加 QUIC 持续出现 `no recent network activity`，在该 overlay 上
改用 FRP `tcp` 传输；FRP TLS 与 VPN 加密仍保持启用。

运行证书部署 hook 前，在 root-only 网关 defaults 中设置
`META_HTTP_HOST=meta.dubnium.top` 与 `META_LOGIC_HOST=logic.dubnium.top`。18082 和
16969 始终只绑定回环。完整顺序见
[`docs/operations/metaserver-deployment.zh-CN.md`](../../../docs/operations/metaserver-deployment.zh-CN.md)。

控制面本地 HTTP 端口可以与网关 remote port 不同。例如 AdminWeb 已占用控制面的
`127.0.0.1:18082` 时，设置 `META_SERVER_HTTP_PORT=18083`，且只把 Meta FRPC
代理的 `localPort` 改为 `18083`；`remotePort` 与网关 origin 仍保持 `18082`。

## 验收

```bash
sudo haproxy -c -f /etc/haproxy/haproxy.cfg
sudo systemctl is-active haproxy frps projectrebound-http-frps projectrebound-meta-frps
sudo ss -lntup | grep -E ':(80|443|7000|7001|7002) '
sudo ss -lntp | grep -E '127.0.0.1:(9443|10443|10444|10445|10446|18081|18082|16969) '
curl -fsS https://meta.dubnium.top/health/ready
test "$(curl -sS -o /dev/null -w '%{http_code}' https://meta.dubnium.top/internal/metrics)" = 404
openssl s_client -connect 127.0.0.1:443 -servername logic.dubnium.top \
  -verify_hostname logic.dubnium.top -verify_return_error </dev/null
```

最后检查所有有效 Relay 节点持续在线、遥测不过期、FRPC/FRPS 无新增重连，然后再执行 10/25/50/100 VU 分级压测。
