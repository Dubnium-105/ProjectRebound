# ProjectRebound Debian Deployment and Operations

生成日期：2026-04-20

本文档描述如何把 `ProjectRebound.MatchServer` 骨干服务部署到 Debian 服务器，以及后续如何日常更新、回滚、备份和做真实联机验证。

## 1. 范围

部署对象：

- 后端：`Backend/ProjectRebound.MatchServer`
- 运行时：.NET 8 / ASP.NET Core / EF Core SQLite
- 反向代理：Nginx
- 进程守护：systemd
- 数据库：SQLite，放在 `/var/lib/projectrebound/projectrebound-matchserver.db`

不部署到 Linux 的部分：

- Windows Python GUI：`Desktop/ProjectRebound.Browser.Python`
- 游戏本体、server wrapper、payload

V1 网络模型仍然是玩家主机公网直连。Linux 骨干服务只负责身份、房间列表、UDP probe、匹配和心跳，不承载每局游戏流量。

## 2. 前置假设

- Debian 12 或 Debian 13 x64 VPS。
- 服务器有 sudo 权限。
- 对外开放 TCP `80`；以后启用 HTTPS 时开放 TCP `443`。
- 玩家当主机时，玩家机器需要开放并转发游戏 UDP 端口，例如 `7777/udp`。
- 当前 C++ `Payload` 更适合 HTTP `host:port` 上报。正式启用 HTTPS 前，建议先补 WinHTTP TLS 支持，或让游戏侧继续使用 `http://host:80`。

## 3. 服务器目录布局

```text
/opt/projectrebound/
  current -> /opt/projectrebound/releases/20260420-193000
  previous -> /opt/projectrebound/releases/20260420-181500
  releases/
    20260420-181500/
    20260420-193000/

/var/lib/projectrebound/
  projectrebound-matchserver.db
  projectrebound-matchserver.db-shm
  projectrebound-matchserver.db-wal

/var/backups/projectrebound/
  projectrebound-matchserver-20260420-193000.db
```

应用发布物放 `/opt/projectrebound/releases/<timestamp>`，`current` 指向当前版本。SQLite 数据库不放在发布目录里，这样更新应用不会误删数据。

## 4. 安装系统依赖

先安装常用工具：

```bash
sudo apt-get update
sudo apt-get install -y curl wget gpg unzip sqlite3 nginx ufw
```

添加 Microsoft 包源并安装 ASP.NET Core Runtime 8.0：

```bash
. /etc/os-release
wget "https://packages.microsoft.com/config/debian/${VERSION_ID}/packages-microsoft-prod.deb" -O packages-microsoft-prod.deb
sudo dpkg -i packages-microsoft-prod.deb
rm packages-microsoft-prod.deb

sudo apt-get update
sudo apt-get install -y aspnetcore-runtime-8.0
```

确认运行时：

```bash
dotnet --list-runtimes
```

如果服务器是 ARM64，需要注意 Microsoft 的 Debian 包源对 .NET 8 的架构支持限制。VPS 推荐使用 x64；ARM64 可考虑 self-contained 发布。

## 5. 创建运行用户

```bash
sudo useradd --system --home /var/lib/projectrebound --create-home --shell /usr/sbin/nologin projectrebound
sudo mkdir -p /opt/projectrebound/releases /var/lib/projectrebound /var/backups/projectrebound
sudo chown -R projectrebound:projectrebound /opt/projectrebound /var/lib/projectrebound /var/backups/projectrebound
```

## 6. 本机发布

在 Windows 开发机的仓库根目录运行：

```powershell
dotnet publish Backend\ProjectRebound.MatchServer\ProjectRebound.MatchServer.csproj -c Release -o publish\matchserver
```

上传到服务器：

```powershell
scp -r publish\matchserver user@YOUR_SERVER:/tmp/projectrebound-matchserver
```

如果想生成不依赖服务器 .NET Runtime 的版本：

