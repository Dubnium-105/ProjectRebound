# V1.1 基线测试结果

基线 commit：`af4343a4cdc0b60836a417c182d0c65c0917c197`

执行时间：2026-07-21 12:53（Asia/Hong_Kong）

工作目录：`C:\wksp\ProjectRebound\Backend`

工作树：执行前为 clean，`main` 与 `origin/main` 一致。

## 环境

```text
OS/ARCH: windows/amd64
Go: go1.26.2
go.mod language version: 1.25.0
CGO_ENABLED: 0
TEST_DATABASE_URL: not set
```

## 必需基线

### `go test ./...`

结果：**通过**，退出码 0。

- OpenAPI、部署配置、认证、管理、连接、迁移器、游戏服务器、健康检查、中间件、可观测性、P2P、Relay registry/runtime 和更新模块的普通测试通过。
- `cmd`、`cache`、部分兼容包等没有测试文件。
- 依赖 `TEST_DATABASE_URL` 的 PostgreSQL 集成测试因本机未配置该变量而跳过。涉及：admin、auth、connection、database、gameserver、p2proom、relayregistry。

### `go vet ./...`

结果：**通过**，退出码 0，无输出。

## 项目现有附加检查

| 命令 | 结果 | 说明 |
| --- | --- | --- |
| `go mod verify` | 通过 | `all modules verified` |
| `python Tools/Docs/check_markdown_links.py` | 通过 | `MARKDOWN_LINKS_OK` |
| `bash -n Backend/scripts/*.sh Backend/deploy/deploy.sh Backend/tests/netem/run-relay-matrix.sh` | 通过 | `BASH_SYNTAX_OK` |
| `go test -race ./... -count=1` | 未执行成功 | 当前 Windows Go 环境 `CGO_ENABLED=0`，命令在测试前退出：`-race requires cgo`。CI 的 Linux runner 会执行该项。 |
| `staticcheck ./...` | 未执行 | 本机未安装 `staticcheck`。 |
| `golangci-lint run` | 未执行 | 本机未安装 `golangci-lint`。 |

## 基线限制与后续门槛

1. 本次本地基线不能替代 CI 的 Linux race 测试。
2. PostgreSQL 集成测试未在本机执行；Milestone 1 开始后必须使用隔离 PostgreSQL/Redis 运行新增迁移与并发事务测试。
3. 本次没有运行真实 UDP Relay、netem、故障迁移、备份恢复或长时间负载；这些属于后续 Milestone 的专门验收。
4. 未安装的静态分析工具已明确记录，不视为“通过”。进入 Release Gate 前应在 CI 固定版本运行或明确采用现有 `go vet` + race 策略。

## 结论

开始 V1.1 修改所需的最低代码基线（`go test ./...` 与 `go vet ./...`）通过。环境限制导致的未执行项和集成测试跳过项已经记录，可进入 Milestone 1，但不能据此声明 V1.1 的数据库、race 或长稳测试已通过。
