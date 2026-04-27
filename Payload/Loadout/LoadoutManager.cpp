// ======================================================
//  LoadoutManager — 装备快照/导出/运行时门面
// ======================================================
//
//  重写版本 — 基于 SDK 原生 API，不再使用 ProcessEvent 拦截
//  或 SEH 守卫的裸 TMap 操作。
//
//  流程：
//    1. 启动时从磁盘预加载 sidecar JSON 快照
//    2. 监听菜单装备变化，导出最新快照
//    3. 角色确认后通过 ServerPreOrderInventory RPC 推送库存
//    4. 角色生成后通过直接属性赋值应用武器配装

#include "LoadoutManager.h"

#include <Windows.h>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <mutex>
#include <sstream>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Libs/json.hpp"

using namespace SDK;

std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
UObject* GetLastOfType(UClass* theClass, bool includeDefault);
#include "../Debug/Debug.h"

extern bool LoginCompleted;
extern bool amServer;

class LoadoutManager::Impl
{
};

namespace LoadoutManagerDetail
{
    using json = nlohmann::json;

    // =====================================================================
    //  待处理角色选择上下文
    // =====================================================================

    struct PendingRoleSelectionContext
    {
        std::string RoleId;
        bool IsAuthoritative = false;
        std::chrono::steady_clock::time_point ConfirmedAt{};
    };

    // =====================================================================
    //  全局状态 — 精简版（从 ~21 字段缩减到 ~10 字段）
    // =====================================================================

    struct State
    {
        std::mutex Mutex;
        json Snapshot;
        bool SnapshotAvailable = false;
        bool SnapshotLoadAttempted = false;
        bool PendingLiveApply = false;
        bool ExportScheduled = false;
        bool ClientApplyComplete = false;
        bool InitialCaptureComplete = false;
        std::chrono::steady_clock::time_point ExportDueAt{};
        std::chrono::steady_clock::time_point LastCaptureAt{};
        std::string PendingExportReason;
        std::string LastMenuSelectedRoleId;
        std::unordered_map<APBPlayerController*, PendingRoleSelectionContext> PendingRoleSelections;
    };

    State& GetState()
    {
        static State StateInstance;
        return StateInstance;
    }

    // =====================================================================
    //  前向声明
    // =====================================================================

