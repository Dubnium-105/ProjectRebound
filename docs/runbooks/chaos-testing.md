# 弱网与故障测试

只允许在隔离 Linux 网络命名空间、veth/dummy 测试接口或一次性测试 VM 中执行。脚本同时要求 root、显式接口和确认字符串，并在 `PROJECTREBOUND_ENVIRONMENT=production` 时拒绝运行。

```bash
sudo env NETEM_I_UNDERSTAND=isolated-test NETEM_INTERFACE=veth-relay \
  scripts/netem/profile.sh moderate
sudo env NETEM_I_UNDERSTAND=isolated-test NETEM_INTERFACE=veth-relay \
  scripts/netem/reset.sh
```

预设：Mild 为 50ms/10ms jitter/1% loss；Moderate 为 120ms/30ms/5%/2Mbps；Severe 为 250ms/80ms/15%/3% reorder/256Kbps。也可用单项脚本和对应 `NETEM_*` 环境变量。

每次测试应在 `trap` 中调用 `reset.sh`，同时记录开始/结束时间、接口、预设、load-bot 报告、Relay migration 成功率、内存和 goroutine。验收包括控制流/WebSocket 重连、Relay BIND 重试、一次心跳丢失不误关房间、SIGKILL 后迁移以及迁移不无限重试。

Compose 故障使用 `scripts/chaos/compose-fault.sh`，并要求项目名以 `project-rebound-chaos` 开头。覆盖 Control Plane/Redis/PostgreSQL 的 restart、pause 和 SIGKILL。对象存储不可用通过测试专用无效 endpoint 注入；DNS 失败通过隔离网络 namespace/netem 注入；磁盘不足和时钟漂移只允许在有容量上限的临时卷或时间 namespace 中执行，不直接修改宿主机。
