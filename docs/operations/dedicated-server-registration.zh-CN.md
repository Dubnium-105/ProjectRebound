# Dedicated Server 注册与运行身份

[English](dedicated-server-registration.md) | 简体中文

本文说明由 Rust Toolbox 与进程内 Payload 实现的生产注册链路。HTTP 请求和响应字段
仍以 OpenAPI 契约为准。

## 凭证流程

```text
已验证玩家 + Dedicated Server 邀请/资格
  -> 与 instance_id 绑定的一次性 Registration Token
  -> Rust Toolbox 生成 Ed25519 密钥和 PKCS#10 CSR
  -> server_id + 运行 Token + 节点证书
  -> Toolbox 通过每次启动专属的命名管道读取非秘密游戏状态
  -> 签名心跳 + 密钥/Token/证书自动轮转
```

合格邀请的不变权限快照必须包含 `allow_game_server_registration: true`。已验证玩家
调用 `POST /v1/game-server-registration-tokens` 时可以兑换该邀请；已有资格的玩家省略
`invite_code`。返回的 `gsr_...` Token：

- 只绑定一个稳定的 `instance_id`；
- 默认 10 分钟过期；
- 只在带 `Cache-Control: no-store` 的响应中返回一次，之后无法恢复；
- 第一次成功注册时原子消费；
- 为同一实例重新签发时撤销此前尚未消费的 Token。

拥有 `game_servers.register` 权限且完成 MFA Step-up 的管理员也可在
**联机管理 / Dedicated Server / 添加服务器**中签发。管理员选择 1–168 小时有效期
并填写审计原因。它仍是实例绑定、单次使用的凭证，而不是可复用的全局秘密。

玩家签发请求示例：

```bash
curl -fsS https://api.project-rebound.space/v1/game-server-registration-tokens \
  -H "Authorization: Bearer $PLAYER_ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"instance_id":"hk-dedicated-01","invite_code":"REDACTED"}'
```

不得把响应 Token 写入 Git、日志、聊天或工单正文；只能安全传递到与其匹配的
Windows 主机，并写入 Toolbox 管理的配置。

## Toolbox 配置

使用 `ProjectReboundToolbox` 中的 Rust Toolbox。生产链路不再安装或启动独立的
`game-server-agent.exe`。

配置 `ServerLauncher/serverconfig.json`：

```json
{
  "backend": "https://api.project-rebound.space",
  "offline": false,
  "registrationToken": "gsr_替换为一次性注册凭证",
  "serverId": "hk-dedicated-01",
  "serverName": "Hong Kong Dedicated 01",
  "serverRegion": "asia-hk",
  "mode": "pvp",
  "gameVersion": "0.7.0",
  "publicHost": "203.0.113.10",
  "externalPort": 7777,
  "maxPlayers": 10
}
```

`serverId` 是稳定的 `instance_id`，必须与 Registration Token 的绑定完全相同；它
不是后端生成的 `server_id`。`publicHost` 必须是公网单播地址，后端会拒绝回环和
私网地址。Toolbox 修改游戏模式时会保留已有配置。

一次性 Token 仅由 Toolbox 读取。Wrapper 不再接受或转发 `-registrationtoken`，
Payload 也不再读取 `GAME_SERVER_REGISTRATION_TOKEN`。不得把 Token 放入任何进程
命令行。

## 注册命名管道

每次启动服务器时，Toolbox 生成带 192 位随机后缀的管道名，只向 Wrapper 传入
`-pipe=<name>`。Wrapper 将该名称转发给游戏进程。Payload 创建单实例、双工、消息
模式的 `\\.\pipe\<name>`，并限制为同一 Windows 用户和同一会话。

注册工作线程作为客户端连接并发送：

```text
server_status\t{"request_id":"server-status-1"}\n
```

Payload 只返回非秘密运行状态：

```text
server_status_ack\t{"state":"RUNNING","player_count":2,"round_state":"InProgress","request_id":"server-status-1"}\n
```

无玩家时 `state` 为 `READY`，有玩家时为 `RUNNING`。Registration Token、节点私钥、
运行 Token、证书、CSR 和签名都不会经过管道。全部 HTTP 和凭证操作由 Toolbox
负责。同一用户的进程若得知随机名称仍可能连接，因此管道契约刻意不包含任何
长期秘密。

