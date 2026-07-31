# 身份验证和会话

[English](authentication.md) | 简体中文

`POST /v1/auth/bind` 接受可选的十六进制 Steam Encrypted App Ticket。未提交 ticket 的旧客户端继续兼容，但只获得会话级 `unverified` 身份。提交 ticket 时，后端通过 stdin 将其发送给独立部署的验证程序；只要解密成功且解密出的 SteamID 与请求 SteamID 一致，就签发会话级 `verified` 身份。AppID、签发时间、VAC 状态和 ticket 是否曾使用不再作为准入条件。只要提交了 ticket，解密失败、解密出的 SteamID 无效或请求/ticket SteamID 不一致仍会拒绝，不会静默降级。

验证程序通过无 shell、无 ticket 命令行参数的方式直接启动，执行时间、stdin、stdout 和 stderr 均受限。独立 Go verifier 动态加载 Valve 对应平台的 `sdkencryptedappticket` 原生库，并从独立环境值或只读文件读取 32 字节应用 key；原生库和 key 均不会编译进控制面。AppID、签发时间和 VAC 状态仅作为尽力采集的审计元数据，读取失败不会让已成功解密的 ticket 失效。后端绝不保存 ticket 明文；元数据可用时，`auth_steam_ticket_verifications` 以 SHA-256 ticket 哈希去重保存权威 SteamID、AppID、签发时间、玩家 ID 和验证时间，重复哈希不会拒绝 bind。创建或加载玩家时使用已匹配的 ticket SteamID。

结构化设备指纹同时接受 `uuid|disk|cpu` 和 `v1|uu:<16hex>|ds:<16hex>|cp:<16hex>`。PostgreSQL 不保存 SMBIOS、磁盘、CPU 或组合指纹原文。`auth_device_fingerprints` 保存带域隔离的 HMAC-SHA-256 组合摘要和各因子独立摘要，并记录稳定的密钥 ID；会话、登录事件和风险事件通过外键引用该记录。`ban_device_fingerprint` 仅在同一封禁记录至少匹配两个因子时限制 bind。旧版无竖线的不透明 Device ID 继续兼容，并沿用已有的 SHA-256 会话/风险摘要与后四位展示值，但无法追溯拆分。

`DEVICE_FINGERPRINT_HMAC_KEY_BASE64` 是独立、长期保存且至少包含 32 个随机字节的密钥；生产环境缺失时拒绝启动。对应的 `DEVICE_FINGERPRINT_KEY_ID` 与摘要一同保存。由于服务端有意丢弃因子原文，密钥遗失或变更后无法重算旧记录；必须备份密钥，并在轮换前设计明确的多密钥过渡方案。不得复用 JWT、Relay、更新签名、数据库或管理员密钥。

访问令牌是绑定到数据库会话的短期 Ed25519 JWT，包含 `user_id`、`auth_level` 和 `steam_verified`。数据库会话是权威来源，因此未验证 bind 不能继承同一玩家此前的 verified 权限。刷新令牌是存储为哈希值的不透明随机秘密；刷新时保留原会话认证等级、迁移内存中的完整性 ticket 状态，并签发同一系列的替代令牌。重用任何轮换令牌都会撤销该系列、记录高严重性事件、递增 `auth_refresh_reuse_total`，并需要新的绑定。注销和会话管理 API 会撤销数据库状态，因此之前签名的访问令牌将停止进行身份验证。

对于 verified bind，解码后的 encrypted ticket 原始字节还会仅在进程内存中保留到该会话到期。bind 响应以及每次 `POST /v1/integrity/challenge` 都会返回新的 32 字节十六进制 nonce，并使上一个 nonce 失效。`POST /v1/integrity/proof` 使用常量时间比较 `SHA256(toolbox_pem_file_bytes || encrypted_ticket_raw_bytes || nonce_ascii)`。proof 成功后，数据库会话和玩家身份提升为 `trusted`；此前签发的 verified Access Token 在该单向提升后仍然有效，下一次 refresh 会在 claims 中携带 `trusted`。连续三次 proof 失败会撤销会话并写入 `INTEGRITY_FAILED` 审计和风险事件；若硬件指纹同时匹配封禁记录，最终风险等级提升为 critical。

ToolBox 证书从 `TOOLBOX_PUBKEY_PATH`（推荐）或 `TOOLBOX_PUBKEY` 读取。PEM 文件的精确字节（包括换行符）参与哈希，因此运维必须挂载客户端构建流程使用的同一份规范文件。challenge 状态和 ticket 明文不会写入 PostgreSQL，并会在进程重启时丢失；重启后 challenge 接口返回空 nonce，直到客户端重新 bind。

邀请代码在日志外部生成，存储为散列，受到期日和配额限制，并在与玩家/会话创建相同的 PostgreSQL 事务中使用。并发使用不能超过`max_uses`。管理员响应在创建时可能仅返回一次明文；列出/撤销响应则不会。

操作细节：

- 公共请求/响应合约：[外部 API](../api/external.zh-CN.md#32-登录和玩家)；
- 权限和凭证边界：[授权矩阵](../../Backend/api/openapi/auth-permission-matrix.zh-CN.md)；
- 风险/会话/邀请管理：[内部 API](../api/internal.zh-CN.md)；
- 滥用响应：[授权滥用操作手册](../operations/runbooks/auth-abuse.zh-CN.md)。

切勿在管理响应中记录 Authorization、Access/Refresh Token、Steam ticket 明文、完整性 nonce/proof、邀请明文、私钥、完整设备 ID、设备因子摘要或未屏蔽的客户端 IP。
