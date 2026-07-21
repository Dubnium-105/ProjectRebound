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

## 节点 mTLS 证书

每次注册和续期都会向 `relay_node_credentials` 写入证书 serial、SHA-256 fingerprint、签发/生效/到期时间和轮换时间。续期事务会把旧证书标记为 `ROTATED`，更新节点当前 fingerprint，再写入新凭据，因此旧证书即使仍可通过 CA 链验证，也无法通过 Control Plane 的节点绑定检查。

Edge 从证书 `NotBefore/NotAfter` 计算完整有效期，在剩余 25% 时自动生成新 Ed25519 私钥和 CSR，通过 Node Token 请求续期，原子替换权限为 0600 的 `identity.json`，更新 Keyset 缓存并重建 mTLS 控制连接。续期失败但旧证书仍有时间时会按控制连接退避重试；证书过期后不能建立新控制连接。

管理员调用节点 `revoke` 时，节点进入不可逆 `REVOKED`，所有未撤销凭据记录为 `ADMIN_REVOKED`，在线控制流收到 `Shutdown`，现有连接进入故障迁移。以后使用该 Node Token、fingerprint 或证书重新连接都会被拒绝。

Edge 会持久化证书、Control Plane CA、最后签名有效的 Keyset 和配置。控制流断开时既有 allocation 与已知 `kid` 的未过期 Token 继续工作；默认宽限 600 秒（`control_disconnect_grace_seconds`）后节点本地进入 `DRAINING`，停止接受新 allocation。控制连接恢复并收到 `READY` 配置快照后退出该保护状态。
