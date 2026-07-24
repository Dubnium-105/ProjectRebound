# V1.1 身份验证和会话

[English](authentication.md) | 简体中文

`POST /v1/auth/bind` 仍然是客户端断言的 SteamID 引导程序，并不证明 Steam 帐户所有权。V1.1 通过独立的 IP、SteamID、Device ID 和 IP+Device 限制、可选邀请批次、仅追加登录/风险观察、轮换 Refresh Token、全族重放撤销以及用户/管理员会话撤销进行补偿。`device_id` 是不受信任的风险信号，绝不授予身份。

结构化设备指纹使用 `v1|uu:<16hex>|ds:<16hex>|cp:<16hex>`。解析器兼容此前未带版本的格式，并在散列前统一版本、因子顺序和十六进制大小写。PostgreSQL 不保存 SMBIOS、磁盘、CPU 或组合指纹原文。`auth_device_fingerprints` 保存带域隔离的 HMAC-SHA-256 组合摘要和各因子独立摘要，并记录稳定的密钥 ID；会话、登录事件和风险事件通过外键引用该记录。每个因子都有独立索引，因此将来的封禁关联在某个硬件因子发生变化时仍可使用。旧版不透明 Device ID 继续兼容，并沿用已有的 SHA-256 会话/风险摘要与后四位展示值，但无法追溯拆分。

`DEVICE_FINGERPRINT_HMAC_KEY_BASE64` 是独立、长期保存且至少包含 32 个随机字节的密钥；生产环境缺失时拒绝启动。对应的 `DEVICE_FINGERPRINT_KEY_ID` 与摘要一同保存。由于服务端有意丢弃因子原文，密钥遗失或变更后无法重算旧记录；必须备份密钥，并在轮换前设计明确的多密钥过渡方案。不得复用 JWT、Relay、更新签名、数据库或管理员密钥。

访问令牌是绑定到数据库会话的短期 Ed25519 JWT。刷新令牌是存储为哈希值的不透明随机秘密。成功的刷新会自动撤销旧令牌并在同一系列中发布替代令牌。重用任何轮换令牌都会撤销该系列、记录高严重性事件、递增 `auth_refresh_reuse_total`，并需要新的绑定。注销和会话管理 API 会撤销数据库状态，因此之前签名的访问令牌将停止进行身份验证。

邀请代码在日志外部生成，存储为散列，受到期日和配额限制，并在与玩家/会话创建相同的 PostgreSQL 事务中使用。并发使用不能超过`max_uses`。管理员响应在创建时可能仅返回一次明文；列出/撤销响应则不会。

操作细节：

- 公共请求/响应合约：[外部 API](../api/external.zh-CN.md#32-登录和玩家)；
- 权限和凭证边界：[授权矩阵](../../Backend/api/openapi/auth-permission-matrix.zh-CN.md)；
- 风险/会话/邀请管理：[内部 API](../api/internal.zh-CN.md)；
- 滥用响应：[授权滥用操作手册](../operations/runbooks/auth-abuse.zh-CN.md)。

切勿在管理响应中记录 Authorization、Access/Refresh Token、邀请明文、私钥、完整设备 ID、设备因子摘要或未屏蔽的客户端 IP。Steamworks Auth Ticket 验证明确不在 V1.1 范围内。
