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

用仅允许 root 修改的 defaults 文件配置证书域名，再安装 deploy hook：

```bash
printf '%s\n' 'PUBLIC_API_HOST=boundary.example.com' | \
  sudo tee /etc/default/projectrebound-http-gateway >/dev/null
sudo chown root:root /etc/default/projectrebound-http-gateway
sudo chmod 0600 /etc/default/projectrebound-http-gateway
sudo install -o root -g root -m 0755 deploy-haproxy-cert.sh \
  /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo certbot renew --dry-run --no-random-sleep-on-renew
```

deploy hook 会将 `fullchain.pem` 与 `privkey.pem` 合并为 HAProxy PEM，并在续期后原子替换、校验配置、热重载 HAProxy。公网 80 端口除 ACME HTTP-01 外只返回 HTTPS 重定向；如果 Cloudflare 被误改为 Flexible，该配置会暴露重定向异常，避免静默使用明文 API 回源。

## 验收

```bash
sudo haproxy -c -f /etc/haproxy/haproxy.cfg
sudo systemctl is-active haproxy frps projectrebound-http-frps
sudo ss -lntup | grep -E ':(80|443|7000|7001) '
sudo ss -lntp | grep -E '127.0.0.1:(9443|10443|18081) '
curl -fsS https://boundary.example.com/health/ready
```

最后检查所有有效 Relay 节点持续在线、遥测不过期、FRPC/FRPS 无新增重连，然后再执行 10/25/50/100 VU 分级压测。
