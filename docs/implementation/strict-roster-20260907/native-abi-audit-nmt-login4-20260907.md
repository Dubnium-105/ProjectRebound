# NMT_Login field2 原生 ABI 审计（2026-09-07）

本审计覆盖锁定 Boundary EXE 的固定 RVA、capstone/Frida 只读证据和当前
Payload 实现边界。没有猜测偏移、写入游戏内存、关闭 strict、使用空 Grant
或通过 `open` 绕过原生准入。

## 固定二进制门禁

- 运行时 EXE：`C:/Steam/steamapps/common/Boundary/ProjectBoundary/Binaries/Win64/ProjectBoundarySteam-Win64-Shipping.exe`
- SHA-256：`181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843`
- PE `SizeOfImage`：`105431040` (`0x648c000`)
- Steam API：Steamv157 Win64，Valve Authenticode 有效，SHA-256
  `a44e5537939ae4ee6bc69000589aa9b2437a667813a1657cc779198bae9b815a9`
- IDA RPC：本轮无可用 session，已知 IDB 打开超时；没有继续重复同一超时。

## 固定 NMT 调用链

客户端 NMT case 4 在 `0x03484EDE` 写控制消息后，依次序列化四个字段：

1. `0x03484F25`：`rdx=rbx`；
2. `0x03484F35`：`rdx=[rbp+0x10]`，当前 field2 URL/options 候选；
3. `0x03484F48`：`rdx=[r13+0x160]`，自定义身份序列化；
4. `0x03484F58`：`rdx=[rbp+0x20]`。

field2 serializer 的固定 RVA 是 `0x0189D040`，其 pinned prologue 由
`Payload/Hooks/Hooks.cpp` 检查；注入只接受返回地址
`0x03484F3A` 且 archive flags 为固定 NMT 保存形状。原始 field2 的
`[Data,Num,Max]` 不会被改写、接管或释放。注入使用有界的临时 16-byte
FString view，调用同步返回后立即擦除 backing storage。

服务端 Login 候选块是 `0x036CDCE0`。离线只读 trace 观察到 field2 数据
进入 `0x0368B200` URL/options helper，helper 输出对象随后进入 PB 和
Engine PreLogin。该关系证明了 URL/options 数据流，却不能单凭它命名
协议字段或证明服务器已经验证 Steam 身份。

相关固定静态证据：

- `C:/wksp/ProjectRebound/docs/implementation/strict-roster-20260907/native-static-nmt.json`
- `C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/nmt-login4-callsite-capstone.txt`
- `C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/nmt-server-login-block-capstone.txt`
- `C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/nmt-serializer-extended.txt`

## 只读运行时证据

`OFFLINE_ABI_ONLY` local-PVE traces used the signed launcher and restored the
original AppID after each run. They observed:

- client field2 shape through the outbound serializer and its owned storage
  release at the fixed post-send boundary;
- server field2 pointer/shape flow through the URL/options helper and into both
  PreLogin boundaries;
- archive flags and object lifetimes without reading FString contents.

The observations are ABI-only and are not strict online acceptance. Exact
summaries are under:

- `C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/offline-abi-client-observation-summary.json`
- `C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/offline-abi-server-options-relation-summary.json`
- `C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/offline-abi-client-lifetime-relation-summary.json`

A fixed-executable sensitive-log injection test was not run. The current hook
passes a sanitized copy of the options to the original native PreLogin, removing
`ReboundGrant` and `ReboundSteamTicket` before the fixed engine logging path.
No Payload log prints those values.

## 当前实现与证明边界

The client carrier calls Steam `GetAuthSessionTicket`, requires callback 163 to
match both requested handle and `k_EResultOK`, and carries only a base64url
bounded value. The authority calls `BeginAuthSession` only after arming the exact
`(platformId, Grant JTI, native_connection_nonce)` scope. Callback 143 is parsed
as the fixed 20-byte `SteamID + EResponse + owner SteamID` structure; success is
accepted only after that exact BeginAuthSession and matching pending scope.
Non-OK responses revoke pending and active bindings. Auth sessions, expected
proofs, and ticket buffers are cleared on disconnect/cancel/cleanup; SDK calls
run outside the auth mutex.

The standalone owned Steam helper proved the official API sequence with separate
client and gameserver pipes:

- callback 163 handle/result matched;
- `BeginAuthSession` returned 0;
- callback 143 response code 0 and SteamID matched.

Evidence: `C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/steam-auth-owned-harness-summary.json`.
This is `OFFLINE_STEAM_AUTH_API_ONLY`; it does not prove the locked game's
normal dispatcher timing or a strict native connection.

The asynchronous callback boundary remains blocked for product readiness. The
fixed PreLogin path must not block or re-enter the game dispatcher. A real online
positive run must prove a pre-auth/pending retry or equivalent native handshake
that binds callback 143 to the same native nonce before Connected. Until that
happens:

- `native_client_grant_injection_ready=false`;
- `native_authority_admission_verified=false`;
- `strict_online_ready` is not reported as a positive capability;
- a self-reported UniqueNetId, arbitrary callback, empty token, and direct open
  cannot authorize a connection.

The current source therefore contains the narrow implementation and keeps the
remaining acceptance explicitly `BLOCKED`; no unrun result is reported as pass.
