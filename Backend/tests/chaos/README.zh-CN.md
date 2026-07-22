# 可重复的混乱场景

[English](README.md) | 简体中文


使用项目名称创建一次性 Compose 部署`project-rebound-chaos-*`，然后运行：

```bash
export PROJECTREBOUND_ENVIRONMENT=test
export CHAOS_I_UNDERSTAND=disposable-staging
export CHAOS_PROJECT=project-rebound-chaos-ci
export CHAOS_TEST_COMMAND='go run ./cmd/load-bot -config tests/load/scenario-basic.yaml'
scripts/chaos/run-matrix.sh
```

`compose-fault.sh`支持显式服务允许列表的重新启动、有限暂停和 SIGKILL/重新创建。暂停总是会安装一个取消暂停陷阱。它无法定位生产 Compose 项目名称。使用 netem 脚本来控制链路/DNS 类型的数据包丢失。磁盘低和时钟偏差测试需要具有有限测试文件系统/时间命名空间的一次性虚拟机或容器；它们故意不针对主机操作系统自动化。

记录恢复时间、请求失败窗口、WebSocket/控件重新连接、迁移尝试、剩余分配、数据库池恢复、Redis 回退指标、内存、goroutine 和磁盘使用情况。每次故障后重新运行相同的请求 ID/玩家以验证幂等性。
