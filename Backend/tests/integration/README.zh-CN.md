# V1.1一次性集成门

[English](README.md) | 简体中文


这些门有意与普通 Go 模块分开。它们需要 Linux Docker 主机，并且仅创建临时容器、卷、网络、密钥、玩家、房间和中继身份。

## 控制平面、两个Relay节点、弱网、短故障恢复

Testcontainers 门显式重建当前源（因此过时的本地映像无法绕过代码更改），启动 PostgreSQL 17、Redis 7、带有集成工作线程的控制平面以及两个 Edge Relay 节点`198.18.11.0/24`。它运行真实的身份验证、房间、WebSocket、中继分配、协议 v2 BIND 和 UDP 数据路径。然后注射轻度、中度和重度`netem`配置文件到一个中继容器中，运行 100 个客户端/50 个分配重新连接风暴，SIGKILL 一个活动中继并要求成功迁移，并在重复干净的端到端流程之前重新启动 Redis、PostgreSQL 和控制平面。重启后准备需要比控制平面重启更新的中继心跳，而不是陈旧的 READY 行。

港口`127.0.0.1:28080`和子网`198.18.11.0/24`必须未使用。如果没有明确的一次性环境确认，安全包装器将拒绝运行：

```bash
cd Backend/tests/integration
sudo env \
  V11_INTEGRATION_I_UNDERSTAND=disposable-docker-stack \
  TESTCONTAINERS_RYUK_DISABLED=true \
  ./run-gate.sh
```

包装器和测试始终请求 Compose 卷/孤立清理。如果进程在 Go cleanup 之外被杀死，则仅查找名称以`project-rebound-v11-`，检查它们的标签，并删除那些确切的临时项目。

## 加密 PostgreSQL 恢复练习

恢复练习创建两个不相关的 PostgreSQL 17 容器。它迁移和播种源，包括故意活动的房间/连接/中继记录，运行生产备份、校验和、加密、验证和恢复脚本，然后验证架构版本 16、当前架构中的所有 22 个应用程序表名称、固定播放器、备份/恢复指标以及针对新目标的迁移幂等性。生产恢复事务还必须关闭恢复的房间，使恢复的连接/分配失败，将中继标记为离线，并重置活动成员/分配计数，以便无法从快照中恢复短暂的活动状态。

需要配套的PostgreSQL客户端工具，`age`和 Docker：

```bash
cd Backend/tests/integration
sudo env \
  PATH="$PATH" \
  RESTORE_DRILL_I_UNDERSTAND=disposable-postgres-containers \
  ./run-restore-drill.sh
```

该命令打印`RESTORE_DRILL_OK`、RTO、总持续时间、模式版本、恢复的玩家 ID、所需表计数和加密备份 SHA-256。生成的身份和备份仅存在于经过验证的临时目录中，并被退出陷阱删除。

生产环境中不允许使用这两种脚本。6 小时/24 小时浸泡场景见 [`../load/README.zh-CN.md`](../load/README.zh-CN.md)。
