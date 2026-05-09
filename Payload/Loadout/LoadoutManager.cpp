// ======================================================
//  LoadoutManager — 配装管理器（原生大厅流程 + 局内 metaserver 桥接）
// ======================================================
//
//  数据流：
//    1. PreloadSnapshot → 服务端初始化 metaserver REST 客户端
//    2. OnRoleSelectionConfirmed → 按 playerId + roleId 拉取角色配装
//    3. PreSpawnApply → 出生前推送角色库存
//    4. TickServer → 轮询待应用快照 → PostSpawnApply 权威应用
//    5. OnServerProcessEventPre → 复活时重新推送库存
//
//  游戏客户端通过原生 GetPlayerArchiveV2 协议从 metaserver 获取
//  默认配装；Payload 只在局内服务端路径读取同一份 metaserver 数据并应用到实体。

#include "LoadoutManager.h"
#include "LoadoutSerializer.h"
#include "LoadoutApplication.h"
#include "MetaserverClient.h"

#include <Windows.h>

#include <algorithm>
#include <cctype>
#include <cstdint>
#include <cstdlib>
#include <mutex>
#include <optional>
#include <string>
#include <unordered_map>
#include <utility>
#include <vector>

#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Libs/json.hpp"
#include "../Debug/Debug.h"
#include "../Config/Config.h"

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

    // ---- 局内 metaserver 桥接状态 ----
    LoadoutMetaserver::MetaserverClient metaserver;
    bool metaserverChecked = false;
    bool metaserverAvailable = false;
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
    // -----------------------------------------------------------------
    //  metaserver 配置 / 玩家身份解析
    // -----------------------------------------------------------------

    constexpr const char* kDefaultMetaserverUrl = "http://127.0.0.1:8000";
    constexpr const char* kFallbackPlayerId = "76561198211631084";

    std::string TrimAscii(std::string value)
    {
        auto isSpace = [](unsigned char ch) { return std::isspace(ch) != 0; };
        value.erase(value.begin(), std::find_if(value.begin(), value.end(), [&](char ch) {
            return !isSpace(static_cast<unsigned char>(ch));
        }));
        value.erase(std::find_if(value.rbegin(), value.rend(), [&](char ch) {
            return !isSpace(static_cast<unsigned char>(ch));
        }).base(), value.end());
        return value;
    }

    std::string ToLowerAscii(std::string value)
    {
        std::transform(value.begin(), value.end(), value.begin(), [](unsigned char ch) {
            return static_cast<char>(std::tolower(ch));
        });
        return value;
    }

    std::string GetEnvValue(const char* name)
    {
        char* raw = nullptr;
        size_t len = 0;
        std::string value;
        if (_dupenv_s(&raw, &len, name) == 0 && raw)
        {
            value = raw;
        }
        free(raw);
        return TrimAscii(value);
    }

    std::string ResolveMetaserverBaseUrl()
    {
        // 专用服优先读取启动器传入的 LogicServerURL，便于与已有 metaserver 复用同一地址。
        std::string url = TrimAscii(GetCmdValue("-LogicServerURL="));
        if (url.empty()) url = GetEnvValue("PROJECT_REBOUND_METASERVER_URL");
        if (url.empty()) url = kDefaultMetaserverUrl;
        return url;
    }

    bool LooksLikePlayerId(const std::string& value)
    {
        if (value.empty() || value == "None" || value.size() > 128) return false;
        for (unsigned char ch : value)
        {
            if (std::isspace(ch) || ch == '{' || ch == '}' || ch == '"' || ch == '\'') return false;
        }
        return true;
    }

    std::string FindPlayerIdInJson(const nlohmann::json& value)
    {
        if (value.is_string())
        {
            const std::string candidate = TrimAscii(value.get<std::string>());
            return LooksLikePlayerId(candidate) ? candidate : "";
        }
        if (value.is_array())
        {
            for (const auto& entry : value)
            {
                const std::string found = FindPlayerIdInJson(entry);
                if (!found.empty()) return found;
            }
            return "";
        }
        if (!value.is_object()) return "";

        static const std::vector<std::string> preferredKeys = {
            "playerid", "player_id", "userid", "user_id", "steamid", "steam_id",
            "uniqueid", "unique_id", "uniquenetid", "platformid", "platform_id", "id"
        };

        for (auto it = value.begin(); it != value.end(); ++it)
        {
            const std::string key = ToLowerAscii(it.key());
            if (std::find(preferredKeys.begin(), preferredKeys.end(), key) == preferredKeys.end()) continue;

            const std::string found = FindPlayerIdInJson(it.value());
            if (!found.empty()) return found;
        }

        for (auto it = value.begin(); it != value.end(); ++it)
        {
            const std::string found = FindPlayerIdInJson(it.value());
            if (!found.empty()) return found;
        }
        return "";
    }

    std::string ResolvePlayerId(APBPlayerController* playerController)
    {
        // 原生登录流程会把平台身份放在 PlayerState 的 JSON 字符串里。
        // metaserver 侧完成真实绑定前，仍保留固定 ID 作为调试回退。
        if (playerController && playerController->PlayerState &&
            playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
        {
            auto* playerState = static_cast<APBPlayerState*>(playerController->PlayerState);
            const std::string raw = TrimAscii(playerState->PlatformUniqueIDJsonString.ToString());
            if (!raw.empty())
            {
                const auto parsed = nlohmann::json::parse(raw, nullptr, false);
                if (!parsed.is_discarded())
                {
                    const std::string found = FindPlayerIdInJson(parsed);
                    if (!found.empty()) return found;
                }
                if (LooksLikePlayerId(raw)) return raw;
            }
        }
        return kFallbackPlayerId;
    }

    bool SnapshotHasRole(const nlohmann::json& snapshot)
    {
        return snapshot.is_object() &&
            snapshot.contains("roles") &&
            snapshot["roles"].is_array() &&
            !snapshot["roles"].empty();
    }

    nlohmann::json WrapSingleRoleSnapshot(nlohmann::json role, const std::string& roleId)
    {
        // 应用层仍消费 roles 数组，这里把 REST 单角色返回包成同一套 snapshot 形状。
        if (!role.is_object()) return nlohmann::json();
        if (!role.contains("roleId") || role.value("roleId", "").empty())
        {
            role["roleId"] = roleId;
        }

        nlohmann::json snapshot;
        snapshot["schemaVersion"] = 2;
        snapshot["source"] = "metaserver";
        snapshot["roles"] = nlohmann::json::array({ role });
        return snapshot;
    }

    nlohmann::json BuildSingleRoleSnapshot(const nlohmann::json& payload, const std::string& roleId)
    {
        // REST 可能返回 flat role、structured role、完整 loadout 或 loadoutSnapshot 包装。
        // 统一交给 LoadoutSerializer 归一化，再裁剪成当前确认的单角色。
        if (!payload.is_object()) return nlohmann::json();

        nlohmann::json effectivePayload = payload;
        if (payload.contains("loadoutSnapshot") && payload["loadoutSnapshot"].is_object())
        {
            effectivePayload = payload["loadoutSnapshot"];
            if (!effectivePayload.contains("roleId") || effectivePayload.value("roleId", "").empty())
            {
                effectivePayload["roleId"] = payload.value("roleId", roleId);
            }
        }

        nlohmann::json normalized = NormalizeLoadoutFormat(effectivePayload);
        if (!normalized.is_object()) return nlohmann::json();

        if (normalized.contains("roles"))
        {
            nlohmann::json roleSnapshot = ExtractSingleRoleFromSnapshot(normalized, roleId);
            return SnapshotHasRole(roleSnapshot) ? roleSnapshot : nlohmann::json();
        }

        const std::string normalizedRoleId = normalized.value("roleId", "");
        if (!normalizedRoleId.empty() && normalizedRoleId != roleId)
        {
            return nlohmann::json();
        }
        return WrapSingleRoleSnapshot(std::move(normalized), roleId);
    }

    void EnsureMetaserverConfigured(
        LoadoutMetaserver::MetaserverClient& metaserver,
        bool& checked,
        bool& available)
    {
        // 只在服务端首次使用时探测一次，后续请求仍会按需尝试，避免 health 失败后永久短路。
        if (checked) return;
        metaserver.SetBaseUrl(ResolveMetaserverBaseUrl());
        available = metaserver.IsAvailable();
        checked = true;

        ClientLog(std::string("[LOADOUT] Metaserver ") +
            (available ? "available: " : "unavailable: ") +
            metaserver.BaseUrl());
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

    EnsureMetaserverConfigured(
        impl_->metaserver,
        impl_->metaserverChecked,
        impl_->metaserverAvailable);
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
    (void)isAuthoritative;
    if (!playerController || !playerController->HasAuthority()) return;
    if (IsBlankName(roleId)) return;

    const std::string roleIdStr = NameToString(roleId);
    const std::string playerId = ResolvePlayerId(playerController);
    ClientLog("[LOADOUT] Role confirmed: player=" + playerController->GetFullName() +
        " playerId=" + playerId + " role=" + roleIdStr);

    // 从 BoundaryMetaServer 拉取本局权威角色配装。
    EnsureMetaserverConfigured(
        impl_->metaserver,
        impl_->metaserverChecked,
        impl_->metaserverAvailable);

    std::optional<nlohmann::json> payload = impl_->metaserver.GetPlayerRoleLoadout(playerId, roleIdStr);
    if (!payload || !payload->is_object())
    {
        ClientLog("[LOADOUT] Role endpoint miss, trying player loadout: playerId=" + playerId +
            " role=" + roleIdStr);
        payload = impl_->metaserver.GetPlayerLoadout(playerId);
    }

    if (!payload || !payload->is_object())
    {
        ClientLog("[LOADOUT] No metaserver loadout data available: playerId=" + playerId +
            " role=" + roleIdStr);
        return;
    }

    nlohmann::json loadoutJson = BuildSingleRoleSnapshot(*payload, roleIdStr);
    if (!SnapshotHasRole(loadoutJson))
    {
        ClientLog("[LOADOUT] No metaserver loadout data for role: playerId=" + playerId +
            " role=" + roleIdStr);
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
    // 配装数据通过 metaserver REST 拉取，不再通过游戏内聊天通道传输。
    (void)playerController; (void)jsonPayload;
}
