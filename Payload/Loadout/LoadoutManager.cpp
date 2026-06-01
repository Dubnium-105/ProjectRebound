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
#include "LoadoutShowroomApplication.h"
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
#include "../SDK/PBFieldModManager_BP_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Libs/json.hpp"
#include "../Debug/Debug.h"
#include "../Config/Config.h"

using namespace SDK;
using namespace LoadoutSerializer;
using namespace LoadoutApplication;
using namespace LoadoutShowroomApplication;

std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
UObject* GetLastOfType(UClass* theClass, bool includeDefault);

extern bool LoginCompleted;
extern "C" void PayloadPushClientProcessEventSuppression();
extern "C" void PayloadPopClientProcessEventSuppression();

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

    // ---- 客户端军械库接管状态 ----
    nlohmann::json clientSnapshot;
    nlohmann::json clientPreviewSnapshot;
    std::string clientPlayerId;
    bool clientSnapshotLoaded = false;
    bool clientPreviewActive = false;
    bool clientWarnedNoSnapshot = false;
    bool clientWarnedWaitingPlayerId = false;
    ULONGLONG nextClientFetchAttemptMs = 0;

    std::string pendingEquipRoleId;
    std::string pendingEquipItemId;
    EPBCharacterSlotType pendingEquipSlotType = EPBCharacterSlotType::None;
    ULONGLONG pendingEquipAtMs = 0;

    std::string pendingPreviewRoleId;
    std::string pendingPreviewItemId;
    EPBCharacterSlotType pendingPreviewSlotType = EPBCharacterSlotType::None;
    ULONGLONG pendingPreviewAtMs = 0;

    std::string lastCommittedRoleId;
    std::string lastCommittedItemId;
    EPBCharacterSlotType lastCommittedSlotType = EPBCharacterSlotType::None;
    ULONGLONG lastCommittedAtMs = 0;

    std::string clientInventoryCacheSignature;
    ULONGLONG nextClientInventoryCachePushMs = 0;
    ULONGLONG nextClientShowroomTickMs = 0;
    ULONGLONG nextClientWidgetTickMs = 0;
    ULONGLONG nextClientPlayerIdResolveMs = 0;
    bool clientWarnedNoFieldModCache = false;
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
    class ScopedClientProcessEventSuppression
    {
    public:
        ScopedClientProcessEventSuppression()
        {
            PayloadPushClientProcessEventSuppression();
        }

        ~ScopedClientProcessEventSuppression()
        {
            PayloadPopClientProcessEventSuppression();
        }

        ScopedClientProcessEventSuppression(const ScopedClientProcessEventSuppression&) = delete;
        ScopedClientProcessEventSuppression& operator=(const ScopedClientProcessEventSuppression&) = delete;
    };

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

    std::string ResolvePlayerIdFromPlayerState(APBPlayerState* playerState)
    {
        if (!playerState) return "";

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

        try
        {
            ScopedClientProcessEventSuppression suppressProcessEventHooks;
            const std::string userId = TrimAscii(playerState->GetUserIdstr().ToString());
            if (LooksLikePlayerId(userId)) return userId;
        }
        catch (...) {}

        try
        {
            ScopedClientProcessEventSuppression suppressProcessEventHooks;
            const std::string platformId = TrimAscii(playerState->GetPlatformIDStr().ToString());
            if (LooksLikePlayerId(platformId)) return platformId;
        }
        catch (...) {}

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
        if (playerController && playerController->PlayerState &&
            playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
        {
            const std::string found = ResolvePlayerIdFromPlayerState(
                static_cast<APBPlayerState*>(playerController->PlayerState));
            if (!found.empty()) return found;
        }
        if (playerController && playerController->PBPlayerState)
        {
            const std::string found = ResolvePlayerIdFromPlayerState(playerController->PBPlayerState);
            if (!found.empty()) return found;
        }
        return "";
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

    constexpr ULONGLONG kClientFetchRetryMs = 5000;

    std::string ResolveClientPlayerId()
    {
        std::string playerId = TrimAscii(GetCmdValue("-ProjectReboundPlayerId="));
        if (playerId.empty()) playerId = GetEnvValue("PROJECT_REBOUND_PLAYER_ID");
        if (LooksLikePlayerId(playerId)) return playerId;

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* playerController = static_cast<APBPlayerController*>(object);
            const std::string found = ResolvePlayerId(playerController);
            if (found != kFallbackPlayerId && LooksLikePlayerId(found)) return found;
        }

        for (UObject* object : getObjectsOfClass(APBPlayerState::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            const std::string found = ResolvePlayerIdFromPlayerState(static_cast<APBPlayerState*>(object));
            if (LooksLikePlayerId(found)) return found;
        }

        for (UObject* object : getObjectsOfClass(APBLobbyPlayerState::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* lobbyPlayerState = static_cast<APBLobbyPlayerState*>(object);
            const std::string platformId = TrimAscii(lobbyPlayerState->PlatformIdURL.ToString());
            if (LooksLikePlayerId(platformId)) return platformId;

            const auto parsed = nlohmann::json::parse(platformId, nullptr, false);
            if (!parsed.is_discarded())
            {
                const std::string found = FindPlayerIdInJson(parsed);
                if (LooksLikePlayerId(found)) return found;
            }
        }

        return "";
    }

    nlohmann::json* FindRoleInSnapshot(nlohmann::json& snapshot, const std::string& roleId, bool createIfMissing)
    {
        if (roleId.empty()) return nullptr;
        if (!snapshot.is_object())
        {
            if (!createIfMissing) return nullptr;
            snapshot = nlohmann::json::object();
        }
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array())
        {
            if (!createIfMissing) return nullptr;
            snapshot["roles"] = nlohmann::json::array();
        }

        for (auto& role : snapshot["roles"])
        {
            if (role.is_object() && role.value("roleId", "") == roleId)
                return &role;
        }

        if (!createIfMissing) return nullptr;
        snapshot["roles"].push_back(EmptyRoleJson(roleId));
        return &snapshot["roles"].back();
    }

    void UpsertInventorySlot(nlohmann::json& role, EPBCharacterSlotType slotType, const std::string& itemId)
    {
        if (!role.contains("inventory") || !role["inventory"].is_object())
            role["inventory"] = EmptyInventoryJson();
        if (!role["inventory"].contains("slots") || !role["inventory"]["slots"].is_array())
            role["inventory"]["slots"] = nlohmann::json::array();

        const int slotValue = static_cast<int>(slotType);
        for (auto& slot : role["inventory"]["slots"])
        {
            if (!slot.is_object()) continue;
            if (slot.value("slotType", 0) == slotValue)
            {
                slot["itemId"] = itemId;
                return;
            }
        }

        role["inventory"]["slots"].push_back({
            { "slotType", slotValue },
            { "itemId", itemId }
        });
    }

    void EnsureWeaponConfig(nlohmann::json& role, const std::string& weaponId)
    {
        if (IsBlankText(weaponId)) return;
        if (!role.contains("weaponConfigs") || !role["weaponConfigs"].is_object())
            role["weaponConfigs"] = nlohmann::json::object();

        nlohmann::json& weaponConfig = role["weaponConfigs"][weaponId];
        if (!weaponConfig.is_object()) weaponConfig = EmptyWeaponJson();
        weaponConfig["weaponId"] = weaponId;
    }

    std::string JsonItemIdValue(const nlohmann::json& value)
    {
        if (value.is_string()) return value.get<std::string>();
        if (!value.is_object()) return "";
        for (const char* key : { "itemId", "id", "mobilityModuleId", "weaponId" })
        {
            if (value.contains(key) && value[key].is_string())
            {
                const std::string itemId = value[key].get<std::string>();
                if (!IsBlankText(itemId)) return itemId;
            }
        }
        return "";
    }

    std::string GetRoleSlotItem(const nlohmann::json& role, EPBCharacterSlotType slotType)
    {
        if (!role.is_object() ||
            !role.contains("inventory") ||
            !role["inventory"].is_object() ||
            !role["inventory"].contains("slots") ||
            !role["inventory"]["slots"].is_array())
        {
            return "";
        }

        const int slotValue = static_cast<int>(slotType);
        for (const auto& slot : role["inventory"]["slots"])
        {
            if (!slot.is_object() || slot.value("slotType", 0) != slotValue) continue;
            if (slot.contains("itemId")) return JsonItemIdValue(slot["itemId"]);
            return JsonItemIdValue(slot);
        }
        return "";
    }

    std::string GetFirstRoleFieldItem(const nlohmann::json& role, const std::vector<const char*>& keys)
    {
        if (!role.is_object()) return "";
        for (const char* key : keys)
        {
            if (!role.contains(key)) continue;
            const std::string itemId = JsonItemIdValue(role[key]);
            if (!IsBlankText(itemId)) return itemId;
        }
        return "";
    }

    std::string GetRoleSlotOrFieldItem(const nlohmann::json& role, EPBCharacterSlotType slotType)
    {
        const std::string slotItem = GetRoleSlotItem(role, slotType);
        if (!IsBlankText(slotItem)) return slotItem;

        switch (slotType)
        {
        case EPBCharacterSlotType::FirstWeapon:
            return GetFirstRoleFieldItem(role, { "primaryWeapon" });
        case EPBCharacterSlotType::SecondWeapon:
            return GetFirstRoleFieldItem(role, { "secondaryWeapon" });
        case EPBCharacterSlotType::LeftPod:
            return GetFirstRoleFieldItem(role, { "leftLauncher", "leftPylon", "leftPod" });
        case EPBCharacterSlotType::RightPod:
            return GetFirstRoleFieldItem(role, { "rightLauncher", "rightPylon", "rightPod" });
        case EPBCharacterSlotType::MeleeWeapon:
            return GetFirstRoleFieldItem(role, { "meleeWeapon" });
        case EPBCharacterSlotType::Mobility:
            return GetFirstRoleFieldItem(role, { "mobilityModule" });
        default:
            return "";
        }
    }

    void SyncRoleSummaryFromInventory(nlohmann::json& role)
    {
        if (!role.is_object()) return;

        const std::string primary = GetRoleSlotOrFieldItem(role, EPBCharacterSlotType::FirstWeapon);
        if (!IsBlankText(primary))
        {
            UpsertInventorySlot(role, EPBCharacterSlotType::FirstWeapon, primary);
            role["primaryWeapon"] = primary;
            EnsureWeaponConfig(role, primary);
        }

        const std::string secondary = GetRoleSlotOrFieldItem(role, EPBCharacterSlotType::SecondWeapon);
        if (!IsBlankText(secondary))
        {
            UpsertInventorySlot(role, EPBCharacterSlotType::SecondWeapon, secondary);
            role["secondaryWeapon"] = secondary;
            EnsureWeaponConfig(role, secondary);
        }

        const std::string leftPod = GetRoleSlotOrFieldItem(role, EPBCharacterSlotType::LeftPod);
        if (!IsBlankText(leftPod))
        {
            UpsertInventorySlot(role, EPBCharacterSlotType::LeftPod, leftPod);
            role["leftLauncher"] = EmptyLauncherJson();
            role["leftLauncher"]["id"] = leftPod;
            role["leftPylon"] = leftPod;
        }

        const std::string rightPod = GetRoleSlotOrFieldItem(role, EPBCharacterSlotType::RightPod);
        if (!IsBlankText(rightPod))
        {
            UpsertInventorySlot(role, EPBCharacterSlotType::RightPod, rightPod);
            role["rightLauncher"] = EmptyLauncherJson();
            role["rightLauncher"]["id"] = rightPod;
            role["rightPylon"] = rightPod;
        }

        const std::string melee = GetRoleSlotOrFieldItem(role, EPBCharacterSlotType::MeleeWeapon);
        if (!IsBlankText(melee))
        {
            UpsertInventorySlot(role, EPBCharacterSlotType::MeleeWeapon, melee);
            role["meleeWeapon"] = EmptyMeleeJson();
            role["meleeWeapon"]["id"] = melee;
        }

        const std::string mobility = GetRoleSlotOrFieldItem(role, EPBCharacterSlotType::Mobility);
        if (!IsBlankText(mobility))
        {
            UpsertInventorySlot(role, EPBCharacterSlotType::Mobility, mobility);
            role["mobilityModule"] = EmptyMobilityJson();
            role["mobilityModule"]["mobilityModuleId"] = mobility;
        }
    }

    void SyncSnapshotSummaryFromInventory(nlohmann::json& snapshot)
    {
        if (!snapshot.is_object() || !snapshot.contains("roles") || !snapshot["roles"].is_array()) return;
        for (auto& role : snapshot["roles"])
        {
            SyncRoleSummaryFromInventory(role);
        }
    }

    nlohmann::json BuildMetaserverPutSnapshot(const nlohmann::json& sourceSnapshot)
    {
        nlohmann::json snapshot = sourceSnapshot;
        SyncSnapshotSummaryFromInventory(snapshot);

        nlohmann::json payload;
        payload["schemaVersion"] = snapshot.value("schemaVersion", 2);
        payload["source"] = "payload-client";
        payload["roles"] = nlohmann::json::object();

        if (!snapshot.is_object() || !snapshot.contains("roles") || !snapshot["roles"].is_array())
            return payload;

        for (auto role : snapshot["roles"])
        {
            if (!role.is_object()) continue;
            SyncRoleSummaryFromInventory(role);
            const std::string roleId = role.value("roleId", "");
            if (IsBlankText(roleId)) continue;

            nlohmann::json storedRole = role;
            storedRole["loadoutSnapshot"] = role;
            payload["roles"][roleId] = std::move(storedRole);
        }

        return payload;
    }

    bool UpdateRoleSlotInSnapshot(
        nlohmann::json& snapshot,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        if (IsBlankText(roleId) || IsBlankText(itemId)) return false;
        nlohmann::json* role = FindRoleInSnapshot(snapshot, roleId, true);
        if (!role) return false;

        const std::string before = role->dump();
        UpsertInventorySlot(*role, slotType, itemId);

        switch (slotType)
        {
        case EPBCharacterSlotType::FirstWeapon:
            EnsureWeaponConfig(*role, itemId);
            (*role)["primaryWeapon"] = itemId;
            break;
        case EPBCharacterSlotType::SecondWeapon:
            EnsureWeaponConfig(*role, itemId);
            (*role)["secondaryWeapon"] = itemId;
            break;
        case EPBCharacterSlotType::LeftPod:
            (*role)["leftLauncher"] = EmptyLauncherJson();
            (*role)["leftLauncher"]["id"] = itemId;
            (*role)["leftPylon"] = itemId;
            break;
        case EPBCharacterSlotType::RightPod:
            (*role)["rightLauncher"] = EmptyLauncherJson();
            (*role)["rightLauncher"]["id"] = itemId;
            (*role)["rightPylon"] = itemId;
            break;
        case EPBCharacterSlotType::MeleeWeapon:
            (*role)["meleeWeapon"] = EmptyMeleeJson();
            (*role)["meleeWeapon"]["id"] = itemId;
            break;
        case EPBCharacterSlotType::Mobility:
            (*role)["mobilityModule"] = EmptyMobilityJson();
            (*role)["mobilityModule"]["mobilityModuleId"] = itemId;
            break;
        default:
            break;
        }

        SyncRoleSummaryFromInventory(*role);
        return role->dump() != before;
    }

    bool TryReadInventoryWidget(
        UObject* object,
        std::string& outRoleId,
        EPBCharacterSlotType& outSlotType,
        std::string& outItemId)
    {
        if (!object || !object->IsA(UPBItemCSTM_Inventory::StaticClass())) return false;

        auto* item = static_cast<UPBItemCSTM_Inventory*>(object);
        outRoleId = NameToString(item->CharacterID);
        outSlotType = item->CharacterSlotType;
        outItemId = NameToString(item->ItemId);
        return !IsBlankText(outRoleId) &&
            !IsBlankText(outItemId) &&
            outSlotType != EPBCharacterSlotType::None;
    }

    void RefreshInventoryWidgetsForSlot(
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        if (IsBlankText(roleId) || IsBlankText(itemId) || slotType == EPBCharacterSlotType::None) return;

        for (UObject* object : getObjectsOfClass(UPBItemCSTM_Inventory::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* item = static_cast<UPBItemCSTM_Inventory*>(object);
            if (NameToString(item->CharacterID) != roleId || item->CharacterSlotType != slotType) continue;

            const bool isEquipped = NameToString(item->ItemId) == itemId;
            item->bIsEquipped = isEquipped;
            item->EquippedSlot = isEquipped ? slotType : EPBCharacterSlotType::None;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                item->RefreshItem();
            }
            catch (...) {}
            item->bIsEquipped = isEquipped;
            item->EquippedSlot = isEquipped ? slotType : EPBCharacterSlotType::None;
        }
    }

    const nlohmann::json* FindRoleInSnapshotConst(
        const nlohmann::json& snapshot,
        const std::string& roleId)
    {
        if (IsBlankText(roleId) ||
            !snapshot.is_object() ||
            !snapshot.contains("roles") ||
            !snapshot["roles"].is_array())
        {
            return nullptr;
        }

        for (const auto& role : snapshot["roles"])
        {
            if (role.is_object() && role.value("roleId", "") == roleId)
                return &role;
        }
        return nullptr;
    }

    std::string ResolveSingleSnapshotRoleId(const nlohmann::json& snapshot)
    {
        if (!snapshot.is_object() ||
            !snapshot.contains("roles") ||
            !snapshot["roles"].is_array() ||
            snapshot["roles"].size() != 1)
        {
            return "";
        }

        const auto& role = snapshot["roles"][0];
        return role.is_object() ? role.value("roleId", "") : "";
    }

    EPBCharacterSlotType FindEquippedSlotForItem(
        const nlohmann::json& snapshot,
        const std::string& roleId,
        const std::string& itemId,
        EPBCharacterSlotType preferredSlot)
    {
        if (IsBlankText(itemId)) return EPBCharacterSlotType::None;
        const nlohmann::json* role = FindRoleInSnapshotConst(snapshot, roleId);
        if (!role) return EPBCharacterSlotType::None;

        if (GetRoleSlotOrFieldItem(*role, preferredSlot) == itemId)
            return preferredSlot;

        for (EPBCharacterSlotType slotType : {
            EPBCharacterSlotType::FirstWeapon,
            EPBCharacterSlotType::SecondWeapon,
            EPBCharacterSlotType::LeftPod,
            EPBCharacterSlotType::RightPod,
            EPBCharacterSlotType::MeleeWeapon,
            EPBCharacterSlotType::Mobility })
        {
            if (GetRoleSlotOrFieldItem(*role, slotType) == itemId)
                return slotType;
        }
        return EPBCharacterSlotType::None;
    }

    bool CorrectInventoryWidgetFromSnapshot(
        UPBItemCSTM_Inventory* item,
        const nlohmann::json& snapshot,
        bool refreshItem)
    {
        if (!item || item->IsDefaultObject()) return false;

        const std::string roleId = NameToString(item->CharacterID);
        const std::string itemId = NameToString(item->ItemId);
        if (IsBlankText(roleId) || IsBlankText(itemId)) return false;

        const EPBCharacterSlotType equippedSlot = FindEquippedSlotForItem(
            snapshot,
            roleId,
            itemId,
            item->CharacterSlotType);
        const bool isEquipped = equippedSlot != EPBCharacterSlotType::None;
        const bool changed = item->bIsEquipped != isEquipped || item->EquippedSlot != equippedSlot;

        item->bIsEquipped = isEquipped;
        item->EquippedSlot = equippedSlot;
        if (refreshItem && changed)
        {
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                item->RefreshItem();
            }
            catch (...) {}
            item->bIsEquipped = isEquipped;
            item->EquippedSlot = equippedSlot;
        }
        return changed;
    }

    bool ResolveActiveCustomizeContext(std::string& outRoleId, EPBCharacterSlotType& outSlotType)
    {
        for (UObject* object : getObjectsOfClass(UPBCustomizeWidget::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* widget = static_cast<UPBCustomizeWidget*>(object);
            const std::string roleId = NameToString(widget->EditingCharacterID);
            if (!IsBlankText(roleId) && widget->EditingCharacterSlot != EPBCharacterSlotType::None)
            {
                outRoleId = roleId;
                outSlotType = widget->EditingCharacterSlot;
                return true;
            }
        }

        for (UObject* object : getObjectsOfClass(UPBPanelCSTM_EditCharacterSlot::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* panel = static_cast<UPBPanelCSTM_EditCharacterSlot*>(object);
            const std::string roleId = NameToString(panel->EditingCharacterID);
            if (!IsBlankText(roleId) && panel->EditingCharacterSlot != EPBCharacterSlotType::None)
            {
                outRoleId = roleId;
                outSlotType = panel->EditingCharacterSlot;
                return true;
            }
        }

        for (UObject* object : getObjectsOfClass(UPBCustomizeUIManager::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* manager = static_cast<UPBCustomizeUIManager*>(object);
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                if (!manager->IsSetEditingCharacterID() || !manager->IsSetEditingCharacterSlotType()) continue;
                const std::string roleId = NameToString(manager->GetEditingCharacterID());
                const EPBCharacterSlotType slotType = manager->GetEditingCharacterSlotType();
                if (!IsBlankText(roleId) && slotType != EPBCharacterSlotType::None)
                {
                    outRoleId = roleId;
                    outSlotType = slotType;
                    return true;
                }
            }
            catch (...) {}
        }

        return false;
    }

    bool CorrectItemDetailWidgetFromSnapshot(
        UPBItemDetailWidget* detail,
        const nlohmann::json& snapshot,
        bool refreshItem)
    {
        if (!detail || detail->IsDefaultObject()) return false;

        std::string roleId;
        EPBCharacterSlotType contextSlot = EPBCharacterSlotType::None;
        if (!ResolveActiveCustomizeContext(roleId, contextSlot)) return false;

        const std::string itemId = NameToString(detail->ItemId);
        if (IsBlankText(itemId)) return false;

        const EPBCharacterSlotType equippedSlot = FindEquippedSlotForItem(
            snapshot,
            roleId,
            itemId,
            contextSlot);
        const bool isEquipped = equippedSlot != EPBCharacterSlotType::None;
        const bool inFirstWeaponSlot = equippedSlot == EPBCharacterSlotType::FirstWeapon;
        const bool isLeftPod = equippedSlot == EPBCharacterSlotType::LeftPod;
        const bool isDisplayInSlot = equippedSlot == contextSlot;

        const bool changed =
            detail->bIsEquipped != isEquipped ||
            detail->bIsEquippedInFirstWeaponSlot != inFirstWeaponSlot ||
            detail->bIsLeftPod != isLeftPod ||
            detail->bIsitemDisplayInSlot != isDisplayInSlot;

        detail->bIsEquipped = isEquipped;
        detail->bIsEquippedInFirstWeaponSlot = inFirstWeaponSlot;
        detail->bIsLeftPod = isLeftPod;
        detail->bIsitemDisplayInSlot = isDisplayInSlot;

        if (refreshItem && changed)
        {
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                detail->K2_NotifyRefreshItemDetail();
            }
            catch (...) {}
            detail->bIsEquipped = isEquipped;
            detail->bIsEquippedInFirstWeaponSlot = inFirstWeaponSlot;
            detail->bIsLeftPod = isLeftPod;
            detail->bIsitemDisplayInSlot = isDisplayInSlot;
        }
        return changed;
    }

    void RefreshItemDetailWidgetsForSnapshot(const nlohmann::json& snapshot, bool refreshItem)
    {
        for (UObject* object : getObjectsOfClass(UPBItemDetailWidget::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            CorrectItemDetailWidgetFromSnapshot(
                static_cast<UPBItemDetailWidget*>(object),
                snapshot,
                refreshItem);
        }
    }

    bool TryResolveSnapshotItemForSlot(
        const nlohmann::json& snapshot,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        std::string& outItemId);
    std::string ResolveSingleSnapshotRoleId(const nlohmann::json& snapshot);
    std::string ResolveQueryRoleId(UObject* object);
    template <typename ImplT>
    bool TryResolvePendingPreview(
        const ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        std::string& outItemId);

    bool SameNameValue(const FName& left, const FName& right)
    {
        return left.ComparisonIndex == right.ComparisonIndex && left.Number == right.Number;
    }

    bool FNameMapEquals(const FName& left, const FName& right)
    {
        return SameNameValue(left, right);
    }

    void AddUniqueFieldModManager(
        std::vector<UPBFieldModManager*>& managers,
        UObject* object)
    {
        if (!object || object->IsDefaultObject() || !object->IsA(UPBFieldModManager::StaticClass()))
            return;

        auto* manager = static_cast<UPBFieldModManager*>(object);
        if (std::find(managers.begin(), managers.end(), manager) == managers.end())
            managers.push_back(manager);
    }

    UObject* ResolveWorldSubsystemContext(UObject* preferredContext)
    {
        if (preferredContext && !preferredContext->IsDefaultObject())
            return preferredContext;

        if (UWorld* world = UWorld::GetWorld())
            return static_cast<UObject*>(world);

        UObject* controller = GetLastOfType(APBPlayerController::StaticClass(), false);
        if (controller) return controller;

        return GetLastOfType(UPBUserWidget::StaticClass(), false);
    }

    UPBFieldModManager* ResolveFieldModManagerFromWorldSubsystem(UObject* contextObject)
    {
        UObject* context = ResolveWorldSubsystemContext(contextObject);
        if (!context) return nullptr;

        auto tryResolve = [&](UClass* managerClass) -> UPBFieldModManager*
        {
            if (!managerClass) return nullptr;

            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                UWorldSubsystem* subsystem = USubsystemBlueprintLibrary::GetWorldSubsystem(
                    context,
                    TSubclassOf<UWorldSubsystem>(managerClass));
                if (subsystem &&
                    !subsystem->IsDefaultObject() &&
                    subsystem->IsA(UPBFieldModManager::StaticClass()))
                {
                    return static_cast<UPBFieldModManager*>(subsystem);
                }
            }
            catch (...) {}
            return nullptr;
        };

        if (UPBFieldModManager* manager = tryResolve(UPBFieldModManager_BP_C::StaticClass()))
            return manager;
        return tryResolve(UPBFieldModManager::StaticClass());
    }

    std::vector<UPBFieldModManager*> CollectFieldModManagers(UObject* contextObject = nullptr)
    {
        std::vector<UPBFieldModManager*> managers;

        AddUniqueFieldModManager(managers, ResolveFieldModManagerFromWorldSubsystem(contextObject));

        for (UObject* object : getObjectsOfClass(UPBFieldModManager_BP_C::StaticClass(), false))
            AddUniqueFieldModManager(managers, object);
        for (UObject* object : getObjectsOfClass(UPBFieldModManager::StaticClass(), false))
            AddUniqueFieldModManager(managers, object);

        return managers;
    }

    bool TryGetFieldModSelectedRoleName(UPBFieldModManager* manager, FName& outRoleName)
    {
        if (!manager || manager->IsDefaultObject() || !manager->Class) return false;

        UFunction* func = manager->Class->GetFunction("PBFieldModManager", "GetSelectCharacterID");
        if (!func) return false;

        Params::PBFieldModManager_GetSelectCharacterID getParms{};
        ScopedClientProcessEventSuppression suppressProcessEventHooks;
        manager->ProcessEvent(func, &getParms);
        outRoleName = getParms.ReturnValue;
        return !IsBlankName(outRoleName);
    }

    bool TryGetFieldModSelectedSlot(UPBFieldModManager* manager, EPBCharacterSlotType& outSlotType)
    {
        if (!manager || manager->IsDefaultObject() || !manager->Class) return false;

        UFunction* func = manager->Class->GetFunction("PBFieldModManager", "GetSelectCharacterSlot");
        if (!func) return false;

        Params::PBFieldModManager_GetSelectCharacterSlot getParms{};
        ScopedClientProcessEventSuppression suppressProcessEventHooks;
        manager->ProcessEvent(func, &getParms);
        outSlotType = getParms.ReturnValue;
        return outSlotType != EPBCharacterSlotType::None;
    }

    void CaptureFieldModSnapshotWeapons(
        UPBFieldModManager* manager,
        const nlohmann::json& snapshot)
    {
        if (!manager || !manager->IsA(UPBFieldModManager_BP_C::StaticClass())) return;
        if (!snapshot.is_object() || !snapshot.contains("roles") || !snapshot["roles"].is_array()) return;

        auto* bpManager = static_cast<UPBFieldModManager_BP_C*>(manager);
        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object()) continue;
            const std::string roleId = role.value("roleId", "");
            if (IsBlankText(roleId)) continue;

            const FName roleName = NameFromString(roleId);
            if (IsBlankName(roleName)) continue;

            for (EPBCharacterSlotType slotType : {
                EPBCharacterSlotType::FirstWeapon,
                EPBCharacterSlotType::SecondWeapon })
            {
                std::string weaponId;
                if (!TryResolveSnapshotItemForSlot(snapshot, roleId, slotType, weaponId)) continue;
                const FName weaponName = NameFromString(weaponId);
                if (IsBlankName(weaponName)) continue;

                try
                {
                    ScopedClientProcessEventSuppression suppressProcessEventHooks;
                    UFunction* func = bpManager->Class
                        ? bpManager->Class->GetFunction("PBFieldModManager_BP_C", "CaptureSpecifyWeapons")
                        : nullptr;
                    if (!func) continue;

                    Params::PBFieldModManager_BP_C_CaptureSpecifyWeapons captureParms{};
                    captureParms.RoleId = roleName;
                    captureParms.WeaponID = weaponName;
                    bpManager->ProcessEvent(func, &captureParms);
                }
                catch (...) {}
            }
        }
    }

    bool CorrectCharacterSlotWidgetFromSnapshot(
        UPBSlotWidget_Character* widget,
        const nlohmann::json& snapshot,
        bool refreshItem)
    {
        if (!widget || widget->IsDefaultObject()) return false;

        const std::string roleId = NameToString(widget->CharacterID);
        if (IsBlankText(roleId) || widget->CharacterSlotType == EPBCharacterSlotType::None)
            return false;

        std::string itemId;
        if (!TryResolveSnapshotItemForSlot(snapshot, roleId, widget->CharacterSlotType, itemId))
            return false;

        const FName itemName = NameFromString(itemId);
        const bool changed = !SameNameValue(widget->EquippedItemID, itemName);
        widget->EquippedItemID = itemName;

        if (refreshItem && changed)
        {
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                widget->RefreshSlot();
            }
            catch (...) {}
            widget->EquippedItemID = itemName;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                widget->K2_OnRefreshSlot();
            }
            catch (...) {}
            widget->EquippedItemID = itemName;
        }
        return changed;
    }

    bool CorrectFieldModSlotWidgetFromSnapshot(
        UPBSlotFieldModWidget_Inventory* widget,
        const nlohmann::json& snapshot,
        bool refreshItem)
    {
        if (!widget || widget->IsDefaultObject()) return false;
        if (widget->SpecifySlotType == EPBCharacterSlotType::None) return false;

        std::string roleId = ResolveQueryRoleId(widget);
        if (IsBlankText(roleId)) roleId = ResolveSingleSnapshotRoleId(snapshot);
        if (IsBlankText(roleId)) return false;

        std::string itemId;
        if (!TryResolveSnapshotItemForSlot(snapshot, roleId, widget->SpecifySlotType, itemId))
            return false;

        const FName itemName = NameFromString(itemId);
        const bool changed =
            !SameNameValue(widget->EquippedItemID, itemName) ||
            !SameNameValue(widget->PreorderItemID, itemName);

        widget->EquippedItemID = itemName;
        widget->PreorderItemID = itemName;

        if (refreshItem && changed)
        {
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                widget->RefreshOnSelectCharacterSlot();
            }
            catch (...) {}
            widget->EquippedItemID = itemName;
            widget->PreorderItemID = itemName;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                widget->K2_OnRefreshSlot();
            }
            catch (...) {}
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                widget->K2_OnSelectCharacterSlot();
            }
            catch (...) {}
            widget->EquippedItemID = itemName;
            widget->PreorderItemID = itemName;
        }
        return changed;
    }

    bool CorrectEditCharacterSlotPanelFromSnapshot(
        UPBPanelCSTM_EditCharacterSlot* panel,
        const nlohmann::json& snapshot,
        bool refreshItem)
    {
        if (!panel || panel->IsDefaultObject()) return false;

        const std::string roleId = NameToString(panel->EditingCharacterID);
        if (IsBlankText(roleId) || panel->EditingCharacterSlot == EPBCharacterSlotType::None)
            return false;

        std::string itemId;
        if (!TryResolveSnapshotItemForSlot(snapshot, roleId, panel->EditingCharacterSlot, itemId))
            return false;

        const FName itemName = NameFromString(itemId);
        const bool changed =
            !SameNameValue(panel->EquippedInventoryID, itemName) ||
            !SameNameValue(panel->PreviewInventoryID, itemName);

        panel->EquippedInventoryID = itemName;
        panel->PreviewInventoryID = itemName;

        if (refreshItem && changed)
        {
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                panel->K2_PreviewInventoryUpdated();
            }
            catch (...) {}
            panel->EquippedInventoryID = itemName;
            panel->PreviewInventoryID = itemName;
        }
        return changed;
    }

    bool ResolveActiveFieldModContext(std::string& outRoleId, EPBCharacterSlotType& outSlotType)
    {
        if (ResolveActiveCustomizeContext(outRoleId, outSlotType)) return true;

        for (UPBFieldModManager* manager : CollectFieldModManagers(nullptr))
        {
            if (!manager) continue;
            try
            {
                FName roleName{};
                EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
                if (!TryGetFieldModSelectedRoleName(manager, roleName)) continue;
                TryGetFieldModSelectedSlot(manager, slotType);
                const std::string roleId = NameToString(roleName);
                if (!IsBlankText(roleId))
                {
                    outRoleId = roleId;
                    outSlotType = slotType;
                    return true;
                }
            }
            catch (...) {}
        }

        return false;
    }

    bool CorrectCstmInventoryListFromSnapshot(
        UPBListCSTM_Inventory* list,
        const nlohmann::json& snapshot)
    {
        if (!list || list->IsDefaultObject()) return false;

        const std::string roleId = NameToString(list->CharacterID);
        if (IsBlankText(roleId) || list->CharacterSlotType == EPBCharacterSlotType::None)
            return false;

        std::string itemId;
        if (!TryResolveSnapshotItemForSlot(snapshot, roleId, list->CharacterSlotType, itemId))
            return false;

        const FName itemName = NameFromString(itemId);
        const bool changed = !SameNameValue(list->EquippedItemID, itemName);
        list->EquippedItemID = itemName;
        if (changed)
        {
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                list->K2_OnRefreshList();
            }
            catch (...) {}
            list->EquippedItemID = itemName;
        }
        return changed;
    }

    bool CorrectFieldModInventoryItemWidgetFromSnapshot(
        UPBItemFieldModWidget_Inventory* item,
        const nlohmann::json& snapshot,
        bool refreshItem)
    {
        if (!item || item->IsDefaultObject()) return false;

        std::string roleId;
        EPBCharacterSlotType contextSlot = EPBCharacterSlotType::None;
        if (!ResolveActiveFieldModContext(roleId, contextSlot))
        {
            roleId = ResolveSingleSnapshotRoleId(snapshot);
            contextSlot = EPBCharacterSlotType::None;
        }
        if (IsBlankText(roleId)) return false;

        const std::string itemId = NameToString(item->ItemId);
        if (IsBlankText(itemId)) return false;

        const EPBCharacterSlotType equippedSlot = FindEquippedSlotForItem(
            snapshot,
            roleId,
            itemId,
            contextSlot);
        const bool isEquipped = equippedSlot != EPBCharacterSlotType::None;
        const bool changed =
            item->bIsPreordering != isEquipped ||
            item->bIsItemLock ||
            item->bIsLocked;

        item->bIsPreordering = isEquipped;
        item->bIsItemLock = false;
        item->bIsLocked = false;

        if (refreshItem && changed)
        {
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                item->RefreshItem();
            }
            catch (...) {}
            item->bIsPreordering = isEquipped;
            item->bIsItemLock = false;
            item->bIsLocked = false;
        }
        return changed;
    }

    void RefreshSlotWidgetsForSnapshot(const nlohmann::json& snapshot, bool refreshItem)
    {
        for (UObject* object : getObjectsOfClass(UPBSlotWidget_Character::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            CorrectCharacterSlotWidgetFromSnapshot(
                static_cast<UPBSlotWidget_Character*>(object),
                snapshot,
                refreshItem);
        }

        for (UObject* object : getObjectsOfClass(UPBSlotFieldModWidget_Inventory::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            CorrectFieldModSlotWidgetFromSnapshot(
                static_cast<UPBSlotFieldModWidget_Inventory*>(object),
                snapshot,
                refreshItem);
        }

        for (UObject* object : getObjectsOfClass(UPBPanelCSTM_EditCharacterSlot::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            CorrectEditCharacterSlotPanelFromSnapshot(
                static_cast<UPBPanelCSTM_EditCharacterSlot*>(object),
                snapshot,
                refreshItem);
        }

        for (UObject* object : getObjectsOfClass(UPBListCSTM_Inventory::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            CorrectCstmInventoryListFromSnapshot(
                static_cast<UPBListCSTM_Inventory*>(object),
                snapshot);
        }

        for (UObject* object : getObjectsOfClass(UPBItemFieldModWidget_Inventory::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            CorrectFieldModInventoryItemWidgetFromSnapshot(
                static_cast<UPBItemFieldModWidget_Inventory*>(object),
                snapshot,
                refreshItem);
        }
    }

    void RefreshInventoryWidgetsForSnapshot(const nlohmann::json& snapshot, bool refreshItem)
    {
        for (UObject* object : getObjectsOfClass(UPBItemCSTM_Inventory::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            CorrectInventoryWidgetFromSnapshot(
                static_cast<UPBItemCSTM_Inventory*>(object),
                snapshot,
                refreshItem);
        }
        RefreshItemDetailWidgetsForSnapshot(snapshot, refreshItem);
        RefreshSlotWidgetsForSnapshot(snapshot, refreshItem);
    }

    template <typename ImplT>
    bool EnsureClientSnapshotLoaded(ImplT& impl, bool force)
    {
        const ULONGLONG now = GetTickCount64();
        if (impl.clientSnapshotLoaded && !force && !impl.clientPlayerId.empty() &&
            now < impl.nextClientPlayerIdResolveMs)
        {
            return true;
        }
        impl.nextClientPlayerIdResolveMs = now + 10000;

        std::string resolvedPlayerId = ResolveClientPlayerId();
        if (resolvedPlayerId.empty() && !impl.clientPlayerId.empty())
        {
            resolvedPlayerId = impl.clientPlayerId;
        }
        if (resolvedPlayerId.empty())
        {
            if (!impl.clientWarnedWaitingPlayerId)
            {
                ClientLog("[LOADOUT] Client waiting for resolved playerId");
                impl.clientWarnedWaitingPlayerId = true;
            }
            return false;
        }

        if (impl.clientPlayerId != resolvedPlayerId)
        {
            if (!impl.clientPlayerId.empty())
            {
                ClientLog("[LOADOUT] Client playerId changed: " + impl.clientPlayerId +
                    " -> " + resolvedPlayerId);
            }
            impl.clientPlayerId = resolvedPlayerId;
            impl.clientSnapshotLoaded = false;
            impl.clientPreviewSnapshot = nlohmann::json();
            impl.clientPreviewActive = false;
            impl.clientWarnedNoSnapshot = false;
            impl.clientWarnedWaitingPlayerId = false;
            impl.clientInventoryCacheSignature.clear();
            impl.nextClientInventoryCachePushMs = 0;
            impl.nextClientShowroomTickMs = 0;
            impl.nextClientWidgetTickMs = 0;
            impl.nextClientPlayerIdResolveMs = 0;
            impl.nextClientFetchAttemptMs = 0;
        }

        if (impl.clientSnapshotLoaded && !force) return true;

        if (!force && now < impl.nextClientFetchAttemptMs) return false;
        impl.nextClientFetchAttemptMs = now + kClientFetchRetryMs;

        EnsureMetaserverConfigured(
            impl.metaserver,
            impl.metaserverChecked,
            impl.metaserverAvailable);

        std::optional<nlohmann::json> payload = impl.metaserver.GetPlayerLoadout(impl.clientPlayerId);
        if (!payload || !payload->is_object())
        {
            if (!impl.clientWarnedNoSnapshot)
            {
                ClientLog("[LOADOUT] Client loadout unavailable: playerId=" + impl.clientPlayerId);
                impl.clientWarnedNoSnapshot = true;
            }
            return false;
        }

        nlohmann::json normalized = NormalizeLoadoutFormat(*payload);
        if (!SnapshotHasRole(normalized))
        {
            ClientLog("[LOADOUT] Client loadout has no roles: playerId=" + impl.clientPlayerId);
            return false;
        }

        impl.clientSnapshot = std::move(normalized);
        impl.clientPreviewSnapshot = nlohmann::json();
        impl.clientSnapshotLoaded = true;
        impl.clientPreviewActive = false;
        impl.clientWarnedNoSnapshot = false;

        ClientLog("[LOADOUT] Client loadout loaded: playerId=" + impl.clientPlayerId +
            " roles=" + std::to_string(impl.clientSnapshot["roles"].size()));
        return true;
    }

    template <typename ImplT>
    const nlohmann::json& GetActiveClientSnapshot(const ImplT& impl)
    {
        return impl.clientPreviewActive ? impl.clientPreviewSnapshot : impl.clientSnapshot;
    }

    bool TryResolveSnapshotItemForSlot(
        const nlohmann::json& snapshot,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        std::string& outItemId)
    {
        const nlohmann::json* role = FindRoleInSnapshotConst(snapshot, roleId);
        if (!role) return false;

        outItemId = GetRoleSlotOrFieldItem(*role, slotType);
        return !IsBlankText(outItemId);
    }

    bool TryResolveSnapshotWeaponConfig(
        const nlohmann::json& snapshot,
        const std::string& roleId,
        const std::string& weaponId,
        FPBWeaponNetworkConfig& outConfig)
    {
        if (IsBlankText(roleId) || IsBlankText(weaponId)) return false;

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig)) return false;

        if (NameToString(roleConfig.FirstWeaponPartData.WeaponID) == weaponId)
        {
            outConfig = roleConfig.FirstWeaponPartData;
            return true;
        }
        if (NameToString(roleConfig.SecondWeaponPartData.WeaponID) == weaponId)
        {
            outConfig = roleConfig.SecondWeaponPartData;
            return true;
        }
        return false;
    }

    bool TryResolveSnapshotWeaponConfigAnyRole(
        const nlohmann::json& snapshot,
        const std::string& weaponId,
        std::string& outRoleId,
        FPBWeaponNetworkConfig& outConfig)
    {
        if (IsBlankText(weaponId) ||
            !snapshot.is_object() ||
            !snapshot.contains("roles") ||
            !snapshot["roles"].is_array())
        {
            return false;
        }

        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object()) continue;

            const std::string roleId = role.value("roleId", "");
            if (IsBlankText(roleId)) continue;

            if (TryResolveSnapshotWeaponConfig(snapshot, roleId, weaponId, outConfig))
            {
                outRoleId = roleId;
                return true;
            }
        }
        return false;
    }

    bool TryResolveSnapshotInventoryConfig(
        const nlohmann::json& snapshot,
        const std::string& roleId,
        FPBInventoryNetworkConfig& outConfig)
    {
        if (IsBlankText(roleId)) return false;

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig)) return false;

        outConfig = roleConfig.InventoryData;
        return outConfig.CharacterSlots.Num() > 0 && outConfig.InventoryItems.Num() > 0;
    }

    int PushClientFieldModCacheToManagers(
        const nlohmann::json& snapshot,
        bool refreshDisplay,
        int* outManagerCount = nullptr)
    {
        if (!snapshot.is_object() || !snapshot.contains("roles") || !snapshot["roles"].is_array())
            return 0;

        int pushed = 0;
        std::vector<UPBFieldModManager*> managers = CollectFieldModManagers(nullptr);
        if (outManagerCount) *outManagerCount = static_cast<int>(managers.size());

        for (UPBFieldModManager* manager : managers)
        {
            if (!manager) continue;
            bool managerChanged = false;

            for (const auto& role : snapshot["roles"])
            {
                if (!role.is_object()) continue;

                const std::string roleId = role.value("roleId", "");
                if (IsBlankText(roleId)) continue;

                FPBInventoryNetworkConfig inventory{};
                if (!TryResolveSnapshotInventoryConfig(snapshot, roleId, inventory)) continue;

                const FName roleName = NameFromString(roleId);
                if (IsBlankName(roleName)) continue;

                if (manager->CharacterPreOrderingInventoryConfigs.IsValid())
                {
                    auto inventoryIt = manager->CharacterPreOrderingInventoryConfigs.Find(roleName, FNameMapEquals);
                    if (inventoryIt != UC::end(manager->CharacterPreOrderingInventoryConfigs))
                    {
                        inventoryIt->Value() = inventory;
                        managerChanged = true;
                    }
                }

                if (manager->CharacterInventoryStatus.IsValid())
                {
                    auto statusIt = manager->CharacterInventoryStatus.Find(roleName, FNameMapEquals);
                    if (statusIt != UC::end(manager->CharacterInventoryStatus) &&
                        statusIt->Value().RoleItemMap.IsValid())
                    {
                        for (auto itemIt = UC::begin(statusIt->Value().RoleItemMap);
                            itemIt != UC::end(statusIt->Value().RoleItemMap);
                            ++itemIt)
                        {
                            const std::string itemId = NameToString(itemIt->Key());
                            const EPBCharacterSlotType equippedSlot = FindEquippedSlotForItem(
                                snapshot,
                                roleId,
                                itemId,
                                EPBCharacterSlotType::None);
                            itemIt->Value().InventoryStatus =
                                equippedSlot == EPBCharacterSlotType::None
                                ? EPBInventoryFieldModStatus::Normal
                                : EPBInventoryFieldModStatus::Equipping;
                            itemIt->Value().InventoryInSlot = equippedSlot;
                        }
                        managerChanged = true;
                    }
                }
            }

            if (managerChanged)
            {
                ++pushed;
                (void)refreshDisplay;
            }
        }
        return pushed;
    }

    APBPlayerState* ResolveClientPlayerState()
    {
        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            auto* playerController = static_cast<APBPlayerController*>(object);
            if (playerController->PBPlayerState) return playerController->PBPlayerState;
            if (playerController->PlayerState &&
                playerController->PlayerState->IsA(APBPlayerState::StaticClass()))
            {
                return static_cast<APBPlayerState*>(playerController->PlayerState);
            }
        }

        for (UObject* object : getObjectsOfClass(APBPlayerState::StaticClass(), false))
        {
            if (!object || object->IsDefaultObject()) continue;
            return static_cast<APBPlayerState*>(object);
        }
        return nullptr;
    }

    int PushClientInventoryCacheToPlayerState(const nlohmann::json& snapshot)
    {
        APBPlayerState* playerState = ResolveClientPlayerState();
        if (!playerState) return 0;
        if (!snapshot.is_object() || !snapshot.contains("roles") || !snapshot["roles"].is_array())
            return 0;

        int pushed = 0;
        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object()) continue;

            const std::string roleId = role.value("roleId", "");
            FPBInventoryNetworkConfig inventory{};
            if (!TryResolveSnapshotInventoryConfig(snapshot, roleId, inventory)) continue;

            const FName roleName = NameFromString(roleId);
            if (IsBlankName(roleName)) continue;

            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                playerState->ClientRefreshRolePreOrderingInventory(roleName, inventory);
                playerState->ClientRefreshRoleEquippingInventory(roleName, inventory);
                ++pushed;
            }
            catch (...) {}
        }
        return pushed;
    }

    template <typename ImplT>
    void PushClientInventoryCacheFromSnapshot(
        ImplT& impl,
        const nlohmann::json& snapshot,
        bool force,
        const std::string& reason)
    {
        if (!snapshot.is_object()) return;

        const ULONGLONG now = GetTickCount64();
        const std::string signature = snapshot.dump();
        const bool signatureChanged = impl.clientInventoryCacheSignature != signature;
        if (!force && !signatureChanged && now < impl.nextClientInventoryCachePushMs)
            return;

        const int pushed = PushClientInventoryCacheToPlayerState(snapshot);
        int fieldModManagerCount = 0;
        int fieldModPushed = 0;
        if (force)
        {
            fieldModPushed = PushClientFieldModCacheToManagers(
                snapshot,
                true,
                &fieldModManagerCount);
        }
        if (pushed > 0 || fieldModPushed > 0 || fieldModManagerCount > 0)
        {
            impl.clientInventoryCacheSignature = signature;
            impl.nextClientInventoryCachePushMs = now + 5000;
            if (force || signatureChanged)
            {
                ClientLog("[LOADOUT] Client native inventory cache pushed: reason=" + reason +
                    " playerStateRoles=" + std::to_string(pushed) +
                    " fieldModManagers=" + std::to_string(fieldModManagerCount) +
                    " fieldModUpdated=" + std::to_string(fieldModPushed));
            }
        }
        else
        {
            impl.nextClientInventoryCachePushMs = now + 1000;
        }
    }

    template <typename ImplT>
    void HandleClientSnapshotInventoryRefreshPre(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms) return;

        if (functionName.find("PBPlayerState.ClientRefreshRoleEquippingInventory") != std::string::npos)
        {
            if (!EnsureClientSnapshotLoaded(impl, false)) return;

            auto* refreshParms = static_cast<Params::PBPlayerState_ClientRefreshRoleEquippingInventory*>(parms);
            FPBInventoryNetworkConfig inventory{};
            if (TryResolveSnapshotInventoryConfig(
                impl.clientSnapshot,
                NameToString(refreshParms->InRoleID),
                inventory))
            {
                refreshParms->InEquippingInventory = inventory;
            }
            return;
        }

        if (functionName.find("PBPlayerState.ClientRefreshRolePreOrderingInventory") != std::string::npos)
        {
            if (!EnsureClientSnapshotLoaded(impl, false)) return;

            auto* refreshParms = static_cast<Params::PBPlayerState_ClientRefreshRolePreOrderingInventory*>(parms);
            FPBInventoryNetworkConfig inventory{};
            if (TryResolveSnapshotInventoryConfig(
                impl.clientSnapshot,
                NameToString(refreshParms->InRoleID),
                inventory))
            {
                refreshParms->InPreOrderingInventory = inventory;
            }
            return;
        }

        if (functionName.find("PBPlayerController.ClientPreOrderUnlockInventory") != std::string::npos)
        {
            if (!EnsureClientSnapshotLoaded(impl, false)) return;

            auto* refreshParms = static_cast<Params::PBPlayerController_ClientPreOrderUnlockInventory*>(parms);
            FPBInventoryNetworkConfig inventory{};
            if (TryResolveSnapshotInventoryConfig(
                impl.clientSnapshot,
                NameToString(refreshParms->InRoleID),
                inventory))
            {
                refreshParms->InPreOrderingInventory = inventory;
            }
            return;
        }
    }

    std::string ResolveFieldModSelectedRoleId(UObject* object)
    {
        UPBFieldModManager* manager = nullptr;
        if (object && object->IsA(UPBFieldModManager::StaticClass()))
            manager = static_cast<UPBFieldModManager*>(object);
        if (!manager)
            manager = ResolveFieldModManagerFromWorldSubsystem(object);

        if (manager)
        {
            try
            {
                FName roleName{};
                if (!TryGetFieldModSelectedRoleName(manager, roleName)) return "";
                const std::string roleId = NameToString(roleName);
                if (!IsBlankText(roleId)) return roleId;
            }
            catch (...) {}
        }
        return "";
    }

    std::string ResolveQueryRoleId(UObject* object)
    {
        std::string roleId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        if (ResolveActiveCustomizeContext(roleId, slotType)) return roleId;

        roleId = ResolveFieldModSelectedRoleId(object);
        if (!IsBlankText(roleId)) return roleId;

        for (UPBFieldModManager* fieldModManager : CollectFieldModManagers(object))
        {
            roleId = ResolveFieldModSelectedRoleId(fieldModManager);
            if (!IsBlankText(roleId)) return roleId;
        }

        return "";
    }

    template <typename ImplT>
    void HandleClientSnapshotQueryPost(
        ImplT& impl,
        UObject* object,
        const std::string& functionName,
        void* parms)
    {
        if (!parms) return;
        if (!EnsureClientSnapshotLoaded(impl, false)) return;

        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);

        if (functionName.find("PBFieldModManager.GetSelectCharacterID") != std::string::npos)
        {
            auto* queryParms = static_cast<Params::PBFieldModManager_GetSelectCharacterID*>(parms);
            const std::string nativeRoleId = NameToString(queryParms->ReturnValue);
            if (IsBlankText(nativeRoleId) || !FindRoleInSnapshotConst(snapshot, nativeRoleId))
            {
                const std::string roleId = ResolveSingleSnapshotRoleId(snapshot);
                if (!IsBlankText(roleId))
                    queryParms->ReturnValue = NameFromString(roleId);
            }
            return;
        }

        if (functionName.find("PBFieldModManager.GetEquippingItemIDInSlotType") != std::string::npos)
        {
            auto* queryParms = static_cast<Params::PBFieldModManager_GetEquippingItemIDInSlotType*>(parms);
            std::string roleId = ResolveQueryRoleId(object);
            if (IsBlankText(roleId)) roleId = ResolveSingleSnapshotRoleId(snapshot);
            std::string itemId;
            if (!TryResolveSnapshotItemForSlot(snapshot, roleId, queryParms->InSlotType, itemId))
            {
                const std::string fallbackRoleId = ResolveSingleSnapshotRoleId(snapshot);
                if (fallbackRoleId != roleId)
                    TryResolveSnapshotItemForSlot(snapshot, fallbackRoleId, queryParms->InSlotType, itemId);
            }
            if (!IsBlankText(itemId))
            {
                queryParms->ReturnValue = NameFromString(itemId);
            }
            return;
        }

        if (functionName.find("PBFieldModManager.GetPreOrderingItemIDInSlotType") != std::string::npos)
        {
            auto* queryParms = static_cast<Params::PBFieldModManager_GetPreOrderingItemIDInSlotType*>(parms);
            std::string roleId = ResolveQueryRoleId(object);
            if (IsBlankText(roleId)) roleId = ResolveSingleSnapshotRoleId(snapshot);
            std::string itemId;
            if (!TryResolveSnapshotItemForSlot(snapshot, roleId, queryParms->InSlotType, itemId))
            {
                const std::string fallbackRoleId = ResolveSingleSnapshotRoleId(snapshot);
                if (fallbackRoleId != roleId)
                    TryResolveSnapshotItemForSlot(snapshot, fallbackRoleId, queryParms->InSlotType, itemId);
            }
            if (!IsBlankText(itemId))
            {
                queryParms->ReturnValue = NameFromString(itemId);
            }
            return;
        }

        if (functionName.find("PBSMShowRoomInstance.GetPreviewInventoryID") != std::string::npos)
        {
            auto* queryParms = static_cast<Params::PBSMShowRoomInstance_GetPreviewInventoryID*>(parms);
            std::string roleId;
            EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
            if (!ResolveActiveCustomizeContext(roleId, slotType)) return;

            std::string itemId;
            if (TryResolvePendingPreview(impl, roleId, slotType, itemId) ||
                TryResolveSnapshotItemForSlot(snapshot, roleId, slotType, itemId))
            {
                queryParms->ReturnValue = NameFromString(itemId);
            }
            return;
        }

        if (functionName.find("PBDisplayCharacter.GetChildByCharacterSlot") != std::string::npos)
        {
            if (!object || !object->IsA(APBDisplayCharacter::StaticClass())) return;
            auto* displayCharacter = static_cast<APBDisplayCharacter*>(object);
            auto* queryParms = static_cast<Params::PBDisplayCharacter_GetChildByCharacterSlot*>(parms);
            const EPBCharacterSlotType slotType = queryParms->CharacterSlot;
            if (slotType != EPBCharacterSlotType::FirstWeapon &&
                slotType != EPBCharacterSlotType::SecondWeapon)
            {
                return;
            }

            std::string roleId = NameToString(displayCharacter->RoleConfig.CharacterID);
            if (IsBlankText(roleId)) roleId = NameToString(displayCharacter->ItemId);
            if (IsBlankText(roleId)) roleId = ResolveSingleSnapshotRoleId(snapshot);

            APBDisplayActor* actor = ApplySnapshotToCharacterSlotActor(
                displayCharacter,
                snapshot,
                roleId,
                slotType,
                false);
            if (actor && actor != queryParms->ReturnValue)
            {
                const std::string oldItem = queryParms->ReturnValue
                    ? NameToString(queryParms->ReturnValue->ItemId)
                    : "null";
                queryParms->ReturnValue = actor;
                ClientLog("[LOADOUT] GetChildByCharacterSlot overridden: role=" + roleId +
                    " slot=" + std::to_string(static_cast<int>(slotType)) +
                    " old=" + oldItem +
                    " new=" + NameToString(actor->ItemId));
            }
            return;
        }

        if (functionName.find("PBCustomizeManager.GetWeaponNetworkConfig") != std::string::npos)
        {
            auto* queryParms = static_cast<Params::PBCustomizeManager_GetWeaponNetworkConfig*>(parms);
            FPBWeaponNetworkConfig weaponConfig{};
            if (TryResolveSnapshotWeaponConfig(
                snapshot,
                NameToString(queryParms->InCharacterID),
                NameToString(queryParms->InWeaponID),
                weaponConfig))
            {
                queryParms->ReturnValue = weaponConfig;
            }
            return;
        }

        if (functionName.find("PBFieldModManager.GetWeaponNetworkConfig") != std::string::npos)
        {
            auto* queryParms = static_cast<Params::PBFieldModManager_GetWeaponNetworkConfig*>(parms);
            std::string roleId = NameToString(queryParms->InRoleID);
            if (IsBlankText(roleId)) roleId = ResolveSingleSnapshotRoleId(snapshot);
            FPBWeaponNetworkConfig weaponConfig{};
            if (TryResolveSnapshotWeaponConfig(
                snapshot,
                roleId,
                NameToString(queryParms->InWeaponID),
                weaponConfig))
            {
                queryParms->ReturnValue = weaponConfig;
            }
            return;
        }

        if (functionName.find("PBPanelCSTM_EditWeaponSlot.GetEquippedWeaponConfig") != std::string::npos ||
            functionName.find("PBPanelCSTM_EditWeaponSlot.GetPreviewWeaponConfig") != std::string::npos)
        {
            if (!object || !object->IsA(UPBPanelCSTM_EditWeaponSlot::StaticClass())) return;

            auto* panel = static_cast<UPBPanelCSTM_EditWeaponSlot*>(object);
            std::string roleId = NameToString(panel->EditingCharacterID);
            std::string weaponId = NameToString(panel->EditingWeaponID);
            if (IsBlankText(roleId)) roleId = ResolveQueryRoleId(object);

            if (IsBlankText(weaponId))
            {
                std::string contextRoleId;
                EPBCharacterSlotType contextSlot = EPBCharacterSlotType::None;
                if (ResolveActiveCustomizeContext(contextRoleId, contextSlot))
                {
                    if (IsBlankText(roleId)) roleId = contextRoleId;
                    TryResolveSnapshotItemForSlot(snapshot, roleId, contextSlot, weaponId);
                }
            }

            FPBWeaponNetworkConfig weaponConfig{};
            if (!TryResolveSnapshotWeaponConfig(snapshot, roleId, weaponId, weaponConfig)) return;

            if (functionName.find("GetEquippedWeaponConfig") != std::string::npos)
            {
                auto* queryParms = static_cast<Params::PBPanelCSTM_EditWeaponSlot_GetEquippedWeaponConfig*>(parms);
                queryParms->ReturnValue = weaponConfig;
            }
            else
            {
                auto* queryParms = static_cast<Params::PBPanelCSTM_EditWeaponSlot_GetPreviewWeaponConfig*>(parms);
                queryParms->ReturnValue = weaponConfig;
            }
            return;
        }
    }

    bool ApplyShowroomSlotFromSnapshot(
        const nlohmann::json& snapshot,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId,
        const std::string& reason)
    {
        const bool isWeaponSlot =
            slotType == EPBCharacterSlotType::FirstWeapon ||
            slotType == EPBCharacterSlotType::SecondWeapon;

        if (isWeaponSlot)
        {
            const bool applied = ApplySnapshotToCharacterSlot(snapshot, roleId, slotType, true);
            ClientLog(std::string("[LOADOUT] Showroom character weapon ") +
                (applied ? "applied" : "apply failed") +
                ": reason=" + reason +
                " role=" + roleId +
                " slot=" + std::to_string(static_cast<int>(slotType)) +
                " item=" + itemId);
            return applied;
        }

        const bool spawned = SpawnInventoryPreview(roleId, itemId, &snapshot);
        ClientLog(std::string("[LOADOUT] Showroom inventory ") +
            (spawned ? "spawned" : "spawn failed") +
            ": reason=" + reason +
            " role=" + roleId +
            " slot=" + std::to_string(static_cast<int>(slotType)) +
            " item=" + itemId);
        return spawned;
    }

    template <typename ImplT>
    void ApplyActiveClientSnapshot(ImplT& impl, bool forceRefresh, const std::string& reason)
    {
        if (!EnsureClientSnapshotLoaded(impl, false)) return;
        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);
        PushClientInventoryCacheFromSnapshot(impl, snapshot, forceRefresh, reason);

        if (forceRefresh)
        {
            RefreshInventoryWidgetsForSnapshot(snapshot, true);
            ApplySnapshotToShowRoom(snapshot, true, reason);

            std::string roleId;
            EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
            if (ResolveActiveCustomizeContext(roleId, slotType))
            {
                std::string itemId;
                if (TryResolveSnapshotItemForSlot(snapshot, roleId, slotType, itemId))
                    ApplyShowroomSlotFromSnapshot(snapshot, roleId, slotType, itemId, reason);
            }
        }
    }

    template <typename ImplT>
    void ApplyPreviewFromInventoryWidget(
        ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        if (!EnsureClientSnapshotLoaded(impl, false)) return;

        impl.clientPreviewSnapshot = impl.clientSnapshot;
        if (!UpdateRoleSlotInSnapshot(impl.clientPreviewSnapshot, roleId, slotType, itemId)) return;
        impl.clientPreviewActive = true;

        RefreshInventoryWidgetsForSlot(roleId, slotType, itemId);
        ApplySnapshotToShowRoom(impl.clientPreviewSnapshot, true, "preview-item");
        ApplyShowroomSlotFromSnapshot(
            impl.clientPreviewSnapshot,
            roleId,
            slotType,
            itemId,
            "preview-item");
    }

    template <typename ImplT>
    bool CommitInventoryWidgetSelection(
        ImplT& impl,
        UPBItemCSTM_Inventory* item,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        if (!EnsureClientSnapshotLoaded(impl, false)) return false;

        const std::string beforeSnapshot = impl.clientSnapshot.dump();
        bool changed = UpdateRoleSlotInSnapshot(impl.clientSnapshot, roleId, slotType, itemId);
        SyncSnapshotSummaryFromInventory(impl.clientSnapshot);
        changed = changed || impl.clientSnapshot.dump() != beforeSnapshot;
        impl.clientPreviewSnapshot = nlohmann::json();
        impl.clientPreviewActive = false;

        PushClientInventoryCacheFromSnapshot(impl, impl.clientSnapshot, true, "equip-item");

        if (item)
        {
            item->bIsEquipped = true;
            item->EquippedSlot = slotType;
            try
            {
                ScopedClientProcessEventSuppression suppressProcessEventHooks;
                item->RefreshItem();
            }
            catch (...) {}
            item->bIsEquipped = true;
            item->EquippedSlot = slotType;
        }
        RefreshInventoryWidgetsForSlot(roleId, slotType, itemId);
        RefreshInventoryWidgetsForSnapshot(impl.clientSnapshot, true);
        ApplySnapshotToShowRoom(impl.clientSnapshot, true, "equip-item");
        ApplyShowroomSlotFromSnapshot(
            impl.clientSnapshot,
            roleId,
            slotType,
            itemId,
            "equip-item");

        if (!changed)
        {
            ClientLog("[LOADOUT] Client loadout already current: playerId=" + impl.clientPlayerId +
                " role=" + roleId + " item=" + itemId +
                " slot=" + std::to_string(static_cast<int>(slotType)));
            return true;
        }

        const nlohmann::json payload = BuildMetaserverPutSnapshot(impl.clientSnapshot);
        if (impl.metaserver.PutPlayerLoadout(impl.clientPlayerId, payload))
        {
            ClientLog("[LOADOUT] Client loadout persisted: playerId=" + impl.clientPlayerId +
                " role=" + roleId + " item=" + itemId);
        }
        else
        {
            ClientLog("[LOADOUT] Client loadout persist failed: playerId=" + impl.clientPlayerId +
                " role=" + roleId + " item=" + itemId);
        }
        return true;
    }

    template <typename ImplT>
    void RememberPendingPreview(
        ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        if (IsBlankText(roleId) || IsBlankText(itemId) || slotType == EPBCharacterSlotType::None) return;
        impl.pendingPreviewRoleId = roleId;
        impl.pendingPreviewSlotType = slotType;
        impl.pendingPreviewItemId = itemId;
        impl.pendingPreviewAtMs = GetTickCount64();
    }

    template <typename ImplT>
    bool TryResolvePendingPreview(
        const ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        std::string& outItemId)
    {
        constexpr ULONGLONG kPendingPreviewWindowMs = 3000;
        const ULONGLONG now = GetTickCount64();
        if ((impl.pendingPreviewRoleId != roleId && !IsBlankText(roleId)) ||
            impl.pendingPreviewSlotType != slotType ||
            IsBlankText(impl.pendingPreviewItemId) ||
            now - impl.pendingPreviewAtMs > kPendingPreviewWindowMs)
        {
            return false;
        }

        outItemId = impl.pendingPreviewItemId;
        return true;
    }

    template <typename ImplT>
    void RememberPendingEquip(
        ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        if (IsBlankText(roleId) || IsBlankText(itemId) || slotType == EPBCharacterSlotType::None) return;
        impl.pendingEquipRoleId = roleId;
        impl.pendingEquipSlotType = slotType;
        impl.pendingEquipItemId = itemId;
        impl.pendingEquipAtMs = GetTickCount64();
    }

    template <typename ImplT>
    bool TryResolvePendingEquip(
        ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        std::string& inOutItemId)
    {
        constexpr ULONGLONG kPendingEquipWindowMs = 5000;
        const ULONGLONG now = GetTickCount64();
        if (impl.pendingEquipRoleId != roleId ||
            impl.pendingEquipSlotType != slotType ||
            IsBlankText(impl.pendingEquipItemId) ||
            now - impl.pendingEquipAtMs > kPendingEquipWindowMs)
        {
            return false;
        }

        if (inOutItemId != impl.pendingEquipItemId)
        {
            if (!IsBlankText(inOutItemId))
            {
                ClientLog("[LOADOUT] Equip callback item overridden by pending request: role=" + roleId +
                    " slot=" + std::to_string(static_cast<int>(slotType)) +
                    " callbackItem=" + inOutItemId +
                    " pendingItem=" + impl.pendingEquipItemId);
            }
            inOutItemId = impl.pendingEquipItemId;
            return true;
        }
        return false;
    }

    template <typename ImplT>
    bool ResolveShowroomSpawnInventoryItem(
        ImplT& impl,
        const std::string& incomingRoleId,
        const std::string& incomingItemId,
        std::string& outRoleId,
        EPBCharacterSlotType& outSlotType,
        std::string& outItemId)
    {
        if (!EnsureClientSnapshotLoaded(impl, false)) return false;

        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);
        std::string roleId = incomingRoleId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;

        std::string contextRoleId;
        EPBCharacterSlotType contextSlotType = EPBCharacterSlotType::None;
        if (ResolveActiveCustomizeContext(contextRoleId, contextSlotType))
        {
            if (IsBlankText(roleId)) roleId = contextRoleId;
            if (roleId == contextRoleId && contextSlotType != EPBCharacterSlotType::None)
            {
                slotType = contextSlotType;
            }
        }

        const std::string singleSnapshotRoleId = ResolveSingleSnapshotRoleId(snapshot);
        if (!IsBlankText(roleId) &&
            !FindRoleInSnapshotConst(snapshot, roleId) &&
            !IsBlankText(singleSnapshotRoleId))
        {
            roleId = singleSnapshotRoleId;
            if (contextRoleId == roleId && contextSlotType != EPBCharacterSlotType::None)
                slotType = contextSlotType;
        }

        constexpr ULONGLONG kPendingSpawnContextMs = 3000;
        const ULONGLONG now = GetTickCount64();
        if (IsBlankText(roleId) &&
            !IsBlankText(impl.pendingPreviewRoleId) &&
            now - impl.pendingPreviewAtMs <= kPendingSpawnContextMs)
        {
            roleId = impl.pendingPreviewRoleId;
        }
        if (slotType == EPBCharacterSlotType::None &&
            roleId == impl.pendingPreviewRoleId &&
            now - impl.pendingPreviewAtMs <= kPendingSpawnContextMs)
        {
            slotType = impl.pendingPreviewSlotType;
        }
        if (slotType == EPBCharacterSlotType::None &&
            roleId == impl.pendingEquipRoleId &&
            now - impl.pendingEquipAtMs <= kPendingSpawnContextMs)
        {
            slotType = impl.pendingEquipSlotType;
        }

        if (slotType == EPBCharacterSlotType::None && !IsBlankText(roleId) && !IsBlankText(incomingItemId))
        {
            slotType = FindEquippedSlotForItem(snapshot, roleId, incomingItemId, EPBCharacterSlotType::None);
        }

        if (IsBlankText(roleId) || slotType == EPBCharacterSlotType::None) return false;

        std::string itemId;
        if (TryResolvePendingPreview(impl, roleId, slotType, itemId) ||
            TryResolvePendingEquip(impl, roleId, slotType, itemId) ||
            TryResolveSnapshotItemForSlot(snapshot, roleId, slotType, itemId))
        {
            if (IsBlankText(itemId)) return false;
            outRoleId = roleId;
            outSlotType = slotType;
            outItemId = itemId;
            return true;
        }

        return false;
    }

    template <typename ImplT>
    void PatchFieldModSpawnWeaponPre(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms) return;
        if (functionName.find("PBFieldModManager.SpawnWeapon") == std::string::npos) return;
        if (!EnsureClientSnapshotLoaded(impl, false)) return;

        auto* spawnParms = static_cast<Params::PBFieldModManager_SpawnWeapon*>(parms);
        const std::string incomingRoleId = NameToString(spawnParms->InRoleID);
        const std::string incomingWeaponId = NameToString(spawnParms->InWeaponID);

        std::string roleId;
        std::string itemId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        if (!ResolveShowroomSpawnInventoryItem(impl, incomingRoleId, incomingWeaponId, roleId, slotType, itemId))
            return;
        if (slotType != EPBCharacterSlotType::FirstWeapon &&
            slotType != EPBCharacterSlotType::SecondWeapon)
            return;
        if (itemId == incomingWeaponId && roleId == incomingRoleId) return;

        spawnParms->InRoleID = NameFromString(roleId);
        spawnParms->InWeaponID = NameFromString(itemId);

        ClientLog("[LOADOUT] FieldMod SpawnWeapon overridden: role=" + roleId +
            " slot=" + std::to_string(static_cast<int>(slotType)) +
            " native=" + incomingWeaponId +
            " payload=" + itemId);
    }

    bool IsShowRoomInventorySpawnFunction(const std::string& functionName)
    {
        return functionName.find("PBShowRoomManager.SpawnInventory") != std::string::npos;
    }

    template <typename ImplT>
    void PatchShowroomSpawnInventoryPre(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms) return;
        if (functionName.find("PBShowRoomManager.SpawnInventorys") != std::string::npos) return;
        if (functionName.find("PBShowRoomManager.SpawnInventory") == std::string::npos) return;

        auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnInventory*>(parms);
        const std::string incomingRoleId = NameToString(spawnParms->InCharacterID);
        const std::string incomingItemId = NameToString(spawnParms->InInventoryID);

        std::string roleId;
        std::string itemId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        if (!ResolveShowroomSpawnInventoryItem(impl, incomingRoleId, incomingItemId, roleId, slotType, itemId))
            return;

        if (itemId == incomingItemId && roleId == incomingRoleId) return;

        spawnParms->InCharacterID = NameFromString(roleId);
        spawnParms->InInventoryID = NameFromString(itemId);

        ClientLog("[LOADOUT] SpawnInventory overridden: role=" + roleId +
            " slot=" + std::to_string(static_cast<int>(slotType)) +
            " native=" + incomingItemId +
            " payload=" + itemId);
    }

    template <typename ImplT>
    bool PatchShowroomSpawnInventoryPost(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms || !IsShowRoomInventorySpawnFunction(functionName)) return false;
        if (!EnsureClientSnapshotLoaded(impl, false)) return true;

        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);

        if (functionName.find("PBShowRoomManager.SpawnInventorys") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnInventorys*>(parms);
            if (spawnParms->ReturnValue.Num() != 1) return true;

            const std::string roleId = NameToString(spawnParms->InCharacterID);
            std::string itemId;
            if (!TryResolveSnapshotItemForSlot(snapshot, roleId, spawnParms->InCharacterSlotType, itemId))
                return true;

            ApplySnapshotToInventoryActor(spawnParms->ReturnValue[0], snapshot, roleId, itemId, true);
            return true;
        }

        auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnInventory*>(parms);
        const std::string incomingRoleId = NameToString(spawnParms->InCharacterID);
        const std::string incomingItemId = NameToString(spawnParms->InInventoryID);

        std::string roleId = incomingRoleId;
        std::string itemId = incomingItemId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        ResolveShowroomSpawnInventoryItem(impl, incomingRoleId, incomingItemId, roleId, slotType, itemId);

        if (!IsBlankText(roleId) && !IsBlankText(itemId))
        {
            const bool changed = ApplySnapshotToInventoryActor(
                spawnParms->ReturnValue,
                snapshot,
                roleId,
                itemId,
                true);
            if (changed)
            {
                ClientLog("[LOADOUT] SpawnInventory actor patched: role=" + roleId +
                    " slot=" + std::to_string(static_cast<int>(slotType)) +
                    " item=" + itemId);
            }
        }
        return true;
    }

    template <typename ImplT>
    bool PatchShowroomCharacterSpawnPost(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms) return false;
        if (functionName.find("PBShowRoomManager.SpawnCharacter") == std::string::npos)
            return false;
        if (!EnsureClientSnapshotLoaded(impl, false)) return true;

        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);

        if (functionName.find("PBShowRoomManager.SpawnCharacters") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnCharacters*>(parms);
            int patched = 0;
            for (int i = 0; i < spawnParms->ReturnValue.Num(); ++i)
            {
                APBDisplayActor* actor = spawnParms->ReturnValue[i];
                if (!actor || !actor->IsA(APBDisplayCharacter::StaticClass())) continue;

                auto* displayCharacter = static_cast<APBDisplayCharacter*>(actor);
                std::string roleId = NameToString(displayCharacter->RoleConfig.CharacterID);
                if (IsBlankText(roleId)) roleId = NameToString(displayCharacter->ItemId);
                if (IsBlankText(roleId) || !FindRoleInSnapshotConst(snapshot, roleId))
                    roleId = ResolveSingleSnapshotRoleId(snapshot);

                APBDisplayCharacter* patchedCharacter = ApplySnapshotToDisplayCharacterActor(
                    displayCharacter,
                    snapshot,
                    roleId,
                    true);
                if (patchedCharacter && patchedCharacter != displayCharacter)
                {
                    spawnParms->ReturnValue[i] = patchedCharacter;
                    ++patched;
                }
            }
            if (patched > 0)
            {
                ClientLog("[LOADOUT] SpawnCharacters return patched from snapshot: count=" +
                    std::to_string(patched));
            }
            return true;
        }

        auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnCharacter*>(parms);
        if (!spawnParms->ReturnValue ||
            !spawnParms->ReturnValue->IsA(APBDisplayCharacter::StaticClass()))
        {
            return true;
        }

        std::string roleId = NameToString(spawnParms->InCharacterID);
        if (IsBlankText(roleId) || !FindRoleInSnapshotConst(snapshot, roleId))
            roleId = ResolveSingleSnapshotRoleId(snapshot);

        auto* displayCharacter = static_cast<APBDisplayCharacter*>(spawnParms->ReturnValue);
        APBDisplayCharacter* patchedCharacter = ApplySnapshotToDisplayCharacterActor(
            displayCharacter,
            snapshot,
            roleId,
            true);
        if (patchedCharacter && patchedCharacter != displayCharacter)
        {
            spawnParms->ReturnValue = patchedCharacter;
            ClientLog("[LOADOUT] SpawnCharacter return patched from snapshot: role=" + roleId +
                " actor=" + patchedCharacter->GetFullName());
        }
        return true;
    }

    template <typename ImplT>
    bool TryResolveShowroomSlotItem(
        ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& incomingItemId,
        std::string& outItemId)
    {
        if (IsBlankText(roleId) || slotType == EPBCharacterSlotType::None) return false;
        if (!EnsureClientSnapshotLoaded(impl, false)) return false;

        std::string itemId = incomingItemId;
        if (TryResolvePendingPreview(impl, roleId, slotType, itemId) ||
            TryResolvePendingEquip(impl, roleId, slotType, itemId) ||
            TryResolveSnapshotItemForSlot(GetActiveClientSnapshot(impl), roleId, slotType, itemId))
        {
            if (IsBlankText(itemId)) return false;
            outItemId = itemId;
            return true;
        }
        return false;
    }

    bool IsShowRoomDirectSpawnFunction(const std::string& functionName)
    {
        return functionName.find("PBShowRoomManager.SpawnWeapon") != std::string::npos ||
            functionName.find("PBShowRoomManager.SpawnMeleeWeapon") != std::string::npos ||
            functionName.find("PBShowRoomManager.SpawnMobility") != std::string::npos ||
            functionName.find("PBShowRoomManager.SpawnPod") != std::string::npos;
    }

    template <typename ImplT>
    void PatchDisplayActorLibrarySpawnPre(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms) return;
        if (functionName.find("PBDisplayActorLibrary.SpawnDisplay") == std::string::npos) return;
        if (!EnsureClientSnapshotLoaded(impl, false)) return;

        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);

        if (functionName.find("PBDisplayActorLibrary.SpawnDisplayCharacter") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBDisplayActorLibrary_SpawnDisplayCharacter*>(parms);
            std::string roleId = NameToString(spawnParms->InCharacterConfig.CharacterID);
            if (IsBlankText(roleId)) roleId = ResolveSingleSnapshotRoleId(snapshot);

            FPBRoleNetworkConfig roleConfig{};
            if (TryResolveRoleConfig(snapshot, roleId, roleConfig))
            {
                const std::string firstWeapon = NameToString(roleConfig.FirstWeaponPartData.WeaponID);
                const std::string nativeFirst = NameToString(spawnParms->InCharacterConfig.FirstWeaponPartData.WeaponID);
                spawnParms->InCharacterConfig = roleConfig;

                if (firstWeapon != nativeFirst)
                {
                    ClientLog("[LOADOUT] SpawnDisplayCharacter config overridden: role=" + roleId +
                        " nativeFirst=" + nativeFirst +
                        " payloadFirst=" + firstWeapon);
                }
            }
            return;
        }

        if (functionName.find("PBDisplayActorLibrary.SpawnDisplayWeapon") != std::string::npos &&
            functionName.find("SpawnDisplayWeaponPart") == std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBDisplayActorLibrary_SpawnDisplayWeapon*>(parms);
            const std::string weaponId = NameToString(spawnParms->InWeaponConfig.WeaponID);

            std::string roleId;
            FPBWeaponNetworkConfig weaponConfig{};
            if (TryResolveSnapshotWeaponConfigAnyRole(snapshot, weaponId, roleId, weaponConfig))
            {
                spawnParms->InWeaponConfig = weaponConfig;
                ClientLog("[LOADOUT] SpawnDisplayWeapon config overridden: role=" + roleId +
                    " weapon=" + weaponId);
            }
            return;
        }
    }

    template <typename ImplT>
    bool PatchDisplayActorLibrarySpawnPost(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms) return false;
        if (functionName.find("PBDisplayActorLibrary.SpawnDisplay") == std::string::npos) return false;
        if (!EnsureClientSnapshotLoaded(impl, false)) return true;

        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);

        if (functionName.find("PBDisplayActorLibrary.SpawnDisplayCharacter") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBDisplayActorLibrary_SpawnDisplayCharacter*>(parms);
            if (spawnParms->ReturnValue)
            {
                std::string roleId = NameToString(spawnParms->InCharacterConfig.CharacterID);
                if (IsBlankText(roleId)) roleId = ResolveSingleSnapshotRoleId(snapshot);

                FPBRoleNetworkConfig roleConfig{};
                if (TryResolveRoleConfig(snapshot, roleId, roleConfig))
                {
                    spawnParms->ReturnValue->RoleConfig = roleConfig;
                    spawnParms->ReturnValue->ItemId = roleConfig.CharacterID;
                }
            }
            return true;
        }

        if (functionName.find("PBDisplayActorLibrary.SpawnDisplayWeapon") != std::string::npos &&
            functionName.find("SpawnDisplayWeaponPart") == std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBDisplayActorLibrary_SpawnDisplayWeapon*>(parms);
            if (spawnParms->ReturnValue)
            {
                spawnParms->ReturnValue->WeaponPartConfig = spawnParms->InWeaponConfig;
                spawnParms->ReturnValue->ItemId = spawnParms->InWeaponConfig.WeaponID;
            }
            return true;
        }

        return false;
    }

    template <typename ImplT>
    void PatchShowroomDirectSpawnPre(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms || !IsShowRoomDirectSpawnFunction(functionName)) return;
        if (functionName.find("SpawnWeaponPart") != std::string::npos ||
            functionName.find("SpawnWeaponParts") != std::string::npos)
        {
            return;
        }

        if (functionName.find("PBShowRoomManager.SpawnWeapon") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnWeapon*>(parms);
            const std::string incomingRoleId = NameToString(spawnParms->InCharacterID);
            const std::string incomingItemId = NameToString(spawnParms->InWeaponID);
            std::string roleId;
            std::string itemId;
            EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
            if (ResolveShowroomSpawnInventoryItem(impl, incomingRoleId, incomingItemId, roleId, slotType, itemId) &&
                itemId != incomingItemId)
            {
                spawnParms->InCharacterID = NameFromString(roleId);
                spawnParms->InWeaponID = NameFromString(itemId);
                ClientLog("[LOADOUT] SpawnWeapon overridden: role=" + roleId +
                    " slot=" + std::to_string(static_cast<int>(slotType)) +
                    " native=" + incomingItemId +
                    " payload=" + itemId);
            }
            return;
        }

        if (functionName.find("PBShowRoomManager.SpawnMeleeWeapon") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnMeleeWeapon*>(parms);
            const std::string roleId = NameToString(spawnParms->InCharacterID);
            const std::string incomingItemId = NameToString(spawnParms->InMeleeWeaponID);
            std::string itemId;
            if (TryResolveShowroomSlotItem(impl, roleId, EPBCharacterSlotType::MeleeWeapon, incomingItemId, itemId) &&
                itemId != incomingItemId)
            {
                spawnParms->InMeleeWeaponID = NameFromString(itemId);
                ClientLog("[LOADOUT] SpawnMeleeWeapon overridden: role=" + roleId +
                    " native=" + incomingItemId +
                    " payload=" + itemId);
            }
            return;
        }

        if (functionName.find("PBShowRoomManager.SpawnMobility") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnMobility*>(parms);
            const std::string roleId = NameToString(spawnParms->InCharacterID);
            const std::string incomingItemId = NameToString(spawnParms->InMobilityID);
            std::string itemId;
            if (TryResolveShowroomSlotItem(impl, roleId, EPBCharacterSlotType::Mobility, incomingItemId, itemId) &&
                itemId != incomingItemId)
            {
                spawnParms->InMobilityID = NameFromString(itemId);
                ClientLog("[LOADOUT] SpawnMobility overridden: role=" + roleId +
                    " native=" + incomingItemId +
                    " payload=" + itemId);
            }
            return;
        }

        if (functionName.find("PBShowRoomManager.SpawnPod") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnPod*>(parms);
            std::string roleId;
            EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
            if (!ResolveActiveCustomizeContext(roleId, slotType)) return;
            if (slotType != EPBCharacterSlotType::LeftPod && slotType != EPBCharacterSlotType::RightPod) return;

            const std::string incomingItemId = NameToString(spawnParms->InPodID);
            std::string itemId;
            if (TryResolveShowroomSlotItem(impl, roleId, slotType, incomingItemId, itemId) &&
                itemId != incomingItemId)
            {
                spawnParms->InPodID = NameFromString(itemId);
                ClientLog("[LOADOUT] SpawnPod overridden: role=" + roleId +
                    " slot=" + std::to_string(static_cast<int>(slotType)) +
                    " native=" + incomingItemId +
                    " payload=" + itemId);
            }
        }
    }

    template <typename ImplT>
    bool PatchShowroomDirectSpawnPost(
        ImplT& impl,
        const std::string& functionName,
        void* parms)
    {
        if (!parms || !IsShowRoomDirectSpawnFunction(functionName)) return false;
        if (functionName.find("SpawnWeaponPart") != std::string::npos ||
            functionName.find("SpawnWeaponParts") != std::string::npos)
        {
            return false;
        }
        if (!EnsureClientSnapshotLoaded(impl, false)) return true;

        const nlohmann::json& snapshot = GetActiveClientSnapshot(impl);

        if (functionName.find("PBShowRoomManager.SpawnWeapon") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnWeapon*>(parms);
            const std::string incomingRoleId = NameToString(spawnParms->InCharacterID);
            const std::string incomingItemId = NameToString(spawnParms->InWeaponID);
            std::string roleId = incomingRoleId;
            std::string itemId = incomingItemId;
            EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
            ResolveShowroomSpawnInventoryItem(impl, incomingRoleId, incomingItemId, roleId, slotType, itemId);
            if (!IsBlankText(roleId) && !IsBlankText(itemId))
            {
                ApplySnapshotToInventoryActor(spawnParms->ReturnValue, snapshot, roleId, itemId, true);
            }
            return true;
        }

        if (functionName.find("PBShowRoomManager.SpawnMeleeWeapon") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnMeleeWeapon*>(parms);
            const std::string roleId = NameToString(spawnParms->InCharacterID);
            const std::string itemId = NameToString(spawnParms->InMeleeWeaponID);
            ApplySnapshotToInventoryActor(spawnParms->ReturnValue, snapshot, roleId, itemId, true);
            return true;
        }

        if (functionName.find("PBShowRoomManager.SpawnMobility") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnMobility*>(parms);
            const std::string roleId = NameToString(spawnParms->InCharacterID);
            const std::string itemId = NameToString(spawnParms->InMobilityID);
            ApplySnapshotToInventoryActor(spawnParms->ReturnValue, snapshot, roleId, itemId, true);
            return true;
        }

        if (functionName.find("PBShowRoomManager.SpawnPod") != std::string::npos)
        {
            auto* spawnParms = static_cast<Params::PBShowRoomManager_SpawnPod*>(parms);
            ApplySnapshotToInventoryActor(spawnParms->ReturnValue, snapshot, "", NameToString(spawnParms->InPodID), true);
            return true;
        }

        return false;
    }

    template <typename ImplT>
    bool IsRecentCommittedEquip(
        const ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        constexpr ULONGLONG kDuplicateCommitWindowMs = 1200;
        const ULONGLONG now = GetTickCount64();
        return impl.lastCommittedRoleId == roleId &&
            impl.lastCommittedSlotType == slotType &&
            impl.lastCommittedItemId == itemId &&
            now - impl.lastCommittedAtMs <= kDuplicateCommitWindowMs;
    }

    template <typename ImplT>
    void MarkCommittedEquip(
        ImplT& impl,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        impl.lastCommittedRoleId = roleId;
        impl.lastCommittedSlotType = slotType;
        impl.lastCommittedItemId = itemId;
        impl.lastCommittedAtMs = GetTickCount64();
    }

    void SwallowClientEquipError(const std::string& functionName, void* parms);

    template <typename ImplT>
    void CommitConfirmedCharacterSlot(
        ImplT& impl,
        UObject* object,
        const std::string& functionName,
        EPBEquipErrorCode originalError,
        FName itemName,
        FName characterName,
        EPBCharacterSlotType slotType)
    {
        if (originalError != EPBEquipErrorCode::NoError &&
            originalError != EPBEquipErrorCode::UnknowError)
        {
            return;
        }

        const std::string roleId = NameToString(characterName);
        std::string itemId = NameToString(itemName);
        if (IsBlankText(roleId) || IsBlankText(itemId) || slotType == EPBCharacterSlotType::None)
        {
            ClientLog("[LOADOUT] Equip callback ignored: fn=" + functionName +
                " role=" + roleId + " item=" + itemId +
                " slot=" + std::to_string(static_cast<int>(slotType)));
            return;
        }

        TryResolvePendingEquip(impl, roleId, slotType, itemId);
        if (IsRecentCommittedEquip(impl, roleId, slotType, itemId))
        {
            return;
        }

        UPBItemCSTM_Inventory* itemWidget = nullptr;
        if (object && object->IsA(UPBItemCSTM_Inventory::StaticClass()))
            itemWidget = static_cast<UPBItemCSTM_Inventory*>(object);

        if (CommitInventoryWidgetSelection(impl, itemWidget, roleId, slotType, itemId))
        {
            ClientLog("[LOADOUT] Equip callback commit: fn=" + functionName +
                " role=" + roleId + " item=" + itemId +
                " slot=" + std::to_string(static_cast<int>(slotType)) +
                " err=" + std::to_string(static_cast<int>(originalError)));
            MarkCommittedEquip(impl, roleId, slotType, itemId);
        }
    }

    template <typename ImplT>
    void HandleClientEquipComplete(
        ImplT& impl,
        UObject* object,
        const std::string& functionName,
        void* parms,
        bool commit)
    {
        if (!parms) return;

        if (functionName.find("PBItemCSTM_Inventory.OnEquipComplete") != std::string::npos)
        {
            auto* equipParms = static_cast<Params::PBItemCSTM_Inventory_OnEquipComplete*>(parms);
            const EPBEquipErrorCode originalError = equipParms->ErrorCode;
            if (equipParms->ErrorCode == EPBEquipErrorCode::UnknowError)
                equipParms->ErrorCode = EPBEquipErrorCode::NoError;
            if (commit)
            {
                CommitConfirmedCharacterSlot(
                    impl, object, functionName, originalError,
                    equipParms->InItemID, equipParms->InCharacterID, equipParms->InCharacterSlotType);
            }
            return;
        }

        if (functionName.find("PBDetailedWeaponDataWidget.OnEquipCharacterSlotComplete") != std::string::npos)
        {
            auto* equipParms = static_cast<Params::PBDetailedWeaponDataWidget_OnEquipCharacterSlotComplete*>(parms);
            const EPBEquipErrorCode originalError = equipParms->ErrorCode;
            if (equipParms->ErrorCode == EPBEquipErrorCode::UnknowError)
                equipParms->ErrorCode = EPBEquipErrorCode::NoError;
            if (commit)
            {
                CommitConfirmedCharacterSlot(
                    impl, object, functionName, originalError,
                    equipParms->InItemID, equipParms->InCharacterID, equipParms->InCharacterSlotType);
            }
            return;
        }

        if (functionName.find("PBPanelCSTM_EditCharacterSlot.OnEquipComplete") != std::string::npos)
        {
            auto* equipParms = static_cast<Params::PBPanelCSTM_EditCharacterSlot_OnEquipComplete*>(parms);
            const EPBEquipErrorCode originalError = equipParms->ErrorCode;
            if (equipParms->ErrorCode == EPBEquipErrorCode::UnknowError)
                equipParms->ErrorCode = EPBEquipErrorCode::NoError;
            if (commit)
            {
                CommitConfirmedCharacterSlot(
                    impl, object, functionName, originalError,
                    equipParms->InItemID, equipParms->InCharacterID, equipParms->InCharacterSlotType);
            }
            return;
        }

        if (functionName.find("PBSlotWidget_Character.OnEquipComplete") != std::string::npos)
        {
            auto* equipParms = static_cast<Params::PBSlotWidget_Character_OnEquipComplete*>(parms);
            const EPBEquipErrorCode originalError = equipParms->ErrorCode;
            if (equipParms->ErrorCode == EPBEquipErrorCode::UnknowError)
                equipParms->ErrorCode = EPBEquipErrorCode::NoError;
            if (commit)
            {
                CommitConfirmedCharacterSlot(
                    impl, object, functionName, originalError,
                    equipParms->InItemID, equipParms->InCharacterID, equipParms->InCharacterSlotType);
            }
            return;
        }

        if (!commit) SwallowClientEquipError(functionName, parms);
    }

    void SwallowClientEquipError(const std::string& functionName, void* parms)
    {
        if (!parms) return;

        if (functionName.find("PBCustomizeWidget.K2_OnEquipComplete") != std::string::npos ||
            functionName.find("PBCustomizeWidget.OnEquipComplete") != std::string::npos)
        {
            auto* equipParms = static_cast<Params::PBCustomizeWidget_K2_OnEquipComplete*>(parms);
            if (equipParms->ErrorCode == EPBEquipErrorCode::UnknowError)
                equipParms->ErrorCode = EPBEquipErrorCode::NoError;
            return;
        }

        if (functionName.find("PBDetailedWeaponDataWidget.OnEquipCharacterSlotComplete") != std::string::npos)
        {
            auto* equipParms = static_cast<Params::PBDetailedWeaponDataWidget_OnEquipCharacterSlotComplete*>(parms);
            if (equipParms->ErrorCode == EPBEquipErrorCode::UnknowError)
                equipParms->ErrorCode = EPBEquipErrorCode::NoError;
            return;
        }

        if (functionName.find("PBItemCSTM_Base.OnEquipItemComplete") != std::string::npos)
        {
            auto* equipParms = static_cast<Params::PBItemCSTM_Base_OnEquipItemComplete*>(parms);
            if (equipParms->InErrorCode == static_cast<int32>(EPBEquipErrorCode::UnknowError))
                equipParms->InErrorCode = static_cast<int32>(EPBEquipErrorCode::NoError);
        }
    }

    bool IsShowRoomRefreshFunction(const std::string& functionName)
    {
        return functionName.find("PBShowRoomManager.SpawnCharacters") != std::string::npos ||
            functionName.find("PBShowRoomManager.SpawnCharacter") != std::string::npos ||
            functionName.find("PBCustomizeWidget.K2_EnterEditCharacter") != std::string::npos ||
            functionName.find("PBCustomizeWidget.K2_EnterEditCharacterSlot") != std::string::npos ||
            functionName.find("K2_EnterEditCharacter") != std::string::npos ||
            functionName.find("K2_EnterEditCharacterSlot") != std::string::npos ||
            functionName.find("K2_OnEquipComplete") != std::string::npos ||
            functionName.find("K2_InRange") != std::string::npos ||
            functionName.find("PBPanelCSTM_EditCharacterSlot.K2_PreviewInventoryUpdated") != std::string::npos ||
            functionName.find("UMG_MainMenuBase_C.Construct") != std::string::npos;
    }

    bool IsClientLoadoutPreFunction(const std::string& functionName)
    {
        return IsShowRoomRefreshFunction(functionName) ||
            functionName.find("PBPlayerState.ClientRefreshRole") != std::string::npos ||
            functionName.find("PBPlayerController.ClientPreOrderUnlockInventory") != std::string::npos ||
            functionName.find("PBItemCSTM_") != std::string::npos ||
            functionName.find("PBItemWidget_Base.") != std::string::npos ||
            functionName.find("PBItemFieldModWidget_Inventory.") != std::string::npos ||
            functionName.find("PBFieldModManager.Select") != std::string::npos ||
            functionName.find("PBFieldModManager.SpawnWeapon") != std::string::npos ||
            functionName.find("PBFieldModManager.K2_CaptureSelectRoleWeapons") != std::string::npos ||
            functionName.find("PBFieldModManager_BP_C.") != std::string::npos ||
            functionName.find("WeaponCaptureActor_C.Capture") != std::string::npos ||
            functionName.find("PBFieldModWidget.") != std::string::npos ||
            functionName.find("PBFieldModInventoryWidget.") != std::string::npos ||
            functionName.find("PBDetailedWeaponDataWidget.OnEquipCharacterSlotComplete") != std::string::npos ||
            functionName.find("PBPanelCSTM_EditCharacterSlot.OnEquipComplete") != std::string::npos ||
            functionName.find("PBSlotWidget_Character.OnEquipComplete") != std::string::npos ||
            functionName.find("PBCustomizeWidget.K2_OnEquipComplete") != std::string::npos ||
            functionName.find("PBCustomizeWidget.OnEquipComplete") != std::string::npos ||
            functionName.find("PBDisplayActorLibrary.SpawnDisplay") != std::string::npos ||
            functionName.find("PBShowRoomManager.Spawn") != std::string::npos ||
            functionName.find("PBCustomizeUIManager.Exit") != std::string::npos ||
            functionName.find("PBCustomizeWidget.K2_ExitEditCharacterSlot") != std::string::npos;
    }

    bool IsClientLoadoutPostFunction(const std::string& functionName)
    {
        return IsShowRoomRefreshFunction(functionName) ||
            functionName.find("PBItemCSTM_") != std::string::npos ||
            functionName.find("PBItemWidget_Base.") != std::string::npos ||
            functionName.find("PBItemFieldModWidget_Inventory.") != std::string::npos ||
            functionName.find("PBDetailedWeaponDataWidget.OnEquipCharacterSlotComplete") != std::string::npos ||
            functionName.find("PBPanelCSTM_EditCharacterSlot.OnEquipComplete") != std::string::npos ||
            functionName.find("PBSlotWidget_Character.OnEquipComplete") != std::string::npos ||
            functionName.find("PBCustomizeWidget.K2_OnEquipComplete") != std::string::npos ||
            functionName.find("PBCustomizeWidget.OnEquipComplete") != std::string::npos ||
            functionName.find("PBFieldModManager.Get") != std::string::npos ||
            functionName.find("PBFieldModManager.Select") != std::string::npos ||
            functionName.find("PBFieldModManager.SpawnWeapon") != std::string::npos ||
            functionName.find("PBFieldModManager.K2_CaptureSelectRoleWeapons") != std::string::npos ||
            functionName.find("PBFieldModManager_BP_C.") != std::string::npos ||
            functionName.find("WeaponCaptureActor_C.Capture") != std::string::npos ||
            functionName.find("PBFieldModWidget.") != std::string::npos ||
            functionName.find("PBFieldModInventoryWidget.") != std::string::npos ||
            functionName.find("PBCustomizeManager.GetWeaponNetworkConfig") != std::string::npos ||
            functionName.find("PBPanelCSTM_EditWeaponSlot.Get") != std::string::npos ||
            functionName.find("PBSlotWidget_Base.") != std::string::npos ||
            functionName.find("PBSlotFieldModWidget_Inventory.") != std::string::npos ||
            functionName.find("PBPanelCSTM_EditCharacterSlot.K2_PreviewInventoryUpdated") != std::string::npos ||
            functionName.find("PBItemDetailWidget.K2_NotifyRefreshItemDetail") != std::string::npos ||
            functionName.find("PBSMShowRoomInstance.GetPreviewInventoryID") != std::string::npos ||
            functionName.find("PBDisplayCharacter.GetChildByCharacterSlot") != std::string::npos ||
            functionName.find("PBDisplayActorLibrary.SpawnDisplay") != std::string::npos ||
            functionName.find("PBShowRoomManager.Spawn") != std::string::npos;
    }

    bool IsFieldModCacheRefreshFunction(const std::string& functionName)
    {
        return functionName.find("PBFieldModManager.SelectCharacter") != std::string::npos ||
            functionName.find("PBFieldModManager.SelectCharacterSlot") != std::string::npos ||
            functionName.find("PBFieldModManager.SelectInventoryItem") != std::string::npos ||
            functionName.find("PBFieldModManager.K2_CaptureSelectRoleWeapons") != std::string::npos ||
            functionName.find("PBFieldModWidget.K2_OnSelectCharacter") != std::string::npos ||
            functionName.find("PBFieldModWidget.K2_OnSelectCharacterSlot") != std::string::npos ||
            functionName.find("PBFieldModInventoryWidget.K2_OpenFieldModMenu") != std::string::npos ||
            functionName.find("PBSlotFieldModWidget_Inventory.K2_OnSelectCharacterSlot") != std::string::npos ||
            functionName.find("PBSlotFieldModWidget_Inventory.RefreshOnSelectCharacterSlot") != std::string::npos ||
            functionName.find("PBItemFieldModWidget_Inventory.SelectItem") != std::string::npos ||
            functionName.find("PBItemFieldModWidget_Inventory.PreOrderItem") != std::string::npos;
    }

    bool IsShowRoomExitFunction(const std::string& functionName)
    {
        return functionName.find("PBCustomizeWidget.K2_ExitEditCharacterSlot") != std::string::npos ||
            functionName.find("PBCustomizeUIManager.ExitEditCharacterSlot") != std::string::npos ||
            functionName.find("PBCustomizeUIManager.ExitCharacterSlotPanel") != std::string::npos;
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
    EnsureClientSnapshotLoaded(*impl_, true);
    ApplyActiveClientSnapshot(*impl_, true, "menu-constructed");
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
    if (playerId.empty())
    {
        ClientLog("[LOADOUT] Role confirmed but playerId is unresolved: player=" +
            playerController->GetFullName() + " role=" + roleIdStr);
        return;
    }
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
    if (!IsClientLoadoutPreFunction(functionName))
    {
        (void)object;
        (void)parms;
        return;
    }

    HandleClientSnapshotInventoryRefreshPre(*impl_, functionName, parms);

    if (IsFieldModCacheRefreshFunction(functionName) &&
        EnsureClientSnapshotLoaded(*impl_, false))
    {
        PushClientInventoryCacheFromSnapshot(*impl_, GetActiveClientSnapshot(*impl_), true, "fieldmod-event-pre");
    }

    if (functionName.find("PBItemCSTM_Base.PreviewItem") != std::string::npos ||
        functionName.find(".PreviewItem") != std::string::npos)
    {
        std::string roleId;
        std::string itemId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        if (TryReadInventoryWidget(object, roleId, slotType, itemId))
        {
            RememberPendingPreview(*impl_, roleId, slotType, itemId);
        }
    }

    if (functionName.find("PBItemCSTM_Base.EquipItem") != std::string::npos ||
        functionName.find(".EquipItem") != std::string::npos)
    {
        std::string roleId;
        std::string itemId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        if (TryReadInventoryWidget(object, roleId, slotType, itemId))
        {
            RememberPendingEquip(*impl_, roleId, slotType, itemId);
        }
    }

    PatchFieldModSpawnWeaponPre(*impl_, functionName, parms);
    PatchShowroomSpawnInventoryPre(*impl_, functionName, parms);
    PatchDisplayActorLibrarySpawnPre(*impl_, functionName, parms);
    PatchShowroomDirectSpawnPre(*impl_, functionName, parms);

    HandleClientEquipComplete(*impl_, object, functionName, parms, false);

    if (IsShowRoomExitFunction(functionName))
    {
        impl_->clientPreviewSnapshot = nlohmann::json();
        impl_->clientPreviewActive = false;
    }

    (void)object;
}

