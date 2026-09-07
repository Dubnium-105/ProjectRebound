# 权威名单版目标契约与状态不变量

> 这是待实现目标，不是现有协议已支持的声明。新增字段、接口名与状态必须落实到真实OpenAPI、Rust DTO、IPC schema和数据库迁移中；不得只改文档。用户已明确：服务未正式上线，直接完成权威名单版，不做旧在线兼容层。

## 1. 唯一在线产品模型

| 产品模式 | hosting_kind | transport_kind | admission_mode | 进入方式 |
|---|---|---|---|---|
| 权威名单专服对局 | DEDICATED | null | strict_roster_v1 | Lobby → Attempt → 分配Dedicated authority → Grant |
| 权威名单P2P直连/中继对局 | P2P | LEGACY_RELAY | strict_roster_v1 | Lobby → Attempt → 本机HOST allocation / MEMBER Grant |
| 权威名单P2P虚拟组网对局 | P2P | VNT | strict_roster_v1 | 同上，VNT只替换传输 |

`LEGACY_RELAY`在这里仅指既有直连/UDP Relay协议实现，不表示旧房间、无名单对局或旧准入。Dedicated大厅创建者是远程玩家；只有P2P指定的本机HOST使用allocation完成本机席位绑定。

已有离线PvE可保留为完全隔离的产品功能或开发harness，必须显式标记offline：不获取在线Grant，不向在线权威战绩报送，不作为线上认证失败的回退。若停止发布某个旧前端或离线功能，记录构建与文档决定，不需要为它额外建设兼容系统。

## 2. 退役边界

直接更新全部继续发布的前后端。停止支持旧普通在线房间目录/CRUD/Start流程、旧任意地址`launch_join`、在线空token/direct-open以及独立产生另一份名单的旧Meta匹配路径。旧公开生命周期路由可删除或显式返回退役错误，不做升级适配或重定向到无名单模式。

可以复用现有`p2proom`、`connection`、Relay、VNT、签名器和数据库原语；复用不等于继续暴露旧业务。传输资源必须从属于一个合法Lobby/Attempt，传输API只允许执行该阶段已经授权的内部操作。保留或强化现有`MANAGED_LOBBY_*`限制，不能删掉这些限制来“兼容”新控制器。

旧数据可作为只读历史留存；不得从旧`p2p_room_members`或旧BattleLog快照重新决定新对局队伍与席位。

## 3. 数据所有权

| 数据/状态 | 唯一决定者 | 允许的派生/观测 | 禁止行为 |
|---|---|---|---|
| 大厅成员、队伍、Ready、roster_revision | MatchLobby | Toolbox镜像、授权的传输成员投影 | UI或传输模块双写名单 |
| 冻结身份、队伍、席位、Attempt | MatchAttempt | MatchAllocation、P2P/Meta/BattleLog投影 | 战绩/网络回调重新冻结另一份名单 |
| 是否授权某个新连接 | Admission服务＋验证过的原生authority边界 | Grant预留、确认、释放 | 自报player_id/显示名充当真实平台身份 |
| 哪个游戏连接已建立/已消失 | 实际authority原生事件，经后端校验 | CONNECTED/DISCONNECTED投影 | Relay BIND/ping/窗口代替原生入场 |
| 玩家是否可操作 | 本次运行的真实客户端/authority观测 | Playable状态/诊断 | PID、ACK、API返回200即宣称可玩 |
| 网络是否可达 | TransportAdapter | transport_ready/path变化 | 修改托管Lobby/Attempt为RUNNING |
| 实例是否可重新分配 | 后端lease/cleanup控制器 | 健康观测与NativeCleared | 普通READY心跳覆盖活动任务或未清理状态 |

## 4. 标识、角色和版本：必须强类型化

