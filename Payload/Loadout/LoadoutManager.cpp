// ======================================================
//  LoadoutManager — 配装管理器（服务端权威版本）
// ======================================================
//
//  数据流：
//    1. 服务端 OnRoleSelectionConfirmed →
//       从 metaserver HTTP 获取配装 →
//       校验 → 存储按玩家快照 → 推送库存
//    2. 服务端 TickServer →
//       轮询待应用快照 → PostSpawnApply 权威应用
//    3. 服务端 OnServerProcessEventPre →
//       复活时重新推送库存
//
//  客户端 ProcessEvent 钩子均已移除 — 游戏客户端通过
//  metaserver 原生 GetPlayerArchiveV2 协议获取配装数据。

#include "LoadoutManager.h"
#include "MetaserverClient.h"
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
extern bool amServer;

// =====================================================================
//  Impl — 内部状态
// =====================================================================

class MetaserverClient;

class LoadoutManager::Impl
{
public:
    MetaserverClient metaserver;

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

    // ---- 兼容：本地快照缓存（降级路径） ----
    nlohmann::json localSnapshot;
    bool localSnapshotAvailable = false;

    // ---- metaserver 连接状态 ----
    bool metaserverChecked = false;
    bool metaserverAvailable = false;

    // ---- 玩家 ID（从游戏状态推断） ----
    std::string playerId;

    Impl() : metaserver("http://127.0.0.1:8000") {}
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
    std::string BuildUtcTimestamp()
    {
        const auto now = std::chrono::system_clock::now();
        const std::time_t tv = std::chrono::system_clock::to_time_t(now);
        std::tm utc{};
        gmtime_s(&utc, &tv);
        std::ostringstream ss;
        ss << std::put_time(&utc, "%Y-%m-%dT%H:%M:%SZ");
        return ss.str();
    }

    std::string GetDefaultPlayerId()
    {
        // 尝试从环境变量或已知位置获取玩家 ID
        // 默认使用固定 ID（与 metaserver 的 TEMP_USER_ID 一致）
        return "76561198211631084";
    }

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

    // 降级：从本地磁盘加载快照（metaserver 不可用时使用）
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
            ClientLog("[LOADOUT] Using custom loadout (metaserver unavailable)");
        }
        else if (ReadJsonFile(launchPath, loaded))
        {
            ClientLog("[LOADOUT] Using launch snapshot (metaserver unavailable)");
            try { std::filesystem::remove(launchPath); } catch (...) {}
        }
        else if (ReadJsonFile(exportPath, loaded))
        {
            ClientLog("[LOADOUT] Using export snapshot (metaserver unavailable)");
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
    if (!amServer) return;

    // 初始化玩家 ID
    impl_->playerId = GetDefaultPlayerId();

    // 检查 metaserver 可用性
    impl_->metaserverAvailable = impl_->metaserver.IsAvailable();
    impl_->metaserverChecked = true;

    if (impl_->metaserverAvailable)
    {
        ClientLog("[LOADOUT] Metaserver available at http://127.0.0.1:8000");
    }
    else
    {
        ClientLog("[LOADOUT] Metaserver not available — falling back to local snapshot");
        EnsureLocalSnapshotLoaded(impl_->localSnapshot, impl_->localSnapshotAvailable);
    }
}

void LoadoutManager::NotifyMenuConstructed()
{
    // 不再需要 — 客户端通过 metaserver 原生协议获取配装
}

void LoadoutManager::RememberMenuSelectedRole(const FName& roleId)
{
    // 不再需要 — 客户端菜单操作由 metaserver UpdateRoleArchiveV2 覆盖
    (void)roleId;
}

// =====================================================================
//  公有接口 — 服务端角色确认
// =====================================================================

