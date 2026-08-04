# 负载测试

[English](README.md) | 简体中文


在具有类似生产环境的 PostgreSQL 和 Redis 池大小的隔离暂存环境中运行：

```powershell
$env:BASE_URL = 'https://staging-api.example.com'
$env:REALTIME_URL = 'wss://staging-api.example.com/v1/realtime/connect'
$env:ACCESS_TOKENS_JSON = Get-Content -Raw .\staging-access-tokens.json
k6 run .\tests\load\control-plane.js
```

令牌文件是在 Git 外部生成的，并且必须包含至少 100 个短期暂存播放器访问令牌才能使用 100 个并发 WebSocket。如果没有它，该场景仍会测试 100 个虚拟用户的运行状况、专用服务器目录、P2P 目录和客户端配置路径。

存储库本机加载机器人可以在没有 k6 的情况下运行，并发出终端、JSON 和 Prometheus 文本报告：

```bash
go run ./cmd/load-bot -config tests/load/scenario-basic.yaml -report load-report.json -prometheus-report load-report.prom
```

使用`scenario: auth-bind`使用暂存邀请代码来执行并发绑定/速率限制行为。生成的 SteamID 和设备 ID 对于每个虚拟客户端都是确定的，因此重复运行很容易关联，而无需记录凭据。长场景必须仅针对隔离的暂存基础架构运行。

邀请码应通过环境变量提供，不得提交到场景 YAML。读取配置文件后，`PROJECT_REBOUND_LOADBOT_INVITE_CODE` 会覆盖 `auth.invite_code`：

```bash
PROJECT_REBOUND_LOADBOT_INVITE_CODE='REPLACE_FROM_SECRET_MANAGER' \
  go run ./cmd/load-bot -config tests/load/scenario-basic.yaml \
  -report load-report.json -prometheus-report load-report.prom
```

每个虚拟客户端成功 bind 都会消费一次邀请码。`max_uses` 必须覆盖客户端数量，并授予 `allow_create_account`；需要创建 P2P 房间的场景还必须授予 `allow_p2p_room_registration`。玩家权限与邀请码同时到期，因此截止时间必须覆盖初始化和完整测试时段。不得输出该环境变量、把它写进报告，或让 load bot 指向生产环境。

`scenario: full`, `relay`， 和`soak`是真正的端到端流。它们绑定每个虚拟客户端，创建和加入房间，建立经过身份验证的 WebSocket，发布候选/检查结果事件，强制直接路径失败，使用特定于参与者的中继分配事件，执行 UDP 协议 v2 BIND 质询/证明，并交换经过身份验证的中继数据。客户端维护房间心跳、轮换刷新令牌、注入配置的 WebSocket 断开连接、使用当前凭据重新连接，并在迁移分配到达时重新绑定。没有中继令牌写入报告或日志。

标准门的版本为：

```bash
# 100 clients, 30 rooms, 20 allocations, one hour
go run ./cmd/load-bot -config tests/load/scenario-v1.1-basic.yaml \
  -report load-report-v1.1-basic-1h.json -prometheus-report load-report-v1.1-basic-1h.prom

# 300 clients, 100 rooms/allocations, six hours
go run ./cmd/load-bot -config tests/load/scenario-v1.1-full.yaml \
  -report load-report-v1.1-6h.json -prometheus-report load-report-v1.1-6h.prom

# 100 Relay allocations, 24 hours; Relays remain continuously online
go run ./cmd/load-bot -config tests/load/scenario-v1.1-relay-soak.yaml \
  -report load-report-v1.1-relay-24h.json -prometheus-report load-report-v1.1-relay-24h.prom

# All 100 Relay participants disconnect together and reconnect within 30 seconds.
go run ./cmd/load-bot -config tests/load/scenario-v1.1-reconnect-storm.yaml \
  -report load-report-v1.1-reconnect.json -prometheus-report load-report-v1.1-reconnect.prom

# Keep 50 allocations active, then SIGKILL their Relay with the chaos runner.
go run ./cmd/load-bot -config tests/load/scenario-v1.1-relay-failure.yaml \
  -report load-report-v1.1-relay-failure.json -prometheus-report load-report-v1.1-relay-failure.prom
```

在执行前替换暂存 URL 和邀请代码。必须明确设置分段身份验证限制的大小以适应受控的设置流量；不要削弱产量限制。版本化的六小时门包括其计划的控制平面/Redis 恢复检查，但中继 SIGKILL/迁移和`tests/netem/run-relay-matrix.sh`保留单独的故障注入证据。切勿将计划的中继重启添加到 24 小时连续在线浸泡中。在中继失败场景中，目标是承载所有 50 个分配的一次性中继；加载机器人本身永远不会终止基础设施。

对于完整的隔离 10 分钟预检、1 小时、6 小时和 24 小时序列（包括计划的控制平面依赖性恢复、连续中继可用性检查、遥测趋势检查和数据库残留检查），请使用 [V1.1 长稳定性安全带](longrun/README.md）。它使用不可变的 CI 映像，并且从不以生产堆栈为目标。分别运行 Relay 崩溃和迁移测试，以便连续在线浸泡不会被故意中断。

JSON 输出包括请求成功/失败计数和类别、成功率、P50/P95/P99、经过的持续时间、房间和中继分配计数、迁移、重新连接、字节、刷新失败和加载机器人进程内存/goroutine 增量。控制平面和中继内存/goroutine 增量必须在同一时间窗口内从 Prometheus 单独导出。

接受阈值是 API P95 低于 200 毫秒、HTTP 故障低于 1%、检查高于 99%、WebSocket 升级 P95 低于 1 秒。在五分钟的运行过程中观察 PostgreSQL 连接、Redis 延迟、goroutine 计数以及提供的 Prometheus 仪表板；延长`DURATION`到`30m`用于泄漏/浸泡验证。