## 运行行为与秘密存储

Toolbox 等待 Payload 管道就绪后才注册，随后：

1. 在本机生成 Ed25519 密钥，并向配置的主 Control Plane 提交 PKCS#10 CSR；
2. 将签发身份保存为 `serverconfig.json` 旁的
   `game-server-identity-<清理后的instance_id>.dpapi`；
3. 使用当前用户范围的 Windows DPAPI 加密完整身份并原子替换文件；
4. 身份安全落盘后，从 `serverconfig.json` 自动删除 `registrationToken`；
5. 通过命名管道轮询实时状态，并按服务端给出的间隔发送签名心跳；
6. 当运行 Token 或证书剩余不超过 6 小时时，生成新 Ed25519 密钥并轮转。

注册和轮转只访问配置的主 Control Plane，避免超时后产生凭证结果不确定。对两个
标准生产地址，幂等签名心跳可在 `https://api.project-rebound.space` 与
`https://cnapi.project-rebound.space` 之间回退；自定义后端没有隐式回退。临时心跳
或轮转失败会记录并重试，不会把私钥操作移入 Payload。

运行 Token 和证书默认有效 24 小时。轮转后，旧凭证对只在 60 秒重叠期内允许普通
运行流量，不能再次轮转或注销服务器。

DPAPI 将身份绑定到 Windows 用户配置文件。只能作为已批准的主机/用户配置文件备份
的一部分保存，并在相同服务身份下测试恢复。不得把一个节点身份复制给其他实例。
身份文件丢失后，需要使用新的实例绑定一次性 Registration Token 重新授权注册。

`Backend/cmd/game-server-agent` 下的 Go 命令仅保留为后端协议参考和开发诊断客户端，
不属于 Toolbox 生产启动链路。

## 生产 Control Plane 前置条件

生产环境只有同时提供 `GAME_SERVER_CA_CERT_PEM_BASE64` 与
`GAME_SERVER_CA_KEY_PEM_BASE64`，且二者构成匹配 CA，才能初始化专服证书签发器。
`Backend/scripts/generate-control-plane-env.sh` 创建的新环境已包含二者。升级旧环境
时，只增加单独生成的 Game Server CA，不得替换既有 Access、Relay、更新、数据库或
MFA 秘密。

跨镜像发布和主机重建必须保持该 CA 稳定。环境文件保持 `0600`，通过正常加密备份
流程保存。不得把 Game Server CA 私钥复制到 Dedicated Server、Toolbox 主机、
MetaServer、网关或 CI 日志。

当前实现不读取旧的 `GAME_SERVER_REGISTRATION_TOKENS` 环境变量。注册凭证由玩家或
管理员 API 签发、存储在数据库中、绑定实例且只能使用一次。

## 验证与排障

注册完成后：

1. 确认 `registrationToken` 已从 `serverconfig.json` 删除；
2. 确认 `.dpapi` 身份文件存在，且不能作为 JSON 直接读取；
3. 确认 Toolbox 日志显示已连接 Payload 管道并接受签名心跳；
4. 确认服务器出现在 `GET /v1/game-servers`；
5. 确认 `player_count` 与实时玩家数一致，状态按 `READY`/`RUNNING` 变化；
6. 确认 Wrapper/Payload 日志和进程命令行中没有 Token、私钥、CSR、签名或完整身份。

服务器未出现在列表时，检查 `serverId`、`publicHost`、`offline`、主后端地址和一次性
Token 是否过期。缺少 `server_status_ack` 表示 Toolbox/Wrapper/Payload 版本不匹配，
或 `-pipe` 未正确转发。不得把注册切换到回退端点，也不得改用不绑定实例的静态
Token。只有确认旧 Token 已过期或在注册前失败后，才为该实例签发新 Token。

完整契约见[外部 API](../api/external.zh-CN.md)、[内部 API](../api/internal.zh-CN.md)、
[命名管道协议](../architecture/command-framework.zh-CN.md)和
[部署指南](deployment-guide.zh-CN.md)。
