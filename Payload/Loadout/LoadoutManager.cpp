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
    ULONGLONG nextClientFetchAttemptMs = 0;
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

    constexpr ULONGLONG kClientFetchRetryMs = 5000;

    std::string ResolveClientPlayerId()
    {
        std::string playerId = TrimAscii(GetCmdValue("-ProjectReboundPlayerId="));
        if (playerId.empty()) playerId = GetEnvValue("PROJECT_REBOUND_PLAYER_ID");
        return LooksLikePlayerId(playerId) ? playerId : kFallbackPlayerId;
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

    template <typename ImplT>
    bool EnsureClientSnapshotLoaded(ImplT& impl, bool force)
    {
        if (impl.clientSnapshotLoaded && !force) return true;

        const ULONGLONG now = GetTickCount64();
        if (!force && now < impl.nextClientFetchAttemptMs) return false;
        impl.nextClientFetchAttemptMs = now + kClientFetchRetryMs;

        EnsureMetaserverConfigured(
            impl.metaserver,
            impl.metaserverChecked,
            impl.metaserverAvailable);

        impl.clientPlayerId = ResolveClientPlayerId();
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

    template <typename ImplT>
    void ApplyActiveClientSnapshot(ImplT& impl, bool forceRefresh, const std::string& reason)
    {
        if (!EnsureClientSnapshotLoaded(impl, false)) return;
        ApplySnapshotToShowRoom(GetActiveClientSnapshot(impl), forceRefresh, reason);
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

        ApplySnapshotToShowRoom(impl.clientPreviewSnapshot, true, "preview-item");
        SpawnInventoryPreview(roleId, itemId);
    }

    template <typename ImplT>
    void CommitInventoryWidgetSelection(
        ImplT& impl,
        UPBItemCSTM_Inventory* item,
        const std::string& roleId,
        EPBCharacterSlotType slotType,
        const std::string& itemId)
    {
        if (!EnsureClientSnapshotLoaded(impl, false)) return;

        const bool changed = UpdateRoleSlotInSnapshot(impl.clientSnapshot, roleId, slotType, itemId);
        impl.clientPreviewSnapshot = nlohmann::json();
        impl.clientPreviewActive = false;

        ApplySnapshotToShowRoom(impl.clientSnapshot, true, "equip-item");
        SpawnInventoryPreview(roleId, itemId);

        if (item)
        {
            item->bIsEquipped = true;
            try { item->RefreshItem(); }
            catch (...) {}
            item->bIsEquipped = true;
        }

        if (!changed) return;

        if (impl.metaserver.PutPlayerLoadout(impl.clientPlayerId, impl.clientSnapshot))
        {
            ClientLog("[LOADOUT] Client loadout persisted: playerId=" + impl.clientPlayerId +
                " role=" + roleId + " item=" + itemId);
        }
        else
        {
            ClientLog("[LOADOUT] Client loadout persist failed: playerId=" + impl.clientPlayerId +
                " role=" + roleId + " item=" + itemId);
        }
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
            functionName.find("PBShowRoomManager.SpawnInventory") != std::string::npos ||
            functionName.find("PBCustomizeWidget.K2_EnterEditCharacter") != std::string::npos ||
            functionName.find("PBCustomizeWidget.K2_EnterEditCharacterSlot") != std::string::npos ||
            functionName.find("PBPanelCSTM_EditCharacterSlot.K2_PreviewInventoryUpdated") != std::string::npos ||
            functionName.find("UMG_Customize") != std::string::npos ||
            functionName.find("UMG_MainMenuBase_C.Construct") != std::string::npos;
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
    SwallowClientEquipError(functionName, parms);

    if (IsShowRoomExitFunction(functionName))
    {
        impl_->clientPreviewSnapshot = nlohmann::json();
        impl_->clientPreviewActive = false;
    }

    (void)object;
}

void LoadoutManager::OnClientProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    (void)parms;

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
            CommitInventoryWidgetSelection(
                *impl_,
                static_cast<UPBItemCSTM_Inventory*>(object),
                roleId,
                slotType,
                itemId);
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
    ApplyActiveClientSnapshot(*impl_, false, "client-tick");
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
