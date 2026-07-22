# 架构文档

- [系统总览](overview.md)：组件、信任边界、控制流和数据流；
- [认证与会话](authentication.md)：玩家身份、Token rotation 和风控；
- [Relay 协议 V2](relay-protocol.md)：UDP BIND、认证数据包和 MTU；
- [Relay 故障迁移](relay-migration.md)：状态机、事件和一致性边界；
- [运行时命令框架](command-framework.md)：桌面浏览器与 Payload 的命名管道协议。

具体端点以 [`../api/`](../api/README.md) 和机器可读契约为准；生产操作以 [`../operations/`](../operations/README.md) 为准。
