# V1.1长稳定门

[English](README.md) | 简体中文


该工具在独立的 Docker Compose 项目中运行 V1.1 稳定性门。它不连接到生产数据库、Redis、控制平面或中继节点。唯一公开的端口是控制平面`127.0.0.1:38080`.

该序列是故意快速失败的：

1. 十分钟飞行前：100 个客户、30 个房间、20 个中继分配；
2. 一小时基本门：100个客户，30个房间，20个中继分配；
3. 六小时全关：300 个客户端、100 个房间和 100 个 Relay 分配，Redis 和控制平面中途重新启动；
4. 24 小时中继浸泡：200 个客户端、100 个房间和 100 个中继分配，两个中继节点保持持续在线。

每个门都从新的 PostgreSQL、Redis 和 Relay 卷开始。仅当负载报告、依赖性/资源遥测、中继控制链路连续性和清理后数据库残留检查全部通过时，门才会通过。报告包括 API 延迟和错误率、UDP 传输、刷新令牌活动、内存/goroutine 趋势、数据库池使用情况、依赖项可用性、迁移、重复记录和孤立资源。浸泡门永远不会重新启动健康的中继。 Docker会自动重启退出的Relay；线束仅在确认继电器保持停止状态后才尝试显式恢复。

使用 CI 生成的不可变图像：

```bash
export V11_CONTROL_PLANE_IMAGE=ghcr.io/owner/projectrebound-control-plane:sha-<full-commit>
export V11_EDGE_RELAY_IMAGE=ghcr.io/owner/projectrebound-edge-relay:sha-<full-commit>
export V11_LOAD_BOT_IMAGE=ghcr.io/owner/projectrebound-load-bot:sha-<full-commit>
export V11_LONGRUN_PROJECT=project-rebound-v11-longrun-$(date -u +%Y%m%d%H%M%S)
export V11_LONGRUN_RESULTS_DIR=/var/lib/projectrebound-longrun/$V11_LONGRUN_PROJECT
export V11_LONGRUN_HARNESS_REVISION=<full-commit>
export V11_LONGRUN_I_UNDERSTAND=isolated-docker-stack

sudo -E ./run-gates.sh
```

在服务管理器的指导下运行它大约 31 小时的序列。例如，瞬态 systemd 服务可以执行相同的环境和命令。读取进度而不附加到进程：

```bash
sudo systemctl status "$V11_LONGRUN_PROJECT.service"
sudo journalctl -u "$V11_LONGRUN_PROJECT.service" -f
sudo cat "$V11_LONGRUN_RESULTS_DIR/status.env"
sudo tail -n 20 "$V11_LONGRUN_RESULTS_DIR/events.tsv"
```

隔离配置特意将中继令牌 TTL 延长至 8 小时，并将分配 TTL 延长至 30 小时，因此固定分配可以在整个浸泡过程中保持持续活动状态。这些值仅安装到该一次性控制平面中。继电器崩溃和迁移行为与一次性继电器故障场景分开测试；不混入连续在线浸泡浇口。

保留报告后，仅删除中指定的确切项目`status.env`:

```bash
docker compose --project-name "$V11_LONGRUN_PROJECT" \
  --env-file "$V11_LONGRUN_RESULTS_DIR/secrets.env" \
  --file ./docker-compose.yaml down --volumes --remove-orphans
```

切勿将此工具指向共享或生产环境。项目名称前缀保护和显式确认变量是安全边界，不能替代检查目标主机。
