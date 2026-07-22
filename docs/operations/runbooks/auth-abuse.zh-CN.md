# 身份验证滥用操作手册

[English](auth-abuse.md) | 简体中文

在 `AuthBindRateLimitSpike`、`RefreshTokenReplayDetected`、`MultiAccountRiskSpike` 或 `InviteCodeFailureSpike` 上触发。

1. 记录警报时间、受影响维度、部署摘要和请求 ID；切勿将凭据复制到事件中。
2. 通过受信任的内部管理端点检查 `/v1/admin/auth/risk-events`。关联屏蔽 IP、SteamID、设备哈希信号、批次和故障代码。
3. 通过记录的管理 API 撤销受损的邀请代码或玩家会话。刷新重用已经撤销了它的代币家族。
4. 保持生产限制。仅在到期且有证据表明不会阻止共享 NAT 用户的情况下应用临时上游 IP 阻止。
5. 确认绑定失败/限制率返回到基线并且活动会话不会意外增长。
6. 保留经过清理的审计行并写入事件结果。使用密钥泄露 Runbook 轮换暴露的管理员令牌。