    // (放在这里避免循环依赖，实现在下方)
    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig);
    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config);
    bool HasLauncherConfig(const FPBLauncherNetworkConfig& config);
    bool HasMeleeConfig(const FPBMeleeWeaponNetworkConfig& config);
    bool HasMobilityConfig(const FPBMobilityModuleNetworkConfig& config);
    std::string ResolveCharacterRoleId(APBCharacter* character);
    std::string ResolveLiveCharacterRoleId(APBCharacter* character);

    // =====================================================================
    //  基础工具 — 字符串转换 / 空值判断
    // =====================================================================

    std::wstring ToWide(const std::string& value)
    {
        return std::wstring(value.begin(), value.end());
    }

    std::string NameToString(const FName& value)
    {
        if (value.ComparisonIndex <= 0)
        {
            return "None";
        }

        if (value.ComparisonIndex > 10000000 || value.Number < 0 || value.Number > 1000000)
        {
            return "None";
        }

        const std::string text = value.ToString();
        return text.empty() ? "None" : text;
    }

    bool IsBlankText(const std::string& text)
    {
        return text.empty() || text == "None";
    }

    bool IsBlankName(const FName& value)
    {
        return value.ComparisonIndex <= 0;
    }

    std::string NormalizeRoleId(const std::string& roleId)
    {
        return IsBlankText(roleId) ? std::string() : roleId;
    }

    FName NameFromString(const std::string& value)
    {
        if (value.empty())
        {
            return FName();
        }

        const std::wstring wideValue = ToWide(value);
        return UKismetStringLibrary::Conv_StringToName(wideValue.c_str());
    }

    // =====================================================================
    //  角色选择记忆
    // =====================================================================

    void RememberMenuSelectedRole(const std::string& roleId)
    {
        const std::string normalizedRoleId = NormalizeRoleId(roleId);
        if (normalizedRoleId.empty())
        {
            return;
        }

        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.LastMenuSelectedRoleId = normalizedRoleId;
    }

    void RememberMenuSelectedRole(const FName& roleId)
    {
        if (IsBlankName(roleId))
        {
            return;
        }

        RememberMenuSelectedRole(NameToString(roleId));
    }

    std::string GetRememberedMenuSelectedRoleId()
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.LastMenuSelectedRoleId;
    }

    void RememberPendingRoleSelection(APBPlayerController* playerController, const std::string& roleId, bool isAuthoritative)
    {
        const std::string normalizedRoleId = NormalizeRoleId(roleId);
        if (!playerController || normalizedRoleId.empty())
        {
            return;
        }

        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        PendingRoleSelectionContext& context = state.PendingRoleSelections[playerController];
        context.RoleId = normalizedRoleId;
        context.IsAuthoritative = isAuthoritative;
        context.ConfirmedAt = std::chrono::steady_clock::now();
    }

    std::string GetPendingRoleIdForController(APBPlayerController* playerController)
    {
        if (!playerController)
        {
            return "";
        }

        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        auto existing = state.PendingRoleSelections.find(playerController);
        if (existing == state.PendingRoleSelections.end())
        {
            return "";
        }

        return existing->second.RoleId;
    }

    // =====================================================================
    //  路径 — 快照文件路径
    // =====================================================================

    std::filesystem::path GetAppDataRoot()
    {
        char* appData = nullptr;
        size_t appDataLength = 0;
        if (_dupenv_s(&appData, &appDataLength, "APPDATA") == 0 && appData && *appData)
        {
            const std::filesystem::path result = std::filesystem::path(appData) / "ProjectReboundBrowser";
            free(appData);
            return result;
        }

        free(appData);
        return std::filesystem::current_path() / "ProjectReboundBrowser";
    }

    std::filesystem::path GetExportSnapshotPath()
    {
        return GetAppDataRoot() / "loadout-export-v1.json";
    }

    std::filesystem::path GetLaunchSnapshotPath()
    {
        return GetAppDataRoot() / "launchers" / "loadout-launch-v1.json";
    }

    std::filesystem::path GetCustomLoadoutPath()
    {
        return GetAppDataRoot() / "custom-loadout-v1.json";
    }

    // =====================================================================
    //  运行时查询 — 对象查找 / 玩家角色
    // =====================================================================

    std::string BuildUtcTimestamp()
    {
        const auto now = std::chrono::system_clock::now();
        const std::time_t timeValue = std::chrono::system_clock::to_time_t(now);

        std::tm utcTime{};
        gmtime_s(&utcTime, &timeValue);

        std::ostringstream stream;
        stream << std::put_time(&utcTime, "%Y-%m-%dT%H:%M:%SZ");
        return stream.str();
    }

    UPBFieldModManager* GetFieldModManager()
    {
        UObject* object = GetLastOfType(UPBFieldModManager::StaticClass(), false);
        return object ? static_cast<UPBFieldModManager*>(object) : nullptr;
    }

    APBPlayerController* GetLocalPlayerController()
    {
        UWorld* world = UWorld::GetWorld();
        if (!world || !world->OwningGameInstance)
        {
            return nullptr;
        }

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            if (playerController && playerController->PBGameInstance == world->OwningGameInstance)
            {
                return playerController;
            }
        }

        return nullptr;
    }

    APBCharacter* GetLocalCharacter()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (playerController && playerController->PBCharacter)
        {
            return playerController->PBCharacter;
        }

        return nullptr;
    }

    std::string GetLocalPlayerPreferredRoleId()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (!playerController)
        {
            return "";
        }

        APBPlayerState* playerState = playerController->PBPlayerState;
        if (playerState)
        {
            if (!IsBlankName(playerState->SelectedCharacterID))
            {
                return NameToString(playerState->SelectedCharacterID);
            }

            if (!IsBlankName(playerState->UsageCharacterID))
            {
                return NameToString(playerState->UsageCharacterID);
            }

            if (!IsBlankName(playerState->PossessedCharacterId))
            {
                return NameToString(playerState->PossessedCharacterId);
            }
        }

        if (playerController->PBCharacter)
        {
            const std::string roleId = ResolveCharacterRoleId(playerController->PBCharacter);
            if (!roleId.empty())
            {
                return roleId;
            }
        }

        return "";
    }

    APBCharacter* GetControllerCharacter(APBPlayerController* playerController)
    {
        if (!playerController)
        {
            return nullptr;
        }

        if (playerController->PBCharacter)
        {
            return playerController->PBCharacter;
        }

        if (playerController->Pawn && playerController->Pawn->IsA(APBCharacter::StaticClass()))
        {
            return static_cast<APBCharacter*>(playerController->Pawn);
        }

        return nullptr;
    }

    APBPlayerController* FindPlayerControllerForCharacter(APBCharacter* character)
    {
        if (!character)
        {
            return nullptr;
        }

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            if (GetControllerCharacter(playerController) == character)
            {
                return playerController;
            }
        }

        return nullptr;
    }

    bool IsCharacterAlive(APBCharacter* character)
    {
        if (!character)
        {
            return false;
        }

        try
        {
            return character->IsAlive();
        }
        catch (...)
        {
            return false;
        }
    }

    // =====================================================================
    //  JSON 序列化 — 网络配置 ↔ JSON 互转
    // =====================================================================

    json EmptyInventoryJson()
    {
        return json{
            { "slots", json::array() }
        };
    }

    json EmptyWeaponJson()
    {
        return json{
            { "weaponId", "" },
            { "weaponClassId", "" },
            { "ornamentId", "" },
            { "parts", json::array() }
        };
    }

    json EmptyCharacterJson()
    {
        return json{
            { "skinClassArray", json::array() },
            { "skinIdArray", json::array() },
            { "skinPaintingId", "" }
        };
    }

    json EmptyLauncherJson()
    {
        return json{
            { "id", "" },
            { "skinId", "" }
        };
    }

    json EmptyMeleeJson()
    {
        return json{
            { "id", "" },
            { "skinId", "" }
        };
    }

    json EmptyMobilityJson()
    {
        return json{
            { "mobilityModuleId", "" }
        };
    }

    json EmptyRoleJson(const std::string& roleId)
    {
        return json{
            { "roleId", roleId },
            { "inventory", EmptyInventoryJson() },
            { "characterData", EmptyCharacterJson() },
            { "firstWeapon", EmptyWeaponJson() },
            { "secondWeapon", EmptyWeaponJson() },
            { "meleeWeapon", EmptyMeleeJson() },
            { "leftLauncher", EmptyLauncherJson() },
            { "rightLauncher", EmptyLauncherJson() },
            { "mobilityModule", EmptyMobilityJson() }
        };
    }

    bool ReadJsonFile(const std::filesystem::path& path, json& outJson)
    {
        try
        {
            if (!std::filesystem::exists(path))
            {
                return false;
            }

            std::ifstream file(path);
            if (!file.is_open())
            {
                return false;
            }

            outJson = json::parse(file, nullptr, false);
            return !outJson.is_discarded() && outJson.is_object();
        }
        catch (...)
        {
            return false;
        }
    }

    bool WriteJsonFile(const std::filesystem::path& path, const json& value)
    {
        try
        {
            std::filesystem::create_directories(path.parent_path());
            std::ofstream file(path, std::ios::trunc);
            if (!file.is_open())
            {
                return false;
            }

            file << value.dump(2);
            return true;
        }
        catch (...)
        {
            return false;
        }
    }

    json WeaponPartToJson(const FPBWeaponPartNetworkConfig& config, EPBPartSlotType slotType)
    {
        return json{
            { "slotType", static_cast<int>(slotType) },
            { "weaponPartId", NameToString(config.WeaponPartID) },
            { "weaponPartSkinId", NameToString(config.WeaponPartSkinID) },
            { "weaponPartSpecialSkinId", NameToString(config.WeaponPartSpecialSkinID) },
            { "weaponPartSkinPaintingId", NameToString(config.WeaponPartSkinPaintingID) }
        };
    }

    json WeaponToJson(const FPBWeaponNetworkConfig& config)
    {
        json parts = json::array();
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int index = 0; index < count; ++index)
        {
            parts.push_back(WeaponPartToJson(config.WeaponPartConfigs[index], config.WeaponPartSlotTypeArray[index]));
        }

        return json{
            { "weaponId", NameToString(config.WeaponID) },
            { "weaponClassId", NameToString(config.WeaponClassID) },
            { "ornamentId", NameToString(config.OrnamentID) },
            { "parts", parts }
        };
    }

    json CharacterToJson(const FPBCharacterNetworkConfig& config)
    {
        json skinClasses = json::array();
        json skinIds = json::array();

        for (int index = 0; index < config.SkinClassArray.Num(); ++index)
        {
            skinClasses.push_back(static_cast<int>(config.SkinClassArray[index]));
        }

        for (int index = 0; index < config.SkinIDArray.Num(); ++index)
        {
            skinIds.push_back(NameToString(config.SkinIDArray[index]));
        }

        return json{
            { "skinClassArray", skinClasses },
            { "skinIdArray", skinIds },
            { "skinPaintingId", NameToString(config.SkinPaintingID) }
        };
    }

    json LauncherToJson(const FPBLauncherNetworkConfig& config)
    {
        return json{
            { "id", NameToString(config.ID) },
            { "skinId", NameToString(config.SkinID) }
        };
    }

    json MeleeToJson(const FPBMeleeWeaponNetworkConfig& config)
    {
        return json{
            { "id", NameToString(config.ID) },
            { "skinId", NameToString(config.SkinID) }
        };
    }

    json MobilityToJson(const FPBMobilityModuleNetworkConfig& config)
    {
        return json{
            { "mobilityModuleId", NameToString(config.MobilityModuleID) }
        };
    }

    json InventoryToJson(const FPBInventoryNetworkConfig& config)
    {
        json slots = json::array();
        const int count = (std::min)(config.CharacterSlots.Num(), config.InventoryItems.Num());
        for (int index = 0; index < count; ++index)
        {
            slots.push_back(json{
                { "slotType", static_cast<int>(config.CharacterSlots[index]) },
                { "itemId", NameToString(config.InventoryItems[index]) }
            });
        }

        return json{
            { "slots", slots }
        };
    }

    bool InventoryFromJson(const json& value, FPBInventoryNetworkConfig& outConfig)
    {
        outConfig.CharacterSlots.Clear();
        outConfig.InventoryItems.Clear();

        const json* slots = value.is_object() && value.contains("slots") ? &value["slots"] : nullptr;
        if (!slots || !slots->is_array())
        {
            return true;
        }

        for (const auto& entry : *slots)
        {
            if (!entry.is_object())
            {
                continue;
            }

            outConfig.CharacterSlots.Add(static_cast<EPBCharacterSlotType>(entry.value("slotType", 0)));
            outConfig.InventoryItems.Add(NameFromString(entry.value("itemId", "")));
        }

        return true;
    }

    bool CharacterFromJson(const json& value, FPBCharacterNetworkConfig& outConfig)
    {
        outConfig.SkinClassArray.Clear();
        outConfig.SkinIDArray.Clear();
        outConfig.SkinPaintingID = NameFromString(value.value("skinPaintingId", ""));

        if (value.contains("skinClassArray") && value["skinClassArray"].is_array())
        {
            for (const auto& skinClass : value["skinClassArray"])
            {
                outConfig.SkinClassArray.Add(static_cast<EPBSkinClass>(skinClass.get<int>()));
            }
        }

        if (value.contains("skinIdArray") && value["skinIdArray"].is_array())
        {
            for (const auto& skinId : value["skinIdArray"])
            {
                outConfig.SkinIDArray.Add(NameFromString(skinId.get<std::string>()));
            }
        }

        return true;
    }

    bool WeaponFromJson(const json& value, FPBWeaponNetworkConfig& outConfig)
    {
        outConfig.WeaponPartSlotTypeArray.Clear();
        outConfig.WeaponPartConfigs.Clear();
        outConfig.OrnamentID = NameFromString(value.value("ornamentId", ""));
        outConfig.WeaponID = NameFromString(value.value("weaponId", ""));
        outConfig.WeaponClassID = NameFromString(value.value("weaponClassId", ""));

        if (value.contains("parts") && value["parts"].is_array())
        {
            for (const auto& entry : value["parts"])
            {
                if (!entry.is_object())
                {
                    continue;
                }

                FPBWeaponPartNetworkConfig partConfig{};
                partConfig.WeaponPartID = NameFromString(entry.value("weaponPartId", ""));
                partConfig.WeaponPartSkinID = NameFromString(entry.value("weaponPartSkinId", ""));
                partConfig.WeaponPartSpecialSkinID = NameFromString(entry.value("weaponPartSpecialSkinId", ""));
                partConfig.WeaponPartSkinPaintingID = NameFromString(entry.value("weaponPartSkinPaintingId", ""));

                outConfig.WeaponPartSlotTypeArray.Add(static_cast<EPBPartSlotType>(entry.value("slotType", 0)));
                outConfig.WeaponPartConfigs.Add(partConfig);
            }
        }

        return true;
    }

    bool LauncherFromJson(const json& value, FPBLauncherNetworkConfig& outConfig)
    {
        outConfig.ID = NameFromString(value.value("id", ""));
        outConfig.SkinID = NameFromString(value.value("skinId", ""));
        return true;
    }

    bool MeleeFromJson(const json& value, FPBMeleeWeaponNetworkConfig& outConfig)
    {
        outConfig.ID = NameFromString(value.value("id", ""));
        outConfig.SkinID = NameFromString(value.value("skinId", ""));
        return true;
    }

    bool MobilityFromJson(const json& value, FPBMobilityModuleNetworkConfig& outConfig)
    {
        outConfig.MobilityModuleID = NameFromString(value.value("mobilityModuleId", ""));
        return true;
    }

    json RoleToJson(const FPBRoleNetworkConfig& config, const FPBInventoryNetworkConfig* inventoryOverride)
    {
        return json{
            { "roleId", NameToString(config.CharacterID) },
            { "inventory", InventoryToJson(inventoryOverride ? *inventoryOverride : config.InventoryData) },
            { "characterData", CharacterToJson(config.CharacterData) },
            { "firstWeapon", WeaponToJson(config.FirstWeaponPartData) },
            { "secondWeapon", WeaponToJson(config.SecondWeaponPartData) },
            { "meleeWeapon", MeleeToJson(config.MeleeWeaponData) },
            { "leftLauncher", LauncherToJson(config.LeftLauncherData) },
            { "rightLauncher", LauncherToJson(config.RightLauncherData) },
            { "mobilityModule", MobilityToJson(config.MobilityModuleData) }
        };
    }

    // =====================================================================
    //  配置存在性判断
    // =====================================================================

    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config)
    {
        return !IsBlankName(config.WeaponID) || !IsBlankName(config.WeaponClassID) || !IsBlankName(config.OrnamentID) || config.WeaponPartConfigs.Num() > 0;
    }

    bool HasLauncherConfig(const FPBLauncherNetworkConfig& config)
    {
        return !IsBlankName(config.ID) || !IsBlankName(config.SkinID);
    }

    bool HasMeleeConfig(const FPBMeleeWeaponNetworkConfig& config)
    {
        return !IsBlankName(config.ID) || !IsBlankName(config.SkinID);
    }

    bool HasMobilityConfig(const FPBMobilityModuleNetworkConfig& config)
    {
        return !IsBlankName(config.MobilityModuleID);
    }

    bool HasWeaponPartConfigData(const FPBWeaponPartNetworkConfig& config)
    {
        return !IsBlankName(config.WeaponPartID) ||
            !IsBlankName(config.WeaponPartSkinID) ||
            !IsBlankName(config.WeaponPartSpecialSkinID) ||
            !IsBlankName(config.WeaponPartSkinPaintingID);
    }

    bool WeaponPartConfigEquals(const FPBWeaponPartNetworkConfig& a, const FPBWeaponPartNetworkConfig& b)
    {
        return NameToString(a.WeaponPartID) == NameToString(b.WeaponPartID) &&
            NameToString(a.WeaponPartSkinID) == NameToString(b.WeaponPartSkinID) &&
            NameToString(a.WeaponPartSpecialSkinID) == NameToString(b.WeaponPartSpecialSkinID) &&
            NameToString(a.WeaponPartSkinPaintingID) == NameToString(b.WeaponPartSkinPaintingID);
    }

    bool WeaponConfigEquals(const FPBWeaponNetworkConfig& a, const FPBWeaponNetworkConfig& b)
    {
        if (NameToString(a.WeaponID) != NameToString(b.WeaponID))
            return false;
        if (NameToString(a.WeaponClassID) != NameToString(b.WeaponClassID))
            return false;
        if (NameToString(a.OrnamentID) != NameToString(b.OrnamentID))
            return false;
        if (a.WeaponPartConfigs.Num() != b.WeaponPartConfigs.Num())
            return false;

        for (int i = 0; i < a.WeaponPartConfigs.Num(); ++i)
        {
            if (!WeaponPartConfigEquals(a.WeaponPartConfigs[i], b.WeaponPartConfigs[i]))
                return false;
        }

        return true;
    }

    bool TryGetInventoryItemForSlot(const FPBInventoryNetworkConfig& inventory, EPBCharacterSlotType slotType, FName& outItemId)
    {
        const int count = (std::min)(inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
        for (int index = 0; index < count; ++index)
        {
            if (inventory.CharacterSlots[index] == slotType)
            {
                outItemId = inventory.InventoryItems[index];
                return !IsBlankName(outItemId);
            }
        }

        return false;
    }

    bool RoleWeaponJsonHasConfig(const json& role, const char* key)
    {
        if (!role.contains(key) || !role[key].is_object())
        {
            return false;
        }

        const json& weapon = role[key];
        const bool hasIdentity =
            !weapon.value("weaponId", "").empty() ||
            !weapon.value("weaponClassId", "").empty() ||
            !weapon.value("ornamentId", "").empty();
        const bool hasParts = weapon.contains("parts") && weapon["parts"].is_array() && !weapon["parts"].empty();
        return hasIdentity || hasParts;
    }

    bool TryGetWeaponPartConfigForSlotLocal(const FPBWeaponNetworkConfig& config, EPBPartSlotType slotType, FPBWeaponPartNetworkConfig& outConfig)
    {
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int index = 0; index < count; ++index)
        {
            if (config.WeaponPartSlotTypeArray[index] == slotType)
            {
                outConfig = config.WeaponPartConfigs[index];
                return true;
            }
        }

        return false;
    }

    void FillBlankWeaponPartFieldsFromFallback(FPBWeaponPartNetworkConfig& target, const FPBWeaponPartNetworkConfig& fallback)
    {
        if (IsBlankName(target.WeaponPartID) && !IsBlankName(fallback.WeaponPartID))
        {
            target.WeaponPartID = fallback.WeaponPartID;
        }

        if (IsBlankName(target.WeaponPartSkinID) && !IsBlankName(fallback.WeaponPartSkinID))
        {
            target.WeaponPartSkinID = fallback.WeaponPartSkinID;
        }

        if (IsBlankName(target.WeaponPartSpecialSkinID) && !IsBlankName(fallback.WeaponPartSpecialSkinID))
        {
            target.WeaponPartSpecialSkinID = fallback.WeaponPartSpecialSkinID;
        }

        if (IsBlankName(target.WeaponPartSkinPaintingID) && !IsBlankName(fallback.WeaponPartSkinPaintingID))
        {
            target.WeaponPartSkinPaintingID = fallback.WeaponPartSkinPaintingID;
        }
    }

    void MergeWeaponConfigPreferPrimary(FPBWeaponNetworkConfig& primary, const FPBWeaponNetworkConfig& fallback)
    {
        if (IsBlankName(primary.WeaponID) && !IsBlankName(fallback.WeaponID))
        {
            primary.WeaponID = fallback.WeaponID;
        }

        if (IsBlankName(primary.WeaponClassID) && !IsBlankName(fallback.WeaponClassID))
        {
            primary.WeaponClassID = fallback.WeaponClassID;
        }

        if (IsBlankName(primary.OrnamentID) && !IsBlankName(fallback.OrnamentID))
        {
            primary.OrnamentID = fallback.OrnamentID;
        }

        for (int i = 0; i < primary.WeaponPartConfigs.Num(); ++i)
        {
            FPBWeaponPartNetworkConfig fallbackPart{};
            if (TryGetWeaponPartConfigForSlotLocal(fallback, primary.WeaponPartSlotTypeArray[i], fallbackPart))
            {
                FillBlankWeaponPartFieldsFromFallback(primary.WeaponPartConfigs[i], fallbackPart);
            }
        }

        for (int i = 0; i < fallback.WeaponPartSlotTypeArray.Num(); ++i)
        {
            FPBWeaponPartNetworkConfig existing{};
            if (!TryGetWeaponPartConfigForSlotLocal(primary, fallback.WeaponPartSlotTypeArray[i], existing))
            {
                primary.WeaponPartSlotTypeArray.Add(fallback.WeaponPartSlotTypeArray[i]);
                primary.WeaponPartConfigs.Add(fallback.WeaponPartConfigs[i]);
            }
        }
    }

    void SetRoleWeaponJson(json& role, const char* key, const FPBWeaponNetworkConfig& weaponConfig, bool isFromDisplayCharacter)
    {
        if (!role.contains(key))
        {
            role[key] = EmptyWeaponJson();
        }

        if (isFromDisplayCharacter)
        {
            role[key] = WeaponToJson(weaponConfig);
        }
        else if (HasWeaponConfig(weaponConfig))
        {
            FPBWeaponNetworkConfig existing{};
            WeaponFromJson(role[key], existing);

            if (!HasWeaponConfig(existing))
            {
                role[key] = WeaponToJson(weaponConfig);
            }
            else
            {
                MergeWeaponConfigPreferPrimary(existing, weaponConfig);
                role[key] = WeaponToJson(existing);
            }
        }
    }

    // =====================================================================
    //  武器配件合集的 JSON 序列化 — weaponConfigs map
    // =====================================================================

    bool TryGetInventoryForRole(UPBFieldModManager* fieldModManager, const FName& roleId, FPBInventoryNetworkConfig& outInventory);

    json WeaponConfigsMapToJson(const std::unordered_map<std::string, FPBWeaponNetworkConfig>& weaponConfigs)
    {
        json result = json::object();
        for (const auto& [weaponId, config] : weaponConfigs)
        {
            if (weaponId.empty() || !HasWeaponConfig(config))
            {
                continue;
            }
            result[weaponId] = WeaponToJson(config);
        }
        return result;
    }

    bool WeaponConfigsMapFromJson(const json& value, std::unordered_map<std::string, FPBWeaponNetworkConfig>& outConfigs)
    {
        if (!value.is_object())
        {
            return false;
        }

        for (auto it = value.begin(); it != value.end(); ++it)
        {
            if (!it.value().is_object())
            {
                continue;
            }

            FPBWeaponNetworkConfig config{};
            WeaponFromJson(it.value(), config);
            if (HasWeaponConfig(config))
            {
                outConfigs[it.key()] = config;
            }
        }

        return true;
    }

    bool TryResolveWeaponConfigFromSnapshot(const json& role, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig)
    {
        const std::string configId = NameToString(NameFromString(weaponId));
        if (configId.empty() || configId == "None")
        {
            return false;
        }

        // 优先从 weaponConfigs map 精确匹配
        if (role.contains("weaponConfigs") && role["weaponConfigs"].is_object())
        {
            const json& weaponConfigs = role["weaponConfigs"];
            if (weaponConfigs.contains(configId) && weaponConfigs[configId].is_object())
            {
                WeaponFromJson(weaponConfigs[configId], outConfig);
                return HasWeaponConfig(outConfig);
            }
        }

        // 回退到 firstWeapon / secondWeapon
        for (const char* key : { "firstWeapon", "secondWeapon" })
        {
            if (!role.contains(key) || !role[key].is_object())
            {
                continue;
            }

            const std::string keyWeaponId = role[key].value("weaponId", "");
            const std::string keyClassId = role[key].value("weaponClassId", "");
            if (keyWeaponId == configId || keyClassId == configId || keyWeaponId.empty())
            {
                WeaponFromJson(role[key], outConfig);
                if (HasWeaponConfig(outConfig))
                {
                    return true;
                }
            }
        }

        return false;
    }

    void CollectWeaponConfigsForRole(UPBFieldModManager* fieldModManager, const FName& roleId, const json& existingRole, std::unordered_map<std::string, FPBWeaponNetworkConfig>& outConfigs)
    {
        if (!fieldModManager || IsBlankName(roleId))
        {
            return;
        }

        const std::string roleIdStr = NameToString(roleId);

        // 从 FieldModManager 库存枚举武器物品
        FPBInventoryNetworkConfig inventory{};
        if (TryGetInventoryForRole(fieldModManager, roleId, inventory))
        {
            for (int i = 0; i < (std::min)(inventory.CharacterSlots.Num(), inventory.InventoryItems.Num()); ++i)
            {
                const EPBCharacterSlotType slotType = inventory.CharacterSlots[i];
                const FName itemId = inventory.InventoryItems[i];
                if (IsBlankName(itemId))
                {
                    continue;
                }

                const std::string itemIdStr = NameToString(itemId);
                if (itemIdStr.empty() || itemIdStr == "None")
                {
                    continue;
                }

                // 对武器类槽位获取完整配件配置
                if (slotType == EPBCharacterSlotType::FirstWeapon || slotType == EPBCharacterSlotType::SecondWeapon)
                {
                    FPBWeaponNetworkConfig config = fieldModManager->GetWeaponNetworkConfig(roleId, itemId);
                    if (HasWeaponConfig(config))
                    {
                        outConfigs[itemIdStr] = config;
                    }
                }
            }
        }

        // 从现有角色快照中保留已有的 weaponConfigs
        if (existingRole.contains("weaponConfigs") && existingRole["weaponConfigs"].is_object())
        {
            for (auto it = existingRole["weaponConfigs"].begin(); it != existingRole["weaponConfigs"].end(); ++it)
            {
                const std::string existingId = it.key();
                if (outConfigs.find(existingId) == outConfigs.end() && it.value().is_object())
                {
                    FPBWeaponNetworkConfig existingConfig{};
                    WeaponFromJson(it.value(), existingConfig);
                    if (HasWeaponConfig(existingConfig))
                    {
                        outConfigs[existingId] = existingConfig;
                    }
                }
            }
        }

        // 从活跃角色库存捕获 — 玩家可能捡起了其他武器
        try
        {
            for (UObject* object : getObjectsOfClass(APBCharacter::StaticClass(), false))
            {
                APBCharacter* character = static_cast<APBCharacter*>(object);
                if (!character)
                {
                    continue;
                }

                const std::string charRoleId = ResolveLiveCharacterRoleId(character);
                if (charRoleId != roleIdStr)
                {
                    continue;
                }

                const int inventoryCount = character->Inventory.Num();
                for (int i = 0; i < inventoryCount; ++i)
                {
                    APBWeapon* weapon = character->Inventory[i];
                    if (!weapon)
                    {
                        continue;
                    }

                    const FPBWeaponNetworkConfig liveConfig = weapon->GetPBWeaponPartSaveConfig();
                    const std::string liveWeaponId = NameToString(liveConfig.WeaponID);
                    if (liveWeaponId.empty() || liveWeaponId == "None")
                    {
                        continue;
                    }

                    if (outConfigs.find(liveWeaponId) == outConfigs.end())
                    {
                        if (HasWeaponConfig(liveConfig))
                        {
                            outConfigs[liveWeaponId] = liveConfig;
                        }
                    }
                    else
                    {
                        MergeWeaponConfigPreferPrimary(outConfigs[liveWeaponId], liveConfig);
                    }
                }

                break;
            }
        }
        catch (...)
        {
        }
    }

    bool TryGetInventoryForRole(UPBFieldModManager* fieldModManager, const FName& roleId, FPBInventoryNetworkConfig& outInventory)
    {
        if (!fieldModManager || IsBlankName(roleId))
        {
            return false;
        }

        for (auto& pair : fieldModManager->CharacterPreOrderingInventoryConfigs)
        {
            if (NameToString(pair.Key()) == NameToString(roleId))
            {
                outInventory = pair.Value();
                return true;
            }
        }

        return false;
    }

    // =====================================================================
    //  快照捕获 — 从菜单侧 DisplayCharacter 和 FieldModManager 抓取配装
    // =====================================================================

    json CaptureSnapshotFromMenu()
    {
        UPBFieldModManager* fieldModManager = GetFieldModManager();
        if (!fieldModManager)
        {
            return {};
        }

        json existingSnapshot;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (state.SnapshotAvailable)
            {
                existingSnapshot = state.Snapshot;
            }
        }

        std::unordered_map<std::string, json> rolesById;
        std::unordered_map<std::string, bool> rolesCapturedFromDisplay;
        if (existingSnapshot.contains("roles") && existingSnapshot["roles"].is_array())
        {
            for (const auto& role : existingSnapshot["roles"])
            {
                if (!role.is_object())
                {
                    continue;
                }

                const std::string roleId = role.value("roleId", "");
                if (!roleId.empty())
                {
                    rolesById[roleId] = role;
                }
            }
        }

        for (UObject* object : getObjectsOfClass(APBDisplayCharacter::StaticClass(), false))
        {
            APBDisplayCharacter* displayCharacter = static_cast<APBDisplayCharacter*>(object);
            if (!displayCharacter || IsBlankName(displayCharacter->RoleConfig.CharacterID))
            {
                continue;
            }

            FPBInventoryNetworkConfig inventoryOverride{};
            FPBInventoryNetworkConfig* inventoryPtr = nullptr;
            if (TryGetInventoryForRole(fieldModManager, displayCharacter->RoleConfig.CharacterID, inventoryOverride))
            {
                inventoryPtr = &inventoryOverride;
            }

            const std::string roleId = NameToString(displayCharacter->RoleConfig.CharacterID);
            json roleJson = RoleToJson(displayCharacter->RoleConfig, inventoryPtr);
            if (displayCharacter->DisplayFirstWeapon)
            {
                SetRoleWeaponJson(roleJson, "firstWeapon", displayCharacter->DisplayFirstWeapon->WeaponPartConfig, true);
            }
            if (displayCharacter->DisplaySecondWeapon)
            {
                SetRoleWeaponJson(roleJson, "secondWeapon", displayCharacter->DisplaySecondWeapon->WeaponPartConfig, true);
            }

            rolesById[roleId] = roleJson;
            rolesCapturedFromDisplay[roleId] = true;
        }

        for (auto& pair : fieldModManager->CharacterPreOrderingInventoryConfigs)
        {
            const std::string roleId = NameToString(pair.Key());
            if (roleId.empty())
            {
                continue;
            }

            if (rolesById.find(roleId) == rolesById.end())
            {
                rolesById[roleId] = EmptyRoleJson(roleId);
            }

            rolesById[roleId]["inventory"] = InventoryToJson(pair.Value());

            const auto capturedFromDisplayIt = rolesCapturedFromDisplay.find(roleId);
            const bool capturedFromDisplay = capturedFromDisplayIt != rolesCapturedFromDisplay.end() && capturedFromDisplayIt->second;
            FName firstWeaponId{};
            FName secondWeaponId{};
            if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::FirstWeapon, firstWeaponId) &&
                (!capturedFromDisplay || !RoleWeaponJsonHasConfig(rolesById[roleId], "firstWeapon")))
            {
                SetRoleWeaponJson(rolesById[roleId], "firstWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), firstWeaponId), false);
            }
            else if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::FirstWeapon, firstWeaponId))
            {
                SetRoleWeaponJson(rolesById[roleId], "firstWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), firstWeaponId), false);
            }

            if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::SecondWeapon, secondWeaponId) &&
                (!capturedFromDisplay || !RoleWeaponJsonHasConfig(rolesById[roleId], "secondWeapon")))
            {
                SetRoleWeaponJson(rolesById[roleId], "secondWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), secondWeaponId), false);
            }
            else if (TryGetInventoryItemForSlot(pair.Value(), EPBCharacterSlotType::SecondWeapon, secondWeaponId))
            {
                SetRoleWeaponJson(rolesById[roleId], "secondWeapon", fieldModManager->GetWeaponNetworkConfig(pair.Key(), secondWeaponId), false);
            }

            // 收集该角色所有武器的配件配置（主副武器 + 活跃角色持有的武器）
            std::unordered_map<std::string, FPBWeaponNetworkConfig> weaponConfigs;
            CollectWeaponConfigsForRole(fieldModManager, pair.Key(), rolesById[roleId], weaponConfigs);
            if (!weaponConfigs.empty())
            {
                rolesById[roleId]["weaponConfigs"] = WeaponConfigsMapToJson(weaponConfigs);
            }
        }

        json roles = json::array();
        for (auto& [_, role] : rolesById)
        {
            roles.push_back(role);
        }

        if (roles.empty())
        {
            return {};
        }

        return json{
            { "schemaVersion", 1 },
            { "savedAtUtc", BuildUtcTimestamp() },
            { "gameVersion", "unknown" },
            { "source", "payload" },
            { "roles", roles }
        };
    }

    // =====================================================================
    //  快照导出 — 立即从菜单抓取并写入磁盘
    // =====================================================================

    bool ExportSnapshotNow(const std::string& reason)
    {
        json snapshot = CaptureSnapshotFromMenu();
        if (!snapshot.is_object())
        {
            return false;
        }

        const std::filesystem::path exportPath = GetExportSnapshotPath();
        if (!WriteJsonFile(exportPath, snapshot))
        {
            ClientLog("[LOADOUT] Failed to write local snapshot: " + exportPath.string());
            return false;
        }

        State& state = GetState();
        {
            std::scoped_lock lock(state.Mutex);
            state.Snapshot = snapshot;
            state.SnapshotAvailable = true;
            state.PendingLiveApply = true;
            state.InitialCaptureComplete = true;
            state.LastCaptureAt = std::chrono::steady_clock::now();
        }

        ClientLog("[LOADOUT] Exported local snapshot (" + reason + ") -> " + exportPath.string());
        return true;
    }

    void TriggerAsyncExport(const std::string& reason, int delayMs)
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.ExportScheduled = true;
        state.ExportDueAt = std::chrono::steady_clock::now() + std::chrono::milliseconds(delayMs);
        state.PendingExportReason = reason;
    }

    // =====================================================================
    //  自定义配装：从简化的 custom-loadout-v1.json 构建快照兼容 JSON
    // =====================================================================

    json RoleFromCustomLoadout(const json& entry)
    {
        const std::string roleId = entry.value("roleId", "");
        json role = EmptyRoleJson(roleId);

        const std::string skinId = entry.value("skinId", "");
        const std::string skinPaintingId = entry.value("skinPaintingId", "");
        if (!skinId.empty())
        {
            role["characterData"]["skinClassArray"] = json::array({ 0 });
            role["characterData"]["skinIdArray"] = json::array({ skinId });
        }
        if (!skinPaintingId.empty())
        {
            role["characterData"]["skinPaintingId"] = skinPaintingId;
        }

        if (entry.contains("weapons") && entry["weapons"].is_array())
        {
            const auto& weapons = entry["weapons"];
            for (size_t wi = 0; wi < weapons.size() && wi < 2; ++wi)
            {
                const auto& w = weapons[wi];
                if (!w.is_object()) continue;

                json weaponJson = EmptyWeaponJson();
                const std::string itemId = w.value("itemId", "");
                weaponJson["weaponId"] = itemId;
                weaponJson["weaponClassId"] = w.value("weaponClassId", "");
                weaponJson["ornamentId"] = w.value("ornamentId", "WO-NONE");

                if (w.contains("parts") && w["parts"].is_object())
                {
                    json partsArray = json::array();
                    for (const auto& [slotKey, partId] : w["parts"].items())
                    {
                        if (!partId.is_string()) continue;
                        json part;
                        part["slotType"] = std::stoi(slotKey);
                        part["weaponPartId"] = partId.get<std::string>();
                        part["weaponPartSkinId"] = "PartOri";
                        part["weaponPartSkinPaintingId"] = "PTOriginal";
                        part["weaponPartSpecialSkinId"] = "None";
                        partsArray.push_back(part);
                    }
                    weaponJson["parts"] = partsArray;
                }

                if (wi == 0) role["firstWeapon"] = weaponJson;
                else         role["secondWeapon"] = weaponJson;

                // weaponConfigs map — 用 FName 规范化 key 以与查找侧对齐
                if (!role.contains("weaponConfigs")) role["weaponConfigs"] = json::object();
                if (!itemId.empty())
                {
                    const std::string normalizedKey = NameToString(NameFromString(itemId));
                    if (!normalizedKey.empty() && normalizedKey != "None")
                        role["weaponConfigs"][normalizedKey] = weaponJson;
                }
            }
        }

        auto setSimpleItem = [](json& target, const std::string& id, const std::string& idField = "id") {
            target[idField] = id;
            if (idField == "id") target["skinId"] = "";
        };

        const std::string meleeId = entry.value("meleeWeapon", "");
        if (!meleeId.empty() && meleeId != "None") setSimpleItem(role["meleeWeapon"], meleeId);

        const std::string leftLauncherId = entry.value("leftLauncher", "");
        if (!leftLauncherId.empty() && leftLauncherId != "None") setSimpleItem(role["leftLauncher"], leftLauncherId);

        const std::string rightLauncherId = entry.value("rightLauncher", "");
        if (!rightLauncherId.empty() && rightLauncherId != "None") setSimpleItem(role["rightLauncher"], rightLauncherId);

        const std::string mobilityId = entry.value("mobilityModule", "");
        if (!mobilityId.empty() && mobilityId != "None") setSimpleItem(role["mobilityModule"], mobilityId, "mobilityModuleId");

        json& slots = role["inventory"]["slots"];
        slots = json::array();
        auto pushSlot = [&](int slotType, const std::string& itemId) {
            if (!itemId.empty() && itemId != "None")
                slots.push_back({ { "slotType", slotType }, { "itemId", itemId } });
        };

        if (entry.contains("weapons") && entry["weapons"].is_array())
        {
            const auto& weapons = entry["weapons"];
            if (weapons.size() > 0 && weapons[0].is_object())
                pushSlot(static_cast<int>(EPBCharacterSlotType::FirstWeapon), weapons[0].value("itemId", ""));
            if (weapons.size() > 1 && weapons[1].is_object())
                pushSlot(static_cast<int>(EPBCharacterSlotType::SecondWeapon), weapons[1].value("itemId", ""));
        }
        pushSlot(static_cast<int>(EPBCharacterSlotType::LeftPod), leftLauncherId);
        pushSlot(static_cast<int>(EPBCharacterSlotType::RightPod), rightLauncherId);
        pushSlot(static_cast<int>(EPBCharacterSlotType::MeleeWeapon), meleeId);
        pushSlot(static_cast<int>(EPBCharacterSlotType::Mobility), mobilityId);

        if (entry.contains("inventoryExtras") && entry["inventoryExtras"].is_array())
        {
            for (const auto& extra : entry["inventoryExtras"])
            {
                if (extra.is_string())
                    pushSlot(static_cast<int>(EPBCharacterSlotType::None), extra.get<std::string>());
            }
        }

        return role;
    }

    bool LoadCustomLoadoutConfig(json& outSnapshot)
    {
        const std::filesystem::path customPath = GetCustomLoadoutPath();
        json custom;
        if (!ReadJsonFile(customPath, custom))
        {
            return false;
        }

        if (!custom.contains("loadouts") || !custom["loadouts"].is_array() || custom["loadouts"].empty())
        {
            ClientLog("[LOADOUT] Custom config has no loadouts array");
            return false;
        }

        json snapshot;
        snapshot["schemaVersion"] = 1;
        snapshot["gameVersion"] = "unknown";
        snapshot["source"] = "custom-config";
        snapshot["roles"] = json::array();

        for (const auto& entry : custom["loadouts"])
        {
            if (!entry.is_object()) continue;
            const std::string roleId = entry.value("roleId", "");
            if (roleId.empty()) continue;

            snapshot["roles"].push_back(RoleFromCustomLoadout(entry));
        }

        if (snapshot["roles"].empty())
        {
            ClientLog("[LOADOUT] Custom config produced no valid roles");
            return false;
        }

        outSnapshot = snapshot;
        ClientLog("[LOADOUT] Loaded custom config: " + customPath.string() +
            " (" + std::to_string(snapshot["roles"].size()) + " roles)");
        return true;
    }

    // =====================================================================
    //  快照加载 — 自定义配装 > 启动器注入 > 本地导出
    // =====================================================================

    void EnsureSnapshotLoaded()
    {
        State& state = GetState();
        {
            std::scoped_lock lock(state.Mutex);
            if (state.SnapshotLoadAttempted)
            {
                return;
            }
            state.SnapshotLoadAttempted = true;
        }

        json loadedSnapshot;
        std::string source;
        const std::filesystem::path customPath = GetCustomLoadoutPath();
        const std::filesystem::path launchPath = GetLaunchSnapshotPath();
        const std::filesystem::path exportPath = GetExportSnapshotPath();

        if (LoadCustomLoadoutConfig(loadedSnapshot))
        {
            source = customPath.string();
        }
        else if (ReadJsonFile(launchPath, loadedSnapshot))
        {
            source = launchPath.string();
            try
            {
                std::filesystem::remove(launchPath);
            }
            catch (...)
            {
            }
        }
        else if (ReadJsonFile(exportPath, loadedSnapshot))
        {
            source = exportPath.string();
        }

        if (!loadedSnapshot.is_object())
        {
            return;
        }

        loadedSnapshot.erase("selectedRoleId");

        {
            std::scoped_lock lock(state.Mutex);
            state.Snapshot = loadedSnapshot;
            state.SnapshotAvailable = true;
            state.PendingLiveApply = true;
        }

        ClientLog("[LOADOUT] Loaded snapshot from " + source);
    }

    void PreloadSnapshot()
    {
        EnsureSnapshotLoaded();
    }

    // =====================================================================
    //  出生前库存推送 — 通过 RPC 将快照库存推送到服务端
    // =====================================================================

    std::vector<std::pair<std::string, FPBInventoryNetworkConfig>> BuildInventoryListFromSnapshot(const json& snapshot)
    {
        std::vector<std::pair<std::string, FPBInventoryNetworkConfig>> inventories;
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array())
        {
            return inventories;
        }

        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object())
            {
                continue;
            }

            const std::string roleId = role.value("roleId", "");
            if (roleId.empty())
            {
                continue;
            }

            FPBInventoryNetworkConfig inventory{};
            InventoryFromJson(role.value("inventory", EmptyInventoryJson()), inventory);
            inventories.push_back({ roleId, inventory });
        }

        return inventories;
    }

    bool PreSpawnApply(const json& snapshot, APBPlayerController* preferredController, std::string& outDetail)
    {
        const auto inventories = BuildInventoryListFromSnapshot(snapshot);
        if (inventories.empty())
        {
            outDetail = "snapshot-inventories-empty";
            return false;
        }

        APBPlayerController* playerController = preferredController;
        if (!playerController)
        {
            playerController = GetLocalPlayerController();
        }
        if (!playerController)
        {
            outDetail = "target-player-controller-missing";
            return false;
        }

        APBPlayerState* playerState = playerController->PBPlayerState;
        int pushedRoleCount = 0;
        int refreshedRoleCount = 0;

        for (const auto& [roleId, inventory] : inventories)
        {
            const FName roleName = NameFromString(roleId);
            if (IsBlankName(roleName))
            {
                continue;
            }

            playerController->ServerPreOrderInventory(roleName, inventory);
            ++pushedRoleCount;

            if (playerState)
            {
                playerState->ClientRefreshRolePreOrderingInventory(roleName, inventory);
                playerState->ClientRefreshRoleEquippingInventory(roleName, inventory);
                ++refreshedRoleCount;
            }
        }

        if (pushedRoleCount <= 0)
        {
            outDetail = "no-valid-role-inventories";
            return false;
        }

        outDetail =
            "controller=" + playerController->GetFullName() + ", " +
            "roles=" + std::to_string(pushedRoleCount) +
            ", refreshed=" + std::to_string(refreshedRoleCount) +
            (playerState ? "" : ", playerState=missing");
        return true;
    }

    // =====================================================================
    //  角色 ID 解析
    // =====================================================================

    std::string ResolveCharacterRoleId(APBCharacter* character)
    {
        if (!character)
        {
            return "";
        }

        APBPlayerController* playerController = FindPlayerControllerForCharacter(character);
        if (playerController && playerController->PBPlayerState)
        {
            APBPlayerState* playerState = playerController->PBPlayerState;
            if (!IsBlankName(playerState->UsageCharacterID))
            {
                return NameToString(playerState->UsageCharacterID);
            }
        }

        if (!IsBlankName(character->CharacterID))
        {
            return NameToString(character->CharacterID);
        }

        return "";
    }

    std::string ResolveLiveCharacterRoleId(APBCharacter* character)
    {
        if (!character)
        {
            return "";
        }

        if (!IsBlankName(character->CharacterID))
        {
            return NameToString(character->CharacterID);
        }

        return ResolveCharacterRoleId(character);
    }

    // =====================================================================
    //  快照解析 — JSON → FPBRoleNetworkConfig
    // =====================================================================

    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig)
    {
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array())
        {
            return false;
        }

        const json* firstRole = nullptr;
        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object())
            {
                continue;
            }

            if (role.value("roleId", "") == roleId)
            {
                outConfig.CharacterID = NameFromString(roleId);
                InventoryFromJson(role.value("inventory", EmptyInventoryJson()), outConfig.InventoryData);
                CharacterFromJson(role.value("characterData", EmptyCharacterJson()), outConfig.CharacterData);
                WeaponFromJson(role.value("firstWeapon", EmptyWeaponJson()), outConfig.FirstWeaponPartData);
                WeaponFromJson(role.value("secondWeapon", EmptyWeaponJson()), outConfig.SecondWeaponPartData);
                MeleeFromJson(role.value("meleeWeapon", EmptyMeleeJson()), outConfig.MeleeWeaponData);
                LauncherFromJson(role.value("leftLauncher", EmptyLauncherJson()), outConfig.LeftLauncherData);
                LauncherFromJson(role.value("rightLauncher", EmptyLauncherJson()), outConfig.RightLauncherData);
                MobilityFromJson(role.value("mobilityModule", EmptyMobilityJson()), outConfig.MobilityModuleData);
                return true;
            }

            if (!firstRole)
            {
                firstRole = &role;
            }
        }

        if (!roleId.empty() && firstRole)
        {
            const std::string fallbackRoleId = (*firstRole).value("roleId", "");
            outConfig.CharacterID = NameFromString(fallbackRoleId);
            InventoryFromJson((*firstRole).value("inventory", EmptyInventoryJson()), outConfig.InventoryData);
            CharacterFromJson((*firstRole).value("characterData", EmptyCharacterJson()), outConfig.CharacterData);
            WeaponFromJson((*firstRole).value("firstWeapon", EmptyWeaponJson()), outConfig.FirstWeaponPartData);
            WeaponFromJson((*firstRole).value("secondWeapon", EmptyWeaponJson()), outConfig.SecondWeaponPartData);
            MeleeFromJson((*firstRole).value("meleeWeapon", EmptyMeleeJson()), outConfig.MeleeWeaponData);
            LauncherFromJson((*firstRole).value("leftLauncher", EmptyLauncherJson()), outConfig.LeftLauncherData);
            LauncherFromJson((*firstRole).value("rightLauncher", EmptyLauncherJson()), outConfig.RightLauncherData);
            MobilityFromJson((*firstRole).value("mobilityModule", EmptyMobilityJson()), outConfig.MobilityModuleData);
            return true;
        }

        return false;
    }

    bool TryResolveSnapshotWeaponConfigByRoleAndWeaponId(const json& snapshot, const std::string& roleId, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig)
    {
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array())
        {
            return false;
        }

        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object() || role.value("roleId", "") != roleId)
            {
                continue;
            }

            for (const char* key : { "firstWeapon", "secondWeapon" })
            {
                if (!role.contains(key) || !role[key].is_object())
                {
                    continue;
                }

                const std::string configWeaponId = role[key].value("weaponId", "");
                const std::string configWeaponClassId = role[key].value("weaponClassId", "");
                if (configWeaponId == weaponId || configWeaponClassId == weaponId)
                {
                    WeaponFromJson(role[key], outConfig);
                    return true;
                }
            }
        }

        return false;
    }

    // =====================================================================
    //  武器评分和匹配 — 用于从快照中选择最佳匹配的武器配置
    // =====================================================================

    int ScoreWeaponSnapshotMatch(const FPBWeaponNetworkConfig& config, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon)
    {
        int score = 0;

        if (!IsBlankName(config.WeaponID) && !IsBlankName(incoming.WeaponID) && NameToString(config.WeaponID) == NameToString(incoming.WeaponID))
        {
            score += 10;
        }

        if (!IsBlankName(config.WeaponClassID) && !IsBlankName(incoming.WeaponClassID) && NameToString(config.WeaponClassID) == NameToString(incoming.WeaponClassID))
        {
            score += 5;
        }

        if (weapon)
        {
            const FPBWeaponNetworkConfig liveConfig = weapon->PartNetworkConfig;
            if (!IsBlankName(config.WeaponID) && !IsBlankName(liveConfig.WeaponID) && NameToString(config.WeaponID) == NameToString(liveConfig.WeaponID))
            {
                score += 8;
            }
            if (!IsBlankName(config.WeaponClassID) && !IsBlankName(liveConfig.WeaponClassID) && NameToString(config.WeaponClassID) == NameToString(liveConfig.WeaponClassID))
            {
                score += 4;
            }
        }

        if (config.WeaponPartConfigs.Num() > 0)
        {
            score += 2;
        }

        return score;
    }

    bool TryGetRoleWeaponConfigForInit(const FPBRoleNetworkConfig& roleConfig, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon, FPBWeaponNetworkConfig& outConfig)
    {
        const int firstScore = ScoreWeaponSnapshotMatch(roleConfig.FirstWeaponPartData, incoming, weapon);
        const int secondScore = ScoreWeaponSnapshotMatch(roleConfig.SecondWeaponPartData, incoming, weapon);

        if (firstScore <= 0 && secondScore <= 0)
        {
            return false;
        }

        outConfig = firstScore >= secondScore ? roleConfig.FirstWeaponPartData : roleConfig.SecondWeaponPartData;
        return true;
    }

    bool TryGetRoleWeaponConfigByWeaponId(const FPBRoleNetworkConfig& roleConfig, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig)
    {
        const std::string firstId = NameToString(roleConfig.FirstWeaponPartData.WeaponID);
        const std::string firstClassId = NameToString(roleConfig.FirstWeaponPartData.WeaponClassID);
        const std::string secondId = NameToString(roleConfig.SecondWeaponPartData.WeaponID);
        const std::string secondClassId = NameToString(roleConfig.SecondWeaponPartData.WeaponClassID);

        if (firstId == weaponId || firstClassId == weaponId)
        {
            outConfig = roleConfig.FirstWeaponPartData;
            return true;
        }

        if (secondId == weaponId || secondClassId == weaponId)
        {
            outConfig = roleConfig.SecondWeaponPartData;
            return true;
        }

        return false;
    }

    bool TryResolveWeaponConfigForRole(const json& snapshot, const std::string& roleId, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon, FPBWeaponNetworkConfig& outConfig)
    {
        if (roleId.empty())
        {
            return false;
        }

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            return false;
        }

        return TryGetRoleWeaponConfigForInit(roleConfig, incoming, weapon, outConfig);
    }

    bool TryResolveSnapshotWeaponConfigFromPendingRoles(const json& snapshot, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon, std::string& outRoleId, FPBWeaponNetworkConfig& outConfig)
    {
        // 收集所有待处理的角色选择（复制到锁外处理）
        std::vector<std::pair<std::string, bool>> pendingRoles; // {roleId, isAuthoritative}
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            for (const auto& [controller, context] : state.PendingRoleSelections)
            {
                if (!context.RoleId.empty())
                {
                    pendingRoles.push_back({ context.RoleId, context.IsAuthoritative });
                }
            }
        }

        int bestScore = 0;
        for (const auto& [roleId, isAuthoritative] : pendingRoles)
        {
            FPBWeaponNetworkConfig candidate{};
            if (!TryResolveWeaponConfigForRole(snapshot, roleId, incoming, weapon, candidate))
            {
                continue;
            }

            const int score = ScoreWeaponSnapshotMatch(candidate, incoming, weapon);
            if (score > bestScore)
            {
                bestScore = score;
                outConfig = candidate;
                outRoleId = roleId;
            }
        }

        return bestScore > 0;
    }

    bool TryResolveSnapshotWeaponConfigForInit(const json& snapshot, APBWeapon* weapon, const FPBWeaponNetworkConfig& incoming, std::string& outResolvedRoleId, FPBWeaponNetworkConfig& outConfig)
    {
        if (!weapon)
        {
            return false;
        }

        APBCharacter* character = nullptr;
        for (UObject* object : getObjectsOfClass(APBCharacter::StaticClass(), false))
        {
            APBCharacter* candidate = static_cast<APBCharacter*>(object);
            if (!candidate)
            {
                continue;
            }

            for (int i = 0; i < candidate->Inventory.Num(); ++i)
            {
                if (candidate->Inventory[i] == weapon)
                {
                    character = candidate;
                    break;
                }
            }

            if (character)
            {
                break;
            }
        }

        std::string roleId;
        if (character)
        {
            roleId = ResolveLiveCharacterRoleId(character);
            if (roleId.empty())
            {
                roleId = ResolveCharacterRoleId(character);
            }
        }

        // 1) 优先：扫描所有待处理角色选择，评分匹配最佳武器配置
        //    解决了 dedicated server 无本地玩家、多玩家同时加入竞态等问题
        if (TryResolveSnapshotWeaponConfigFromPendingRoles(snapshot, incoming, weapon, outResolvedRoleId, outConfig))
        {
            return true;
        }

        // 2) 回退：武器持有者的角色
        if (!roleId.empty() && TryResolveWeaponConfigForRole(snapshot, roleId, incoming, weapon, outConfig))
        {
            outResolvedRoleId = roleId;
            return true;
        }

        // 3) 回退：本地玩家控制器的待处理角色
        if (roleId.empty())
        {
            APBPlayerController* playerController = GetLocalPlayerController();
            roleId = GetPendingRoleIdForController(playerController);
            if (!roleId.empty() && TryResolveWeaponConfigForRole(snapshot, roleId, incoming, weapon, outConfig))
            {
                outResolvedRoleId = roleId;
                return true;
            }
        }

        // 4) 回退：本地首选角色 / 记住的菜单角色
        if (roleId.empty()) { roleId = GetLocalPlayerPreferredRoleId(); }
        if (roleId.empty()) { roleId = GetRememberedMenuSelectedRoleId(); }

        if (!roleId.empty() && TryResolveWeaponConfigForRole(snapshot, roleId, incoming, weapon, outConfig))
        {
            outResolvedRoleId = roleId;
            return true;
        }

        return false;
    }

    // =====================================================================
    //  InitWeapon 参数覆盖 — 在武器初始化前将快照配件配置写入参数
    //  仅覆写 ProcessEvent 参数结构体的 InPartSaved 字段，不修改任何数据表
    // =====================================================================

    void MaybeOverrideInitWeaponConfig(UObject* object, const std::string& functionName, void* parms)
    {
        if (!object || !parms || !object->IsA(APBWeapon::StaticClass()))
        {
            return;
        }

        // 临时诊断：记录所有包含 "Init" 或 "Weapon" 的 APBWeapon ProcessEvent 调用
        // 以确认 InitWeapon 的实际函数名格式
        static int diagCount = 0;
        if (diagCount < 20 && (functionName.find("Init") != std::string::npos ||
            functionName.find("Weapon") != std::string::npos ||
            functionName.find("Part") != std::string::npos ||
            functionName.find("Skin") != std::string::npos))
        {
            ++diagCount;
            ClientLog("[LOADOUT-DIAG] APBWeapon PE: " + functionName);
        }

        if (functionName.find("PBWeapon.InitWeapon") == std::string::npos &&
            functionName.find("InitWeapon") == std::string::npos)
        {
            return;
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (!state.SnapshotAvailable)
            {
                return;
            }
            snapshotCopy = state.Snapshot;
        }

        APBWeapon* weapon = static_cast<APBWeapon*>(object);
        auto* initParms = static_cast<Params::PBWeapon_InitWeapon*>(parms);
        const FPBWeaponNetworkConfig previousConfig = initParms->InPartSaved;

        const std::string weaponId = NameToString(previousConfig.WeaponID);
        const std::string classId = NameToString(previousConfig.WeaponClassID);

        std::string resolvedRoleId;
        FPBWeaponNetworkConfig targetConfig{};
        if (!TryResolveSnapshotWeaponConfigForInit(snapshotCopy, weapon, previousConfig, resolvedRoleId, targetConfig))
        {
            ClientLog("[LOADOUT] InitWeapon: no snapshot config for weapon=" + weaponId +
                " class=" + classId);
            return;
        }

        if (!HasWeaponConfig(targetConfig))
        {
            return;
        }

        // 始终覆写 — 即使字符串比较相同，FName 实例或 TArray 内部指针可能不同，
        // 导致原生 InitWeapon 在比较时认为配置未变化而跳过视觉更新
        const bool configDiffers = !WeaponConfigEquals(previousConfig, targetConfig);

        initParms->InPartSaved = targetConfig;
        weapon->PartNetworkConfig = targetConfig;

        ClientLog("[LOADOUT] InitWeapon: overrode parts for weapon=" + weaponId +
            " class=" + classId + " role=" + resolvedRoleId +
            " parts=" + std::to_string(targetConfig.WeaponPartConfigs.Num()) +
            (configDiffers ? "" : " (was already matching by string compare)"));
    }

    // =====================================================================
    //  武器查找 — 在角色身上定位武器
    // =====================================================================

    APBWeapon* FindWeaponForConfig(APBCharacter* character, const FPBWeaponNetworkConfig& config, int preferredIndex)
    {
        if (!character)
        {
            return nullptr;
        }

        const std::string configWeaponId = NameToString(config.WeaponID);
        const std::string configClassId = NameToString(config.WeaponClassID);
        const bool hasIdentity = !configWeaponId.empty() && configWeaponId != "None";

        APBWeapon* byIndex = nullptr;
        int matchCount = 0;

        for (int i = 0; i < character->Inventory.Num(); ++i)
        {
            APBWeapon* weapon = character->Inventory[i];
            if (!weapon)
            {
                continue;
            }

            if (i == preferredIndex)
            {
                byIndex = weapon;
            }

            if (hasIdentity)
            {
                const std::string liveId = NameToString(weapon->PartNetworkConfig.WeaponID);
                const std::string liveClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);

                if (liveId == configWeaponId || liveClassId == configClassId || liveClassId == configWeaponId)
                {
                    return weapon;
                }
            }

            ++matchCount;
        }

        if (byIndex && hasIdentity)
        {
            return nullptr;
        }

        return byIndex;
    }

    APBWeapon* GetWeaponByPreferredIndex(APBCharacter* character, int index)
    {
        if (!character || index < 0 || index >= character->Inventory.Num())
        {
            return nullptr;
        }

        return character->Inventory[index];
    }

    // =====================================================================
    //  武器视觉效果刷新
    // =====================================================================

    void RefreshWeaponRuntimeVisuals(APBWeapon* weapon)
    {
        if (!weapon)
        {
            return;
        }

        weapon->ApplyPartModification();
        weapon->K2_InitSimulatedPartsComplete();
        weapon->K2_RefreshSkin();
        weapon->NotifyRecalculateSpecialPartOffset();
        weapon->CalculateAimPointToSightSocketOffset();
    }

    // =====================================================================
    //  标记网络复制
    // =====================================================================

    void MarkActorForReplication(AActor* actor)
    {
        if (!actor)
        {
            return;
        }

        actor->ForceNetUpdate();
        actor->FlushNetDormancy();
    }

    // =====================================================================
    //  出生后实时应用 — 快照 → 活跃角色/武器/配件
    // =====================================================================

    bool ApplyLauncherConfig(APBLauncher* launcher, const FPBLauncherNetworkConfig& config, bool& outChanged)
    {
        if (!HasLauncherConfig(config))
        {
            return true;
        }

        if (!launcher)
        {
            return false;
        }

        launcher->SavedData = config;
        launcher->OnRep_SavedData();
        MarkActorForReplication(launcher);
        outChanged = true;
        return true;
    }

    bool ApplyMeleeConfig(APBMeleeWeapon* meleeWeapon, const FPBMeleeWeaponNetworkConfig& config, bool& outChanged)
    {
        if (!HasMeleeConfig(config))
        {
            return true;
        }

        if (!meleeWeapon)
        {
            return false;
        }

        meleeWeapon->MeleeNetworkConfig = config;
        meleeWeapon->OnRep_MeleeNetworkConfig();
        MarkActorForReplication(meleeWeapon);
        outChanged = true;
        return true;
    }

    bool ApplyMobilityConfig(APBCharacter* character, const FPBMobilityModuleNetworkConfig& config, bool& outChanged)
    {
        if (!HasMobilityConfig(config))
        {
            return true;
        }

        if (!character || !character->CurrentMobilityModule)
        {
            return false;
        }

        character->CurrentMobilityModule->SavedData = config;
        character->OnRep_CurrentMobilityModule();
        MarkActorForReplication(character->CurrentMobilityModule);
        MarkActorForReplication(character);
        outChanged = true;
        return true;
    }

    bool PostSpawnApply(APBCharacter* character, const json& snapshot)
    {
        if (!character)
        {
            return false;
        }

        try
        {
            FPBRoleNetworkConfig roleConfig{};
            const std::string roleId = ResolveLiveCharacterRoleId(character);
            if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
            {
                return false;
            }

            // 查找角色 JSON 条目以访问 weaponConfigs map
            const json* roleJson = nullptr;
            if (snapshot.contains("roles") && snapshot["roles"].is_array())
            {
                for (const auto& role : snapshot["roles"])
                {
                    if (role.is_object() && role.value("roleId", "") == roleId)
                    {
                        roleJson = &role;
                        break;
                    }
                }
            }

            bool changed = false;

            character->CharacterSkinConfig = roleConfig.CharacterData;
            character->OnRep_CharacterSkinConfig();

            if (auto* skinManager = const_cast<UPBSkinManager*>(UPBSkinManager::GetPBSkinManager()))
            {
                skinManager->RefreshCharacterSkin(character, roleConfig.CharacterData);
            }

            MarkActorForReplication(character);
            changed = true;

            // 武器配装 — 按 Inventory 索引直接映射 first/second weapon
            // 备选：weaponConfigs map 精确匹配
            static int diagCount = 0;
            const int inventoryCount = character->Inventory.Num();
            for (int i = 0; i < inventoryCount; ++i)
            {
                APBWeapon* weapon = character->Inventory[i];
                if (!weapon || !weapon->IsA(APBWeapon::StaticClass()))
                {
                    continue;
                }

                const std::string liveWeaponId = NameToString(weapon->PartNetworkConfig.WeaponID);
                const std::string liveClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);

                FPBWeaponNetworkConfig matchedConfig{};
                bool found = false;

                // 1) 索引 0 → firstWeapon, 索引 1 → secondWeapon（直接映射）
                if (i == 0 && HasWeaponConfig(roleConfig.FirstWeaponPartData))
                {
                    matchedConfig = roleConfig.FirstWeaponPartData;
                    found = true;
                }
                else if (i == 1 && HasWeaponConfig(roleConfig.SecondWeaponPartData))
                {
                    matchedConfig = roleConfig.SecondWeaponPartData;
                    found = true;
                }

                // 2) weaponConfigs map 精确匹配（备选）
                if (!found && roleJson)
                {
                    if (!liveWeaponId.empty() && liveWeaponId != "None")
                    {
                        found = TryResolveWeaponConfigFromSnapshot(*roleJson, liveWeaponId, matchedConfig);
                    }
                    if (!found && !liveClassId.empty() && liveClassId != "None")
                    {
                        found = TryResolveWeaponConfigFromSnapshot(*roleJson, liveClassId, matchedConfig);
                    }
                }

                // 3) 评分回退
                if (!found)
                {
                    const int firstScore = ScoreWeaponSnapshotMatch(roleConfig.FirstWeaponPartData, weapon->PartNetworkConfig, weapon);
                    const int secondScore = ScoreWeaponSnapshotMatch(roleConfig.SecondWeaponPartData, weapon->PartNetworkConfig, weapon);
                    if (firstScore > 0 || secondScore > 0)
                    {
                        matchedConfig = firstScore >= secondScore
                            ? roleConfig.FirstWeaponPartData
                            : roleConfig.SecondWeaponPartData;
                        found = true;
                    }
                }

                if (found && HasWeaponConfig(matchedConfig))
                {
                    weapon->InitWeapon(matchedConfig, false);
                    RefreshWeaponRuntimeVisuals(weapon);
                    MarkActorForReplication(weapon);

                    if (diagCount < 5)
                    {
                        ++diagCount;
                        ClientLog("[LOADOUT] Applied weapon[" + std::to_string(i) +
                            "] parts: weaponId=" + liveWeaponId +
                            " parts=" + std::to_string(matchedConfig.WeaponPartConfigs.Num()) +
                            " role=" + roleId);
                    }
                }
                else if (diagCount < 5)
                {
                    ++diagCount;
                    ClientLog("[LOADOUT] No match for weapon[" + std::to_string(i) +
                        "]: weaponId=" + liveWeaponId +
                        " classId=" + liveClassId +
                        " role=" + roleId);
                }
            }

        ApplyMeleeConfig(character->CurrentMeleeWeapon, roleConfig.MeleeWeaponData, changed);
        ApplyLauncherConfig(character->CurrentLeftLauncher, roleConfig.LeftLauncherData, changed);
        ApplyLauncherConfig(character->CurrentRightLauncher, roleConfig.RightLauncherData, changed);
        ApplyMobilityConfig(character, roleConfig.MobilityModuleData, changed);

        if (changed)
        {
            ClientLog("[LOADOUT] Applied live snapshot to role " + NameToString(roleConfig.CharacterID));
        }

        return true;
        }
        catch (...)
        {
            return false;
        }
    }

    // =====================================================================
    //  预生成库存推送 — 在角色生成前通过 RPC 推送快照库存
    // =====================================================================

    void PushPreSpawnInventory(APBPlayerController* playerController)
    {
        if (!playerController)
        {
            return;
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (!state.SnapshotAvailable)
            {
                return;
            }
            snapshotCopy = state.Snapshot;
        }

        std::string detail;
        if (PreSpawnApply(snapshotCopy, playerController, detail))
        {
            ClientLog("[LOADOUT] Pre-spawn inventory pushed: " + detail);
        }
    }

    // =====================================================================
    //  Tick — 客户端
    // =====================================================================

    // =====================================================================
    //  菜单/存档就绪检查
    // =====================================================================

    bool HasArchiveLoaded()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (playerController)
        {
            return playerController->bHasLoadedArchive;
        }

        return LoginCompleted && GetFieldModManager() != nullptr;
    }

    bool HasDisplayCharactersReady()
    {
        for (UObject* object : getObjectsOfClass(APBDisplayCharacter::StaticClass(), false))
        {
            APBDisplayCharacter* displayCharacter = static_cast<APBDisplayCharacter*>(object);
            if (displayCharacter && !IsBlankName(displayCharacter->RoleConfig.CharacterID))
            {
                return true;
            }
        }

        return false;
    }

    bool IsMenuCaptureReady()
    {
        UPBFieldModManager* fieldModManager = GetFieldModManager();
        if (!fieldModManager)
        {
            return false;
        }

        if (fieldModManager->CharacterPreOrderingInventoryConfigs.Num() <= 0)
        {
            return false;
        }

        return HasDisplayCharactersReady();
    }

    std::chrono::steady_clock::time_point NextClientApplyAt{};

    void ClientTick()
    {
        const auto now = std::chrono::steady_clock::now();

        // 批量读取状态 — 在锁外执行所有操作避免死锁
        bool shouldExport = false;
        bool shouldInitialCapture = false;
        bool shouldPeriodicRefresh = false;
        bool shouldLiveApply = false;
        std::string exportReason;
        json snapshotCopy;

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);

            // 1) 计划导出
            if (state.ExportScheduled && now >= state.ExportDueAt)
            {
                state.ExportScheduled = false;
                exportReason = state.PendingExportReason;
                shouldExport = true;
            }

            // 2) 初始捕获
            if (!state.InitialCaptureComplete)
            {
                shouldInitialCapture = true;
            }

            // 3) 周期性刷新
            if (state.SnapshotAvailable && state.LastCaptureAt != std::chrono::steady_clock::time_point{})
            {
                if (now >= state.LastCaptureAt + std::chrono::seconds(30))
                {
                    shouldPeriodicRefresh = true;
                }
            }

            // 4) 出生后实时应用
            if (!state.ClientApplyComplete && state.SnapshotAvailable && state.PendingLiveApply)
            {
                snapshotCopy = state.Snapshot;
                shouldLiveApply = true;
            }
        }

        if (shouldExport)
        {
            ExportSnapshotNow(exportReason);
        }

        // 初始捕获 — 不依赖 ProcessEvent 触发器
        if (shouldInitialCapture && HasArchiveLoaded() && IsMenuCaptureReady())
        {
            ExportSnapshotNow("initial-capture");
        }

        // 周期性刷新 — 30 秒间隔降级路径
        if (shouldPeriodicRefresh && HasArchiveLoaded() && IsMenuCaptureReady())
        {
            ExportSnapshotNow("periodic-refresh");
        }

        // 出生后实时应用
        if (shouldLiveApply && LoginCompleted && now >= NextClientApplyAt)
        {
            NextClientApplyAt = now + std::chrono::milliseconds(500);

            APBCharacter* character = GetLocalCharacter();
            if (character && character->Inventory.Num() > 0 && IsCharacterAlive(character))
            {
                if (snapshotCopy.is_object() && PostSpawnApply(character, snapshotCopy))
                {
                    State& state = GetState();
                    std::scoped_lock lock(state.Mutex);
                    state.ClientApplyComplete = true;
                }
            }
        }
    }

    // =====================================================================
    //  Tick — 服务端
    // =====================================================================

    void ServerTick()
    {
        State& state = GetState();

        if (!state.SnapshotAvailable)
        {
            return;
        }

        json snapshotCopy;
        {
            std::scoped_lock lock(state.Mutex);
            snapshotCopy = state.Snapshot;
        }

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            APBCharacter* character = GetControllerCharacter(playerController);
            if (!character || character->Inventory.Num() <= 0 || !IsCharacterAlive(character))
            {
                continue;
            }

            PostSpawnApply(character, snapshotCopy);
        }
    }
}

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
//  公有接口 — 启动 / 菜单信号
// =====================================================================

