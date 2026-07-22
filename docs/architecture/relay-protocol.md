# Relay 协议 V2

机器可读实现所对应的权威线格式见 [`Backend/api/relay-protocol.md`](../../Backend/api/relay-protocol.md)。本页说明客户端和运维侧的兼容边界。

生产协议版本固定为 `2`，默认 `accept_protocol_v1: false`。握手依次为 `BIND_INIT → BIND_CHALLENGE → BIND_PROOF → BIND_OK`；Challenge Cookie 绑定源 IP/端口、nonce、MTU、Token 哈希和短时间桶，Relay 在 Proof 前不创建 allocation。无效 Cookie、认证标签、句柄、重放序列和超大包一律静默丢弃，避免 UDP 反射。

Relay Token 是节点、allocation、connection 和 HOST/PEER 角色绑定的短期 Ed25519 凭证，并带 `kid`、`jti`、`nbf`/`exp`、PPS/BPS/总字节上限。数据包使用随机 64 位 handle、每端会话密钥、序列窗口和 16 字节 HMAC 标签；包内没有目标地址，因此只能在同一 allocation 的已验证 HOST/PEER 之间转发。默认 Payload MTU 为 1200 字节，可配置范围为 1000～1350。

V1 只用于明确开启的短期迁移，不具备 V2 数据面保障，不得在生产启用。V1.1 不承诺传输加密、可靠重传、顺序保证或无损迁移；游戏 Payload 仍由端到端游戏协议自行保护。

故障切换见 [Relay 故障迁移](relay-migration.md)，签名 Keyset 和节点证书见 [Relay 密钥轮换](../operations/key-and-certificate-rotation.md)，异常处置见 [Relay 故障 Runbook](../operations/runbooks/relay-outage.md)。
