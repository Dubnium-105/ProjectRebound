# Boundary 军械库 Frida 观测器

这组脚本用于确认 Meta `QueryAssets`、原生 `UPBArmoryManager::OwnedItems` 与
`HasItem` 返回值之间的关系。探针不写游戏内存、不替换返回值，也不记录认证
令牌。

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

`query_assets_status_ab.js` 是单独的可选 A/B 探针。它只把当前 QueryAssets
payload 开头字段 1 的 40462 原位改成等长编码的 0，用于验证该字段究竟是条目数
还是状态/保留字段。它会修改本机游戏的接收缓冲区，因此不属于默认只读探针；
停止探针并重启游戏即可恢复。

`query_assets_single_item_ab.js` 是第二阶段 A/B：保持帧长不变，让客户端只看到
`PEACE_RU-AKM` 这一条 ItemData，并把顶层 `ItemCount` 等长改写为 1，用于区分
“超大/混杂资产列表被整体拒绝”和“三个整数字段的语义仍不正确”。当前脚本还会
校验完整的 1,615,627 字节 QueryAssets payload 窗口，避免匹配其他 RPC 响应。

`persistent_armory_probe.js` 是一次性只读对照探针，用于比较
`PBPersistentUser_BP_C::ArmorySaved`、其运行时 `Armorys` 和
`UPBArmoryManager::Armorys`。可对已运行游戏执行：

```powershell
frida -p 1234 -l .\Tools\Frida\persistent_armory_probe.js
```

收到 `probe.done` 后即可按 `Ctrl+C` 退出。它不会改写游戏内存。
