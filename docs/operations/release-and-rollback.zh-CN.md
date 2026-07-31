# V1.1发布与回滚

[English](release-and-rollback.md) | 简体中文

生产部署使用不可变的 GHCR 引用：`sha-<40-character commit>`、发布标签（例如 `1.1.0`）或最好是 `@sha256:<digest>`。 `latest` 被拒绝。 CI 记录每个图像工件的 Git 提交、UTC 构建时间、Go 版本、图像摘要、中继协议版本和架构版本。

## Admin Web 客户端版本发布

本节管理游戏客户端更新，与后文的控制面容器发布不同。

1. 使用 `RELEASE_MANAGER` 或等价细粒度权限创建 `DRAFT`，填写平台、架构、stable/beta/toolbox 渠道、版本、最低兼容版本、强制更新策略和对象存储文件描述。beta 渠道只包含一个完整 `Release.zip`；toolbox 渠道只包含一个 `Rebound_Toolbox.exe`。
2. 执行“校验”，确认 Manifest Schema、路径、大小、SHA-256、压缩方式、CDN URL、服务端对象 `HEAD` 可用性、版本顺序和 Ed25519 签名全部通过。
3. 只有 `READY` 可发布。确认受影响平台和策略，填写工单原因，并完成 MFA Step-up。
4. 发布后验证 `/v1/updates/check` 和签名 Manifest，观察错误率与版本覆盖。
5. 回滚同样要求原因和 MFA；它会让该版本退出后续公开选择，但保留元数据与审计历史。
6. `DRAFT`、`READY` 和 `ROLLED_BACK` 可由具备 `updates.rollback` 权限的管理员填写原因、完成 MFA 后归档；`PUBLISHED` 必须先回滚。归档不可恢复，但不会删除版本和审计记录。

控制面会在校验和正式发布时，对每个生成的 CDN 下载 URL 执行有限并发、有超时的 `HEAD` 探测，因此配置的 CDN 必须支持 `HEAD`。探测成功只证明当时可达，客户端仍必须在下载后验证 SHA-256。

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
