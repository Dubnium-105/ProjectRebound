# V1.1 测试与 Release Gate 报告

[English](test-report.md) | 简体中文

报告日期：2026-07-22

生产候选：`66cde85a7528781b18e4302289d5d6087364076d`

房间 TTL 修复：`447e27a`；连续在线策略：`66cde85a`

总体状态：`PRODUCTION_DEPLOYED_24H_SOAK_PENDING`

V1.1 已部署到生产控制面、LAX 和 HGH Relay；CI 的 7 个 job 全部通过。短期功能、安全、弱网、迁移、故障恢复、告警和全新恢复门禁已通过；1 小时和 6 小时容量长稳已通过。正式 24 小时 Relay 连续在线 Soak 尚未按修正后的“不做计划重启”口径完成，因此生产可继续观察，但不得把当前证据标记为完整 24 小时 Release Gate。

## 自动化质量门禁

| 命令 | 场景 | 结果 | 实际观察 |
| --- | --- | --- | --- |
| `gofmt -l .` | 全部 Go 源码格式 | PASS | 无输出 |
| `go vet ./...` | 全部 Go package 静态检查 | PASS | 无诊断 |
| `go test ./... -count=1` | 单元与进程内集成测试 | PASS | 本机约 15 秒；所有已运行 package 通过 |
| `go test ./internal/loadbot -count=20` | load-bot 短周期稳定性回归 | PASS | 修复首个请求等待 ticker 导致的短测偶发空报告 |
| `TEST_DATABASE_URL=… TEST_REDIS_ADDRESS=… go test -race ./... -count=1` | Linux、PostgreSQL 17、Redis 7、全 package Race Detector | PASS | 约 14.3 秒；临时容器已删除 |
| `go test -tags=integration ./... -run '^$'` | 独立 Testcontainers 模块编译 | PASS | 无测试执行，仅验证集成门禁可编译 |
| `promtool test rules v1.1.rules.test.yml` | V1.1 告警 firing/resolved 单元测试 | PASS | PostgreSQL/Redis、Auth/Relay replay、无可用 Relay、备份与恢复告警通过 |
| `staticcheck ./...` / `golangci-lint run` | 额外静态检查 | NOT_RUN | 当前执行环境未安装；不是仓库既有 CI 门禁 |

CI 现在将 Testcontainers 门禁作为镜像发布的前置依赖，并在每次运行前显式构建当前源码，避免固定本地镜像标签掩盖代码修改。CI 同时运行 Go race、PostgreSQL/Redis 集成、OpenAPI/部署配置、Prometheus 规则、Shell 语法、文档链接、部署/回滚脚本与镜像 provenance。

## 真实容器联合测试

环境：Debian 13、PostgreSQL 17、Redis 7、当前源码 Control Plane（含集成式 Worker）、两个 Edge Relay、隔离网段 `198.18.11.0/24`。最终不含 netem 的组合复跑耗时 330.2 秒并 PASS；同一代码的完整 netem 组合中三个子测试均 PASS。

| 场景 | 客户端 / 房间 / allocation | 结果 |
| --- | --- | --- |
| 基线全链路 | 4 / 2 / 2，20s | API 32/32，P50 6.8ms、P95 13.5ms、P99 18.0ms；BIND 4/4；UDP 196/196；allocation 2/2 回收；内存 +1,256,736B，goroutine +4 |
| 集中重连短测 | 100 / 50 / 50，90.6s | API 900/900，P50 4.4ms、P95 8.5ms、P99 77.5ms；BIND 100/100；UDP 8200/8200；200 次 WebSocket 重连；allocation 50/50 回收；内存 +2,476,080B，goroutine +4 |
| Relay A `SIGKILL` | 4 / 2 / 2，45.2s | API 54/54；迁移 1/1；BIND 6/6；UDP 415/418，故障窗口丢包 0.718%；无无限重试；allocation 清理完成 |
| Redis 重启 | 短故障 | 2.791s 恢复 READY |
| PostgreSQL 重启 | 短故障 | 3.608s 恢复 READY，连接池随后完成真实 Auth/房间/Relay 流程 |
| Control Plane 重启 | 短故障 | 8.648s 内恢复并观察到两台 Relay 的新心跳；随后 API 32/32、BIND 4/4、UDP 196/196 |

Control Plane 重启后的 Relay 判断不再只相信数据库中的旧 READY 状态，而是要求 `last_heartbeat_at` 晚于本次重启，防止测试在 lease 即将过期时误判恢复完成。

## 弱网矩阵

