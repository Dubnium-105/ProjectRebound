# 中继网络损伤测试

[English](README.md) | 简体中文


仅在隔离的 Linux 测试中继或网络命名空间上运行。该脚本需要明确的`NETEM_INTERFACE`、root 权限和调用者提供的集成命令；它总是在退出时删除其根 qdisc。

```sh
sudo NETEM_INTERFACE=veth-relay \
  NETEM_TEST_COMMAND='go test ./internal/relayruntime -run TestUDP -count=1' \
  ./tests/netem/run-relay-matrix.sh
```

该矩阵涵盖 50–300 毫秒延迟、20–100 毫秒抖动、1–5% 丢失、重新排序、重复数据包、受限带宽和五秒断开连接。验证 WebSocket/控制重新连接、幂等空间和分配行为、可重试的中继 BIND、从故障节点迁移以及单个丢失心跳的容忍度。切勿在生产界面上运行脚本。
