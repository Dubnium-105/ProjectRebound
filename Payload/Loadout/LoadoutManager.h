#pragma once

// ======================================================
//  LoadoutManager — 装备快照/导出/运行时门面
// ======================================================
//
//  职责：
//    将完整的装备（Loadout）管线封装在一个公有管理器背后，包括
//    本地快照预加载、菜单侧导出触发、比赛内实时应用、
//    以及 ProcessEvent 作用域内的武器定义覆盖。
//
//  使用方式：
//    1. 在 Payload 启动时构造一个长生命周期实例
//    2. 在服务端/客户端运行前调用 PreloadSnapshot()
//    3. 从 Hook 层转发 ProcessEvent pre/post 回调
//    4. 从已有的 Worker/Tick Hook 转发 TickClient()/TickServer()
//    5. 观察到菜单角色选择/菜单构建信号时转发
//
//  设计原则：
//    - Hook 层保持薄转发：仅将事件传递给本门面
//    - 内部状态完全私有，后续装备修复不会扩散回 dllmain.cpp
//    - 公有 API 显式暴露顺序敏感的入口点：
//      调用方必须正确配对 Pre/Post Hook

#include <memory>
#include <string>

// 前向声明 — 与 SDK 命名空间保持一致
namespace SDK
{
    class APBPlayerController;
    class FName;
    class UObject;
}

class LoadoutManager
{
public:
    // ------------------------------------------------------------------
    //  构造 / 生命周期
    // ------------------------------------------------------------------

    LoadoutManager();
    ~LoadoutManager();

    LoadoutManager(const LoadoutManager&) = delete;
    LoadoutManager& operator=(const LoadoutManager&) = delete;
    LoadoutManager(LoadoutManager&&) noexcept;
    LoadoutManager& operator=(LoadoutManager&&) noexcept;

    // ------------------------------------------------------------------
    //  公有接口 — 启动 / 菜单信号
    // ------------------------------------------------------------------

    // @brief 从磁盘预加载持久化的装备快照。
    //        在早期调用，使后续菜单/实时应用逻辑启动时即拥有最新的 sidecar 状态。
    void PreloadSnapshot();

    // @brief 通知管理器主菜单已构建完成。
    //        这将打开延迟初始快照捕获及受保护的菜单侧重应用逻辑的窗口。
    void NotifyMenuConstructed();

    // @brief 记住菜单中最后显式选择的角色。
    //        作为比原始 selectedRoleId 字段更高质量的提示，
    //        用于将本地玩家匹配回正确的快照。
    void RememberMenuSelectedRole(const SDK::FName& roleId);

    // @brief 从已确认的运行时角色选择中设置待处理的出生角色上下文，
    //        使首次生命武器生成能在任何实时武器重建之前解析到正确的快照。
    void OnRoleSelectionConfirmed(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId,
        bool isAuthoritative);

    // ------------------------------------------------------------------
    //  公有接口 — ProcessEvent Hook 桥接
    // ------------------------------------------------------------------

    // @brief 客户端 ProcessEvent pre-hook 入口。
    //        在原始 ProcessEvent 之前调用，使临时武器定义覆盖在原生代码执行期间可见。
    void OnClientProcessEventPre(SDK::UObject* object, const std::string& functionName, void* parms);

    // @brief 客户端 ProcessEvent post-hook 入口。
    //        必须与 OnClientProcessEventPre() 配对调用，以恢复临时覆盖并调度导出触发。
    void OnClientProcessEventPost(SDK::UObject* object, const std::string& functionName, void* parms);

    // @brief 服务端 ProcessEvent pre-hook 入口。
    //        与客户端路径对称，驱动权威比赛侧快照应用和作用域覆盖设置。
    void OnServerProcessEventPre(SDK::UObject* object, const std::string& functionName, void* parms);

    // @brief 服务端 ProcessEvent post-hook 入口。
    //        恢复作用域内的修改，并让权威应用观察原始调用的最终原生结果。
    void OnServerProcessEventPost(SDK::UObject* object, const std::string& functionName, void* parms);

    // ------------------------------------------------------------------
    //  公有接口 — Worker/Tick 桥接
    // ------------------------------------------------------------------

    // @brief 推进客户端侧异步导出/应用工作。
    void TickClient();

    // @brief 推进服务端侧权威应用工作。
    void TickServer();

private:
    // PIMPL 将 JSON 相关状态、线程局部细节和大型辅助结构
    // 从公有头文件中隔离出来。
    class Impl;
    std::unique_ptr<Impl> impl_;
};