`tc netem` 只注入到一台 Relay 的容器网络命名空间，并在每个子测试后强制 reset。每个场景持续 20 秒，其中约 8 秒处于注入状态；因此报告中的全场景有效丢包率低于配置的瞬时丢包率。

| Profile | 注入参数 | API / BIND | UDP 结果 | 判定 |
| --- | --- | --- | --- | --- |
| Mild | 50ms、10ms jitter、1% loss | 32/32；4/4 | 181/182，0.549% | PASS |
| Moderate | 120ms、30ms、5% loss、2Mbps | 32/32；4/4 | 196/196，本次随机窗口 0% | PASS |
| Severe | 250ms、80ms、15% loss、3% reorder、256Kbps | 32/32；4/4 | 163/165，1.212% | PASS |

## 备份、恢复与告警

| Gate | 状态 | 证据 |
| --- | --- | --- |
| Prometheus 告警规则 | PASS | `promtool test rules` 同时验证 firing 与 resolved |
| 加密 PostgreSQL 备份/校验 | PASS | custom dump、压缩、age、SHA-256、`pg_restore --list` |
| 独立密钥恢复 | PASS | Access、Relay、Manifest、Relay CA 独立加密包恢复 |
| 全新 Control Plane | PASS | 恢复后 `/health/ready` 与管理员 player 查询成功 |
| 旧 Manifest 连续性 | PASS | 恢复前后归一化请求 ID 后逐字节一致 |
| Relay 恢复 | PASS | 新 Relay 使用恢复出的 CA/密钥重新注册并进入 READY |
| 易失状态不复活 | PASS | room CLOSED、connection/allocation FAILED、旧 Relay OFFLINE、活动计数归零 |

完整数值和 SHA-256 见 [恢复演练报告](restore-test-report.zh-CN.md)。

## 长稳与正式容量门禁

| 场景 | 实际结果 | 状态 |
| --- | --- | --- |
| 预检 | 600.3 秒；HTTP 成功率 100%；UDP 零丢包 | PASS |
| 基础并发 | 3,600.3 秒；HTTP 成功率 100%；P95 5.733 ms；UDP 零丢包 | PASS |
| 设计上限 | 21,601.0 秒；155,345 个 HTTP 请求，成功率 100%，P95 5.520 ms；UDP 43,170,300 发、43,170,297 收，丢包率 0.00000695%；包含 Redis 和 Control Plane 恢复；无残留 allocation | PASS |
| 修正后 Relay 连续在线验证 | 601.2 秒；HTTP 4,780/4,780，P95 6.482 ms；UDP 1,170,600/1,170,600；100 次 allocation；控制断链样本 0；无残留 | PASS（10 分钟） |
| Relay Soak | 100～300 allocation、24 小时；Relay 不做计划重启，仅在确认掉线后恢复 | PENDING_24H |
| 正式重连风暴 | 100 客户端、10 分钟 | SHORT_PASS；90.6 秒功能门禁通过，未满足正式时长 |
| 正式 Relay 故障 | 50 allocation、30 分钟 | SHORT_PASS；双 Relay 真实 SIGKILL/迁移通过，未满足正式规模与时长 |

曾执行一轮 17.1 小时测试，每小时强制重启 Relay，最终 UDP 丢包率为 14.7928%，且没有形成有效迁移证据。该结果证明“周期重启”会主动破坏正在中继的内存态对局，不属于连续在线 Soak。测试策略现已改为健康节点不显式重启、仅在确认掉线后恢复；故障注入另行执行。该轮失败数据保留作为反例，不计入 24 小时门禁。

## Release Gate 结论

| Gate | 状态 |
| --- | --- |
| Auth Gate | PASS |
| Relay Security Gate | SHORT_PASS；功能/安全与 6h 容量通过，24h 连续在线项待完成 |
| Migration Gate | SHORT_PASS；真实故障迁移、幂等、恢复 READY 通过 |
| Key Gate | PASS；自动轮换/证书测试及恢复演练通过 |
| Operations Gate | PASS；备份恢复、告警、迁移幂等、部署/回滚脚本均有自动化门禁 |
| Performance Gate | PARTIAL_PASS；1h/6h 通过，修正后 10 分钟连续在线通过，24h 待完成 |

结论：当前生产版本无已知短期发布阻断问题，1 小时和 6 小时门禁通过；正式 24 小时连续在线证据仍待补齐，版本状态保持 `PRODUCTION_DEPLOYED_24H_SOAK_PENDING`。Relay 的权威运行口径见 [连续在线与恢复策略](../../operations/relay-continuity.zh-CN.md)。
