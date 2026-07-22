# 边缘中继 UDP 协议 v2

[English](relay-protocol.md) | 简体中文


所有整数都是无符号且大端的。数据报开始于`PRLY`, 协议版本`2`，以及一字节消息类型。无效数据报将被丢弃而不会得到响应。生产默认为`accept_protocol_v1: false`;旧版 v1 格式仅可用作临时的显式兼容模式。

## 绑定挑战

1. `BIND_INIT (1)`: `magic[4] | version[1] | type[1] | client_nonce[16] | requested_mtu[2] | token_length[2] | relay_token[n]`
2. `BIND_CHALLENGE (2)`: `magic[4] | version[1] | type[1] | server_nonce[16] | expires_in_ms[4] | cookie[32]`
3. `BIND_PROOF (3)`: `magic[4] | version[1] | type[1] | client_nonce[16] | server_nonce[16] | requested_mtu[2] | cookie[32] | token_length[2] | relay_token[n]`
4. `BIND_OK (4)`: `magic[4] | version[1] | type[1] | allocation_handle[8] | endpoint_role[1] | negotiated_mtu[2]`

cookie 是一个基于域分隔符、源 IP/端口、两个随机数、请求的 MTU、令牌哈希（涵盖分配声明）和短时间段的 HMAC。中继接受当前和上一个存储桶。挑战的配置寿命为 5-15 秒，永远不会大于`BIND_INIT`，并且不创建分配或每次挑战服务器状态。无效的 cookie 会被悄悄删除，不会出现详细的错误。

有效的签名令牌恰好绑定一个`HOST`或者`PEER`端点。中继验证签名、发行者、受众、密钥 ID、`jti`、节点/分配/连接身份、端点角色、协议、`nbf`/`exp`，以及分配状态之前的所有流量限制。一个`jti`从同一端点重试是幂等的。同一 IP 上新受到挑战的源端口只能在配置的短 NAT 重新绑定窗口期间替换端点；跨IP或后期重用将被拒绝并计数。内存中的重播缓存具有 TTL 清理和硬条目上限。

## 数据包

`DATA (5)`用途：

```text
magic[4] | version[1] | type[1] | allocation_handle[8] |
endpoint_role[1] | flags[1] | sequence[8] | authentication_tag[16] | opaque_payload[n]
```

v2端点密钥是`HMAC-SHA256(relay_token, "project-rebound-relay-data-v2")`。身份验证标签是标头上 HMAC-SHA256 的前 16 个字节（包括标志但不包括标签）加上不透明的有效负载。 V1.1 要求标志为零；带有未知标志位的数据包将被丢弃。中继对绑定接收者的数据包进行身份验证和重新标记，而无需解析或解密游戏负载。

仅在两个角色绑定后才开始转发。该数据包没有目标地址字段，因此流量只能在一个分配的两个端点之间移动。中继拒绝未知句柄、错误角色或来源、无效标签、重复/超出窗口的序列、过期/空闲分配以及超出每个 IP、PPS、带宽或总字节限制的数据包。

协商的不透明负载 MTU 为 1000–1350 字节，默认为 1200。过大的数据包将被丢弃而没有响应。分配句柄是随机的非零 64 位值，并且在分配关闭时立即变得无效。

## 资源限制

未经验证的来源有单独的令牌桶`BIND_INIT`, `BIND_PROOF`、格式错误的流量和无效的签名令牌尝试。重复的无效令牌暂时禁止来自该 IP 的新握手流量；已经经过身份验证的数据仍会通过其端点和节点存储桶。 IP 状态表具有硬基数上限和空闲清理功能。可配置的每 IP 限制对唯一的分配进行计数，因此同一 NAT 后面的 HOST 和 PEER 对于一次分配不会计数两次。

每个端点强制执行令牌声明的 PPS 和每秒字节数存储桶。每个分配都强制执行绝对到期、空闲超时和总字节上限；超过总字节上限会立即关闭分配并使其句柄无效。节点范围的入口 PPS 和出口字节桶可保护现有分配免受无界聚合流量的影响。 Cleanup 使用共享清理器，并且不会为每个分配创建 goroutine 或 Ticker。

## 过载状态

在每个心跳间隔，中继都会从分配计数、入口和出口速率、入口 PPS 和 Go 堆分配中获取其负载。最大利用率选择`NORMAL`, `DEGRADED`， 或者`REJECT_NEW`;操作员漏极选择`DRAINING`。阈值和容量分母是显式配置值。

`REJECT_NEW`和`DRAINING`仅拒绝新分配的第一个端点。它们继续接受第二个端点和已安装在节点上的分配的经过身份验证的数据。状态在本地 Prometheus 指标上公开，并通过经过身份验证的控制流发送。控制平面保留它并排除`REJECT_NEW`和`DRAINING`来自初始放置和迁移目标的节点。
