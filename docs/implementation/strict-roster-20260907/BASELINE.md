# 权威名单唯一在线版实施基线

2026-09-07 开始实施。交接包是规格输入，其中历史结论需以实际源码复核。

- ProjectRebound：`213d5f967d0df26e8110842958a52be5d96908d8`，初始工作区干净。
- Toolbox：`0aa604eade3662951eeaa94cc6566630cea5a40b`，初始工作区干净。
- 两仓库实施分支：`codex/authoritative-only-20260907`。
- 主机：Windows x64；Go、Rust、Python、Node、Visual Studio 已发现。工具存在不等于构建或游戏验收通过。
- 没有重置工作区、数据库或修改远程部署。

继续实现 DEDICATED、P2P/LEGACY_RELAY、P2P/VNT 三种目标组合，在线准入固定为 strict_roster_v1。LEGACY_RELAY 仅是传输名称。离线 PvE 必须隔离；旧普通在线入口直接退役。签名、MAC、DPAPI、平台身份和原生能力门禁保持有效。

MatchLobby 决定成员与队伍；MatchAttempt 原子冻结身份和逻辑席位。传输、Meta 和 BattleLog 仅维护授权投影。授权代次和 live connection 代次分别维护。专服清理确认前不得回池。

本次 L3/L4 游戏验收尚未运行。任何现有日志和旧产物均不计入本次源码验证。逐项状态见 progress.json；实际执行记录必须有命令、退出码和日志。
