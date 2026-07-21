# Relay 密钥轮换

Relay Token 使用独立 Ed25519 密钥集。数据库只保存公钥、状态和 `env:` 私钥引用，不保存私钥内容。活动私钥来自 `RELAY_TOKEN_PRIVATE_KEY_BASE64`；待轮换私钥通过仅注入 Control Plane 的 `RELAY_TOKEN_ROTATION_KEYS` 提供：

```dotenv
RELAY_TOKEN_KEY_ID=relay-2026-07
RELAY_TOKEN_PRIVATE_KEY_BASE64=...
RELAY_TOKEN_ROTATION_KEYS=relay-2026-08=...;relay-2026-09=...
```

启动时当前 key 记录为 `ACTIVE`，额外 key 记录为 `PENDING`。Control Plane 下发包含版本、生成时间、全部公钥、签名者和 Ed25519 签名的 Keyset。Edge 仅接受由已信任 key 签名、版本不回退的更新，并通过 mTLS 控制流上报 `KeysetLoaded(keyset_version)`。

确认所有 `READY` Relay 已确认当前 staged Keyset 后，管理员调用：

```http
POST /internal/v1/relay-signing-keys/{key_id}/activate
Authorization: Bearer <admin-token>
```

新 key 变为 `ACTIVE`，旧 key 变为 `VERIFY_ONLY`，并至少保留到 Relay Token 最大 TTL 结束。新 Keyset 版本随后推送全部在线节点；新 Token 使用新 `kid`，旧 Token 在宽限期继续验签。未知 `kid` 始终拒绝，不会绕过签名。

轮换期间不得从环境移除旧私钥材料；待所有旧 Token 过期并完成后续 `RETIRED` 操作才可从运行配置和离线密钥库存中移除。私钥备份必须与数据库备份分离且加密保存。