```powershell
dotnet publish Backend\ProjectRebound.MatchServer\ProjectRebound.MatchServer.csproj -c Release -r linux-x64 --self-contained true -p:PublishSingleFile=true -o publish\matchserver-linux-x64
```

常规部署建议先使用 framework-dependent 发布，因为文件更小，更新更快。

## 7. 首次部署发布物

在 Debian 服务器上：

```bash
RELEASE="$(date +%Y%m%d-%H%M%S)"
sudo mkdir -p "/opt/projectrebound/releases/${RELEASE}"
sudo cp -a /tmp/projectrebound-matchserver/. "/opt/projectrebound/releases/${RELEASE}/"
sudo chown -R projectrebound:projectrebound "/opt/projectrebound/releases/${RELEASE}"
sudo ln -sfn "/opt/projectrebound/releases/${RELEASE}" /opt/projectrebound/current
```

## 8. systemd 服务

创建服务文件：

```bash
sudo nano /etc/systemd/system/projectrebound-matchserver.service
```

内容：

```ini
[Unit]
Description=ProjectRebound Match Server
After=network-online.target
Wants=network-online.target

[Service]
WorkingDirectory=/opt/projectrebound/current
ExecStart=/usr/bin/dotnet /opt/projectrebound/current/ProjectRebound.MatchServer.dll --urls http://127.0.0.1:5000
Restart=always
RestartSec=5
KillSignal=SIGINT
SyslogIdentifier=projectrebound-matchserver
User=projectrebound
Environment=ASPNETCORE_ENVIRONMENT=Production
Environment=ConnectionStrings__MatchServer=Data Source=/var/lib/projectrebound/projectrebound-matchserver.db

[Install]
WantedBy=multi-user.target
```

