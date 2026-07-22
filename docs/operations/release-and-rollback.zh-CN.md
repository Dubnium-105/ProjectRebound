# V1.1发布与回滚

[English](release-and-rollback.md) | 简体中文

生产部署使用不可变的 GHCR 引用：`sha-<40-character commit>`、发布标签（例如 `1.1.0`）或最好是 `@sha256:<digest>`。 `latest` 被拒绝。 CI 记录每个图像工件的 Git 提交、UTC 构建时间、Go 版本、图像摘要、中继协议版本和架构版本。

## 迁移政策

V1.1 迁移 000009 到 000016 是扩展/迁移更改：它们添加表、索引、约束、兼容字段和非破坏性中继分配 `MIGRATING` 状态。控制平面迁移器使用 PostgreSQL 咨询锁序列化启动，将每个迁移包装在事务中，如果已应用的校验和发生更改，则拒绝部署。 V1.1 迁移不会删除表或列。在旧代码退役并且恢复演习通过后，合同更改将推迟到以后的版本；因此，正常的映像回滚不会回滚数据库。

## 控制平面发布

准备 `DATABASE_URL`、`BACKUP_ENCRYPTION_RECIPIENT`、`CONTROL_PLANE_ENV_FILE`、`CONTROL_PLANE_IMAGE`、可访问的 `PREFLIGHT_OBJECT_STORAGE_PROBE_URL` 以及可选的脱离主机 `BACKUP_RCLONE_REMOTE`。然后运行：

```bash
cd Backend
scripts/release/control-plane.sh
```

该脚本创建并验证加密备份，运行 `scripts/release/preflight.sh`，提取并验证映像摘要，启动新的控制平面（应用兼容的迁移），执行公共/内部烟雾检查，并观察 PostgreSQL/Redis 指标。失败的部署会恢复以前的映像并保留兼容的迁移。

对于单个控制平面实例，在运行之前宣布维护时段。多实例部署应启动一个新实例，传递运行状况和小流量金丝雀，然后移动剩余流量。

## 接力滚动发布

一次只升级一个节点：

```bash
export RELAY_NODE_ID=relay_hgh
export RELAY_ADMIN_BASE_URL=http://127.0.0.1:18080
export RELAY_ADMIN_TOKEN='...'
export EDGE_RELAY_IMAGE='ghcr.io/dubnium-105/projectrebound-edge-relay@sha256:...'
Backend/scripts/release/rolling-edge-relay.sh
```

该脚本在启用迁移的情况下请求 `DRAINING`，等待 `active_allocations=0`，部署固定映像，等待 mTLS 控制连接，恢复节点并验证 `READY`。失败时，它会尝试前一个映像并恢复节点。不要在每个中继上同时运行此操作。

Relay 没有计划的重启窗口：让健康的节点保持运行。在计划升级之前，还要验证其证书是否具有足够的剩余有效性，并且目标映像支持运行时更新。部署后记录新的映像摘要、证书到期、控制连接、新的心跳和分配计数。如果节点已经离线，恢复可能只会重新启动受影响的容器；这不是滚动发布的快捷方式。

## 手动回滚

停止进一步发布，保留日志和发布记录，然后运行：

```bash
CONTROL_PLANE_ENV_FILE=/secure/control-plane.env \
  Backend/scripts/release/rollback.sh control-plane \
  ghcr.io/dubnium-105/projectrebound-control-plane@sha256:...
```

对于中继，首先将其耗尽并使用 `edge-relay` 作为目标。回滚后验证健康状况、身份验证绑定/刷新、房间创建/加入/心跳、WebSocket 传递、中继 BIND/数据转发和迁移。仅对明确审查的破坏性迁移或已确认的数据损坏使用数据库恢复；它不是普通 V1.1 映像回滚的一部分。在事件报告中记录触发器、时间戳、图像摘要、数据库架构、受影响的节点、验证结果和操作员。

## 释放门

- 配置没有占位符，秘密文件的模式为 0600；
- PostgreSQL、Redis、对象存储探针、OpenAPI、迁移状态、磁盘、备份新鲜度和图像摘要传递预检；
- CI `go test -race ./...`、`go vet ./...`、Compose、shell、Caddy、Prometheus 和图像作业 pass；
- 没有 `latest` 映像，没有计划的健康中继重启，并且没有同时的中继队列重启；
- 每个中继报告新的心跳、连接的控制流、足够的证书空间和支持运行时更新的镜像；
- 迁移前备份校验和/验证成功；
- 仪表板和警报显示观察窗口期间健康的新实例。
