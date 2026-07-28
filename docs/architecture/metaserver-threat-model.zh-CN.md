# MetaServer 威胁模型

[English](metaserver-threat-model.md) | 简体中文

## 资产与参与方

受保护资产包括玩家身份和会话、配装和武器档案、Party 成员关系、对局预留、Game
Server 凭据、管理员权限、协议/definitions 完整性及服务可用性。参与方包括正常和恶意
客户端、已注册 Dedicated Server、Relay 运维、管理员、Cloudflare、公网网关，以及
能够观察或注入普通互联网流量的攻击者。

客户端机器不受信任。硬件指纹、`playerId`、`loginToken`、protobuf 身份字段、QoS
源地址和全部配装 JSON 都是不可信输入。PostgreSQL、Redis、控制面签名密钥及由 root
管理的网关/FRP 配置位于可信运维边界。

## 控制措施

| 威胁 | 控制 |
| --- | --- |
| 客户端冒充其他玩家 | 现有签名 Access Token 与活动会话检查；玩家 ID 只取自 principal |
| Gate 凭据窃取/重放 | 256 位熵、60 秒 TTL、Redis 哈希 key、原子 `GETDEL`、TLS、禁止凭据日志 |
| Party、Ticket 或配装 IDOR | Repository 查询始终包含已认证玩家的所有权/成员条件 |
| Dedicated Server 跨对局读取 | token 哈希 + server ID + scope + 活动状态 + 已分配对局/玩家检查 |
| 多副本重复分配 | PostgreSQL advisory leader lock、行锁、`SKIP LOCKED`、活动预留唯一约束 |
| 丢失更新或恶意配装 | 固定 definitions 校验、JSON 对象/大小限制、乐观 revision 锁 |
| Slowloris、帧或连接洪泛 | TLS/握手/读取/空闲超时、帧上限、IP 连接和速率限制 |
| 伪造地址绕过每 IP 限制 | 两段 HAProxy Logic 路径通过 PROXY v1 保留来源；MetaServer 只在显式启用的 FRP 私网 listener 接受它 |
| QoS 反射/放大 | 精确识别 `0x59`、限制请求、响应不大于请求、按 IP PPS、畸形包静默丢弃 |
| 管理员账号或 CSRF 滥用 | 可信网段、管理员专用会话、权限、Step-up、原因和审计 |
| 供应链/协议漂移 | 固定 commit/哈希、静态 protobuf、生成差异门禁、SBOM、provenance、漏洞/镜像扫描 |
| Secret 或租户数据泄漏 | 专用 DB/Redis 角色、隔离 FRP 凭据、日志脱敏、不挂载旧 JSON |
| MetaServer 容器失陷 | 非 root、只读根目录、无 capabilities、NNP、tmpfs 和资源限制 |

## 剩余风险

- 网关终止 Logic TLS，因此网关 root 和 HAProxy 内存能够看到原生流量。必须加固、及时
  更新网关；疑似失陷后轮换独立 FRP token。
- 被入侵但仍有效的 Game Server 能在其 token 或对局撤销前访问已分配名单与快照。
  Token 过期、最小 scope、审计和快速 OFFLINE/disable 缩短风险窗口。
- 未经真实客户端抓包确认的原生映射仍为兼容 stub，不能靠猜测提升到生产逻辑。
- Redis 丢失会使未消费 Gate Ticket 失效，但不会损坏持久化玩家数据；PostgreSQL
  故障按现有备份恢复 Runbook 处理。

## 安全验证

发布门禁覆盖无/伪造 token、封禁玩家、IDOR、跨服务器访问、Gate 并发消费/重放、
畸形 protobuf、Slowloris、连接洪泛、调度并发、QoS 放大、race、fuzz、
`govulncheck`、镜像扫描、SBOM 和构建 provenance。对重放/畸形帧突增、队列堆积、
FRP 断线、readiness 失败和证书到期告警。

疑似事故通过私有运维渠道报告；不得在公开 issue 附加原始 bearer token、Gate
Ticket、完整 protobuf 帧或配装档案。处置步骤见
[MetaServer Runbook](../operations/runbooks/metaserver.zh-CN.md)。
