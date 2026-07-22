# 测试与验收

[English](README.md) | 简体中文

ProjectRebound 的测试分为四层：

1. 单元、Race Detector、PostgreSQL/Redis 集成和契约检查；
2. 一次性控制面加双 Relay 的真实容器联合测试；
3. 弱网、SIGKILL、依赖重启和迁移故障门禁；
4. 1 小时、6 小时和 24 小时长稳/容量门禁。

连续在线长稳不得主动重启健康 Relay。中继故障迁移、证书恢复和弱网注入必须使用独立场景，避免把预期故障时间混入稳态丢包率。

- [V1.1 验收索引](v1.1/README.zh-CN.md)
- [真实容器联合测试](../../Backend/tests/integration/README.zh-CN.md)
- [负载测试](../../Backend/tests/load/README.zh-CN.md)
- [V1.1 长稳 Harness](../../Backend/tests/load/longrun/README.zh-CN.md)
- [弱网测试](../../Backend/tests/netem/README.zh-CN.md)
- [故障注入](../../Backend/tests/chaos/README.zh-CN.md)
