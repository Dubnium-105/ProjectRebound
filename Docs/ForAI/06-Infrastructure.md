# 06 — Infrastructure & Deployment

> 来源合并：game-and-server-launch-flow.md、debian-deployment-and-ops.md、implementation-status-v2.md
> 最后更新：2026-04-26

## 启动链路（当前已验证）

```
Toolbox (Rust)
  └─ 安装/更新 Release.zip → 解压到 Win64/
       └─ ServerLauncher.exe（Slint UI）
            ├─ LoadConfigFile() / SaveConfigFile()
            ├─ InitServerUniqueId()（8位hex，持久化）
            └─ LaunchServerLocked()
                 └─ 游戏进程
                      ├─ dxgi.dll 自动注入 → Payload.dll
                      ├─ Payload 启动 Fake Login Server (BoundaryMetaServer:8000)
                      └─ Payload 初始化 Hooks → 心跳轮询
```

### 各组件职责

| 组件 | 职责 |
|------|------|
| **Toolbox** | 下载 Release.zip、解压到 Win64、版本检查、安装/更新 |
| **ServerLauncher** | 管理 DS 进程生命周期、Watchdog（30s 心跳超时）、日志轮转（1MB）、启动/停止/重启 |
| **dxgi.dll** | Proxy DLL，劫持 DirectX 加载链，自动加载 Payload.dll |
| **Payload.dll** | 注入游戏进程，Hook ProcessEvent，DS 逻辑、日志、心跳 |
| **Metaserver** | Login server（8000）、装备存档、TCP RPC（6969）、UDP QoS（9000） |
| **Backend** | 服务端列表/心跳后端（已废弃，见 Deprecated/TestProjects/Backend/） |

---

## 最小可玩闭环

### 单机（Offline）

1. Toolbox 启动 → "离线模式" → "启动 PVE"
2. ServerLauncher 以 `-cli` 启动
3. 游戏客户端以 `-match=127.0.0.1` 启动

### 联机（Online）

1. Toolbox 启动 → "在线模式" → "启动 PVE"
2. ServerLauncher 带 `-online=host:port` 启动
3. 游戏客户端通过房间浏览器连接
4. Payload 向 Backend POST `/server/status` 心跳
5. 其他玩家看到服务器 → 加入

---

## 当前实现状态（v2）

### 已完成

- `.NET 8` 后端：匿名登录、UDP host probe、房间 CRUD、快速匹配 ticket、后台 matchmaking loop、lifecycle cleanup
- 旧 `/server/status` 兼容层
- Host migration 接口（返回 501，预留）
- UDP NAT rendezvous：5001/udp
- Punch ticket：host/client 交换公网 UDP endpoint
- 最小 UDP relay：5002/udp（P2P 失败时兜底）
- SQLite DateTimeOffset → Unix milliseconds 存储修复
- 共享契约：`Shared/ProjectRebound.Contracts`（DTO + enum）
- Python GUI 原型（已废弃）
- A/B 机联机验证成功（P2P 不通时 UDP relay 兜底）

### 待完成 / 已延后

- [ ] Host migration（V1 预留接口，V2 可能不做）
- [ ] HTTPS / WinHTTP TLS 支持
- [ ] 更多游戏模式
- [ ] ServerLauncher GUI 完善（进度条、端口验证）
- [ ] 字体/Node.js 嵌入（被同事坚持在线下载阻塞）

---

## Debian 部署

### 目标

部署 `ProjectRebound.MatchServer` 到 Debian 12/13 VPS 作为后端。

### 目录布局

```
/opt/projectrebound/
  current -> /opt/projectrebound/releases/<timestamp>
  previous -> /opt/projectrebound/releases/<old-timestamp>
  releases/
    <timestamp>/

/var/lib/projectrebound/
  projectrebound-matchserver.db
  projectrebound-matchserver.db-shm
  projectrebound-matchserver.db-wal

/var/backups/projectrebound/
  projectrebound-matchserver-<timestamp>.db
```

### 安装

```bash
# 依赖
sudo apt-get install -y curl wget gpg unzip sqlite3 nginx ufw

# .NET 8 Runtime
wget "https://packages.microsoft.com/config/debian/${VERSION_ID}/packages-microsoft-prod.deb"
sudo dpkg -i packages-microsoft-prod.deb
sudo apt-get update && sudo apt-get install -y aspnetcore-runtime-8.0

# 创建用户与目录
sudo useradd --system --home /var/lib/projectrebound --create-home --shell /usr/sbin/nologin projectrebound
sudo mkdir -p /opt/projectrebound/releases /var/lib/projectrebound /var/backups/projectrebound
sudo chown -R projectrebound:projectrebound /opt/projectrebound /var/lib/projectrebound /var/backups/projectrebound
```