void LoadoutManager::OnRoleSelectionConfirmed(APBPlayerController* playerController, const FName& roleId, bool isAuthoritative)
{
    if (!playerController || !amServer) return;
    if (IsBlankName(roleId)) return;

    const std::string roleIdStr = NameToString(roleId);
    ClientLog("[LOADOUT] Role confirmed: player=" + playerController->GetFullName() +
        " role=" + roleIdStr + " authoritative=" + (isAuthoritative ? "true" : "false"));

    // 从 metaserver 获取配装数据
    nlohmann::json loadoutJson;
    bool fromMetaserver = false;

    if (impl_->metaserverAvailable)
    {
        // 尝试获取完整 loadout
        auto fullLoadout = impl_->metaserver.GetPlayerLoadout(impl_->playerId);
        if (fullLoadout.has_value())
        {
            // 转换为标准 snapshot 格式
            nlohmann::json snapshot;
            snapshot["schemaVersion"] = 2;
            snapshot["savedAtUtc"] = BuildUtcTimestamp();
            snapshot["gameVersion"] = "unknown";
            snapshot["source"] = "metaserver";
            snapshot["roles"] = nlohmann::json::array();

            if (fullLoadout->contains("roles") && (*fullLoadout)["roles"].is_object())
            {
                for (auto& [rid, roleData] : (*fullLoadout)["roles"].items())
                {
                    if (roleData.is_object())
                    {
                        nlohmann::json roleEntry = roleData;
                        if (!roleEntry.contains("roleId"))
                        {
                            roleEntry["roleId"] = rid;
                        }
                        snapshot["roles"].push_back(roleEntry);
                    }
                }
            }
            // 格式归一化：转换 metaserver 新 flat 格式为结构化格式
            snapshot = NormalizeLoadoutFormat(snapshot);

            if (!snapshot.contains("roles") || !snapshot["roles"].is_array() || snapshot["roles"].empty())
            {
                ClientLog("[LOADOUT] Normalized loadout has no valid roles");
            }
            else
            {
                // 校验
                auto validation = impl_->metaserver.ValidateLoadout(snapshot);
                if (!validation.warnings.empty())
                {
                    for (const auto& w : validation.warnings)
                        ClientLog("[LOADOUT] Validation warning: " + w);
                }

                // 过滤不兼容物品
                loadoutJson = impl_->metaserver.FilterLoadout(snapshot);
                fromMetaserver = true;

                ClientLog("[LOADOUT] Loaded loadout from metaserver: " +
                    std::to_string(snapshot["roles"].size()) + " roles");
            }
        }

        // 如果完整 loadout 不可用，尝试按角色获取
        if (!fromMetaserver)
        {
            auto roleLoadout = impl_->metaserver.GetPlayerRoleLoadout(impl_->playerId, roleIdStr);
            if (roleLoadout.has_value())
            {
                // 归一化：单角色数据也可能是新 flat 格式
                nlohmann::json normalized = NormalizeLoadoutFormat(roleLoadout.value());
                nlohmann::json snapshot;
                snapshot["schemaVersion"] = 2;
                snapshot["source"] = "metaserver";
                if (normalized.contains("roles") && normalized["roles"].is_array())
                    snapshot["roles"] = normalized["roles"];
                else
                    snapshot["roles"] = nlohmann::json::array({ normalized });
                loadoutJson = snapshot;
                fromMetaserver = true;
            }
        }
    }

    // 降级：使用本地快照
    if (!fromMetaserver)
    {
        EnsureLocalSnapshotLoaded(impl_->localSnapshot, impl_->localSnapshotAvailable);
        if (impl_->localSnapshotAvailable)
        {
            loadoutJson = ExtractSingleRoleFromSnapshot(impl_->localSnapshot, roleIdStr);
            ClientLog("[LOADOUT] Using local snapshot for role " + roleIdStr);
        }
    }

    if (!loadoutJson.is_object())
    {
        ClientLog("[LOADOUT] No loadout data available for role " + roleIdStr);
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
    // 游戏客户端现在通过 metaserver 原生 GetPlayerArchiveV2 协议获取配装。
    // InitWeapon 覆盖已移除 — 原生协议提供了正确的武器配装数据。
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::OnClientProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    // 客户端 ProcessEvent 钩子已移除。
    // 配装变更通过 metaserver UpdateRoleArchiveV2 协议持久化。
    (void)object; (void)functionName; (void)parms;
}

void LoadoutManager::OnServerProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    if (!amServer) return;

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

        if (playerController)
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

    // 注意：InitWeapon 覆盖已移除。
    // metaserver 原生协议提供正确配装数据，不需要运行时覆盖武器初始化参数。
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
    if (!amServer) return;

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
    // 配装数据现在通过 metaserver HTTP API 获取，不再通过游戏内聊天通道传输。
    (void)playerController; (void)jsonPayload;
}
