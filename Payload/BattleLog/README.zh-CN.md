# BattleLog 原始对局提取

[English](README.md) | 简体中文

`BattleLogExtractor` 在选定的 `ProcessEvent` 调用结束后，于 Unreal 游戏线程
同步执行。它把赛后 SDK 状态复制为脱离 UObject 生命周期的 JSON，再写入磁盘。
工作线程不会读取任何 `UObject`。

## 输出

默认目录为：

```text
%LOCALAPPDATA%\ProjectRebound\battlelog-dumps
```

如果 `LOCALAPPDATA` 不可用，则回退到
`<process-current-directory>\battlelog-dumps`。

每个结算阶段单独生成文件：

```text
<pve|pvp|unknown>\<timestamp>_<sequence>_<pve|pvp|unknown>_<server|client>_<match-id>_<trigger>.json
```

游戏或服务器控制台会用 `[BATTLELOG]` 前缀输出绝对路径。同一结算阶段的重复
调用会被去重，直到下一次对局开始事件或 `UWorld` 发生变化。

## P2P v3 封存

Launcher 在注入前通过以下非秘密的进程环境变量启用客户端观察者 v3：

```text
PROJECT_REBOUND_P2P_MATCH_ID=p2pm_...
PROJECT_REBOUND_P2P_CAPABILITY_ID=p2rc_...
PROJECT_REBOUND_P2P_SERVER_NONCE=p2n_...
PROJECT_REBOUND_CLIENT_VERSION=<launcher-version>
PROJECT_REBOUND_P2P_AUTHORITY_KIND=CLIENT_OBSERVER
```

仅房主监听服务器使用 `LISTEN_HOST_OBSERVER`。报告 Token 不属于 DLL 契约，必须
只保留在 Launcher 内存中。

三个上下文 ID 有效时，提取器输出 schema v3：开局先封存一份 `PARTIAL`，每轮
结束再封存有界检查点，结果界面触发时封存一份 `FINAL`。事件以
`match_id|capability_id|server_nonce|timeline_session_id` 为根组成 SHA-256 链。
文件先写入同目录 `.tmp`，再原子改名为 `*.json.ready`；Launcher 只能扫描
`.ready` 文件。未设置这些变量的专用服务器仍使用 schema v2 和原有 `.json`
行为。

## PvE 与 PvP 分类

Schema v2 将 `match_classification.type` 输出为 `pve`、`pvp` 或 `unknown`。
Dedicated Server 快照优先使用服务器 `-pve` 启动参数写入的 `Config.IsPvE`
作为权威来源。如果启动器传入了 PvE 模式路径但遗漏 `-pve`，例如
`Rush_PVE_Normal` 的明确运行时元数据仍可识别 PvE；覆盖结果和两类证据都会
保留在 JSON 中。客户端快照使用同样的 SDK 模式元数据，但含糊的客户端元数据
不会被视为权威来源。

文件分别写入 `pve`、`pvp` 和 `unknown` 子目录。每个 JSON 还包含：

- `participant_summary`：全体、真人、AI 和各队伍聚合。
- `pve_record`：PvE 对局结果和参与者汇总，非 PvE 时为 null。
- `pvp_record`：PvP 对局结果和参与者汇总，非 PvP 时为 null。

完整的原始 `players` 数组保持不变，同时为下游 BattleLog 持久化提供明确类型
以及分离的 PvE/PvP 记录。

## MetaServer 接收

服务端接收接口要求把完整 schema v2 对象放入 `snapshot` 属性：

```text
PUT /internal/v1/meta/battlelog/reports/<report-id>
```

调用方提供具备相应 scope 的 Game Server Token 和 `X-Game-Server-Id`。
`report-id` 在重试间保持稳定；当前转储文件名去掉 `.json` 后可作为过渡期
report ID。MetaServer 对规范化 JSON 计算哈希，因此相同内容可安全重试，同一
ID 对应不同内容时会被拒绝。

快照不能决定玩家认证等级。后端使用托管对局预留名单时固化的
`unverified`、`verified` 或 `trusted` 等级。没有托管对局关联的报告始终为
非官方记录。

## 已捕获来源

- `APBPlayerState`：复制/原始计数器、身份字段、职业和队伍分配、SDK getter
  派生值、胜负标志、职业/角色分数映射以及 `FPBInGameData`。
- `APBGameState`：地图、模式、计时、回合、队伍状态、`FPBMatchResult` 和最后
  一个 `FPBRoundResult`。
- 服务端 `APBGameMode::GetMatchResultInfo(PlayerState)`：每位玩家对应的
  `FMatchResultInfo`。
- `ClientMatchHasEnded` 参数：RPC 实际下发的 `FPBMatchResult`。
- `UPBGameInstance::SaveMatchResultInfo` 参数和 `LocalMatchResultInfo`：客户端
  UI 使用的 `FMatchResultInfo`。
- `UPBCareerManager::GetLastPostMatchSettlementData`：存在时捕获队伍、成员
  结算和勋章数据。

原始枚举数字始终和 SDK 已知名称一起输出。指针地址和 Unreal 对象路径仅用于
诊断关联，不是稳定的 BattleLog 标识符。

## 首轮验证

在 Dedicated Server 和一个客户端中注入 Payload，各完成一场完整对局并保留
两个进程生成的全部文件。优先比较以下阶段：

1. `ClientMatchHasEnded`
2. `SaveMatchResultInfo`
3. `K2_StartShowingMatchResult`
4. `GetLastPostMatchSettlementData`

后续 BattleLog 持久化结构必须以实际转储值为依据，尤其需要确认准确身份字段、
分数语义、比赛时间单位、队伍编号，以及准确率和 KDA 是否已经归一化。