void LoadoutManager::PreloadSnapshot()
{
    LoadoutManagerDetail::PreloadSnapshot();
}

void LoadoutManager::NotifyMenuConstructed()
{
    // 不再是必须的（原实现用于启动延迟快照捕获窗口）。
    // 快照捕获在首次导出时按需触发，菜单构建通知为兼容性保留。
}

void LoadoutManager::RememberMenuSelectedRole(const FName& roleId)
{
    LoadoutManagerDetail::RememberMenuSelectedRole(roleId);
}

void LoadoutManager::OnRoleSelectionConfirmed(APBPlayerController* playerController, const FName& roleId, bool isAuthoritative)
{
    if (!playerController || LoadoutManagerDetail::IsBlankName(roleId))
    {
        return;
    }

    LoadoutManagerDetail::RememberMenuSelectedRole(roleId);
    LoadoutManagerDetail::RememberPendingRoleSelection(
        playerController,
        LoadoutManagerDetail::NameToString(roleId),
        isAuthoritative);

    LoadoutManagerDetail::PushPreSpawnInventory(playerController);
}

// =====================================================================
//  公有接口 — ProcessEvent Hook 桥接
//  重写后不再拦截 ProcessEvent，仅为兼容性保留空实现
// =====================================================================

void LoadoutManager::OnClientProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    // 复活时重新推送出生前库存
    if (functionName.find("OnRestartInStartSpot") != std::string::npos)
    {
        APBPlayerController* playerController = nullptr;
        if (parms)
        {
            auto* restartParms = static_cast<Params::PBFieldModManager_OnRestartInStartSpot*>(parms);
            if (restartParms && restartParms->InController && restartParms->InController->IsA(APBPlayerController::StaticClass()))
            {
                playerController = static_cast<APBPlayerController*>(restartParms->InController);
            }
        }
        LoadoutManagerDetail::PushPreSpawnInventory(playerController);
    }

    // InitWeapon 参数覆盖 — 在武器初始化前将快照配件写入 InPartSaved
    LoadoutManagerDetail::MaybeOverrideInitWeaponConfig(object, functionName, parms);
}

