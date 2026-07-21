# V1.1 测试与 Release Gate 报告

报告日期：2026-07-21

候选代码：`main`（最终文档提交前实现/测试提交 `458f36f`）

总体状态：`NOT_READY`

`NOT_READY` 是发布阻断状态，不表示已完成 V1.1 正式验收。仓库内自动化测试已通过，但目标环境的 6 小时、24 小时、弱网/故障矩阵、告警触发和全新环境恢复演练尚未执行并归档。

## 已执行的自动化验证

| 命令 | 场景 | 结果 | 实际观察 |
| --- | --- | --- | --- |
| `gofmt -l .` | 全部 Go 源码格式 | PASS | 无输出 |
| `go vet ./...` | 全部 Go package 静态检查 | PASS | 无诊断 |
| `go test ./... -count=1` | 单元测试、进程内集成测试；无 `TEST_DATABASE_URL` 的本地 PostgreSQL 测试按设计 SKIP | PASS | 约 12.3 秒；所有已运行 package 通过 |
| `go test ./internal/loadbot ./internal/relayclient ./internal/relayruntime -count=1` | load-bot 配置、协议 V2 客户端、真实 UDP BIND/转发 | PASS | 约 3 秒 |
| `go test -race ./internal/loadbot ./internal/relayclient ./internal/relayruntime -count=1` | 本机 Race Detector 尝试 | NOT_RUN | Windows 环境缺少 `gcc`，`runtime/cgo` 构建前失败；Linux CI 执行 `go test -race ./...` |
| `staticcheck ./...` | 额外静态检查 | NOT_RUN | 工具未安装 |
| `golangci-lint run` | 额外静态检查 | NOT_RUN | 工具未安装 |

CI 使用 PostgreSQL 17 与 Redis 7 service container；设置 `TEST_DATABASE_URL` 后会运行 Auth、邀请码、房间、连接、Relay registry/migration 和迁移器的 PostgreSQL 集成测试，设置 `TEST_REDIS_ADDRESS` 后会验证 Redis 限流脚本的原子配额和 TTL。当前仓库没有把 Control Plane、集成式 Worker 和两个 Edge Relay 全部封装成 Testcontainers 测试；这是正式 Integration Gate 的未完成项。

## 安全与功能覆盖

已自动覆盖 SteamID/Device ID、限流维度与本地保守回退、邀请码最后名额并发、Refresh rotation/reuse、Session 撤销、Cookie Challenge、节点/角色/时间绑定 Token、`jti` 与数据序列重放、认证数据包、MTU、PPS/BPS/总字节、任意目标不可表达、过载/Drain、迁移幂等与重试、Keyset 轮换、证书签发/续期/撤销。`internal/relayclient` 的集成测试会启动真实 UDP Runtime，分别绑定 HOST/PEER 并验证认证 Payload 转发。

## 长时与性能结果

| 场景 | 客户端 | 房间 | Relay allocation | 要求时长 | 状态 | 成功率/延迟/资源变化 |
| --- | ---: | ---: | ---: | ---: | --- | --- |
| 基础并发 | 100 | 30 | 20 | 1h | NOT_RUN | 无目标环境报告 |
| 设计上限 | 300 | 100 | 100 | 6h | NOT_RUN | 无成功率、P50/P95/P99、内存或 goroutine 证据 |
| Relay Soak | 200 | 100 | 100 | 24h | NOT_RUN | 无泄漏、残留 allocation 或房间证据 |
| 重连风暴 | 100 | 50 | 50 | 10m | NOT_RUN | 无 WS 重连结果 |
| Relay 故障迁移 | 100 | 50 | 50 | 30m | NOT_RUN | 无迁移成功率；尚未 SIGKILL 目标 Relay |

版本化场景位于 `Backend/tests/load/scenario-v1.1-*.yaml`。JSON/Prometheus/终端报告会记录请求成功率、P50/P95/P99、BIND、迁移、包数/丢包率、字节数、Refresh 失败、allocation 创建/关闭以及 load-bot 内存和 goroutine 差值。Control Plane 与 Edge Relay 的资源差值必须从同一时窗的 Prometheus 数据另行归档。

## 弱网、故障、备份与告警

| Gate | 状态 | 缺少的实际证据 |
| --- | --- | --- |
| Mild/Moderate/Severe netem 矩阵 | NOT_RUN | 延迟、抖动、丢包、乱序、限速下的报告 |
| 每小时轮流重启 Relay | NOT_RUN | 24 次重启和迁移结果 |
| Control Plane/Redis 中途重启 | NOT_RUN | 恢复时间、错误窗口、幂等检查 |
| PostgreSQL 暂停/恢复 | NOT_RUN | 连接池恢复和重复请求验证 |
| Prometheus 告警触发 | NOT_RUN | firing/resolved 截图或 API 证据 |
| 加密备份校验与全新恢复 | NOT_RUN | 见 [`restore-test-report.md`](restore-test-report.md) |

## Release Gate 结论

| Gate | 状态 |
| --- | --- |
| Auth Gate | 自动化覆盖 PASS；日志/目标环境检查待归档 |
| Relay Security Gate | 功能测试 PASS；24h 泄漏项 NOT_RUN |
| Migration Gate | 状态机测试 PASS；双 Edge 实机故障 NOT_RUN |
| Key Gate | 自动化测试 PASS；实际证书轮换演练 NOT_RUN |
| Operations Gate | 脚本/配置存在；恢复、告警、完整回滚演练 NOT_RUN |
| Performance Gate | NOT_RUN |

因此不得创建 V1.1 正式完成标签。执行命令、环境准备和判定阈值见 [load-bot 手册](../../Backend/tests/load/README.md)、[Chaos Runbook](../runbooks/chaos-testing.md) 与 [发布清单](release-checklist.md)。
