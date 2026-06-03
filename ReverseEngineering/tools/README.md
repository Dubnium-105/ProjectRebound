# Frida 半自动逆向工具集

## 文件

| 文件 | 用途 | 依赖前一步 |
|------|------|:--:|
| `master_trace.js` | **一体机**：自动发现 MessageId、handler 回调、调用栈 | 无 |
| `session1_probe_dispatch.js` | 分步：仅探测 sub_9C4780 签名和 MessageId 列表 | 无 |
| `session2_capture_handler.js` | 分步：指定 MessageId，捕获 handler 回调地址 | Session1 |
| `session3_stalk_trace.js` | 分步：Stalker 指令级跟踪 handler 内部执行路径 | Session2 |
| `session4_ida_find_validation.py` | 静态：IDA 中自动定位校验条件分支 + 生成 patch 建议 | Session2 |

## 快速开始

```bash
# 0. 先启动 proxy（会每 5 秒输出 msgid_map.json）
cd g:\wksp\boundaries\ProjectRebound\Metaserver
node proxy.js

# 1. 启动游戏，进入军械库一次，触发所有 RPC

# 2. 附加 Frida
frida -p <游戏PID> -l tools\master_trace.js

# 3. 在 Frida 控制台
start()               # 安装所有 hook
# → 退出军械库再进入（触发 GetPlayerArchiveV2）
dump()                # 查看所有 MessageId
findArchive()         # 找 archive 相关的 MessageId
export()              # 导出结果到 logs/frida_export.json
```

## 分步工作流

### Session 1: MessageId 映射（5 min）
```bash
frida -p <PID> -l tools\session1_probe_dispatch.js
```
目标产出：`/assets.Assets/GetPlayerArchiveV2` 的 MessageId = ?

### Session 2: Handler 回调地址（15 min）
```bash
# 编辑 session2_capture_handler.js，设置 TARGET_MSG_ID
frida -p <PID> -l tools\session2_capture_handler.js
```
目标产出：handler 函数地址（计算 RVA = 地址 - base）

### Session 3: 指令级跟踪（10 min）
```bash
# 编辑 session3_stalk_trace.js，设置 HANDLER_RVA
frida -p <PID> -l tools\session3_stalk_trace.js
# → trace() 或自动触发
# → analyze() 查看完整调用链
```
目标产出：handler 内部调了哪些函数、分支走了哪条路

### Session 4: IDA 静态验证（30 min）
```
1. 打开 IDA Pro，加载 ProjectBoundary-Win64-Shipping.exe
2. 跳转到 Session 2 得到的 handler 地址
3. Alt+F7 → 运行 session4_ida_find_validation.py
4. 脚本自动定位校验分支 + 输出 patch 地址
```

## 典型输出示例

```
[DISPATCH] NEW msgId=42 → /assets.Assets/GetPlayerArchiveV2  (rdx)  ret=0x7ff6a3154800
[DISPATCH] NEW msgId=58 → /assets.Assets/UpdateRoleArchiveV2  (rdx)  ret=0x7ff6a3154800

=== MessageId Summary ===
  ID=42 → /assets.Assets/GetPlayerArchiveV2 ← KNOWN  ret=0x7ff6a3154800
  ID=58 → /assets.Assets/UpdateRoleArchiveV2 ← KNOWN  ret=0x7ff6a3154800

=== Handler callback found ===
  msgId=42 → handler at 0x7ff6a3XXXXXX (RVA=0xXXXXXX)
```
