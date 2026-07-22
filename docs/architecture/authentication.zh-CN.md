# V1.1 身份验证和会话

[English](authentication.md) | 简体中文

`POST /v1/auth/bind` 仍然是客户端断言的 SteamID 引导程序，并不证明 Steam 帐户所有权。 V1.1补偿独立IP、SteamID、Device ID、IP+Device限制；可选邀请批次；仅附加登录/风险观察；轮换刷新令牌；全族范围内的重用撤销；和用户/管理员会话撤销。 `device_id` 是一个不受信任的风险信号，仅存储为密钥哈希/后缀，并且从不授予身份。

访问令牌是绑定到数据库会话的短期 Ed25519 JWT。刷新令牌是存储为哈希值的不透明随机秘密。成功的刷新会自动撤销旧令牌并在同一系列中发布替代令牌。重用任何轮换令牌都会撤销该系列、记录高严重性事件、递增 `auth_refresh_reuse_total`，并需要新的绑定。注销和会话管理 API 会撤销数据库状态，因此之前签名的访问令牌将停止进行身份验证。

邀请代码在日志外部生成，存储为散列，受到期日和配额限制，并在与玩家/会话创建相同的 PostgreSQL 事务中使用。并发使用不能超过`max_uses`。管理员响应在创建时可能仅返回一次明文；列出/撤销响应则不会。

操作细节：

- 公共请求/响应合约：[外部 API](../api/external.zh-CN.md#32-登录和玩家)；
- 权限和凭证边界：[授权矩阵](../../Backend/api/openapi/auth-permission-matrix.zh-CN.md)；
- 风险/会话/邀请管理：[内部 API](../api/internal.zh-CN.md)；
- 滥用响应：[授权滥用操作手册](../operations/runbooks/auth-abuse.zh-CN.md)。

切勿在管理响应中记录授权、访问/刷新令牌、邀请明文、私钥、完整设备 ID 或未屏蔽的客户端 IP。 Steamworks Auth Ticket 验证故意位于 V1.1 之外。
