#pragma once

// ======================================================
//  LoadoutManager — 配装管理器（原生大厅流程 + 局内 metaserver 桥接）
// ======================================================
//
//  职责：
//    通过 HasAuthority()/IsLocallyControlled() 判断网络角色，
//    服务端权威在角色确认后从 Metaserver 拉取配装并应用到游戏实体，
//    Listen Server 下本地控制 Pawn 同时走客户端视觉路径。
//    游戏客户端通过原生 GetPlayerArchiveV2 协议从 metaserver 获取
//    默认配装；Payload 不接管大厅读写流程，只桥接局内应用流程。
//
//  使用方式：
//    1. 在 Payload 启动时构造实例，网络角色自动感知
//    2. 从 Hook 层转发 ProcessEvent pre 和角色确认信号
//    3. 从 Worker/Tick Hook 转发 TickServer()
//
//  设计原则：
//    - Metaserver 是配装权威源
//    - HasAuthority() 判定服务端权威操作，IsLocallyControlled() 判定本地视觉路径
//    - 大厅配装继续使用原生 protobuf 协议
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

