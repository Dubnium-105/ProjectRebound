// ======================================================
//  LoadoutManager — 配装管理器（网络角色感知 + 本地快照驱动）
// ======================================================
//
//  数据流：
//    1. PreloadSnapshot → 加载本地快照文件（custom > launch > export）
//    2. OnRoleSelectionConfirmed → 从快照提取角色配装 → 推送库存
//    3. TickServer → 轮询待应用快照 → PostSpawnApply 权威应用
//    4. OnServerProcessEventPre → 复活时重新推送库存
//
//  游戏客户端通过原生 GetPlayerArchiveV2 协议从 metaserver 获取
//  默认配装，Payload 用本地快照覆盖/修改服务端库存。

#include "LoadoutManager.h"
#include "LoadoutSerializer.h"
#include "LoadoutApplication.h"

#include <Windows.h>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <mutex>
#include <sstream>
#include <string>
#include <unordered_map>
#include <utility>
#include <vector>

#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Libs/json.hpp"
#include "../Debug/Debug.h"

using namespace SDK;
using namespace LoadoutSerializer;
using namespace LoadoutApplication;

std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
UObject* GetLastOfType(UClass* theClass, bool includeDefault);

extern bool LoginCompleted;

// =====================================================================
//  Impl — 内部状态
// =====================================================================

class LoadoutManager::Impl
{
public:
    // ---- 按玩家快照存储 ----
    struct PerPlayerSnapshot
    {
        nlohmann::json Snapshot;
        std::string RoleId;
        bool HasArrived = false;
        bool Applied = false;
        bool InventoryPushed = false;
    };

    std::mutex mutex;
    std::unordered_map<APBPlayerController*, PerPlayerSnapshot> perPlayerSnapshots;

    // ---- 本地快照（配装数据源） ----
    nlohmann::json localSnapshot;
    bool localSnapshotAvailable = false;
};

// =====================================================================
//  构造 / 生命周期
// =====================================================================

LoadoutManager::LoadoutManager()
    : impl_(std::make_unique<Impl>())
{
}

LoadoutManager::~LoadoutManager() = default;
LoadoutManager::LoadoutManager(LoadoutManager&&) noexcept = default;
LoadoutManager& LoadoutManager::operator=(LoadoutManager&&) noexcept = default;

// =====================================================================
//  辅助函数
// =====================================================================

namespace
{
    std::filesystem::path GetExportSnapshotPath()
    {
        char* appData = nullptr;
        size_t len = 0;
        if (_dupenv_s(&appData, &len, "APPDATA") == 0 && appData && *appData)
        {
            auto result = std::filesystem::path(appData) / "ProjectReboundBrowser" / "loadout-export-v1.json";
            free(appData);
            return result;
        }
        free(appData);
        return std::filesystem::current_path() / "ProjectReboundBrowser" / "loadout-export-v1.json";
    }

    // 从本地磁盘加载快照
    void EnsureLocalSnapshotLoaded(nlohmann::json& localSnapshot, bool& localSnapshotAvailable)
    {
        if (localSnapshotAvailable) return;

        // 优先级：custom > launch > export
        const auto appDataRoot = GetExportSnapshotPath().parent_path();
        const auto customPath = appDataRoot / "custom-loadout-v1.json";
        const auto launchPath = appDataRoot / "launchers" / "loadout-launch-v1.json";
        const auto exportPath = appDataRoot / "loadout-export-v1.json";

        nlohmann::json loaded;
        if (LoadCustomLoadoutConfig(loaded))
        {
            ClientLog("[LOADOUT] Using custom loadout");
        }
        else if (ReadJsonFile(launchPath, loaded))
        {
            ClientLog("[LOADOUT] Using launch snapshot");
            try { std::filesystem::remove(launchPath); } catch (...) {}
        }
        else if (ReadJsonFile(exportPath, loaded))
        {
            ClientLog("[LOADOUT] Using export snapshot");
        }

        if (loaded.is_object())
        {
            loaded.erase("selectedRoleId");
            localSnapshot = loaded;
            localSnapshotAvailable = true;
        }
    }
}

// =====================================================================
//  公有接口 — 启动 / 菜单信号
// =====================================================================

void LoadoutManager::PreloadSnapshot()
{
    UWorld* World = UWorld::GetWorld();
    if (!World) return;
    AGameStateBase* GS = World->GameState;
    if (!GS || !GS->HasAuthority()) return;

    EnsureLocalSnapshotLoaded(impl_->localSnapshot, impl_->localSnapshotAvailable);
    if (impl_->localSnapshotAvailable)
        ClientLog("[LOADOUT] Local snapshot loaded");
    else
        ClientLog("[LOADOUT] No local snapshot available");
}

void LoadoutManager::NotifyMenuConstructed()
{
    // 不再需要 — 游戏客户端通过原生协议获取配装用于菜单显示
}

void LoadoutManager::RememberMenuSelectedRole(const FName& roleId)
{
    // 不再需要 — 菜单操作由游戏原生协议处理
    (void)roleId;
}

// =====================================================================
//  公有接口 — 服务端角色确认
// =====================================================================

