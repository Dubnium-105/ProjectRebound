# 架构文档

[English](README.md) | 简体中文

- [系统总览](overview.zh-CN.md)：组件、信任边界、控制流和数据流；
- [P2P 联机完整架构](online-architecture.zh-CN.md)：房间、连接协调、直连、Relay、BattleLog、状态机、部署与当前实现边界；
- [社区 VNT 节点联机目标架构](vnt-community-online-architecture.zh-CN.md)：社区节点、房间隔离、VNT bootstrap、ToolBox/VNT-Node、安全边界和从现有 Relay 的迁移设计；
- [认证与会话](authentication.zh-CN.md)：玩家身份、Token rotation 和风控；
- [Relay 协议 V2](relay-protocol.zh-CN.md)：UDP BIND、认证数据包和 MTU；
- [Relay 故障迁移](relay-migration.zh-CN.md)：状态机、事件和一致性边界；
- [运行时命令框架](command-framework.zh-CN.md)：桌面浏览器与 Payload 的命名管道协议。
- [CommandFramework C++ 实现分析](command-framework-code-analysis.zh-CN.md)：逐函数行为、所有权、缺陷、安全与测试指南；
- [MetaServer 架构](metaserver.zh-CN.md)：Go 服务、持久化、身份、调度和 Relay 动态发现；
- [MetaServer 原生协议](metaserver-native-protocol.zh-CN.md)：TLS 隧道、分帧、Gate 和已确认 RPC 边界；
- [MetaServer 威胁模型](metaserver-threat-model.zh-CN.md)：资产、控制、剩余风险和安全门禁。

具体端点以 [API 文档](../api/README.zh-CN.md)和机器可读契约为准；生产操作以[运维文档](../operations/README.zh-CN.md)为准。
