# Dedicated Server 注册与运行身份

[English](dedicated-server-registration.md) | 简体中文

本文说明当前已经实现的 Dedicated Server 注册流程。请求与响应字段以 OpenAPI 为
准；本文重点描述运维、Windows Wrapper 与 Agent 的实际行为。

## 凭证流程

```text
专服邀请码/资格 + 已验证玩家
  -> 与 instance_id 绑定的一次性 Registration Token
  -> 节点本地生成 Ed25519 密钥与 PKCS#10 CSR
  -> server_id + 24 小时运行 Token + 24 小时证书
  -> 签名心跳与自动凭证轮转
```

合格邀请码的不可变权限快照必须包含
`allow_game_server_registration: true`。已验证玩家调用
`POST /v1/game-server-registration-tokens` 时可以消费该邀请码；已经拥有资格的玩家
省略 `invite_code`。返回的 `gsr_...` Token：

- 只绑定一个稳定的 `instance_id`；
- 默认 10 分钟过期；
- 只在带 `Cache-Control: no-store` 的响应中返回一次，之后无法恢复；
- 第一次成功注册时在同一事务中消费；
- 为同一实例重新签发时，会撤销此前尚未消费的 Token。

拥有 `game_servers.register` 权限并完成 MFA Step-up 的管理员也可以在
**联机管理 / Dedicated Server / 添加服务器**中签发。管理员选择 1–168 小时有效期
并填写审计原因。该 Token 仍然只绑定一个实例且只能使用一次，不是可复用的全局注册
秘密。

玩家签发请求示例：

```bash
curl -fsS https://api.project-rebound.space/v1/game-server-registration-tokens \
  -H "Authorization: Bearer $PLAYER_ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"instance_id":"hk-dedicated-01","invite_code":"REDACTED"}'
```

不得把响应 Token 写入 Git、日志、聊天或工单正文，只能安全传递到与其匹配的服务器
主机。

## 构建与安装 Windows Agent

Agent 必须与 Wrapper/Payload 使用同一提交构建。在 Windows 上执行：

```powershell
New-Item -ItemType Directory -Force .\build | Out-Null
Set-Location Backend
go build -trimpath -o ..\build\game-server-agent.exe .\cmd\game-server-agent
```

从 Linux 交叉构建 Windows 版本：

```bash
mkdir -p build
(cd Backend && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -o ../build/game-server-agent.exe ./cmd/game-server-agent)
```

将 `game-server-agent.exe` 放在 Dedicated Server 可执行文件旁，或在 Wrapper 配置中
指定路径。Agent 是真实的 Control Plane 客户端：它在本地生成节点密钥、提交 CSR、
保存签发身份、签名心跳并轮转凭证。

## Wrapper 配置

在 `serverconfig.json` 中增加：

```json
{
  "backend": "https://api.project-rebound.space",
  "registrationToken": "gsr_替换为一次性注册凭证",
  "serverId": "hk-dedicated-01",
  "publicHost": "203.0.113.10",
  "maxPlayers": 10,
  "gameServerAgent": "game-server-agent.exe"
}
```

`serverId` 必须与 Registration Token 绑定的 `instance_id` 完全相同。
`publicHost` 必须是游戏服务器对外公布的公网单播地址；后端拒绝回环和私网地址。
Wrapper 的既有字段继续提供显示名称、区域、模式、外部端口和其他启动设置。

等价的命令行覆盖参数为：

| JSON 字段 | Wrapper/Payload 参数 |
| --- | --- |
| `registrationToken` | `-registrationtoken=<token>` |
| `serverId` | `-serverid=<instance_id>` |
| `publicHost` | `-publichost=<address>` |
| `maxPlayers` | `-maxplayers=<count>` |
| `gameServerAgent` | `-gameserveragent=<path>` |

程序先读取 JSON，非空命令行参数再覆盖它。Wrapper 将一次性 Token 传给 Payload，
Payload 只通过 `GAME_SERVER_REGISTRATION_TOKEN` 向 Agent 暴露该值；Agent 命令行和
日志中不包含 Token。首次注册优先使用本机 ACL 受限的配置文件；兼容参数
`-registrationtoken=` 可能被有权查看进程命令行的其他本机进程读取。

由服务管理器直接调用 Agent 时，完整参数如下：

