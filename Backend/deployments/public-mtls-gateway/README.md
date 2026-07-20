# 公网 mTLS FRP 网关

当控制面没有公网 IPv4、Cloudflare Tunnel 又不能代理任意 TCP mTLS/gRPC 时，使用一台有公网 IPv4 的轻量主机运行 FRPS。控制面运行独立 FRPC，将本机回环 mTLS 端口映射到网关 TCP 443。Cloudflare Tunnel 继续只负责控制面的 HTTPS/WSS API，无需安装到 FRP 网关。

## DNS 与端口

- `relay.example.com` 的 A 记录指向网关公网 IPv4，必须设为 DNS Only（灰云）。没有可用公网 IPv6 时不要创建 AAAA。
- 网关公网开放 TCP 443；TCP/UDP 7000 只允许控制面出口地址或两机 VPN 地址访问。UDP 7000 用于默认的 QUIC 控制链路，TCP 7000 保留为回退路径。
- 控制面不向局域网或公网开放 Relay 控制端口，`RELAY_CONTROL_BIND_IP=127.0.0.1`。
- FRPS `allowPorts` 只允许 443，且每个客户端最多创建一个代理。
- FRP token 使用至少 32 字节随机值，通过秘密通道放入两端配置，配置权限设为 `640`，不得提交 Git。

## 安装网关 FRPS

从 FRP 官方 release 下载同一固定版本的 `frps`/`frpc`，校验发布哈希后安装。网关执行：

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin frp
sudo install -d -o root -g frp -m 0750 /etc/frp
sudo install -o root -g root -m 0755 frps /usr/local/bin/frps
sudo install -o root -g frp -m 0640 frps.toml /etc/frp/frps.toml
sudo install -o root -g root -m 0644 frps.service /etc/systemd/system/frps.service
sudo systemctl daemon-reload
sudo systemctl enable --now frps
sudo systemctl status frps --no-pager
```

以 `frps.toml.example` 为模板，只替换 `auth.token`。若网关已有 Xray、Hysteria、Squid 等服务，先用 `ss -lntup` 确认 443/7000 没有冲突，不要修改或重启无关服务。

## 安装控制面独立 FRPC

不要复用 1Panel 或其他应用管理的 FRPC 容器。创建独立用户、目录、配置和 unit：

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-frpc
sudo install -d -o root -g projectrebound-frpc -m 0750 /etc/projectrebound-frpc
sudo install -o root -g root -m 0755 frpc /usr/local/bin/frpc
sudo install -o root -g projectrebound-frpc -m 0640 frpc.toml /etc/projectrebound-frpc/frpc.toml
sudo install -o root -g root -m 0644 projectrebound-frpc.service /etc/systemd/system/projectrebound-frpc.service
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-frpc
sudo systemctl status projectrebound-frpc --no-pager
```

以 `frpc.toml.example` 为模板，填写网关地址、相同 token，并令 `localPort` 等于控制面 `.env` 中的 `RELAY_CONTROL_PORT`。控制面还必须设置：

```text
RELAY_CONTROL_BIND_IP=127.0.0.1
RELAY_CONTROL_SERVER_NAMES=control-plane,localhost,relay.example.com
```

模板默认使用 QUIC，并预建 2 条工作连接。跨区域链路有丢包时，这能避免每次边缘节点重连都等待新的 TCP 工作连接；`tcpMuxKeepaliveInterval` 负责复用会话探活，因此关闭独立的 FRP heartbeat。部署前先用临时、无代理的 FRPC 配置验证 QUIC 能登录，再切换生产服务。若 UDP 7000 无法连通或日志出现持续的 QUIC inactivity/timeout，删除 `transport.protocol = "quic"` 回退 TCP，不要反复重启现网服务做盲测。

任何传输切换都应先备份两端配置，依次验证配置语法，再重启 FRPS、FRPC：

```bash
sudo /usr/local/bin/frps verify -c /etc/frp/frps.toml
sudo /usr/local/bin/frpc verify -c /etc/projectrebound-frpc/frpc.toml
sudo systemctl restart frps                  # 在网关执行
sudo systemctl restart projectrebound-frpc   # 在控制面执行
```

## 边缘节点

所有边缘节点使用相同的稳定入口：

```yaml
control_addr: relay.example.com:443
control_server_name: relay.example.com
```

每个节点仍使用独立的一次性 Bootstrap Token；首次注册后身份和客户端证书保存在节点自己的持久卷中。FRP 网关不保存节点私钥，也不终止 mTLS。

## 验证

```bash
# 公共 DNS 必须直接返回网关 IP，而不是 Cloudflare Anycast IP
dig +short @1.1.1.1 relay.example.com A
dig +short @8.8.8.8 relay.example.com A

# 网关：7000 应同时存在 TCP 与 UDP 监听
sudo ss -lntup | grep -E ':(443|7000) '
sudo systemctl is-active frps

# 控制面
sudo ss -lntp | grep ':9090 '
sudo systemctl is-active projectrebound-frpc
curl -fsS http://127.0.0.1:18080/health/ready

# 无客户端证书必须失败并出现 certificate required
curl -kvsS --resolve relay.example.com:443:GATEWAY_IPV4 \
  https://relay.example.com/ --max-time 10 -o /dev/null
```

最终正向验收以边缘节点日志出现 `relay control connected`、控制面节点状态为 `READY` 为准。服务端证书由控制面进程在到期前自动轮换；Relay CA 到期或轮换仍需要单独的双 CA 迁移方案。

传输参数变更后至少观察 10 分钟，并检查 FRPC/FRPS 日志没有新的 `session shutdown`、`timeout trying to get work connection` 或代理关闭；只有在线节点全部恢复且遥测持续更新后才开始联合压测。