void LoadoutManager::OnRoleSelectionConfirmed(APBPlayerController* playerController, const FName& roleId, bool isAuthoritative)
{
    if (!playerController || !playerController->HasAuthority()) return;
    if (IsBlankName(roleId)) return;

    const std::string roleIdStr = NameToString(roleId);
    ClientLog("[LOADOUT] Role confirmed: player=" + playerController->GetFullName() +
        " role=" + roleIdStr);

    // 从本地快照获取配装
    EnsureLocalSnapshotLoaded(impl_->localSnapshot, impl_->localSnapshotAvailable);
    if (!impl_->localSnapshotAvailable)
    {
        ClientLog("[LOADOUT] No loadout data available");
        return;
    }

    nlohmann::json loadoutJson = ExtractSingleRoleFromSnapshot(impl_->localSnapshot, roleIdStr);
    if (!loadoutJson.is_object())
    {
        ClientLog("[LOADOUT] No loadout data for role " + roleIdStr);
        return;
    }

    // 存储按玩家快照
    {
        std::scoped_lock lock(impl_->mutex);
        auto& perPlayer = impl_->perPlayerSnapshots[playerController];
        perPlayer.Snapshot = loadoutJson;
        perPlayer.RoleId = roleIdStr;
        perPlayer.HasArrived = true;
        perPlayer.Applied = false;
        perPlayer.InventoryPushed = false;
    }

    // 推送出生前库存
    {
        std::string detail;
        if (PreSpawnApply(loadoutJson, playerController, detail))
        {
            std::scoped_lock lock(impl_->mutex);
            auto it = impl_->perPlayerSnapshots.find(playerController);
            if (it != impl_->perPlayerSnapshots.end())
            {
                it->second.InventoryPushed = true;
            }
            ClientLog("[LOADOUT] Pre-spawn inventory pushed: " + detail);
        }
        else
        {
            ClientLog("[LOADOUT] Pre-spawn inventory push failed: " + detail);
        }
    }
}

// =====================================================================
//  公有接口 — ProcessEvent Hook 桥接
// =====================================================================

void LoadoutManager::OnClientProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    // 客户端 ProcessEvent 钩子已移除。
    // 游戏客户端通过原生 GetPlayerArchiveV2 协议获取配装数据。
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::OnClientProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    // 客户端 ProcessEvent 钩子已移除。
    // 配装变更通过原生 UpdateRoleArchiveV2 协议持久化。
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::OnServerProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    // 复活时重新推送库存（仅服务端权威路径）
    if (functionName.find("OnRestartInStartSpot") != std::string::npos)
    {
        APBPlayerController* playerController = nullptr;
        if (parms)
        {
            auto* restartParms = static_cast<Params::PBFieldModManager_OnRestartInStartSpot*>(parms);
            if (restartParms && restartParms->InController &&
                restartParms->InController->IsA(APBPlayerController::StaticClass()))
            {
                playerController = static_cast<APBPlayerController*>(restartParms->InController);
            }
        }

        if (playerController && playerController->HasAuthority())
        {
            std::scoped_lock lock(impl_->mutex);
            auto it = impl_->perPlayerSnapshots.find(playerController);
            if (it != impl_->perPlayerSnapshots.end() && it->second.HasArrived && !it->second.InventoryPushed)
            {
                std::string detail;
                if (PreSpawnApply(it->second.Snapshot, playerController, detail))
                {
                    it->second.InventoryPushed = true;
                }
            }
        }
    }
}

void LoadoutManager::OnServerProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    // 不需要恢复操作
    (void)object; (void)functionName; (void)parms;
}

// =====================================================================
//  公有接口 — Worker/Tick 桥接
// =====================================================================

void LoadoutManager::TickClient()
{
    // 客户端 Tick 已移除。
    // 不再需要菜单捕获、异步导出、实时应用等功能。
}

void LoadoutManager::TickServer()
{
    UWorld* World = UWorld::GetWorld();
    if (!World) return;
    AGameStateBase* GS = World->GameState;
    if (!GS || !GS->HasAuthority()) return;

    // 复制待应用列表（锁外操作）
    std::vector<std::pair<APBPlayerController*, Impl::PerPlayerSnapshot>> pendingApplies;
    {
        std::scoped_lock lock(impl_->mutex);
        for (auto& [controller, perPlayer] : impl_->perPlayerSnapshots)
        {
            if (perPlayer.HasArrived && !perPlayer.Applied)
            {
                pendingApplies.push_back({ controller, perPlayer });
            }
        }
    }

    for (auto& [playerController, perPlayer] : pendingApplies)
    {
        APBCharacter* character = GetControllerCharacter(playerController);
        if (!character || character->Inventory.Num() <= 0 || !IsCharacterAlive(character))
        {
            continue;
        }

        if (PostSpawnApply(character, perPlayer.Snapshot))
        {
            std::scoped_lock lock(impl_->mutex);
            auto it = impl_->perPlayerSnapshots.find(playerController);
            if (it != impl_->perPlayerSnapshots.end())
            {
                it->second.Applied = true;
            }

            ClientLog("[LOADOUT] Server applied loadout for player=" +
                playerController->GetFullName() + " role=" + perPlayer.RoleId);
        }
    }
}

// =====================================================================
//  公有接口 — 已弃用（兼容性保留）
// =====================================================================

void LoadoutManager::OnServerLoadoutDataReceived(APBPlayerController* playerController, const std::string& jsonPayload)
{
    // __LDS__ 聊天通道已弃用。
    // 配装数据通过本地快照文件加载，不再通过游戏内聊天通道传输。
    (void)playerController; (void)jsonPayload;
}