| Agent 参数 | 默认值/用途 |
| --- | --- |
| `-control-plane-url` | `http://127.0.0.1:8080`；主 API 地址 |
| `-fallback-control-plane-url` | 空；只用于心跳回退 |
| `-identity-file` | `game-server-identity.json` |
| `-instance-id` | 首次注册必填 |
| `-display-name` | `Dedicated Server` |
| `-region` | `asia-hk` |
| `-mode` | `tdm` |
| `-version` | `1.0.0` |
| `-public-host` | 首次注册必填 |
| `-public-port` | `7777` |
| `-max-players` | `16` |
| `-rotate-before` | `6h` |
| `-heartbeat-state` | `READY` |
| `-player-count` | `0` |
| `-once` | 发送一次心跳后退出，而不是常驻运行 |

直接调用时，首次使用的秘密仍然只能来自
`GAME_SERVER_REGISTRATION_TOKEN`；应通过服务的秘密注入机制设置，并在注册后删除。
不得自行增加会把 Token 放进进程参数的脚本选项。

## 运行行为与秘密存储

上一次成功执行后，Payload 最多每 15 秒以 `-once` 模式启动一次 Agent。Wrapper
传入实时玩家数；无玩家时上报 `READY`，至少一名玩家在线后上报 `RUNNING`。首次
运行要求 Registration Token、`serverId` 和 `publicHost`；后续运行只使用身份文件，
不再需要 Registration Token。

使用标准生产后端时，Wrapper 会提供 `https://cnapi.project-rebound.space` 作为回退。
Agent 只允许幂等的签名心跳使用回退；注册和凭证轮转始终只访问配置的主 Control
Plane，避免超时后出现凭证结果不确定。自定义后端不会隐式使用生产回退地址。

Wrapper 将身份文件命名为
`game-server-identity-<清理后的serverId>.json`。其中包含节点私钥、运行 Token、
证书、CA 证书、凭证代数和过期时间。Agent 原子写入该文件，并在 Windows 上把 ACL
限制为当前用户；非 Windows 版本使用 `0600`。只能将其备份到批准的秘密存储，不能
把同一身份复制给另一个实例。

运行 Token 和证书默认有效 24 小时。当任意一项只剩 6 小时或更少时，Agent 使用新
生成的 Ed25519 密钥同时轮转两者。旧凭证对只在 60 秒重叠期内允许普通运行流量，
不能再次轮转或注销服务器。

身份文件生成后，Payload 会从自身环境和内存清除 Registration Token。Payload 无法
安全改写另一个 Wrapper 进程的配置，因此运维人员仍须从 `serverconfig.json` 删除
`registrationToken` 并重启 Wrapper。该 Token 此时已经消费，但删除它可以减少本机
秘密暴露。

## 生产 Control Plane 前置条件

生产环境只有同时提供 `GAME_SERVER_CA_CERT_PEM_BASE64` 与
`GAME_SERVER_CA_KEY_PEM_BASE64`，且二者构成匹配的 CA，才能初始化专服证书签发器。
`Backend/scripts/generate-control-plane-env.sh` 创建的新环境已经包含二者。升级旧环境
时，只增加单独生成的 Game Server CA，不得替换既有 Access、Relay、更新、数据库或
MFA 秘密。

跨镜像发布和主机重建必须保持该 CA 稳定。替换或丢失 CA 后，旧 CA 签发的专服证书
将无法正常续期。环境文件保持 `0600`，通过正常加密备份流程保存；不得把 Game
Server CA 私钥复制到 Dedicated Server、MetaServer、网关或 GitHub Actions 日志。

当前实现不读取旧的 `GAME_SERVER_REGISTRATION_TOKENS` 环境变量。注册凭证是由玩家
或管理员 API 签发、保存在数据库中、绑定实例且只能使用一次的记录。

## 验证与排障

注册完成后：

1. 确认身份文件存在且 ACL 受限；
2. 从 Wrapper 配置删除一次性 Token；
3. 确认服务器出现在 `GET /v1/game-servers`；
4. 确认 `player_count` 与实时玩家数一致，状态按 `READY`/`RUNNING` 变化；
5. 确认日志中没有 Token、私钥或完整身份文档。

服务器未出现在列表时，检查 Wrapper 日志中的 `serverId`、`publicHost`、Agent 路径
或 Registration Token 缺失提示。HTTP 注册失败不代表可以切换到回退端点或改用不
绑定实例的静态 Token。只有确认旧 Token 已过期或注册未成功后，才为该实例签发新
Token。

完整 HTTP 与主机契约见[外部 API](../api/external.zh-CN.md)、
[内部 API](../api/internal.zh-CN.md)和[部署手册](deployment-guide.zh-CN.md)。
