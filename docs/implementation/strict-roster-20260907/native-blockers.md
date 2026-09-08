# 原生验收状态

当前源码已包含严格 Grant、Steam Ticket、账号身份绑定、完整连接 scope 与后端回执链；真实单客户端运行已观察到 RESERVED、NATIVE_ADMITTED、CONNECTED、DISCONNECTED 和后端 release。组件测试与 DLL 构建结果、每次失败及后续修复见 native-report.json 的实际执行记录。

Playable 仍未通过。在线 Config.cpp 固定要求至少 2 名玩家才开始倒计时和选角，本环境只有 1 名真实 Steam 玩家；本次日志显示 1 次玩家连接、1 次初始 LateJoin 排队、0 次选角，客户端停留 WorldReady。合成的第二个冻结席位没有伪装成实际连接。未改 IsPvE、MinPlayersToStart、严格名单或原生 Token。

完整 3 人传输与生命周期矩阵还需要另外 2 个可同时运行的真实 Steam 会话。原生 HOST 恢复、最终清理 ACK/复用，以及 serializer 临时 FString 的动态 ownership 证明仍按独立执行证据判定；没有以组件 PASS 或注入成功代替。

固定 EXE 的 field2 serializer 静态 writer 证据已保留在 `docs/implementation/strict-roster-20260907/evidence/serializer-static-callees-20260908/FINDINGS.md` 及其绑定 JSON：它只证明该固定同步调用图内借用 FString 在调用期间未被 retain/free，不证明动态 serializer 覆盖或产品整体 ownership；BP038 动态验收仍未通过。

本轮固定 297c6 DLL 的私有 `wrong_audience` 诊断已真实启动 Dedicated authority 并完成 allocation/authority-ready；首次运行有 227 次、跟进诊断有 229 次 `native_admission_unverified`，均在 Grant 交付前停止，没有到达 `client-ready`、NMT_Login、Reserve、NATIVE_ADMITTED 或 CONNECTED，因此没有把它记为负向拒绝 PASS。绑定 addendum 和跟进 observation 明确 `mutation_executed=false`、`guard_coverage=0`，原始 receipt/hash 保留不改。跟进采样显示 authority/client MetaTunnel profile preflight=200，但客户端没有 game-side `POST /connectServer`、loadouts 或 `UMG_MainMenuBase_C.Construct`；对照 42C72 成功 run，固定 EXE、AppID 文件、startgame.ps1、地图/模式、客户端/authority 参数与 preflight 一致，旧 run 到达主菜单和 signed native join。当前是尚未定位的本地客户端启动/认证边界差异。源码调用链仍显示 `native_authority_admission_verified=false` 只参与严格 readiness gate；客户端登录完成由固定 `UMG_MainMenuBase_C.Construct` → `NotifyClientLoginCompleted` 独立设置，不能把差异归因该 flag，也不能把 flag 改为 true。已按同一零覆盖规则停止重复运行；下一步须先复现 login-ready 或记录具体启动/认证失败，再运行 wrong-audience 目标 guard。

本轮唯一的 42C72B A/B 对照使用同一冻结 DLL、wrapper 和 driver：当前 run 的 orchestration exit=0、Backend finalize exit=1，client 停在 `native_admission_unverified`，没有 Grant mutation 或目标 guard 覆盖；旧 42C72B 历史 run 到达 `/connectServer`、Join 与原生 admission。A/B receipt 保留两次的源码/制品哈希，canonical reason 是当前启动/认证边界尚未定位；这只是有限对照推论，不能声称环境已完全排除，也不把旧 DLL 当兼容回退。

BP047 的 74000 authority-only 执行在精确 owned 进程退出后观察到 cleanup_state=CLEARED，但该私有 fixture 没有把 owned Payload 进程接入实例绑定注册 worker，因此服务按安全规则保持 UNHEALTHY，READY 复用与旧 receipt 隔离未验收。本轮另有独立隔离 gameserver service test 已真实执行注册、gst_ 凭证、签名 heartbeat 和旧能力清除，状态为 PASS_COMPONENT_ONLY；仍缺的是将该合法注册链接到真实 owned Payload 进程并读取其 capability。下一步应复用隔离测试 CA/实例注册流程完成 process-level heartbeat、NativeCleared 和新 world 流程，禁止手写 token 或 SQL READY。

历史失败不会改写为通过，后续成功也只能覆盖其实际运行源码、DLL 和环境。