- `lobby_id`：大厅；`p2p_room_id`：底层传输资源，不能互换。
- `attempt_id`：一次冻结后的开局尝试。失败后重新开局使用新值；同一大厅可以按政策重新开放。
- `authority_id`：本次权威实例/主体；`authority_session_id`：它在该Attempt中的会话；新增可验证的`world_instance_id`用来区分真正的新游戏世界。
- `roster_revision`：大厅名单版本；只单调增加。生命周期可以按显式规则回OPEN，不是所有状态值都只能单向增加。
- `route_generation`：对局准入所绑定的路线代次；不能直接等同VNT内部generation或Relay migration_id。
- `connection_generation`：席位的新连接授权代次；实现中分开`minimum_authorized_generation`和`live_connection_generation`，不能更新一个数字就把旧连接冒充新连接。
- `session_epoch`：账号会话代次；`operation_id/run_id`：本地异步操作与实际运行，不能代替后端Attempt。
- `request_id`：具体请求关联；`event_seq`：观测序号。只有在所属会话键正确时才有序，不能全局比较不同会话。
- `RelayAllocation`、`MatchAllocation`、`JoinGrant`、`GateTicket`、房主令牌、节点凭据和战绩capability分别建模，不使用一个泛化`token`字段跨用途流转。
- `GameEndpoint`包含host与port，IPv6按`[address]:port`序列化。游戏端口取实际监听配置，不是VNT节点端口，也不是无条件补7777。

## 5. 启动任务契约

建议统一为异步API/IPC前端桥接：

```text
StartLaunch(intent) -> { operation_id, status: ACCEPTED }
GetLaunchOperation(operation_id) -> 当前快照
CancelLaunchOperation(operation_id) -> 已受理取消 / 已经终态
LaunchOperationUpdated -> 同一operation_id的状态与脱敏错误
```

在线`intent`必须包含受服务端验证的Lobby/Attempt上下文，而不是任意地址。P2P Host和Member、Dedicated authority和玩家使用不同类型。Rust返回空PID不是启动完成；不要用假PID，也不要放宽前端检查来掩盖接口语义错误。

建议操作状态：`ACCEPTED → RUNNING → SUCCEEDED | FAILED | CANCELLED`，终态CAS保护。细分阶段为：

```text
ValidatingBuild / Authenticating / LobbyJoining / TransportPreparing
AuthorityProvisioning / PayloadHandshaking / GameLoginWaiting
AllocationInstalling / AdmissionPreparing / TravelIssued
Admitted / WorldReady / PawnReady / Playable / Cleaning
```

这些阶段不是所有角色都按同一顺序走过。例如Member必须等authority ready才能申请Grant；Host先建立本机权威世界并绑定本机席位。进度可以并发或被省略，但每项成功必须有证据来源和时间。`SUCCEEDED`表示该玩家已达到可操作目标；单独的`ProcessStarted`用于早期反馈，不改变最终成功语义。

事件至少携带必要的`operation_id`、`run_id`、`attempt_id`、authority/world/session上下文、阶段、序号和安全错误。拒绝上一账号、上一Attempt、上一世界的迟到回执。前端重开/重连后先查询权威快照，不能凭未持久化的本地事件重建安全状态。

## 6. 大厅与对局状态

保留现有OPEN、PROVISIONING、CONNECTING、RUNNING、COMPLETED、ABORTED等业务含义，新增清理阶段可以是显式状态或独立lease字段，但必须有一致的不变量。

`FROZEN`可以是同一事务内部过渡；消费者不得要求一定看到独立FROZEN事件。冻结必须原子保存名单、固定team/slot、创建Attempt并链接投影。

先按现有产品政策实现：所有已占席成员在冻结前在线且Ready，两队非空；初始连接窗口内全体已接纳可推进，窗口到期两队各至少一个已接纳玩家可按既有规则继续，否则中止。原生角色确认和RoundStart单独建模，不让未到场玩家永久阻塞已接纳集合。具体迟到/角色确认超时策略写ADR并测试；禁止用自动改队/新增非冻结玩家解决缺席。

后端RUNNING与每名玩家Playable是不同事实。游戏进入RoundStart之前，相关实际玩家仍须完成原生角色/控制交接；UI可以显示“对局已开始，当前玩家加载中”，不能把两者揉成一个布尔值。

## 7. 严格准入：预留、原生接入、确认、释放

当前`MarkConnected`会消费Grant并把玩家/Attempt推进状态，不能把它简单提前调用，当成“预验证接口”。目标需要明确授权线性化点和补偿规则，建议按以下事务拆分：