void LoadoutManager::OnClientProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    if (!IsClientLoadoutPostFunction(functionName))
    {
        (void)object;
        (void)parms;
        return;
    }

    HandleClientEquipComplete(*impl_, object, functionName, parms, true);
    HandleClientSnapshotQueryPost(*impl_, object, functionName, parms);

    if (PatchShowroomSpawnInventoryPost(*impl_, functionName, parms))
    {
        return;
    }
    if (PatchShowroomCharacterSpawnPost(*impl_, functionName, parms))
    {
        return;
    }
    if (PatchDisplayActorLibrarySpawnPost(*impl_, functionName, parms))
    {
        return;
    }
    if (PatchShowroomDirectSpawnPost(*impl_, functionName, parms))
    {
        return;
    }

    if (IsFieldModCacheRefreshFunction(functionName) &&
        EnsureClientSnapshotLoaded(*impl_, false))
    {
        const nlohmann::json& snapshot = GetActiveClientSnapshot(*impl_);
        PushClientInventoryCacheFromSnapshot(*impl_, snapshot, true, "fieldmod-event-post");
        RefreshSlotWidgetsForSnapshot(snapshot, true);
        ApplySnapshotToShowRoom(snapshot, true, "fieldmod-event");
    }

    if (functionName.find("PBItemCSTM_Base.PreviewItem") != std::string::npos ||
        functionName.find(".PreviewItem") != std::string::npos)
    {
        std::string roleId;
        std::string itemId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        if (TryReadInventoryWidget(object, roleId, slotType, itemId))
        {
            ApplyPreviewFromInventoryWidget(*impl_, roleId, slotType, itemId);
        }
        return;
    }

    if (functionName.find("PBItemCSTM_Base.EquipItem") != std::string::npos ||
        functionName.find(".EquipItem") != std::string::npos)
    {
        std::string roleId;
        std::string itemId;
        EPBCharacterSlotType slotType = EPBCharacterSlotType::None;
        if (TryReadInventoryWidget(object, roleId, slotType, itemId))
        {
            RememberPendingEquip(*impl_, roleId, slotType, itemId);
            if (IsRecentCommittedEquip(*impl_, roleId, slotType, itemId))
            {
                return;
            }
            if (CommitInventoryWidgetSelection(
                *impl_,
                static_cast<UPBItemCSTM_Inventory*>(object),
                roleId,
                slotType,
                itemId))
            {
                MarkCommittedEquip(*impl_, roleId, slotType, itemId);
            }
        }
        return;
    }

    if (functionName.find("PBItemCSTM_Base.RefreshItem") != std::string::npos ||
        functionName.find("PBItemCSTM_Base.K2_OnRefreshItem") != std::string::npos ||
        functionName.find("PBItemWidget_Base.RefreshItem") != std::string::npos ||
        functionName.find("PBItemWidget_Base.K2_OnRefreshItem") != std::string::npos)
    {
        if (object && EnsureClientSnapshotLoaded(*impl_, false))
        {
            const nlohmann::json& snapshot = GetActiveClientSnapshot(*impl_);
            if (object->IsA(UPBItemCSTM_Inventory::StaticClass()))
            {
                CorrectInventoryWidgetFromSnapshot(
                    static_cast<UPBItemCSTM_Inventory*>(object),
                    snapshot,
                    true);
            }
            else if (object->IsA(UPBItemFieldModWidget_Inventory::StaticClass()))
            {
                CorrectFieldModInventoryItemWidgetFromSnapshot(
                    static_cast<UPBItemFieldModWidget_Inventory*>(object),
                    snapshot,
                    true);
            }
        }
        return;
    }

    if (functionName.find("PBItemDetailWidget.K2_NotifyRefreshItemDetail") != std::string::npos)
    {
        if (object && object->IsA(UPBItemDetailWidget::StaticClass()) &&
            EnsureClientSnapshotLoaded(*impl_, false))
        {
            CorrectItemDetailWidgetFromSnapshot(
                static_cast<UPBItemDetailWidget*>(object),
                GetActiveClientSnapshot(*impl_),
                true);
        }
        return;
    }

    if (functionName.find("PBSlotWidget_Base.RefreshSlot") != std::string::npos ||
        functionName.find("PBSlotWidget_Base.K2_OnRefreshSlot") != std::string::npos ||
        functionName.find("PBSlotFieldModWidget_Inventory.RefreshOnSelectCharacterSlot") != std::string::npos ||
        functionName.find("PBSlotFieldModWidget_Inventory.K2_OnSelectCharacterSlot") != std::string::npos ||
        functionName.find("PBPanelCSTM_EditCharacterSlot.K2_PreviewInventoryUpdated") != std::string::npos)
    {
        if (EnsureClientSnapshotLoaded(*impl_, false))
        {
            const nlohmann::json& snapshot = GetActiveClientSnapshot(*impl_);
            if (object && object->IsA(UPBSlotWidget_Character::StaticClass()))
            {
                CorrectCharacterSlotWidgetFromSnapshot(
                    static_cast<UPBSlotWidget_Character*>(object),
                    snapshot,
                    true);
            }
            else if (object && object->IsA(UPBSlotFieldModWidget_Inventory::StaticClass()))
            {
                CorrectFieldModSlotWidgetFromSnapshot(
                    static_cast<UPBSlotFieldModWidget_Inventory*>(object),
                    snapshot,
                    true);
            }
            else if (object && object->IsA(UPBPanelCSTM_EditCharacterSlot::StaticClass()))
            {
                CorrectEditCharacterSlotPanelFromSnapshot(
                    static_cast<UPBPanelCSTM_EditCharacterSlot*>(object),
                    snapshot,
                    true);
            }
            else
            {
                RefreshSlotWidgetsForSnapshot(snapshot, true);
            }
        }
        return;
    }

    if (IsShowRoomRefreshFunction(functionName))
    {
        EnsureClientSnapshotLoaded(*impl_, false);
        ApplyActiveClientSnapshot(*impl_, true, "process-event:" + functionName);
    }
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
    if (!EnsureClientSnapshotLoaded(*impl_, false)) return;

    PushClientInventoryCacheFromSnapshot(*impl_, impl_->clientSnapshot, false, "client-tick");
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
