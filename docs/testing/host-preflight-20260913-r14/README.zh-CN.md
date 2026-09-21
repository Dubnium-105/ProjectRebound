# r14 候选包在交付前被拒绝

[English](README.md) | 简体中文

r14 包已在本机完成构建和安装，但在源码复核阶段被拒绝：监听前的 NetDriver 绑定可能在未确认原生 `InitListen` 成功时报告 authority-ready。

原始 ZIP 和回执均已保留。ZIP 的 SHA-256 为 `0eefdd95968142cf4dbde615188b147603dc397ea0332f27b7c9dd0109e583d1`（23,798,680 字节）。组件测试和包预检虽然通过，但没有覆盖这一集成缺陷。本轮没有声称通过真实原生多人流程。

请使用[修订后的 r14b 记录](../host-preflight-20260913-r14b/README.zh-CN.md)，其中保留了历史证据。不要分发 r14 候选包。
