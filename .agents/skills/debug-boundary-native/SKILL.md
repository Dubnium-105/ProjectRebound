---
name: debug-boundary-native
description: "诊断、逆向、修改并验证固定版本 Boundary Steam 客户端的 MetaTunnel、军械库、配装、自定义外观与对局出生原生流程。用于 QueryAssets/HasItem/OwnedItems、Get/UpdatePlayerArchiveV2、武器或角色自定义、FieldMod/APBPlayerState、Payload hook/RVA、Meta-tunnel 启动、Frida/IDA 验证或已安装游戏 DLL 的工作；不要用于无关的通用后端或网页任务。"
---

# Boundary 原生逆向与调试

## 先读取项目证据

- 在分析协议、RVA、对象布局或配装行为前，完整读取 `references/current-findings.md`。
- 在启动游戏、附加 Frida、操作 UI、替换 DLL 或做冷启动验证前，完整读取 `references/runtime-playbook.md`。
- 把引用中的地址和布局仅用于其记录的 EXE SHA-256；构建不一致时停止使用旧 RVA，重新做静态与运行时确认。

## 按证据强度推进

1. 固定游戏构建、模块大小、EXE 哈希、Payload 哈希和仓库提交。
2. 从一个可证伪的窄假设开始，例如“响应未进入 native consumer”或“某 completion 未被分发”。
3. 优先做 IDA/Hex-Rays 静态分析和只读 Frida 观测；记录函数签名、调用点、对象偏移和证据来源。
4. 在同一时间线关联 Meta RPC、原生 completion、Manager/PlayerState 状态、UI 显示和出生装备。
5. 只有在原生链缺失已经复现后，才实现最小兼容层；优先调用已确认的原生入口，不直接改写容器或缓存。
6. 为协议、序列化、状态码归一化和角色/武器路由增加回归测试。
7. 构建后校验源 DLL 与目标 DLL 哈希，保留可恢复备份，再执行两次冷启动及相关场景验收。

## 保持组件职责清晰

- 让 MetaServer 负责稳定、去重、版本固定的 wire 数据和持久化。
- 让游戏原生 `QueryAssets`、`GetPlayerArchiveV2` 和 completion 函数维护客户端对象状态。
- 仅让客户端兼容层补齐已证实未被当前游戏构建分发的 archive 内容；不要恢复定时 DT 扫描、`OwnedItems`/PersistentUser 覆盖或 `FieldModManager + 0x98` 直接写入。
- 让 `LoadoutManager` 保持服务端 listen-host 的 FieldMod/出生桥接职责；不要让它接管客户端军械库 UI 或 archive 初始化。

## 遵守逆向与注入约束

- 附加前校验进程可执行文件的完整路径、模块大小和 SHA-256；不要按模糊进程名盲附加。
- 对每个固定构建只保留一套 ABI 签名。修改签名或枚举解释时，同时更新只读探针和证据文档。
- 在 x64 Frida hook 中读取小枚举参数时屏蔽未定义高位，使用低字节作为值。
- 不把 Payload 自己写入后的结果当作 native RPC 成功证据。
- 不记录令牌、完整 archive、用户标识或敏感响应；记录 message ID、角色/槽位、数量、集合哈希、阶段和失败原因。
- 不扩大错误码拦截范围：通用持久化 completion 只把 `404` 归一化为成功；装备路径才额外把 `9002` 归一化为成功。

## 完成标准

- 保存请求有明确的 RPC message ID、路由结果和持久化后再次读取结果。
- 原生 completion 的类型、参数、原始状态码和归一化状态码均可观测。
- 首次冷启动和第二次冷启动的军械库显示与 Meta 数据一致。
- 涉及出生流程时，覆盖首次出生、复活、换角色、晚加入和断线重连。
- 无效或空槽遵从游戏原生回退规则，不伪造客户端内存状态。
- 交付时报告源/目标 DLL 哈希、备份位置、测试结果和仍未覆盖的场景。

## 使用现有资产

- 优先扩展 `Tools/Frida/armory_probe.js` 及其启动器，不新建重复的 FName、GObjects 或 ProcessEvent 辅助实现。
- 优先扩展 `Backend/internal/metaserver/native_rpc_test.go`、`p2p_loadout_test.go` 和 `Payload/Tests` 中的既有测试。
- 把新确认的地址、布局、协议语义和 A/B 结论更新到 `references/current-findings.md`；把运行或部署流程变化更新到 `references/runtime-playbook.md`。