启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-matchserver
sudo systemctl status projectrebound-matchserver
```

看实时日志：

```bash
sudo journalctl -u projectrebound-matchserver -f
```

本机健康检查：

```bash
curl -fsS http://127.0.0.1:5000/health
```

## 9. Nginx 反向代理

创建站点：

```bash
sudo nano /etc/nginx/sites-available/projectrebound
```

内容：

```nginx
server {
    listen 80;
    server_name YOUR_DOMAIN_OR_SERVER_IP;

    location / {
        proxy_pass http://127.0.0.1:5000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

启用：

```bash
sudo ln -s /etc/nginx/sites-available/projectrebound /etc/nginx/sites-enabled/projectrebound
sudo nginx -t
sudo systemctl reload nginx
```

外部健康检查：

```bash
curl -fsS http://YOUR_DOMAIN_OR_SERVER_IP/health
```

`X-Forwarded-For` 很重要。后端创建 host probe 时会根据请求来源 IP 推断玩家公网 IP，Nginx 必须把真实来源 IP 传给后端。

## 10. 防火墙

```bash
sudo ufw allow OpenSSH
sudo ufw allow 80/tcp
sudo ufw enable
sudo ufw status
```

以后启用 HTTPS：

```bash
sudo ufw allow 443/tcp
```

不要把 Kestrel 的 `5000` 端口暴露到公网。systemd 已经让后端只监听 `127.0.0.1:5000`。

## 11. 冒烟测试

健康检查：

```bash
curl -fsS http://YOUR_DOMAIN_OR_SERVER_IP/health
```

匿名登录：

```bash
curl -sS -X POST http://YOUR_DOMAIN_OR_SERVER_IP/v1/auth/guest \
  -H "Content-Type: application/json" \
  -d '{"displayName":"Smoke","deviceToken":null}'
```

房间列表：

```bash
curl -sS "http://YOUR_DOMAIN_OR_SERVER_IP/v1/rooms?region=CN&version=dev"
```

旧心跳兼容路径：

```bash
curl -sS -X POST http://YOUR_DOMAIN_OR_SERVER_IP/server/status \
  -H "Content-Type: application/json" \
  -d '{"name":"legacy-smoke","endpoint":"127.0.0.1:7777","map":"test","mode":"test","version":"dev","playerCount":0,"maxPlayers":4}'
```

如果这些都返回 HTTP 200，说明 Nginx、Kestrel、SQLite 初始化和基本 API 都通了。

## 12. 真实联机验证

准备两台 Windows 机器，最好不在同一局域网：

- A：玩家主机。
- B：玩家客户端。
- 两台都运行 `Desktop/ProjectRebound.Browser.Python/run_browser.bat`。
- Backend URL 都填 `http://YOUR_DOMAIN_OR_SERVER_IP`。
- Region、Version 保持一致。

A 机器：

1. Windows 防火墙允许游戏 UDP 端口，例如 `7777/udp`。
2. 如果 A 在路由器后面，转发 `UDP 7777` 到 A 的局域网 IP。
3. GUI 点创建房间。
4. 期望：UDP probe 成功，房间创建成功，服务端房间列表出现该房间。

B 机器：

1. GUI 刷新房间列表。
2. 选择 A 的房间并加入。
3. 期望：GUI 调 `/v1/rooms/{roomId}/join`，拿到 `connect = ip:port`，启动游戏并传入 `-match=ip:port`。

快速匹配：

1. A 点快速匹配并允许当主机。
2. B 点快速匹配。
3. 期望：B 被分配到 A 的房间，或者后端选择可当主机的 ticket 创建房间。

主机掉线：

1. A 创建房间后关闭 wrapper 或游戏进程。
2. 约 45 秒内房间应变为 ended。
3. B 刷新后不应再能加入该房间。
4. 服务端日志不应出现连续异常。

查看服务端日志：

```bash
sudo journalctl -u projectrebound-matchserver -f
```

如果 A 的创建房间卡在 UDP probe，优先检查 A 的公网 UDP 可达性，而不是 Linux 服务器。V1 没有 NAT 打洞或 Relay。

## 13. 日常应用更新

开发机发布并上传：

```powershell
dotnet publish Backend\ProjectRebound.MatchServer\ProjectRebound.MatchServer.csproj -c Release -o publish\matchserver
scp -r publish\matchserver user@YOUR_SERVER:/tmp/projectrebound-matchserver-next
```

服务器切换版本：

```bash
set -e

RELEASE="$(date +%Y%m%d-%H%M%S)"
CURRENT="$(readlink -f /opt/projectrebound/current || true)"

sudo mkdir -p "/opt/projectrebound/releases/${RELEASE}"
sudo cp -a /tmp/projectrebound-matchserver-next/. "/opt/projectrebound/releases/${RELEASE}/"
sudo chown -R projectrebound:projectrebound "/opt/projectrebound/releases/${RELEASE}"

if [ -n "${CURRENT}" ]; then
  sudo ln -sfn "${CURRENT}" /opt/projectrebound/previous
fi

sudo ln -sfn "/opt/projectrebound/releases/${RELEASE}" /opt/projectrebound/current
sudo systemctl restart projectrebound-matchserver
sudo systemctl status projectrebound-matchserver
curl -fsS http://127.0.0.1:5000/health
```

外部再测：

```bash
curl -fsS http://YOUR_DOMAIN_OR_SERVER_IP/health
curl -sS "http://YOUR_DOMAIN_OR_SERVER_IP/v1/rooms"
```

更新期间已有房间会受影响，因为当前是单实例内存后台服务加 SQLite。正式服更新建议先公告维护窗口。

## 14. 回滚

如果更新后冒烟测试失败：

```bash
PREVIOUS="$(readlink -f /opt/projectrebound/previous)"
sudo ln -sfn "${PREVIOUS}" /opt/projectrebound/current
sudo systemctl restart projectrebound-matchserver
sudo systemctl status projectrebound-matchserver
curl -fsS http://127.0.0.1:5000/health
```

回滚应用不会自动回滚 SQLite 数据库。如果新版本引入了数据库结构变更，必须先备份数据库，并准备对应的数据库回滚策略。当前实现使用 `EnsureCreated`，没有 EF migrations；变更实体结构时要特别谨慎。

## 15. 数据库备份

安装 `sqlite3` 后，可以在线备份 SQLite：

```bash
BACKUP="/var/backups/projectrebound/projectrebound-matchserver-$(date +%Y%m%d-%H%M%S).db"
sudo -u projectrebound sqlite3 /var/lib/projectrebound/projectrebound-matchserver.db ".backup '${BACKUP}'"
sudo ls -lh "${BACKUP}"
```

不要只复制 `.db` 文件而忽略 `.db-wal` 和 `.db-shm`。当前 SQLite 开启 WAL 时，直接复制单个 `.db` 文件可能拿到不完整数据。优先使用 `.backup`。

建议：

- 每次应用更新前备份一次。
- 每天定时备份一次。
- 至少保留最近 7 天。
- 在另一台机器上定期恢复验证。

## 16. 日常运维命令

查看状态：

```bash
sudo systemctl status projectrebound-matchserver
```

实时日志：

```bash
sudo journalctl -u projectrebound-matchserver -f
```

最近 200 行日志：

```bash
sudo journalctl -u projectrebound-matchserver -n 200 --no-pager
```

重启后端：

```bash
sudo systemctl restart projectrebound-matchserver
```

检查 Nginx 配置：

```bash
sudo nginx -t
```

重载 Nginx：

```bash
sudo systemctl reload nginx
```

查看监听端口：

```bash
ss -lntup
```

查看数据库大小：

```bash
sudo du -h /var/lib/projectrebound/projectrebound-matchserver.db*
```

## 17. 系统和 .NET Runtime 更新

常规系统更新：

```bash
sudo apt-get update
sudo apt-get upgrade
sudo systemctl restart projectrebound-matchserver
```

只更新 ASP.NET Core Runtime：

```bash
sudo apt-get update
sudo apt-get install --only-upgrade aspnetcore-runtime-8.0
sudo systemctl restart projectrebound-matchserver
```

更新后确认：

```bash
dotnet --list-runtimes
curl -fsS http://127.0.0.1:5000/health
```

## 18. 常见故障

### 后端启动失败

检查：

```bash
sudo journalctl -u projectrebound-matchserver -n 200 --no-pager
dotnet --list-runtimes
sudo ls -lah /opt/projectrebound/current
sudo ls -lah /var/lib/projectrebound
```

常见原因：

- 没安装 `aspnetcore-runtime-8.0`。
- `/var/lib/projectrebound` 没有写权限。
- 发布目录缺文件。
- 旧 SQLite 文件来自不兼容结构。

### 外网访问失败

检查：

```bash
curl -fsS http://127.0.0.1:5000/health
curl -fsS http://YOUR_DOMAIN_OR_SERVER_IP/health
sudo nginx -t
sudo ufw status
```

如果本机 `127.0.0.1:5000` 通，外网不通，优先看 Nginx、UFW、云厂商安全组和 DNS。

### UDP probe 失败

这通常不是 Debian 服务器防火墙问题。后端只是向玩家主机公网 IP 和端口发 UDP nonce。

检查玩家主机：

- Windows 防火墙是否允许游戏 UDP 端口。
- 路由器是否转发 UDP 端口到正确 LAN IP。
- 玩家是否处在运营商 CGNAT 后面。
- GUI 中端口是否和路由器转发端口一致。

### 房间创建成功但别人连不上

检查：

- `/v1/rooms` 返回的 `connect` 是否是公网 IP 和正确端口。
- 客户端是否实际启动了 `-match=ip:port`。
- 主机游戏进程是否真的监听 UDP 端口。
- 同一个局域网内测试可能被 NAT loopback 干扰，建议用不同网络测试。

## 19. 参考资料

- Microsoft Learn: Install .NET on Debian: https://learn.microsoft.com/en-us/dotnet/core/install/linux-debian
- Microsoft Learn: Host ASP.NET Core on Linux with Nginx: https://learn.microsoft.com/aspnet/core/host-and-deploy/linux-nginx
- Microsoft Learn: dotnet publish: https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-publish
- Debian Wiki: systemd documentation: https://wiki.debian.org/systemd/documentation
