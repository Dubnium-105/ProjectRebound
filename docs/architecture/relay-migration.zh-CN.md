# Relay 故障迁移

[English](relay-migration.md) | 简体中文

## 触发与调度资格

Control Plane 按配置的心跳租约将节点依次标记为 `UNHEALTHY` 和 `OFFLINE`。控制流断开最终通过租约超时触发同一流程。调度器只选择同时满足以下条件的节点：

- 生命周期状态为 `READY`；
- 运行负载状态为 `NORMAL` 或 `DEGRADED`；
- Relay 协议版本为 2；
- 节点证书和心跳租约仍有效；
- allocation 数和出口带宽低于容量阈值；
- 支持所需传输协议。

恢复节点必须重新连接并完成心跳，按 `OFFLINE -> CONNECTING -> READY` 返回调度池；旧 allocation 不会复活。

## 故障迁移状态机

1. 幂等后台任务查询故障节点上的活动 allocation。
2. 对 connection 加活动 migration 唯一约束，保证同一时刻最多一个 `BINDING` migration。
3. 原 allocation 立即标记 `FAILED`，并在另一合格节点创建新 allocation。
4. Control Plane 依次向双方发送 `connection.relay_migrating` 和各自独立的 `connection.relay_allocated`。后者包含新的短期 Relay Token。
5. 新 Relay 的 HOST 与 PEER 均绑定后，上报 `AllocationOpened`；重复上报是幂等的。
6. migration 标记 `COMPLETED`，connection 回到 `CONNECTED`，并发送 `connection.relay_migrated`。

每次尝试具有 `migration_id`、递增的 `attempt` 和 bind deadline。默认 45 秒内未完成 BIND 即标记 `BIND_TIMEOUT`，释放该节点容量，并选择一个此前未尝试的节点。默认最多尝试 3 次。无可用节点或尝试耗尽时，connection 进入 `FAILED` 并发送 `connection.relay_failed`；后台任务不会无限重试。

## WebSocket 事件顺序

```text
connection.relay_migrating
connection.relay_allocated
connection.relay_migrated | connection.relay_failed
```

`connection.relay_migrating` 包含旧节点、旧 allocation、原因、attempt 和 `migration_id`，不包含凭证。`connection.relay_allocated` 向 HOST/PEER 分别发送不同的 Relay Token，并在迁移时附带 `migration_id` 和旧 allocation。客户端必须以 `migration_id` 和 `allocation_id` 幂等处理重复事件。

## 一致性边界

实时 UDP endpoint 只保存在 Edge 内存中，不写 PostgreSQL。PostgreSQL 保存 connection、节点、allocation、迁移尝试、deadline 和失败原因。数据库唯一索引阻止同一 connection 并发存在多个活动 migration；行锁和条件更新使重复 sweep、重复 BIND 成功以及多个 Control Plane 实例并行执行保持幂等。

V1.1 迁移允许短暂中断，不承诺无损切换、包重传或主机迁移。

强制 Drain 使用 make-before-break：旧 allocation 在数据库中进入 `MIGRATING`，不占用“当前新 allocation”唯一索引但仍计入节点容量；新 allocation 的双方 BIND 完成后旧 allocation 才变为 `CLOSED` 并收到撤销命令。若迁移尝试耗尽，连接和 `MIGRATING` allocation 一并失败清理。

## 管理员 Drain

```http
POST /internal/v1/relay-nodes/{node_id}/drain
Content-Type: application/json

{"deadline_seconds":600,"migrate_existing":false}
```

空请求体仍兼容旧调用，使用服务端默认 deadline 且不主动迁移。`migrate_existing=false` 立即停止新调度并允许现有 allocation 自然结束；`true` 使用与故障迁移相同的有界重试状态机逐步迁移现有连接，迁移原因是 `RELAY_DRAIN`。deadline 会通过控制流下发；到期时 Edge 关闭仍残留的 allocation，并上报关闭事件。节点重新连接时，`ConfigSnapshot` 会恢复已持久化的 Drain 状态。

恢复接流量需显式调用：

```http
POST /internal/v1/relay-nodes/{node_id}/resume
```
