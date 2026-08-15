# Boundary 军械库 Frida 观测器

[English](README.md) | 简体中文

这组脚本用于确认 Meta `QueryAssets`、原生 `UPBArmoryManager::OwnedItems` 与
`HasItem` 返回值之间的关系。探针不写游戏内存、不替换返回值，也不记录认证
令牌。两个 Python 控制器都会校验目标 EXE 的精确 SHA-256：
`181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`。

## 运行

启动 MetaTunnel、游戏并自动附加：

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1
```

附加到已经运行的游戏：

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1 -AttachOnly
```

指定进程：

```powershell
powershell -ExecutionPolicy Bypass -File .\Tools\Frida\run-armory-probe.ps1 -ProcessId 1234
```

默认日志位于：

```text
%LOCALAPPDATA%\ProjectRebound\frida-captures\YYYYMMDD-HHMMSS\events.jsonl
```

进入游戏后按既定路径操作：`NO -> customization -> HR&Armory`，查看一个默认
物品、一个应解锁但锁定的物品和线上配装使用的物品，然后进入一次对局。

## 关键事件

- `rpc.query_assets`：客户端真正收到的物品行数、`item_count` 和三个未知字段分布。
- `rpc.player_archive`：客户端收到的线上角色配装摘要。
- `armory.snapshot` / `armory.changed`：原生库存数量、Count 分布和 NewItemCounter。
- `armory.has_item`：物品是否位于数组、Count、bIsNew 和原生返回值。
- `persistent_user.snapshot`：Saved/Runtime 库存数量及集合哈希。
- `fieldmod.native_call`：`ClientInitFieldMod`、两个 Refresh RPC、选择、getter
  和武器生成调用边界。
- `fieldmod.snapshot`：上述调用前后的逐角色 pre-ordering 槽位。
- `progression.player_level_table`：该精确 EXE 的运行时等级表行数与最高数字等级。
- `unreal.lifecycle`：进入军械库原生事件前后的快照边界。

判读重点：

| 结果 | 含义 |
| --- | --- |
| `present=true,count=0,return_value=true` | 当前版本的原生 `HasItem` 只按 FName 命中，Count 不是门槛 |
| `present=true,count=0,return_value=false` | 探针偏移或目标版本已变化，需要重新核对 IDA |
| RPC 有该 ID、OwnedItems 没有 | QueryAssets 整体未落地或被覆盖 |
| `HasItem=true` 但 UI 锁定 | 类型/兼容性/UI 过滤问题，不是所有权 |
| 军械库正确、进入对局后变化 | 对局初始化覆盖了库存或 Loadout |

当前 Steam 构建的 IDA 结果显示，`UPBArmoryManager::HasItem` 以 0x10 为步长遍历
`FPBItem`，只比较偏移 0x0 的 `FName`，不会读取偏移 0x8 的 `Count`。

运行 `run_query_assets_status_ab.py --script query_assets_observe.js` 可获得只读
基线：它只报告稳定的 QueryAssets protobuf 前缀，不修改接收缓冲区。

QueryAssets A/B 探针现保留为回归诊断。已完成的实验确认当前 wire shape 是顶层值
`1` 加 40,462 条重复 ItemData；`UserAsset` 候选只产生 268 条原生库存，因此 MetaServer
不采用该候选协议。

`query_assets_single_item_ab.js` 是第二阶段 A/B：保持帧长不变，让客户端只看到
一条 ItemData（默认 `PEACE_RU-AKM`），并把顶层 `ItemCount` 等长改写为 1，用于区分
“超大/混杂资产列表被整体拒绝”和“三个整数字段的语义仍不正确”。当前脚本还会
校验完整的 1,615,627 字节 QueryAssets payload 窗口，避免匹配其他 RPC 响应。

需要做有判别力的所有权测试时，可对该脚本使用
`run_query_assets_status_ab.py --target-item PEACE_GSW-IDW`，只保留一个正常情况下
明确锁定的条目。再加 `--top-level-value 0` 可验证字段 1 是否其实是成功状态码而非
物品数量；默认测试值仍为 1。

`query_assets_user_asset_ab.js` 会直接验证运行时反射得到的候选 schema：隐藏旧的
顶层数量，只把所选行作为 field 1 `UserAsset` 暴露，同时保持缓冲区和帧长不变。
运行时传入 `--script query_assets_user_asset_ab.js --target-item PEACE_GSW-IDW`。

`player_archive_level_ab.js` 会解析完整原生帧和 protobuf wrapper，只改写单字节的
顶层玩家等级。使用带 EXE 哈希校验的控制器运行低/高两组：

```powershell
python .\Tools\Frida\run_query_assets_status_ab.py --pid 1234 --output .\level-low.jsonl --script player_archive_level_ab.js --target-player-level 1
$maxLevel = 100 # 替换成 progression.player_level_table.maximum_numeric_level
python .\Tools\Frida\run_query_assets_status_ab.py --pid 1234 --output .\level-high.jsonl --script player_archive_level_ab.js --target-player-level $maxLevel
```

`persistent_armory_probe.js` 是一次性只读对照探针，用于比较
`PBPersistentUser_BP_C::ArmorySaved`、其运行时 `Armorys` 和
`UPBArmoryManager::Armorys`。可对已运行游戏执行：

```powershell
python .\Tools\Frida\run_query_assets_status_ab.py --pid 1234 --output .\persistent.jsonl --script persistent_armory_probe.js
```

收到 `probe.done` 后即可按 `Ctrl+C` 退出。它不会改写游戏内存。