### 构建与上传

```powershell
# 从 Windows 开发机
dotnet publish Backend\ProjectRebound.MatchServer\ProjectRebound.MatchServer.csproj -c Release -o publish\matchserver
scp -r publish\matchserver user@SERVER:/tmp/projectrebound-matchserver-next
```

### 部署脚本（服务器端）

```bash
set -e
RELEASE="$(date +%Y%m%d-%H%M%S)"
CURRENT="$(readlink -f /opt/projectrebound/current || true)"
sudo mkdir -p "/opt/projectrebound/releases/${RELEASE}"
sudo cp -a /tmp/projectrebound-matchserver-next/. "/opt/projectrebound/releases/${RELEASE}/"
sudo chown -R projectrebound:projectrebound "/opt/projectrebound/releases/${RELEASE}"
[ -n "${CURRENT}" ] && sudo ln -sfn "${CURRENT}" /opt/projectrebound/previous
sudo ln -sfn "/opt/projectrebound/releases/${RELEASE}" /opt/projectrebound/current
sudo systemctl restart projectrebound-matchserver
```

### systemd 服务

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
Environment="ConnectionStrings__MatchServer=Data Source=/var/lib/projectrebound/projectrebound-matchserver.db"

[Install]
WantedBy=multi-user.target
```

### Nginx 反向代理

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

### 防火墙

```bash
sudo ufw allow OpenSSH
sudo ufw allow 80/tcp
sudo ufw allow 5001/udp    # UDP rendezvous
sudo ufw allow 5002/udp    # UDP relay
sudo ufw enable
```

不要暴露 Kestrel 的 5000 端口到公网。

---

## 冒烟测试

```bash
# 健康检查
curl -fsS http://YOUR_SERVER_IP/health

# 匿名登录
curl -sS -X POST http://YOUR_SERVER_IP/v1/auth/guest \
  -H "Content-Type: application/json" \
  -d '{"displayName":"Smoke","deviceToken":null}'

# 房间列表
curl -sS "http://YOUR_SERVER_IP/v1/rooms?region=CN&version=dev"

# 旧心跳兼容路径
curl -sS -X POST http://YOUR_SERVER_IP/server/status \
  -H "Content-Type: application/json" \
  -d '{"name":"legacy-smoke","endpoint":"127.0.0.1:7777","map":"test","mode":"test","version":"dev","playerCount":0,"maxPlayers":4}'
```

---

## 日常运维

```bash
# 查看状态
sudo systemctl status projectrebound-matchserver

# 实时日志
sudo journalctl -u projectrebound-matchserver -f

# 重启
sudo systemctl restart projectrebound-matchserver

# 重载 Nginx
sudo nginx -t && sudo systemctl reload nginx

# 查看监听端口
ss -lntup

# 备份数据库
BACKUP="/var/backups/projectrebound/projectrebound-matchserver-$(date +%Y%m%d-%H%M%S).db"
sudo -u projectrebound sqlite3 /var/lib/projectrebound/projectrebound-matchserver.db ".backup '${BACKUP}'"
```

---

## 常见故障

| 问题 | 排查 |
|------|------|
| 后端启动失败 | `journalctl -u projectrebound-matchserver -n 200`；检查 .NET runtime 是否安装；`/var/lib/projectrebound` 写权限 |
| 外网访问不通 | `curl http://127.0.0.1:5000/health` vs `curl http://SERVER_IP/health`；检查 Nginx、UFW、云安全组 |
| UDP probe 失败 | 检查 `X-Forwarded-For` 是否传递真实 IP；Windows 防火墙/路由端口转发/CGNAT |
| UDP relay 失败 | `ss -lunp | grep 5002`；放行 `5002/udp`；云安全组 |
| 502 from GUI | GUI URL 不要带 `:5000`；检查 `%APPDATA%/ProjectReboundBrowser/config-python.json` |

---

## 数据库注意事项

- SQLite 开启 WAL：直接复制 `.db` 文件可能拿到不完整数据
- 优先使用 `.backup` 命令备份
- 当前实现使用 `EnsureCreated`，无 EF migrations
- 变更实体结构前备份数据库

---

## 相关文档

- `01-System-Overview.md` — 系统全景
- `05-Backend-API.md` — API 规范
- `07-Toolbox.md` — 工具箱
