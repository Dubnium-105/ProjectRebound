#pragma once

// ======================================================
//  LoadoutManager — 配装管理器（服务端权威版本）
// ======================================================
//
//  职责：
//    服务端从 metaserver 获取配装数据，权威校验并应用到游戏实体。
//    客户端侧 Hook 已移除 — 游戏通过 metaserver 原生协议
//    (GetPlayerArchiveV2) 获取配装，不再需要客户端注入/覆盖。
//
//  使用方式：
//    1. 在 Payload 启动时构造实例，仅服务端启用
//    2. 从 Hook 层转发服务端 ProcessEvent pre 和角色确认信号
//    3. 从 Worker/Tick Hook 转发 TickServer()
//
//  设计原则：
//    - Metaserver 是配装存储和物品定义的权威源
//    - Payload 仅做服务端权威应用，不做客户端修改
//    - 还原原生游戏体验

#include <memory>
#include <string>

namespace SDK
{
    class APBPlayerController;
    class FName;
    class UObject;
}

class LoadoutManager
{
public:
    LoadoutManager();
    ~LoadoutManager();

    LoadoutManager(const LoadoutManager&) = delete;
    LoadoutManager& operator=(const LoadoutManager&) = delete;
    LoadoutManager(LoadoutManager&&) noexcept;
    LoadoutManager& operator=(LoadoutManager&&) noexcept;

    // ---- 启动 / 菜单信号 ----
    void PreloadSnapshot();
    void NotifyMenuConstructed();
    void RememberMenuSelectedRole(const SDK::FName& roleId);

    // ---- 服务端角色确认 ----
    void OnRoleSelectionConfirmed(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId,
        bool isAuthoritative);

    // ---- ProcessEvent Hook 桥接（客户端方法已为空桩） ----
    void OnClientProcessEventPre(SDK::UObject* object, const std::string& functionName, void* parms);
    void OnClientProcessEventPost(SDK::UObject* object, const std::string& functionName, void* parms);
    void OnServerProcessEventPre(SDK::UObject* object, const std::string& functionName, void* parms);
    void OnServerProcessEventPost(SDK::UObject* object, const std::string& functionName, void* parms);

    // ---- Worker/Tick 桥接 ----
    void TickClient();
    void TickServer();

    // ---- 已弃用：保留兼容性（不再使用 __LDS__ 通道） ----
    void OnServerLoadoutDataReceived(SDK::APBPlayerController* playerController, const std::string& jsonPayload);

private:
    class Impl;
    std::unique_ptr<Impl> impl_;
};