1. **IssueJoinIntent / Grant**：同一次加入意图具备幂等键；网络重试返回同一意图结果。用户主动建立替换连接才开启新的授权代次。Grant临近真正发起原生握手时签发，不在漫长冷启动前签发。
2. **ReserveAdmission**：authority根据原生认证得到的平台身份，向后端请求短期准入预留。后端在锁定席位/Attempt的事务中检查身份、用途、当前route、代次、Grant撤销/过期、authority/world/session，并保证并发竞争唯一。此时不标CONNECTED或RUNNING。
3. **NativeAdmit**：在经过验证的游戏原生登录/准入位置绑定team/slot；必要的后端等待必须有界且不能在游戏线程无界阻塞。尚未确认的玩家不得成为可操作参与者；可以使用经验证的原生待确认状态，不能先开放权限再靠事后日志补救。
4. **ConfirmConnected**：实际原生连接成功后，后端确认同一预留与当前授权，原子标记连接并消费相应Grant。后端确认失败或期间撤销，原生端关闭/回收该连接，不留下活跃假玩家。
5. **Release / Cancel**：握手失败、超时或取消释放预留；若引入更新代次、租约回收或替换连接，说明旧live连接何时失效。新allocation只更新授权代次时，不得把尚未替换的旧连接改名成新generation。

接口名称是建议，Codex可调整，但必须满足上述不变量。P2P HOST使用本机可信平台身份与签名allocation匹配，不使用远程Member Grant；Dedicated authority没有HOST玩家席位。

Payload同route刷新必须原子更新授权下界；保留相应JTI防重放与不可变席位检查。旧Grant、旧断线、旧世界回执均不能越过代次栅栏。后端不可达时按有界失败关闭策略处理，不以空token/direct-open继续。

## 8. 原生能力与信任边界

必须实际完成锁定游戏版本的客户端Grant注入位置、authority的PreLogin/登录验证、队伍/席位应用和connected/disconnected观测。不能把`nativeAdmissionPathReady=false`改为true就算实现；应在真实验证通过的构建中按版本能力启用，并拒绝未支持产物。

游戏哈希、Payload握手和构建清单可以用于版本/兼容门禁，但不是强远程硬件证明。不要声称本地同一用户的可修改进程因此具有完全防篡改保证。保持既有ACL、会话/PID归属、签名与身份验证，不把UI传来的任意公钥/平台ID当作可信根。

## 9. 租约、恢复和清理

分别维护Lobby Presence、Transport心跳、实例注册心跳、Attempt authority心跳。各自有owner、服务端时间和过期动作；服务注册心跳不能代替Attempt心跳。配置期限必须与冷启动、安装、游戏登录、Grant申请时点匹配，禁止简单无限延时。

同世界短断线恢复：确认authority/world实例仍在 → 取新allocation → 安装route/seat代次 → 后端确认本代次安装 → 成员取新Grant → 原生重新接纳。错过多代需要协议化重同步或中止，不直接删连续性校验。真正游戏进程/World丢失只能终止旧Attempt并新建。

结束/超时：停止新加入 → 撤销未消费授权和关闭预留 → 原生连接/名单/世界清理 → Transport资源清理 → NativeCleared/lease释放 → 实例重新可用。后端先记录结束意图可行，但未清理实例不能回池。清理失败记录`cleanup_pending`并由reconciler重试，不能只清空UI。

Runtime先停止接收新操作，再取消/排空任务；解除锁后join worker；保留清理所需凭据到完成。子进程按PID＋启动时间/路径或Job Object归属管理，不能广义杀Node/game进程。

## 10. 配置、部署与发布

没有第二个无名单在线模式。`accept_new_lobbies=false`只能停止新建，不能自动ABORT其他实例的所有活动Attempt；drain与强制中止必须独立、授权并审计。配置或schema不匹配时拒绝该实例启动，而非共享数据库批量清理。

服务未上线允许破坏性API升级，不代表授权删除全部测试数据。默认追加迁移、备份与隔离测试；只有明确一次性测试库并另获授权才可重建。最终部署配套的新服务、Meta、Toolbox、Payload与schema，不建设长期旧版本兼容。

完整交付必须包含真实三人以上玩法、负向准入、取消/超时、重连、下一局和资源回收证据。关闭整个在线功能可以作为开发期间的保护，不能作为“权威名单版已完成”的最终交付。
