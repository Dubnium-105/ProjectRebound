# Boundary 军械库 Frida 观测器

[English](README.md) | 简体中文

这些工具用于把 Meta `QueryAssets` 响应与原生军械库、archive、FieldMod、PlayerState 以及对局出生状态放到同一时间线分析。常规流程保持只读：不写游戏内存、不替换返回值，也不记录认证 token。控制器必须校验目标 EXE 的 SHA-256，并以 `.agents/skills/debug-boundary-native/references/current-findings.md` 记录的固定构建为准。

## 当前工作流

常规诊断使用统一探针：

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

默认日志目录：

```text
%LOCALAPPDATA%\ProjectRebound\frida-captures\YYYYMMDD-HHMMSS\events.jsonl
```

进入游戏后按 `NO -> customization -> HR&Armory` 操作，检查代表性物品和配装；只有需要验证出生链时再进入对局。

## 关键事件

- `rpc.query_assets`：客户端实际收到的 QueryAssets 状态和物品行摘要。
- `rpc.player_archive`：客户端收到的线上角色配装摘要。
- `armory.snapshot` / `armory.changed`：原生库存数量与状态变化。
- `armory.has_item`：指定物品的原生所有权查询证据。
- `persistent_user.snapshot`：saved/runtime 库存对照哈希。
- `http.message`：回环 HTTP 方法/路径或响应状态，以及正文大小/哈希；不记录 Authorization、Cookie、查询凭据或正文。
- `player_state.snapshot` / `player_state.changed`：选择/占有角色 ID，以及原生 pre-ordering/equipping map。
- `fieldmod.native_call`：`ClientInitFieldMod`、原生 refresh、选择/getter、武器生成和对局配装 reflected RPC 的调用边界。
- `fieldmod.snapshot`：上述调用前后的逐角色 pre-ordering 状态。
- `match.native_boundary`：服务端 pre-order、角色确认以及占有时提升 equipping 的原生边界。
- `progression.player_level_table`：该精确 EXE 的运行时等级表摘要。
- `unreal.lifecycle`：军械库生命周期边界。

所有权证据按下面方式判读：

| 结果 | 含义 |
| --- | --- |
| `present=true,count=0,return_value=true` | 固定构建中原生 `HasItem` 按物品 FName 命中，Count 不是所有权门槛。 |
| `present=true,count=0,return_value=false` | 目标构建或探针布局已变化，修改代码前先重新核对 IDA。 |
| RPC 有该 ID、OwnedItems 没有 | QueryAssets 没有落入原生库存状态，或随后被覆盖。 |
| `HasItem=true` 但 UI 锁定 | 优先检查物品类型、等级、兼容性或 UI 过滤，不再归因于所有权。 |
| 对局前军械库正确、出生后状态偏离 | 优先检查 FieldMod/PlayerState/出生应用链，而不是 QueryAssets。 |

固定构建的当前结论统一维护在 `current-findings.md`：QueryAssets field 1 是 result/status，repeated field 2 才是所有权物品行；原生完成链为 `FOnlineAsyncTaskQueryAssets -> LogicServer delegate -> PBArmoryManager`；当前确定性的全量所有权响应为 1,372,853 字节。客户端 frame patch 只对精确匹配的 EXE 哈希，把四个关联的 1 MiB 常量作为一个事务原子提升到 2 MiB。

## 历史与 A/B 探针

`query_assets_*_ab.js`、`player_archive_level_ab.js`、`fieldmod_native_probe.js`、`persistent_armory_probe.js` 等专项脚本只作为历史回归证据保留，**不属于当前默认诊断链或生产行为**。

其中部分脚本会故意改写缓冲区、返回值、帧元数据或单个字段。脚本里的固定 payload 长度、偏移和实验假设只描述当时的捕获样本，不能继续当作当前生产常量。尤其是旧说明中的 `1,615,627` 字节 QueryAssets 窗口已经是历史数据，不代表当前 1,372,853 字节的确定性响应。

只有同时满足以下条件才重新使用 A/B 探针：

1. EXE SHA 与探针固定构建完全一致；
2. `current-findings.md` 仍把对应假设列为未决或值得重新验证；
3. 一次只改变一个变量，并能立即回滚；
4. 结果必须与只读 `armory_probe.js` 基线对照；
5. 新确认的地址、布局或协议语义应写回 `current-findings.md`，不能继续让 A/B 脚本充当事实来源。

`logic_server_armory_probe.js` 仍可作为 QueryAssets 原生 completion 链的只读专项视图，但一般性的新增观测点仍应优先加入 `armory_probe.js`。
