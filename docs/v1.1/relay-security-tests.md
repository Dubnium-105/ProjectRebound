# Relay V1.1 安全测试套件

运行：

```bash
cd Backend
bash scripts/test/relay-security.sh
```

第一轮使用 Go race detector，第二轮把关键状态机重复 20 次以发现随机 handle、时间窗口和限流边界的不稳定行为。

| 威胁/约束 | 自动化测试 |
| --- | --- |
| 未验证地址不分配状态、Challenge 不放大 | `TestRuntimeCookieBindingAndAuthorizedForwarding`、`TestV2CookieIsStatelessAndAcceptsCurrentOrPreviousBucket` |
| 错误 Cookie | 同上 |
| 伪造/篡改 Token、错误节点、过期、未来 nbf、错误角色 | `TestRuntimeRejectsWrongNodeExpiredAndInvalidRoleTokens` 与 token verifier tests |
| `jti` 跨 IP 重放、NAT 短时换端口 | `TestRuntimeAllowsShortNATPortRebindButRejectsLateOrCrossIPReplay` |
| 数据认证标签、源端点冒充、未知 flags | `TestRuntimeCookieBindingAndAuthorizedForwarding` |
| 任意目标转发 | 数据包协议没有目标字段；测试确认输出地址只能是同 allocation 已绑定的另一端 |
| sequence 重放/过旧 | `TestRuntimeCookieBindingAndAuthorizedForwarding` 与 replay-window 单元路径 |
| MTU/超大包 | `TestRuntimeCookieBindingAndAuthorizedForwarding` |
| PPS、BPS、总字节、绝对/空闲到期 | `TestRuntimeRateLimitsTotalBytesAndExpiresInMemoryAllocations` |
| 无效 Token 洪泛和 IP 状态表上限 | `TestRuntimeTemporarilyBansInvalidTokenFlood`、`TestRuntimeSeparatesUnverifiedLimitsAndBoundsIPState` |
| 单 IP allocation 上限 | `TestRuntimeLimitsUniqueAllocationsPerIP` |
| allocation 关闭后 handle 失效 | total-byte/expiry tests |
| 过载保护保留已有连接 | `TestRuntimeOverloadStateRejectsOnlyNewAllocations` |

这些测试使用进程内虚拟时钟和真实 UDP listener，不伪造“源地址欺骗可穿越互联网”的结论。公网源地址过滤仍依赖主机防火墙和上游网络；Relay 自身的安全边界是 Cookie 地址所有权验证、Token scope、绑定源检查及无响应丢弃。
