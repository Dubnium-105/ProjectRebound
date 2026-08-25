# Dedicated Server 单进程多局

[English](dedicated-server-multimatch.md) | 简体中文

该功能仅用于固定版本 Boundary Dedicated Server。默认行为不变：未显式启用时，每局结束仍执行原生返回主菜单通知、最终清理和进程退出，由 Wrapper 启动下一进程。P2P/listen server 始终保留原生单局边界。

## 固定构建的静态依据

- RVA `0x036D61B0` 按 Win64 ABI 接收 `UWorld*`、`FString*` 和两个 bool；函数读取 `UWorld +0x118` 的 AuthorityGameMode，把 URL 复制到 `UWorld +0x5F0` 并返回 bool，确认是 `UWorld::ServerTravel`，不是按字符串猜测的地址。
- 固定 `.text` 中两个直接 caller 为 `0x032948EC` 和 `0x0350B0F4`；前者位于 `AGameMode::RestartGame` 原生主体 `0x03294830` 内。因此同图 restart 和跨图 travel 共用 Engine 原生边界。
- PB 原有赛后状态机仍没有下一局调用链；它默认从 `WaitingToEndGame 0x0162B1C0` 进入最终清理 `0x0163EFD0` 和 `RequestExit(false) 0x019EFEE0`。本功能是在明确 opt-in 时增加最小 Engine travel 分支，并非声称找到了被禁用的 PB 自动轮换。

## 配置

在 Wrapper 同目录的 `serverconfig.json` 中加入：

```json
{
  "map": "Warehouse",
  "mode": "pve",
  "multiMatch": {
    "enabled": true,
    "playlist": ["Warehouse", "OSS", "DataCenter"],
    "travelTimeoutSeconds": 45,
    "vote": {
      "enabled": true,
      "durationSeconds": 15,
      "candidateCount": 3
    }
  }
}
```

Console `ProjectReboundServerWrapper` 会规范化地图名并拒绝未知、重复或与 PVE 不兼容的地图。支持的范围为：

- `travelTimeoutSeconds`: 10–180；
- `vote.durationSeconds`: 0–60；
- `vote.candidateCount`: 1–3；
- PVE 可用地图：`OSS`、`MiniFarm`、`Warehouse`、`DataCenter`、`CircularX`。

配置有效且启用时，Wrapper 才向子进程添加 `-DedicatedMultiMatch` 和指向该 JSON 的 `-multimatchconfig`。无效配置会安全降级为原有一局一进程模式。

## 对局间流程

服务端保留原生结果冻结、结果页和 `MatchEnding`。进入 `WaitingToEndGame` 时，只有 Dedicated NetMode 1 且显式启用的实例会分流：

1. 候选地图按 playlist 顺序产生，默认跳过当前地图；
2. 玩家用 `/vote 1`、`/vote 2` 或 `/vote 3` 投票，平票时 playlist 中靠前者获胜；
3. 同图调用原生 `RestartGame`，跨图调用固定构建的原生 `UWorld::ServerTravel`，并启用 seamless travel；
4. travel 窗口拒绝新连接；只有新的 Dedicated GameMode/GameState 已建立、原 NetDriver 仍归属新 World 且原连接集合完全连续时，才提交新 match generation；
5. Payload 清除跨局指针缓存并重新绑定持久 PlayerController、配装和晚加入流程。

任何 travel 返回失败、超时、迁移期间玩家断开，或 World/NetDriver/连接连续性不满足时，服务端恢复原生客户端返回主菜单通知和最终退出；Wrapper 随后从 playlist 的下一张地图以新 PID 恢复。如果失败发生时权威 GameMode 已不可用，则不能再安全调用客户端通知，最后一道保护会直接请求退出；这一退化路径仍可能在客户端表现为掉线，必须在实机故障注入中单独验证。

运行时状态通过服务器状态 JSON 公开：`lifecycleState`、`activeMap`、`nextMap`、`matchGeneration` 和 `vote`。常见状态为 `Running`、`Voting`、`Traveling`、`LoadingNext`、`FallbackExit`。

## 验收

当前实现必须在部署后完成实机验收，编译通过不代表连接已连续。建议同时对服务端和至少一个客户端附加只读探针：

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1 -ProcessId <PID>
```

检查 `events.jsonl` 中的 `match.lifecycle`、`match.native_boundary` 和 `match.server_travel`。成功条件：

- 服务端 PID 在至少三局中保持不变；
- `ServerTravel` 返回 true，NetDriver 指针和原有连接集合保持连续；
- 新 World 或新 GameMode/GameState 建立，`matchGeneration` 递增；
- 客户端没有进入 MainMenu、Inactive、`HandleNetworkError` 或 `HandleTravelError`；
- 每局 `Running` 阶段的角色选择、出生、配装、复活、晚加入和断线重连正常；travel 窗口的新连接被拒绝，迁移中断线应触发安全回退；
- travel 失败注入能够回退到原生退出并由 Wrapper 新进程恢复。

本功能的 RVA 与布局只适用于仓库所固定的 EXE SHA-256。更换游戏版本前必须重新静态确认并完成上述矩阵。

当前交付只完成静态接线、Release 构建、策略测试和探针语法检查，没有部署 DLL、启动游戏、附加 Frida 或创建实际服务器。尤其是 seamless travel 后 ReplicationGraph/channel 原生清理、目的 World 名称判定与连接连续性仍属于必须通过探针证明的运行时门槛。GUI launcher 的重复 wrapper 尚未接入 multi-match 配置；它只修正了地图表多出的空元素。