void LoadoutManager::OnClientProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    // 触发快照导出 — 仅匹配函数名部分（避免 Blueprint _C 后缀导致类名不匹配）
    if (functionName.find("SaveGameData") != std::string::npos)
    {
        LoadoutManagerDetail::TriggerAsyncExport("player-save", 0);
    }
    else if (functionName.find("SavePreOrderGameSaved") != std::string::npos)
    {
        LoadoutManagerDetail::TriggerAsyncExport("preorder-save", 0);
    }
    else if (functionName.find("SelectInventoryItem") != std::string::npos)
    {
        LoadoutManagerDetail::TriggerAsyncExport("inventory-select", 0);
    }
    else if (functionName.find("OnEquipComplete") != std::string::npos ||
        functionName.find("OnEquipPartCompleted") != std::string::npos ||
        functionName.find("OnEquipWeaponOrnamentComplete") != std::string::npos ||
        functionName.find("K2_OnEquipPartCompleted") != std::string::npos)
    {
        LoadoutManagerDetail::TriggerAsyncExport("equip-complete", 0);
    }
    else if (functionName.find("ExitEditWeaponPart") != std::string::npos ||
        functionName.find("ExitWeaponOrnamentPanel") != std::string::npos ||
        functionName.find("K2_ExitEditWeaponPart") != std::string::npos ||
        functionName.find("K2_CaptureSelectRoleWeapons") != std::string::npos)
    {
        LoadoutManagerDetail::TriggerAsyncExport("customize-exit", 0);
    }
}

void LoadoutManager::OnServerProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    // 复活时重新推送出生前库存
    if (functionName.find("OnRestartInStartSpot") != std::string::npos)
    {
        APBPlayerController* playerController = nullptr;
        if (parms)
        {
            auto* restartParms = static_cast<Params::PBFieldModManager_OnRestartInStartSpot*>(parms);
            if (restartParms && restartParms->InController && restartParms->InController->IsA(APBPlayerController::StaticClass()))
            {
                playerController = static_cast<APBPlayerController*>(restartParms->InController);
            }
        }
        LoadoutManagerDetail::PushPreSpawnInventory(playerController);
    }

    // InitWeapon 参数覆盖 — 在武器初始化前将快照配件写入 InPartSaved
    LoadoutManagerDetail::MaybeOverrideInitWeaponConfig(object, functionName, parms);
}

void LoadoutManager::OnServerProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    // 重写后不再需要恢复武器定义覆盖 — 已彻底移除该机制。
}

// =====================================================================
//  公有接口 — Worker/Tick 桥接
// =====================================================================

void LoadoutManager::TickClient()
{
    LoadoutManagerDetail::ClientTick();
}

void LoadoutManager::TickServer()
{
    LoadoutManagerDetail::ServerTick();
}
