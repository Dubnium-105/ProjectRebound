// ======================================================
//  LoadoutManager implementation
// ======================================================
//
//  Loadout flow overview:
//    1. Preload snapshot sidecar JSON from disk during startup.
//    2. Observe menu/save/customization events and export fresh snapshots.
//    3. Observe runtime ProcessEvent traffic on both client and server.
//    4. Apply snapshot data back into live characters/weapons when safe.
//    5. Use scoped weapon-definition overrides only around the exact native
//       calls that need them, then restore state immediately.
//
//  Relationship to dllmain.cpp:
//    - dllmain.cpp owns hook registration and long-lived manager lifetime.
//    - This file owns the loadout pipeline detail, state machines, and
//      diagnostics so the hook layer remains readable.
//
//  Maintenance rule:
//    Keep behavior changes inside this translation unit whenever possible.
//    Hook sites should stay as thin forwarders into the public facade.

#include "LoadoutManager.h"

#include <Windows.h>

#include <algorithm>
#include <chrono>
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

#include "SDK.hpp"
#include "SDK/Engine_parameters.hpp"
#include "SDK/ProjectBoundary_parameters.hpp"
#include "json.hpp"

using namespace SDK;

std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
UObject* GetLastOfType(UClass* theClass, bool includeDefault);
void ClientLog(const std::string& msg);

extern bool LoginCompleted;
extern bool amServer;

class LoadoutManager::Impl
{
};

// Internal detail namespace:
//   The old loadout pipeline now lives here as private implementation detail.
//   The public LoadoutManager methods below are the only supported entry points
//   for the rest of Payload.
namespace LoadoutManagerDetail
{
    using json = nlohmann::json;

    struct State
    {
        std::mutex Mutex;
        json Snapshot;
        bool SnapshotAvailable = false;
        bool SnapshotLoadAttempted = false;
        bool MenuApplyComplete = false;
        bool PendingLiveApply = false;
        bool MenuSeen = false;
        bool InitialMenuCaptureComplete = false;
        bool InGameThreadTick = false;
        bool ExportScheduled = false;
        int LiveApplyAttempts = 0;
        std::chrono::steady_clock::time_point MenuSeenAt{};
        std::chrono::steady_clock::time_point NextWorkerTickAt{};
        std::chrono::steady_clock::time_point ExportDueAt{};
        std::chrono::steady_clock::time_point NextLiveApplyAt{};
        std::chrono::steady_clock::time_point NextServerTickAt{};
        std::string PendingExportReason;
        APBCharacter* LastObservedCharacter = nullptr;
        std::unordered_map<APBCharacter*, int> ServerLiveApplyAttempts;
        std::unordered_set<APBCharacter*> ServerLiveApplyComplete;
        std::unordered_map<APBWeapon*, std::string> WeaponVisualRefreshSignatures;
        std::unordered_map<APBWeapon*, std::string> WeaponInitOverrideSignatures;
        std::unordered_set<std::string> WeaponQueryOverrideLogs;
        std::unordered_set<std::string> WeaponDefinitionOverrideLogs;
        std::unordered_set<std::string> WeaponDefinitionOverrideFailureLogs;
        std::unordered_set<std::string> WeaponProcessEventProbeLogs;
        std::string LastMenuSelectedRoleId;
    };

    State& GetState()
    {
        static State StateInstance;
        return StateInstance;
    }

    std::wstring ToWide(const std::string& value)
    {
        return std::wstring(value.begin(), value.end());
    }

    std::string NameToString(const FName& value)
    {
        return value.ToString();
    }

    bool IsBlankText(const std::string& text)
    {
        return text.empty() || text == "None";
    }

    bool IsBlankName(const FName& value)
    {
        return value.ComparisonIndex <= 0;
    }

    bool CanSafelyResolveLoadedObjectName(const FName& value)
    {
        return value.ComparisonIndex > 0 && value.ComparisonIndex < 1000000 && value.Number < 1024;
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

    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig);
    bool TryResolveSnapshotWeaponConfigByRoleAndWeaponId(const json& snapshot, const std::string& roleId, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig);
    bool TryResolveSnapshotWeaponConfigForInit(const json& snapshot, APBWeapon* weapon, const FPBWeaponNetworkConfig& incoming, std::string& outResolvedRoleId, FPBWeaponNetworkConfig& outConfig);
    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config);
    std::string ResolveCharacterRoleId(APBCharacter* character);

    std::string GetSnapshotSelectedRoleId(const json& snapshot)
    {
        if (!snapshot.is_object())
        {
            return "";
        }

        return snapshot.value("selectedRoleId", "");
    }

    bool IsRoleAllowedBySnapshotSelection(const json& snapshot, const std::string& roleId)
    {
        if (roleId.empty())
        {
            return false;
        }

        const std::string selectedRoleId = GetSnapshotSelectedRoleId(snapshot);
        return selectedRoleId.empty() || roleId == selectedRoleId;
    }

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

        if (playerController->PBCharacter)
        {
            const std::string roleId = ResolveCharacterRoleId(playerController->PBCharacter);
            if (!roleId.empty())
            {
                return roleId;
            }
        }

        APBPlayerState* playerState = playerController->PBPlayerState;
        if (!playerState)
        {
            return "";
        }

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

    std::vector<APBCharacter*> GetServerPlayerCharacters()
    {
        std::vector<APBCharacter*> characters;
        std::unordered_set<APBCharacter*> seen;

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            APBCharacter* character = GetControllerCharacter(playerController);
            if (!character || seen.contains(character))
            {
                continue;
            }

            seen.insert(character);
            characters.push_back(character);
        }

        return characters;
    }

    bool HasArchiveLoaded()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (playerController)
        {
            return playerController->bHasLoadedArchive;
        }

        return LoginCompleted && GetFieldModManager() != nullptr;
    }

    bool IsUnsafeMenuApplyEnabled()
    {
        static const bool enabled = []() -> bool
        {
            const std::string commandLine = GetCommandLineA();
            if (commandLine.find("-unsafeMenuLoadoutApply") != std::string::npos)
            {
                return true;
            }

            char* envValue = nullptr;
            size_t envValueLength = 0;
            const bool envEnabled =
                _dupenv_s(&envValue, &envValueLength, "PROJECTREBOUND_ENABLE_UNSAFE_MENU_APPLY") == 0 &&
                envValue &&
                std::string(envValue) == "1";
            free(envValue);
            return envEnabled;
        }();

        return enabled;
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

    bool IsMenuApplyReady()
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

    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config)
    {
        return !IsBlankName(config.WeaponID) || !IsBlankName(config.WeaponClassID) || !IsBlankName(config.OrnamentID) || config.WeaponPartConfigs.Num() > 0;
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

    bool HasWeaponPartConfigData(const FPBWeaponPartNetworkConfig& config)
    {
        return !IsBlankName(config.WeaponPartID) ||
            !IsBlankName(config.WeaponPartSkinID) ||
            !IsBlankName(config.WeaponPartSpecialSkinID) ||
            !IsBlankName(config.WeaponPartSkinPaintingID);
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
        const bool canBorrowCosmetics =
            IsBlankName(target.WeaponPartID) ||
            IsBlankName(fallback.WeaponPartID) ||
            NameToString(target.WeaponPartID) == NameToString(fallback.WeaponPartID);

        if (IsBlankName(target.WeaponPartID))
        {
            target.WeaponPartID = fallback.WeaponPartID;
        }

        if (IsBlankName(target.WeaponPartSkinID))
        {
            target.WeaponPartSkinID = fallback.WeaponPartSkinID;
        }

        if (canBorrowCosmetics && IsBlankName(target.WeaponPartSpecialSkinID))
        {
            target.WeaponPartSpecialSkinID = fallback.WeaponPartSpecialSkinID;
        }

        if (canBorrowCosmetics && IsBlankName(target.WeaponPartSkinPaintingID))
        {
            target.WeaponPartSkinPaintingID = fallback.WeaponPartSkinPaintingID;
        }
    }

    FPBWeaponNetworkConfig MergeWeaponConfigPreferPrimary(const FPBWeaponNetworkConfig& primary, const FPBWeaponNetworkConfig& fallback)
    {
        FPBWeaponNetworkConfig merged = primary;

        if (IsBlankName(merged.WeaponID))
        {
            merged.WeaponID = fallback.WeaponID;
        }

        if (IsBlankName(merged.WeaponClassID))
        {
            merged.WeaponClassID = fallback.WeaponClassID;
        }

        if (IsBlankName(merged.OrnamentID))
        {
            merged.OrnamentID = fallback.OrnamentID;
        }

        std::vector<EPBPartSlotType> slotOrder;
        auto appendSlots = [&](const FPBWeaponNetworkConfig& config)
        {
            const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
            for (int index = 0; index < count; ++index)
            {
                const EPBPartSlotType slotType = config.WeaponPartSlotTypeArray[index];
                if (std::find(slotOrder.begin(), slotOrder.end(), slotType) == slotOrder.end())
                {
                    slotOrder.push_back(slotType);
                }
            }
        };

        appendSlots(primary);
        appendSlots(fallback);

        merged.WeaponPartSlotTypeArray.Clear();
        merged.WeaponPartConfigs.Clear();

        for (const EPBPartSlotType slotType : slotOrder)
        {
            FPBWeaponPartNetworkConfig primaryPart{};
            FPBWeaponPartNetworkConfig fallbackPart{};
            const bool hasPrimaryPart = TryGetWeaponPartConfigForSlotLocal(primary, slotType, primaryPart);
            const bool hasFallbackPart = TryGetWeaponPartConfigForSlotLocal(fallback, slotType, fallbackPart);
            if (!hasPrimaryPart && !hasFallbackPart)
            {
                continue;
            }

            FPBWeaponPartNetworkConfig mergedPart = hasPrimaryPart ? primaryPart : fallbackPart;
            if (hasPrimaryPart && hasFallbackPart)
            {
                FillBlankWeaponPartFieldsFromFallback(mergedPart, fallbackPart);
            }

            if (!HasWeaponPartConfigData(mergedPart))
            {
                continue;
            }

            merged.WeaponPartSlotTypeArray.Add(slotType);
            merged.WeaponPartConfigs.Add(mergedPart);
        }

        return merged;
    }

    void SetRoleWeaponJson(json& role, const char* key, const FPBWeaponNetworkConfig& config, bool preferIncoming = true)
    {
        FPBWeaponNetworkConfig existingConfig{};
        const bool hasExistingConfig =
            role.contains(key) &&
            role[key].is_object() &&
            WeaponFromJson(role[key], existingConfig) &&
            HasWeaponConfig(existingConfig);

        FPBWeaponNetworkConfig mergedConfig{};
        if (HasWeaponConfig(config))
        {
            mergedConfig = hasExistingConfig
                ? (preferIncoming ? MergeWeaponConfigPreferPrimary(config, existingConfig) : MergeWeaponConfigPreferPrimary(existingConfig, config))
                : config;
        }
        else if (hasExistingConfig)
        {
            mergedConfig = existingConfig;
        }

        if (HasWeaponConfig(mergedConfig))
        {
            role[key] = WeaponToJson(mergedConfig);
        }
    }

    bool TryGetInventoryForRole(UPBFieldModManager* fieldModManager, const FName& roleId, FPBInventoryNetworkConfig& outInventory)
    {
        if (!fieldModManager)
        {
            return false;
        }

        for (auto& pair : fieldModManager->CharacterPreOrderingInventoryConfigs)
        {
            if (pair.Key() == roleId)
            {
                outInventory = pair.Value();
                return true;
            }
        }

        return false;
    }

    std::string ResolvePreferredSelectedRoleId(UPBFieldModManager* fieldModManager, const json& existingSnapshot, const std::unordered_map<std::string, json>& rolesById)
    {
        auto resolveCandidate = [&](const std::string& candidate) -> std::string
        {
            const std::string normalizedRoleId = NormalizeRoleId(candidate);
            if (normalizedRoleId.empty())
            {
                return "";
            }

            if (!rolesById.empty() && !rolesById.contains(normalizedRoleId))
            {
                return "";
            }

            return normalizedRoleId;
        };

        if (fieldModManager)
        {
            if (const std::string selectedRoleId = resolveCandidate(NameToString(fieldModManager->GetSelectCharacterID())); !selectedRoleId.empty())
            {
                return selectedRoleId;
            }
        }

        if (const std::string localPlayerRoleId = resolveCandidate(GetLocalPlayerPreferredRoleId()); !localPlayerRoleId.empty())
        {
            return localPlayerRoleId;
        }

        if (const std::string rememberedRoleId = resolveCandidate(GetRememberedMenuSelectedRoleId()); !rememberedRoleId.empty())
        {
            return rememberedRoleId;
        }

        if (existingSnapshot.is_object())
        {
            if (const std::string snapshotRoleId = resolveCandidate(existingSnapshot.value("selectedRoleId", "")); !snapshotRoleId.empty())
            {
                return snapshotRoleId;
            }
        }

        if (rolesById.size() == 1)
        {
            return rolesById.begin()->first;
        }

        return "";
    }

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

            if (!rolesById.contains(roleId))
            {
                rolesById[roleId] = EmptyRoleJson(roleId);
            }

            rolesById[roleId]["inventory"] = InventoryToJson(pair.Value());

            const bool capturedFromDisplay = rolesCapturedFromDisplay.contains(roleId) && rolesCapturedFromDisplay[roleId];
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

        const std::string selectedRoleId = ResolvePreferredSelectedRoleId(fieldModManager, existingSnapshot, rolesById);

        return json{
            { "schemaVersion", 1 },
            { "savedAtUtc", BuildUtcTimestamp() },
            { "gameVersion", "unknown" },
            { "source", "payload" },
            { "selectedRoleId", selectedRoleId },
            { "roles", roles }
        };
    }

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
            const std::string selectedRoleId = NormalizeRoleId(snapshot.value("selectedRoleId", ""));
            if (!selectedRoleId.empty())
            {
                state.LastMenuSelectedRoleId = selectedRoleId;
            }
        }

        ClientLog("[LOADOUT] Exported local snapshot (" + reason + ") -> " + exportPath.string());
        return true;
    }

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
        const std::filesystem::path launchPath = GetLaunchSnapshotPath();
        const std::filesystem::path exportPath = GetExportSnapshotPath();

        if (ReadJsonFile(launchPath, loadedSnapshot))
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

        {
            std::scoped_lock lock(state.Mutex);
            state.Snapshot = loadedSnapshot;
            state.SnapshotAvailable = true;
            state.PendingLiveApply = true;
            const std::string selectedRoleId = NormalizeRoleId(loadedSnapshot.value("selectedRoleId", ""));
            if (!selectedRoleId.empty())
            {
                state.LastMenuSelectedRoleId = selectedRoleId;
            }
        }

        ClientLog("[LOADOUT] Loaded snapshot from " + source);
    }

    void PreloadSnapshot()
    {
        EnsureSnapshotLoaded();
    }

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

    bool ApplySnapshotToFieldModManager(const json& snapshot)
    {
        UPBFieldModManager* fieldModManager = GetFieldModManager();
        if (!fieldModManager)
        {
            return false;
        }

        const auto inventories = BuildInventoryListFromSnapshot(snapshot);
        if (inventories.empty())
        {
            return false;
        }

        const std::string originalRoleId = NameToString(fieldModManager->GetSelectCharacterID());
        APBPlayerController* playerController = GetLocalPlayerController();

        for (const auto& [roleId, inventory] : inventories)
        {
            const FName roleName = NameFromString(roleId);
            fieldModManager->SelectCharacter(roleName);

            const int count = (std::min)(inventory.CharacterSlots.Num(), inventory.InventoryItems.Num());
            for (int index = 0; index < count; ++index)
            {
                if (IsBlankName(inventory.InventoryItems[index]))
                {
                    continue;
                }

                fieldModManager->SelectCharacterSlot(inventory.CharacterSlots[index]);
                fieldModManager->SelectInventoryItem(inventory.InventoryItems[index]);
            }

            if (playerController)
            {
                playerController->ServerPreOrderInventory(roleName, inventory);
                if (playerController->PBPlayerState)
                {
                    playerController->PBPlayerState->ClientRefreshRolePreOrderingInventory(roleName, inventory);
                    playerController->PBPlayerState->ClientRefreshRoleEquippingInventory(roleName, inventory);
                }
            }
        }

        std::string targetRoleId = NormalizeRoleId(snapshot.value("selectedRoleId", ""));
        if (targetRoleId.empty())
        {
            targetRoleId = NormalizeRoleId(originalRoleId);
        }
        if (targetRoleId.empty())
        {
            targetRoleId = GetRememberedMenuSelectedRoleId();
        }
        if (!targetRoleId.empty())
        {
            fieldModManager->SelectCharacter(NameFromString(targetRoleId));
        }

        ClientLog("[LOADOUT] Applied snapshot inventory to FieldModManager. Live actors will receive remaining cosmetic fields once spawned.");
        return true;
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

    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig)
    {
        json roleJson;
        if (snapshot.contains("roles") && snapshot["roles"].is_array())
        {
            for (const auto& role : snapshot["roles"])
            {
                if (!role.is_object())
                {
                    continue;
                }

                if (!roleId.empty() && role.value("roleId", "") == roleId)
                {
                    roleJson = role;
                    break;
                }
            }

            if (!roleJson.is_object())
            {
                const std::string selectedRoleId = snapshot.value("selectedRoleId", "");
                for (const auto& role : snapshot["roles"])
                {
                    if (role.is_object() && role.value("roleId", "") == selectedRoleId)
                    {
                        roleJson = role;
                        break;
                    }
                }
            }

            if (!roleJson.is_object() && !snapshot["roles"].empty())
            {
                roleJson = snapshot["roles"][0];
            }
        }

        if (!roleJson.is_object())
        {
            return false;
        }

        outConfig = {};
        outConfig.CharacterID = NameFromString(roleJson.value("roleId", roleId));
        CharacterFromJson(roleJson.value("characterData", EmptyCharacterJson()), outConfig.CharacterData);
        WeaponFromJson(roleJson.value("firstWeapon", EmptyWeaponJson()), outConfig.FirstWeaponPartData);
        WeaponFromJson(roleJson.value("secondWeapon", EmptyWeaponJson()), outConfig.SecondWeaponPartData);
        MeleeFromJson(roleJson.value("meleeWeapon", EmptyMeleeJson()), outConfig.MeleeWeaponData);
        LauncherFromJson(roleJson.value("leftLauncher", EmptyLauncherJson()), outConfig.LeftLauncherData);
        LauncherFromJson(roleJson.value("rightLauncher", EmptyLauncherJson()), outConfig.RightLauncherData);
        MobilityFromJson(roleJson.value("mobilityModule", EmptyMobilityJson()), outConfig.MobilityModuleData);
        InventoryFromJson(roleJson.value("inventory", EmptyInventoryJson()), outConfig.InventoryData);
        return true;
    }

    bool WeaponPartConfigEquals(const FPBWeaponPartNetworkConfig& left, const FPBWeaponPartNetworkConfig& right)
    {
        return NameToString(left.WeaponPartID) == NameToString(right.WeaponPartID) &&
            NameToString(left.WeaponPartSkinID) == NameToString(right.WeaponPartSkinID) &&
            NameToString(left.WeaponPartSpecialSkinID) == NameToString(right.WeaponPartSpecialSkinID) &&
            NameToString(left.WeaponPartSkinPaintingID) == NameToString(right.WeaponPartSkinPaintingID);
    }

    bool WeaponConfigEquals(const FPBWeaponNetworkConfig& left, const FPBWeaponNetworkConfig& right)
    {
        if (NameToString(left.OrnamentID) != NameToString(right.OrnamentID) ||
            NameToString(left.WeaponID) != NameToString(right.WeaponID) ||
            NameToString(left.WeaponClassID) != NameToString(right.WeaponClassID) ||
            left.WeaponPartSlotTypeArray.Num() != right.WeaponPartSlotTypeArray.Num() ||
            left.WeaponPartConfigs.Num() != right.WeaponPartConfigs.Num())
        {
            return false;
        }

        const int slotCount = left.WeaponPartSlotTypeArray.Num();
        for (int index = 0; index < slotCount; ++index)
        {
            if (left.WeaponPartSlotTypeArray[index] != right.WeaponPartSlotTypeArray[index])
            {
                return false;
            }
        }

        const int partCount = left.WeaponPartConfigs.Num();
        for (int index = 0; index < partCount; ++index)
        {
            if (!WeaponPartConfigEquals(left.WeaponPartConfigs[index], right.WeaponPartConfigs[index]))
            {
                return false;
            }
        }

        return true;
    }

    bool CharacterConfigEquals(const FPBCharacterNetworkConfig& left, const FPBCharacterNetworkConfig& right)
    {
        if (NameToString(left.SkinPaintingID) != NameToString(right.SkinPaintingID) ||
            left.SkinClassArray.Num() != right.SkinClassArray.Num() ||
            left.SkinIDArray.Num() != right.SkinIDArray.Num())
        {
            return false;
        }

        for (int index = 0; index < left.SkinClassArray.Num(); ++index)
        {
            if (left.SkinClassArray[index] != right.SkinClassArray[index])
            {
                return false;
            }
        }

        for (int index = 0; index < left.SkinIDArray.Num(); ++index)
        {
            if (NameToString(left.SkinIDArray[index]) != NameToString(right.SkinIDArray[index]))
            {
                return false;
            }
        }

        return true;
    }

    bool LauncherConfigEquals(const FPBLauncherNetworkConfig& left, const FPBLauncherNetworkConfig& right)
    {
        return NameToString(left.ID) == NameToString(right.ID) &&
            NameToString(left.SkinID) == NameToString(right.SkinID);
    }

    bool MeleeConfigEquals(const FPBMeleeWeaponNetworkConfig& left, const FPBMeleeWeaponNetworkConfig& right)
    {
        return NameToString(left.ID) == NameToString(right.ID) &&
            NameToString(left.SkinID) == NameToString(right.SkinID);
    }

    bool MobilityConfigEquals(const FPBMobilityModuleNetworkConfig& left, const FPBMobilityModuleNetworkConfig& right)
    {
        return NameToString(left.MobilityModuleID) == NameToString(right.MobilityModuleID);
    }

    void MarkActorForReplication(AActor* actor)
    {
        if (!actor)
        {
            return;
        }

        actor->FlushNetDormancy();
        actor->ForceNetUpdate();
    }

    std::string ResolveCharacterRoleId(APBCharacter* character)
    {
        if (!character)
        {
            return "";
        }

        if (!IsBlankName(character->CharacterID))
        {
            return NameToString(character->CharacterID);
        }

        APBPlayerState* playerState = character->PBPlayerState;
        if (playerState)
        {
            if (!IsBlankName(playerState->PossessedCharacterId))
            {
                return NameToString(playerState->PossessedCharacterId);
            }

            if (!IsBlankName(playerState->SelectedCharacterID))
            {
                return NameToString(playerState->SelectedCharacterID);
            }

            if (!IsBlankName(playerState->UsageCharacterID))
            {
                return NameToString(playerState->UsageCharacterID);
            }
        }

        return "";
    }

    std::vector<APBWeapon*> GetCharacterWeaponList(APBCharacter* character)
    {
        std::vector<APBWeapon*> weapons;
        if (!character)
        {
            return weapons;
        }

        for (int index = 0; index < character->Inventory.Num(); ++index)
        {
            APBWeapon* weapon = character->Inventory[index];
            if (!weapon)
            {
                continue;
            }

            APBCharacter* owner = nullptr;
            try
            {
                owner = weapon->GetPawnOwner();
            }
            catch (...)
            {
                owner = nullptr;
            }

            if (!owner || owner == character)
            {
                weapons.push_back(weapon);
            }
        }

        return weapons;
    }

    std::string DescribeWeaponConfig(const FPBWeaponNetworkConfig& config)
    {
        std::ostringstream stream;
        stream << "weaponId=" << NameToString(config.WeaponID)
            << ", classId=" << NameToString(config.WeaponClassID)
            << ", ornament=" << NameToString(config.OrnamentID)
            << ", parts=" << config.WeaponPartConfigs.Num();
        return stream.str();
    }

    std::string DescribeWeaponSlotMap(APBWeapon* weapon);
    bool RefreshWeaponRuntimeVisualsOnce(APBWeapon* weapon, const FPBWeaponNetworkConfig& config);

    bool TryGetWeaponPartConfigForSlot(const FPBWeaponNetworkConfig& config, EPBPartSlotType slotType, FPBWeaponPartNetworkConfig& outConfig)
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

    FPBWeaponPartLoadout BuildWeaponPartLoadout(const FPBWeaponNetworkConfig& weaponConfig, const FPBWeaponPartNetworkConfig& partConfig)
    {
        FPBWeaponPartLoadout loadout{};
        loadout.ID = partConfig.WeaponPartID;
        loadout.SkinID = partConfig.WeaponPartSkinID;
        loadout.SkinPaintingID = partConfig.WeaponPartSkinPaintingID;
        loadout.SpecileSlotID = partConfig.WeaponPartSpecialSkinID;
        loadout.OrnamentID = weaponConfig.OrnamentID;
        return loadout;
    }

    bool WeaponPartLoadoutEquals(const FPBWeaponPartLoadout& loadout, const FPBWeaponNetworkConfig& weaponConfig, const FPBWeaponPartNetworkConfig& targetPart)
    {
        return NameToString(loadout.ID) == NameToString(targetPart.WeaponPartID) &&
            NameToString(loadout.SkinID) == NameToString(targetPart.WeaponPartSkinID) &&
            NameToString(loadout.SkinPaintingID) == NameToString(targetPart.WeaponPartSkinPaintingID) &&
            NameToString(loadout.SpecileSlotID) == NameToString(targetPart.WeaponPartSpecialSkinID) &&
            NameToString(loadout.OrnamentID) == NameToString(weaponConfig.OrnamentID);
    }

    std::string DescribeWeaponPartLoadout(const FPBWeaponPartLoadout& loadout)
    {
        std::ostringstream stream;
        stream << NameToString(loadout.ID)
            << "/" << NameToString(loadout.SkinID)
            << "/" << NameToString(loadout.SkinPaintingID)
            << "/" << NameToString(loadout.SpecileSlotID)
            << "/" << NameToString(loadout.OrnamentID);
        return stream.str();
    }

    std::string DescribeWeaponParts(const FPBWeaponNetworkConfig& config)
    {
        std::ostringstream stream;
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        for (int index = 0; index < count; ++index)
        {
            const FPBWeaponPartNetworkConfig& part = config.WeaponPartConfigs[index];
            if (index > 0)
            {
                stream << " | ";
            }

            stream << static_cast<int>(config.WeaponPartSlotTypeArray[index])
                << ":" << NameToString(part.WeaponPartID)
                << "/" << NameToString(part.WeaponPartSkinID)
                << "/" << NameToString(part.WeaponPartSkinPaintingID)
                << "/" << NameToString(part.WeaponPartSpecialSkinID);
        }

        return stream.str();
    }

    std::string BuildWeaponConfigSignature(const FPBWeaponNetworkConfig& config)
    {
        std::ostringstream stream;
        stream << NameToString(config.WeaponID)
            << "|" << NameToString(config.WeaponClassID)
            << "|" << NameToString(config.OrnamentID)
            << "|" << DescribeWeaponParts(config);
        return stream.str();
    }

    std::vector<std::string> BuildWeaponDefinitionCandidateIds(const FPBWeaponNetworkConfig& config)
    {
        std::vector<std::string> candidates;
        auto pushUnique = [&](const std::string& candidate)
        {
            if (IsBlankText(candidate))
            {
                return;
            }

            if (std::find(candidates.begin(), candidates.end(), candidate) == candidates.end())
            {
                candidates.push_back(candidate);
            }
        };

        pushUnique(NameToString(config.WeaponClassID));

        const std::string weaponId = NameToString(config.WeaponID);
        pushUnique(weaponId);

        const size_t prefixSplit = weaponId.find('_');
        if (prefixSplit != std::string::npos && prefixSplit + 1 < weaponId.size())
        {
            pushUnique(weaponId.substr(prefixSplit + 1));
        }

        return candidates;
    }

    template<typename TObjectType>
    TObjectType* LoadSoftObjectPtrBlocking(const TSoftObjectPtr<TObjectType>& softPtr)
    {
        if (TObjectType* loaded = softPtr.Get())
        {
            return loaded;
        }

        const TSoftObjectPtr<UObject>& genericPtr =
            reinterpret_cast<const TSoftObjectPtr<UObject>&>(softPtr);
        return static_cast<TObjectType*>(UKismetSystemLibrary::LoadAsset_Blocking(genericPtr));
    }

    std::string JoinStrings(const std::vector<std::string>& values, const std::string& delimiter)
    {
        std::ostringstream stream;
        for (size_t index = 0; index < values.size(); ++index)
        {
            if (index > 0)
            {
                stream << delimiter;
            }

            stream << values[index];
        }

        return stream.str();
    }

    UPBDataTableManager* GetDataTableManager(std::string* outSource = nullptr)
    {
        if (UObject* object = GetLastOfType(UPBDataTableManager::StaticClass(), false))
        {
            if (outSource)
            {
                *outSource = "runtime-last";
            }
            return static_cast<UPBDataTableManager*>(object);
        }

        if (UObject* object = GetLastOfType(UPBDataTableManager::StaticClass(), true))
        {
            if (outSource)
            {
                *outSource = "default-last";
            }
            return static_cast<UPBDataTableManager*>(object);
        }

        if (UPBDataTableManager* defaultObject = UPBDataTableManager::GetDefaultObj())
        {
            if (outSource)
            {
                *outSource = "class-default-object";
            }
            return defaultObject;
        }

        return nullptr;
    }

    template<typename TRow>
    TRow* FindDataTableRowPtrRecursive(UDataTable* table, const FName& rowId, std::unordered_set<UDataTable*>& visitedTables)
    {
        if (!table || IsBlankName(rowId) || !visitedTables.insert(table).second)
        {
            return nullptr;
        }

        for (auto& pair : table->RowMap)
        {
            if (pair.Key() == rowId && pair.Value())
            {
                return reinterpret_cast<TRow*>(pair.Value());
            }
        }

        if (table->IsA(UCompositeDataTable::StaticClass()))
        {
            UCompositeDataTable* composite = static_cast<UCompositeDataTable*>(table);
            for (UDataTable* parentTable : composite->ParentTables)
            {
                if (TRow* row = FindDataTableRowPtrRecursive<TRow>(parentTable, rowId, visitedTables))
                {
                    return row;
                }
            }
        }

        return nullptr;
    }

    template<typename TRow>
    TRow* FindDataTableRowPtr(UDataTable* table, const FName& rowId)
    {
        std::unordered_set<UDataTable*> visitedTables;
        return FindDataTableRowPtrRecursive<TRow>(table, rowId, visitedTables);
    }

    std::vector<std::string> ExpandWeaponDefinitionRedirectCandidates(
        UPBDataTableManager* dataTableManager,
        const std::vector<std::string>& initialCandidates)
    {
        std::vector<std::string> expandedCandidates;
        auto pushUnique = [&](const std::string& candidate)
        {
            if (IsBlankText(candidate))
            {
                return;
            }

            if (std::find(expandedCandidates.begin(), expandedCandidates.end(), candidate) == expandedCandidates.end())
            {
                expandedCandidates.push_back(candidate);
            }
        };

        for (const std::string& candidate : initialCandidates)
        {
            pushUnique(candidate);
        }

        if (!dataTableManager)
        {
            return expandedCandidates;
        }

        UDataTable* redirectTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponRedirectionDataTable);
        if (!redirectTable)
        {
            return expandedCandidates;
        }

        for (size_t index = 0; index < expandedCandidates.size(); ++index)
        {
            const std::string& candidate = expandedCandidates[index];

            if (FPBWeaponDefinitionRedirectionRow* redirectRow =
                FindDataTableRowPtr<FPBWeaponDefinitionRedirectionRow>(redirectTable, NameFromString(candidate)))
            {
                pushUnique(NameToString(redirectRow->WeaponDefinitionID));
            }

            for (auto& pair : redirectTable->RowMap)
            {
                if (!pair.Value())
                {
                    continue;
                }

                FPBWeaponDefinitionRedirectionRow* redirectRow =
                    reinterpret_cast<FPBWeaponDefinitionRedirectionRow*>(pair.Value());
                if (NameToString(redirectRow->WeaponDefinitionID) != candidate)
                {
                    continue;
                }

                pushUnique(NameToString(pair.Key()));
            }
        }

        return expandedCandidates;
    }

    FPBWeaponDefinitionRow* FindWeaponDefinitionRow(
        UPBDataTableManager* dataTableManager,
        const FPBWeaponNetworkConfig& targetConfig,
        std::string* outResolvedRowId,
        std::vector<std::string>* outCandidatesTried = nullptr,
        std::string* outFailureReason = nullptr)
    {
        if (outResolvedRowId)
        {
            outResolvedRowId->clear();
        }

        if (outCandidatesTried)
        {
            outCandidatesTried->clear();
        }

        if (outFailureReason)
        {
            outFailureReason->clear();
        }

        if (!dataTableManager)
        {
            if (outFailureReason)
            {
                *outFailureReason = "missing-data-table-manager";
            }
            return nullptr;
        }

        UDataTable* weaponDefinitionTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponDefinitionDataTable);
        if (!weaponDefinitionTable)
        {
            if (outFailureReason)
            {
                *outFailureReason = "missing-weapon-definition-table";
            }
            return nullptr;
        }

        const std::vector<std::string> candidates =
            ExpandWeaponDefinitionRedirectCandidates(dataTableManager, BuildWeaponDefinitionCandidateIds(targetConfig));
        if (outCandidatesTried)
        {
            *outCandidatesTried = candidates;
        }

        for (const std::string& candidate : candidates)
        {
            if (FPBWeaponDefinitionRow* row = FindDataTableRowPtr<FPBWeaponDefinitionRow>(weaponDefinitionTable, NameFromString(candidate)))
            {
                if (outResolvedRowId)
                {
                    *outResolvedRowId = candidate;
                }
                return row;
            }
        }

        if (outFailureReason)
        {
            *outFailureReason = "weapon-definition-row-not-found";
        }

        return nullptr;
    }

    template<typename TObjectType>
    TObjectType* FindLoadedNamedObject(const std::string& expectedName)
    {
        if (expectedName.empty())
        {
            return nullptr;
        }

        TObjectType* fuzzyMatch = nullptr;
        for (UObject* object : getObjectsOfClass(TObjectType::StaticClass(), false))
        {
            if (!object)
            {
                continue;
            }

            if (object->GetName() == expectedName)
            {
                return static_cast<TObjectType*>(object);
            }

            if (!fuzzyMatch && object->GetFullName().find(expectedName) != std::string::npos)
            {
                fuzzyMatch = static_cast<TObjectType*>(object);
            }
        }

        return fuzzyMatch;
    }

    bool TryFindPartSlotNameMapValuePointer(
        TMap<EPBPartSlotType, FName>& map,
        EPBPartSlotType slotType,
        FName** outValue);
    bool TryReadPartSlotNameValue(FName* valuePtr, FName& outValue);
    bool TryWritePartSlotNameValue(FName* valuePtr, const FName& value);

    bool UpdatePartSlotNameMapEntry(TMap<EPBPartSlotType, FName>& map, EPBPartSlotType slotType, const FName& targetValue, std::string* outPreviousValue = nullptr)
    {
        if (IsBlankName(targetValue))
        {
            return false;
        }

        FName* currentValuePtr = nullptr;
        if (!TryFindPartSlotNameMapValuePointer(map, slotType, &currentValuePtr))
        {
            return false;
        }

        FName previousValueName{};
        if (!TryReadPartSlotNameValue(currentValuePtr, previousValueName))
        {
            return false;
        }

        const std::string previousValue = NameToString(previousValueName);
        if (outPreviousValue)
        {
            *outPreviousValue = previousValue;
        }

        if (previousValueName == targetValue)
        {
            return false;
        }

        return TryWritePartSlotNameValue(currentValuePtr, targetValue);
    }

    struct PartSlotNameBackup
    {
        EPBPartSlotType SlotType{};
        FName PreviousValue{};
    };

    // Some live weapon-definition assets expose Unreal container state that is
    // readable most of the time but can transiently point at invalid memory
    // during load/replication. Keep raw TMap traversal behind tiny guarded
    // helpers so a bad native container degrades to "skip override" instead of
    // crashing the whole client/server.
    bool TryFindPartSlotNameMapValuePointer(
        TMap<EPBPartSlotType, FName>& map,
        EPBPartSlotType slotType,
        FName** outValue)
    {
        if (outValue)
        {
            *outValue = nullptr;
        }

        __try
        {
            if (!map.IsValid())
            {
                return false;
            }

            const int32 numAllocated = map.NumAllocated();
            const int32 numElements = map.Num();
            if (numAllocated <= 0 || numAllocated > 512 || numElements < 0 || numElements > numAllocated)
            {
                return false;
            }

            const auto& allocationFlags = map.GetAllocationFlags();
            if (!allocationFlags.IsValid() || allocationFlags.Num() < numAllocated)
            {
                return false;
            }

            for (int32 index = 0; index < numAllocated; ++index)
            {
                if (!map.IsValidIndex(index))
                {
                    continue;
                }

                auto& pair = map[index];
                if (pair.Key() != slotType)
                {
                    continue;
                }

                if (outValue)
                {
                    *outValue = &pair.Value();
                }
                return true;
            }
        }
        __except (EXCEPTION_EXECUTE_HANDLER)
        {
            if (outValue)
            {
                *outValue = nullptr;
            }
        }

        return false;
    }

    bool TryReadPartSlotNameValue(FName* valuePtr, FName& outValue)
    {
        if (!valuePtr)
        {
            return false;
        }

        __try
        {
            outValue = *valuePtr;
            return true;
        }
        __except (EXCEPTION_EXECUTE_HANDLER)
        {
        }

        return false;
    }

    bool TryWritePartSlotNameValue(FName* valuePtr, const FName& value)
    {
        if (!valuePtr)
        {
            return false;
        }

        __try
        {
            *valuePtr = value;
            return true;
        }
        __except (EXCEPTION_EXECUTE_HANDLER)
        {
        }

        return false;
    }

    bool UpdatePartSlotNameMapEntryWithBackup(
        TMap<EPBPartSlotType, FName>& map,
        EPBPartSlotType slotType,
        const FName& targetValue,
        std::vector<PartSlotNameBackup>* backups,
        std::string* outPreviousValue = nullptr)
    {
        if (IsBlankName(targetValue))
        {
            return false;
        }

        FName* currentValuePtr = nullptr;
        if (!TryFindPartSlotNameMapValuePointer(map, slotType, &currentValuePtr))
        {
            return false;
        }

        FName previousValueName{};
        if (!TryReadPartSlotNameValue(currentValuePtr, previousValueName))
        {
            return false;
        }

        const std::string previousValue = NameToString(previousValueName);
        if (outPreviousValue)
        {
            *outPreviousValue = previousValue;
        }

        if (previousValueName == targetValue)
        {
            return false;
        }

        if (backups)
        {
            backups->push_back({ slotType, previousValueName });
        }

        return TryWritePartSlotNameValue(currentValuePtr, targetValue);
    }

    void RestorePartSlotNameMapEntries(TMap<EPBPartSlotType, FName>& map, const std::vector<PartSlotNameBackup>& backups)
    {
        for (auto it = backups.rbegin(); it != backups.rend(); ++it)
        {
            FName* currentValuePtr = nullptr;
            if (!TryFindPartSlotNameMapValuePointer(map, it->SlotType, &currentValuePtr))
            {
                continue;
            }

            TryWritePartSlotNameValue(currentValuePtr, it->PreviousValue);
        }
    }

    struct WeaponDefinitionOverrideContext
    {
        UPBWeaponDefaultConfig* DefaultConfig = nullptr;
        std::vector<PartSlotNameBackup> DefaultConfigEquippedPartIDMapBackups;
        bool DefaultConfigWeaponOrnamentChanged = false;
        FName DefaultConfigWeaponOrnament{};

        FPBWeaponSuiteDefinitionRow* SuitRow = nullptr;
        std::vector<PartSlotNameBackup> SuitRowPartSlotAndIDMapBackups;
        std::vector<PartSlotNameBackup> SuitRowPartSlotAndSkinIDMapBackups;
        bool SuitRowWeaponOrnamentChanged = false;
        FName SuitRowWeaponOrnamentId{};
        bool SuitRowCrossHairChanged = false;
        FName SuitRowCrossHairId{};

        FPBWeaponSuitePaintingDefinitionRow* SuitPaintingRow = nullptr;
        std::vector<PartSlotNameBackup> SuitPaintingRowSlotAndWeaponPartSkinPaintingIDMapBackups;

        UPBWeaponSuit* SuitAsset = nullptr;
        std::vector<PartSlotNameBackup> SuitAssetPartSlotAndIDMapBackups;
        std::vector<PartSlotNameBackup> SuitAssetPartSlotAndSkinIDMapBackups;
        bool SuitAssetWeaponOrnamentChanged = false;
        FName SuitAssetWeaponOrnamentId{};
        bool SuitAssetCrossHairChanged = false;
        FName SuitAssetCrossHairId{};

        UPBWeaponSuitPaintingCustom* SuitPaintingAsset = nullptr;
        std::vector<PartSlotNameBackup> SuitPaintingAssetSlotAndWeaponPartSkinPaintingIDMapBackups;
    };

    thread_local std::vector<WeaponDefinitionOverrideContext> ScopedWeaponDefinitionOverrides;

    bool ShouldLogWeaponDefinitionOverride(const std::string& source, const FPBWeaponNetworkConfig& config)
    {
        const std::string key = source + "|" + BuildWeaponConfigSignature(config);
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponDefinitionOverrideLogs.insert(key).second;
    }

    bool ShouldLogWeaponDefinitionOverrideFailure(
        const std::string& source,
        const FPBWeaponNetworkConfig& config,
        const std::string& reason)
    {
        const std::string key = source + "|" + BuildWeaponConfigSignature(config) + "|" + reason;
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponDefinitionOverrideFailureLogs.insert(key).second;
    }

    bool ShouldLogWeaponProcessEventProbe(const std::string& key)
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponProcessEventProbeLogs.insert(key).second;
    }

    bool TryResolveSnapshotWeaponConfigForLiveWeapon(
        const json& snapshot,
        APBWeapon* weapon,
        std::string& outRoleId,
        FPBWeaponNetworkConfig& outConfig)
    {
        outRoleId.clear();
        if (!weapon)
        {
            return false;
        }

        if (TryResolveSnapshotWeaponConfigForInit(snapshot, weapon, weapon->PartNetworkConfig, outRoleId, outConfig))
        {
            return true;
        }

        const FPBWeaponNetworkConfig nativeSaveConfig = weapon->GetPBWeaponPartSaveConfig();
        return TryResolveSnapshotWeaponConfigForInit(snapshot, weapon, nativeSaveConfig, outRoleId, outConfig);
    }

    void MaybeLogWeaponProcessEventProbe(UObject* object, const std::string& functionName, void* parms, const json* snapshot)
    {
        if (!object || functionName.empty())
        {
            return;
        }

        auto containsAny = [&](std::initializer_list<const char*> patterns) -> bool
        {
            for (const char* pattern : patterns)
            {
                if (functionName.contains(pattern))
                {
                    return true;
                }
            }

            return false;
        };

        APBWeapon* probedWeapon = nullptr;
        std::string channel;

        if (object->IsA(APBWeapon::StaticClass()) &&
            containsAny({ "InitWeapon", "OnRep_PartNetworkConfig", "K2_InitSimulatedPartsComplete", "NotifyEquipFinished", "K2_RefreshSkin" }))
        {
            probedWeapon = static_cast<APBWeapon*>(object);
            channel = "weapon";
        }
        else if (object->IsA(APBCharacter::StaticClass()) &&
            containsAny({ "AddWeapon", "EquipPendingWeapon", "OnRep_PendingWeapon" }))
        {
            if (functionName.contains("PBCharacter.AddWeapon") && parms)
            {
                probedWeapon = static_cast<Params::PBCharacter_AddWeapon*>(parms)->Weapon;
            }
            else if (functionName.contains("PBCharacter.EquipPendingWeapon") && parms)
            {
                probedWeapon = static_cast<Params::PBCharacter_EquipPendingWeapon*>(parms)->NewWeapon;
            }

            channel = "character";
        }
        else if ((object->IsA(UPBFieldModManager::StaticClass()) || object->IsA(UPBCustomizeManager::StaticClass())) &&
            containsAny({ "GetWeaponNetworkConfig", "SpawnWeapon" }))
        {
            channel = "manager";
        }
        else
        {
            return;
        }

        const std::string key = channel + "|" + functionName;
        if (!ShouldLogWeaponProcessEventProbe(key))
        {
            return;
        }

        std::ostringstream message;
        message << "[LOADOUT] Observed weapon-related ProcessEvent (" << channel << "): fn=" << functionName
            << " object=" << object->GetFullName();

        if (probedWeapon)
        {
            message << " live={" << DescribeWeaponConfig(probedWeapon->PartNetworkConfig) << "}"
                << " liveParts=[" << DescribeWeaponParts(probedWeapon->PartNetworkConfig) << "]"
                << " nativeSaveParts=[" << DescribeWeaponParts(probedWeapon->GetPBWeaponPartSaveConfig()) << "]";

            if (snapshot)
            {
                FPBWeaponNetworkConfig targetConfig{};
                std::string resolvedRoleId;
                if (TryResolveSnapshotWeaponConfigForLiveWeapon(*snapshot, probedWeapon, resolvedRoleId, targetConfig))
                {
                    message << " snapshotRole=" << (resolvedRoleId.empty() ? "<unknown>" : resolvedRoleId)
                        << " snapshotTarget={" << DescribeWeaponConfig(targetConfig) << "}"
                        << " snapshotParts=[" << DescribeWeaponParts(targetConfig) << "]";
                }
            }
        }

        ClientLog(message.str());
    }

    bool TryApplyWeaponDefinitionOverrideContext(
        const FPBWeaponNetworkConfig& targetConfig,
        WeaponDefinitionOverrideContext& outContext,
        std::string& outSummary,
        std::string& outFailureReason)
    {
        outSummary.clear();
        outFailureReason.clear();
        if (!HasWeaponConfig(targetConfig))
        {
            outFailureReason = "missing-weapon-config";
            return false;
        }

        std::string dataTableManagerSource;
        UPBDataTableManager* dataTableManager = GetDataTableManager(&dataTableManagerSource);
        if (!dataTableManager)
        {
            outFailureReason = "missing-data-table-manager";
            return false;
        }

        std::string resolvedWeaponDefinitionRowId;
        std::vector<std::string> candidatesTried;
        std::string findRowFailureReason;
        FPBWeaponDefinitionRow* weaponDefinitionRow =
            FindWeaponDefinitionRow(
                dataTableManager,
                targetConfig,
                &resolvedWeaponDefinitionRowId,
                &candidatesTried,
                &findRowFailureReason);
        if (!weaponDefinitionRow)
        {
            outFailureReason = findRowFailureReason;
            if (!candidatesTried.empty())
            {
                outFailureReason += " candidates=[" + JoinStrings(candidatesTried, " | ") + "]";
            }
            if (!dataTableManagerSource.empty())
            {
                outFailureReason += " dataTableManager=" + dataTableManagerSource;
            }
            return false;
        }

        UPBWeaponDefaultConfig* defaultConfig = LoadSoftObjectPtrBlocking(weaponDefinitionRow->DefaultConfig);
        if (!defaultConfig)
        {
            outFailureReason = "missing-weapon-default-config row=" + resolvedWeaponDefinitionRowId;
            return false;
        }

        bool mutated = false;
        std::ostringstream summary;
        summary << "weaponDef=" << resolvedWeaponDefinitionRowId;

        auto appendSection = [&](const std::string& label, const std::vector<std::string>& entries)
        {
            if (entries.empty())
            {
                return;
            }

            summary << " " << label << "=[";
            for (size_t index = 0; index < entries.size(); ++index)
            {
                if (index > 0)
                {
                    summary << " | ";
                }
                summary << entries[index];
            }
            summary << "]";
        };

        std::vector<std::string> defaultConfigChanges;
        outContext.DefaultConfig = defaultConfig;
        outContext.DefaultConfigWeaponOrnament = defaultConfig->WeaponOrnament;

        const int targetPartCount = (std::min)(targetConfig.WeaponPartSlotTypeArray.Num(), targetConfig.WeaponPartConfigs.Num());
        for (int index = 0; index < targetPartCount; ++index)
        {
            const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
            const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

            std::string previousPartId;
            if (UpdatePartSlotNameMapEntryWithBackup(
                defaultConfig->EquippedPartIDMap,
                slotType,
                targetPart.WeaponPartID,
                &outContext.DefaultConfigEquippedPartIDMapBackups,
                &previousPartId))
            {
                mutated = true;
                defaultConfigChanges.push_back(
                    std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(targetPart.WeaponPartID));
            }
        }

        if (!IsBlankName(targetConfig.OrnamentID) && NameToString(defaultConfig->WeaponOrnament) != NameToString(targetConfig.OrnamentID))
        {
            defaultConfigChanges.push_back("ornament:" + NameToString(defaultConfig->WeaponOrnament) + "->" + NameToString(targetConfig.OrnamentID));
            defaultConfig->WeaponOrnament = targetConfig.OrnamentID;
            outContext.DefaultConfigWeaponOrnamentChanged = true;
            mutated = true;
        }

        appendSection("defaultConfig", defaultConfigChanges);

        const FName suitSkinId = defaultConfig->SuitSkin;
        const FName suitPaintingId = defaultConfig->SuitPainting;

        FPBWeaponPartNetworkConfig sightConfig{};
        const bool hasSightConfig = TryGetWeaponPartConfigForSlot(targetConfig, EPBPartSlotType::Sight_Optical, sightConfig);

        UDataTable* weaponSuitTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponSuitDefinitionDataTable);
        if (weaponSuitTable && !IsBlankName(suitSkinId))
        {
            if (FPBWeaponSuiteDefinitionRow* suitRow = FindDataTableRowPtr<FPBWeaponSuiteDefinitionRow>(weaponSuitTable, suitSkinId))
            {
                outContext.SuitRow = suitRow;
                outContext.SuitRowWeaponOrnamentId = suitRow->WeaponOrnamentId;
                outContext.SuitRowCrossHairId = suitRow->CrossHairId;

                std::vector<std::string> suitRowChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPartId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitRow->PartSlotAndIDMap,
                        slotType,
                        targetPart.WeaponPartID,
                        &outContext.SuitRowPartSlotAndIDMapBackups,
                        &previousPartId))
                    {
                        mutated = true;
                        suitRowChanges.push_back(
                            "part" + std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(targetPart.WeaponPartID));
                    }

                    std::string previousSkinId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitRow->PartSlotAndSkinIDMap,
                        slotType,
                        targetPart.WeaponPartSkinID,
                        &outContext.SuitRowPartSlotAndSkinIDMapBackups,
                        &previousSkinId))
                    {
                        mutated = true;
                        suitRowChanges.push_back(
                            "skin" + std::to_string(static_cast<int>(slotType)) + ":" + previousSkinId + "->" + NameToString(targetPart.WeaponPartSkinID));
                    }
                }

                if (!IsBlankName(targetConfig.OrnamentID) && NameToString(suitRow->WeaponOrnamentId) != NameToString(targetConfig.OrnamentID))
                {
                    suitRowChanges.push_back("ornament:" + NameToString(suitRow->WeaponOrnamentId) + "->" + NameToString(targetConfig.OrnamentID));
                    suitRow->WeaponOrnamentId = targetConfig.OrnamentID;
                    outContext.SuitRowWeaponOrnamentChanged = true;
                    mutated = true;
                }

                if (hasSightConfig &&
                    !IsBlankName(sightConfig.WeaponPartSpecialSkinID) &&
                    NameToString(suitRow->CrossHairId) != NameToString(sightConfig.WeaponPartSpecialSkinID))
                {
                    suitRowChanges.push_back("crosshair:" + NameToString(suitRow->CrossHairId) + "->" + NameToString(sightConfig.WeaponPartSpecialSkinID));
                    suitRow->CrossHairId = sightConfig.WeaponPartSpecialSkinID;
                    outContext.SuitRowCrossHairChanged = true;
                    mutated = true;
                }

                appendSection("suitRow", suitRowChanges);
            }
        }

        UDataTable* weaponSuitPaintingTable = LoadSoftObjectPtrBlocking(dataTableManager->WeaponSuitPaintingDefinitionDataTable);
        if (weaponSuitPaintingTable && !IsBlankName(suitPaintingId))
        {
            if (FPBWeaponSuitePaintingDefinitionRow* suitPaintingRow =
                FindDataTableRowPtr<FPBWeaponSuitePaintingDefinitionRow>(weaponSuitPaintingTable, suitPaintingId))
            {
                outContext.SuitPaintingRow = suitPaintingRow;

                std::vector<std::string> suitPaintingChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPaintingId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitPaintingRow->SlotAndWeaponPartSkinPaintingIDMap,
                        slotType,
                        targetPart.WeaponPartSkinPaintingID,
                        &outContext.SuitPaintingRowSlotAndWeaponPartSkinPaintingIDMapBackups,
                        &previousPaintingId))
                    {
                        mutated = true;
                        suitPaintingChanges.push_back(
                            std::to_string(static_cast<int>(slotType)) + ":" + previousPaintingId + "->" + NameToString(targetPart.WeaponPartSkinPaintingID));
                    }
                }

                appendSection("suitPaintRow", suitPaintingChanges);
            }
        }

        if (!IsBlankName(suitSkinId) && CanSafelyResolveLoadedObjectName(suitSkinId))
        {
            if (UPBWeaponSuit* suitAsset = FindLoadedNamedObject<UPBWeaponSuit>(NameToString(suitSkinId)))
            {
                outContext.SuitAsset = suitAsset;
                outContext.SuitAssetWeaponOrnamentId = suitAsset->WeaponOrnamentId;
                outContext.SuitAssetCrossHairId = suitAsset->CrossHairId;

                std::vector<std::string> suitAssetChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPartId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitAsset->PartSlotAndIDMap,
                        slotType,
                        targetPart.WeaponPartID,
                        &outContext.SuitAssetPartSlotAndIDMapBackups,
                        &previousPartId))
                    {
                        mutated = true;
                        suitAssetChanges.push_back(
                            "part" + std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(targetPart.WeaponPartID));
                    }

                    std::string previousSkinId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitAsset->PartSlotAndSkinIDMap,
                        slotType,
                        targetPart.WeaponPartSkinID,
                        &outContext.SuitAssetPartSlotAndSkinIDMapBackups,
                        &previousSkinId))
                    {
                        mutated = true;
                        suitAssetChanges.push_back(
                            "skin" + std::to_string(static_cast<int>(slotType)) + ":" + previousSkinId + "->" + NameToString(targetPart.WeaponPartSkinID));
                    }
                }

                if (!IsBlankName(targetConfig.OrnamentID) && NameToString(suitAsset->WeaponOrnamentId) != NameToString(targetConfig.OrnamentID))
                {
                    suitAssetChanges.push_back("ornament:" + NameToString(suitAsset->WeaponOrnamentId) + "->" + NameToString(targetConfig.OrnamentID));
                    suitAsset->WeaponOrnamentId = targetConfig.OrnamentID;
                    outContext.SuitAssetWeaponOrnamentChanged = true;
                    mutated = true;
                }

                if (hasSightConfig &&
                    !IsBlankName(sightConfig.WeaponPartSpecialSkinID) &&
                    NameToString(suitAsset->CrossHairId) != NameToString(sightConfig.WeaponPartSpecialSkinID))
                {
                    suitAssetChanges.push_back("crosshair:" + NameToString(suitAsset->CrossHairId) + "->" + NameToString(sightConfig.WeaponPartSpecialSkinID));
                    suitAsset->CrossHairId = sightConfig.WeaponPartSpecialSkinID;
                    outContext.SuitAssetCrossHairChanged = true;
                    mutated = true;
                }

                appendSection("suitAsset", suitAssetChanges);
            }
        }

        if (!IsBlankName(suitPaintingId) && CanSafelyResolveLoadedObjectName(suitPaintingId))
        {
            if (UPBWeaponSuitPaintingCustom* suitPaintingAsset =
                FindLoadedNamedObject<UPBWeaponSuitPaintingCustom>(NameToString(suitPaintingId)))
            {
                outContext.SuitPaintingAsset = suitPaintingAsset;

                std::vector<std::string> suitPaintingAssetChanges;
                for (int index = 0; index < targetPartCount; ++index)
                {
                    const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
                    const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];

                    std::string previousPaintingId;
                    if (UpdatePartSlotNameMapEntryWithBackup(
                        suitPaintingAsset->SlotAndWeaponPartSkinPaintingIDMap,
                        slotType,
                        targetPart.WeaponPartSkinPaintingID,
                        &outContext.SuitPaintingAssetSlotAndWeaponPartSkinPaintingIDMapBackups,
                        &previousPaintingId))
                    {
                        mutated = true;
                        suitPaintingAssetChanges.push_back(
                            std::to_string(static_cast<int>(slotType)) + ":" + previousPaintingId + "->" + NameToString(targetPart.WeaponPartSkinPaintingID));
                    }
                }

                appendSection("suitPaintAsset", suitPaintingAssetChanges);
            }
        }

        outSummary = summary.str();
        if (!mutated)
        {
            outFailureReason = "weapon-definition-override-no-mutations row=" + resolvedWeaponDefinitionRowId;
        }
        return mutated;
    }

    void RestoreWeaponDefinitionOverrideContext(const WeaponDefinitionOverrideContext& context)
    {
        if (context.DefaultConfig)
        {
            RestorePartSlotNameMapEntries(context.DefaultConfig->EquippedPartIDMap, context.DefaultConfigEquippedPartIDMapBackups);
            if (context.DefaultConfigWeaponOrnamentChanged)
            {
                context.DefaultConfig->WeaponOrnament = context.DefaultConfigWeaponOrnament;
            }
        }

        if (context.SuitRow)
        {
            RestorePartSlotNameMapEntries(context.SuitRow->PartSlotAndIDMap, context.SuitRowPartSlotAndIDMapBackups);
            RestorePartSlotNameMapEntries(context.SuitRow->PartSlotAndSkinIDMap, context.SuitRowPartSlotAndSkinIDMapBackups);
            if (context.SuitRowWeaponOrnamentChanged)
            {
                context.SuitRow->WeaponOrnamentId = context.SuitRowWeaponOrnamentId;
            }
            if (context.SuitRowCrossHairChanged)
            {
                context.SuitRow->CrossHairId = context.SuitRowCrossHairId;
            }
        }

        if (context.SuitPaintingRow)
        {
            RestorePartSlotNameMapEntries(
                context.SuitPaintingRow->SlotAndWeaponPartSkinPaintingIDMap,
                context.SuitPaintingRowSlotAndWeaponPartSkinPaintingIDMapBackups);
        }

        if (context.SuitAsset)
        {
            RestorePartSlotNameMapEntries(context.SuitAsset->PartSlotAndIDMap, context.SuitAssetPartSlotAndIDMapBackups);
            RestorePartSlotNameMapEntries(context.SuitAsset->PartSlotAndSkinIDMap, context.SuitAssetPartSlotAndSkinIDMapBackups);
            if (context.SuitAssetWeaponOrnamentChanged)
            {
                context.SuitAsset->WeaponOrnamentId = context.SuitAssetWeaponOrnamentId;
            }
            if (context.SuitAssetCrossHairChanged)
            {
                context.SuitAsset->CrossHairId = context.SuitAssetCrossHairId;
            }
        }

        if (context.SuitPaintingAsset)
        {
            RestorePartSlotNameMapEntries(
                context.SuitPaintingAsset->SlotAndWeaponPartSkinPaintingIDMap,
                context.SuitPaintingAssetSlotAndWeaponPartSkinPaintingIDMapBackups);
        }
    }

    bool BeginScopedWeaponDefinitionOverride(const FPBWeaponNetworkConfig& targetConfig, const std::string& source)
    {
        WeaponDefinitionOverrideContext context{};
        std::string summary;
        std::string failureReason;
        if (!TryApplyWeaponDefinitionOverrideContext(targetConfig, context, summary, failureReason))
        {
            if (!failureReason.empty() && ShouldLogWeaponDefinitionOverrideFailure(source, targetConfig, failureReason))
            {
                ClientLog("[LOADOUT] Scoped weapon definition override skipped (" + source + "): " +
                    failureReason + " target={" + DescribeWeaponConfig(targetConfig) +
                    "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
            }
            return false;
        }

        ScopedWeaponDefinitionOverrides.push_back(std::move(context));
        if (!summary.empty() && ShouldLogWeaponDefinitionOverride(source, targetConfig))
        {
            ClientLog("[LOADOUT] Scoped weapon definition override (" + source + "): " + summary);
        }
        return true;
    }

    void EndScopedWeaponDefinitionOverride()
    {
        if (ScopedWeaponDefinitionOverrides.empty())
        {
            return;
        }

        WeaponDefinitionOverrideContext context = std::move(ScopedWeaponDefinitionOverrides.back());
        ScopedWeaponDefinitionOverrides.pop_back();
        RestoreWeaponDefinitionOverrideContext(context);
    }

    bool BeginProcessEventWeaponDefinitionOverride(UObject* object, const std::string& functionName, void* parms)
    {
        if (!object)
        {
            return false;
        }

        const bool isFieldModGetWeapon = functionName.contains("PBFieldModManager.GetWeaponNetworkConfig");
        const bool isCustomizeGetWeapon = functionName.contains("PBCustomizeManager.GetWeaponNetworkConfig");
        const bool isFieldModSpawnWeapon = functionName.contains("PBFieldModManager.SpawnWeapon");
        const bool isWeaponInit = functionName.contains("PBWeapon.InitWeapon");
        const bool isWeaponOnRep = functionName.contains("PBWeapon.OnRep_PartNetworkConfig");
        const bool isWeaponInitComplete = functionName.contains("PBWeapon.K2_InitSimulatedPartsComplete");
        const bool isWeaponNotifyEquipFinished = functionName.contains("PBWeapon.NotifyEquipFinished");
        const bool isWeaponRefreshSkin = functionName.contains("PBWeapon.K2_RefreshSkin");
        const bool isCharacterAddWeapon = functionName.contains("PBCharacter.AddWeapon");
        const bool isCharacterEquipPendingWeapon = functionName.contains("PBCharacter.EquipPendingWeapon");
        if (!isFieldModGetWeapon && !isCustomizeGetWeapon && !isFieldModSpawnWeapon && !isWeaponInit &&
            !isWeaponOnRep && !isWeaponInitComplete && !isWeaponNotifyEquipFinished && !isWeaponRefreshSkin &&
            !isCharacterAddWeapon && !isCharacterEquipPendingWeapon)
        {
            return false;
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (!state.SnapshotAvailable)
            {
                return false;
            }

            snapshotCopy = state.Snapshot;
        }

        MaybeLogWeaponProcessEventProbe(object, functionName, parms, &snapshotCopy);

        FPBWeaponNetworkConfig targetConfig{};
        std::string source;

        if (isFieldModGetWeapon)
        {
            if (!parms)
            {
                return false;
            }

            auto* getWeaponParms = static_cast<Params::PBFieldModManager_GetWeaponNetworkConfig*>(parms);
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(
                snapshotCopy,
                NameToString(getWeaponParms->InRoleID),
                NameToString(getWeaponParms->InWeaponID),
                targetConfig))
            {
                return false;
            }
            source = "process-fieldmod-get";
        }
        else if (isCustomizeGetWeapon)
        {
            if (!parms)
            {
                return false;
            }

            auto* getWeaponParms = static_cast<Params::PBCustomizeManager_GetWeaponNetworkConfig*>(parms);
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(
                snapshotCopy,
                NameToString(getWeaponParms->InCharacterID),
                NameToString(getWeaponParms->InWeaponID),
                targetConfig))
            {
                return false;
            }
            source = "process-customize-get";
        }
        else if (isFieldModSpawnWeapon)
        {
            if (!parms)
            {
                return false;
            }

            auto* spawnWeaponParms = static_cast<Params::PBFieldModManager_SpawnWeapon*>(parms);
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(
                snapshotCopy,
                NameToString(spawnWeaponParms->InRoleID),
                NameToString(spawnWeaponParms->InWeaponID),
                targetConfig))
            {
                return false;
            }
            source = "process-fieldmod-spawn";
        }
        else if (isWeaponInit)
        {
            if (!parms)
            {
                return false;
            }

            APBWeapon* weapon = object->IsA(APBWeapon::StaticClass()) ? static_cast<APBWeapon*>(object) : nullptr;
            auto* initParms = static_cast<Params::PBWeapon_InitWeapon*>(parms);
            std::string resolvedRoleId;
            if (!weapon ||
                !TryResolveSnapshotWeaponConfigForInit(snapshotCopy, weapon, initParms->InPartSaved, resolvedRoleId, targetConfig))
            {
                targetConfig = initParms->InPartSaved;
            }

            source = "process-weapon-init";
        }
        else if (isWeaponOnRep || isWeaponInitComplete || isWeaponNotifyEquipFinished || isWeaponRefreshSkin)
        {
            APBWeapon* weapon = object->IsA(APBWeapon::StaticClass()) ? static_cast<APBWeapon*>(object) : nullptr;
            std::string resolvedRoleId;
            if (!weapon || !TryResolveSnapshotWeaponConfigForLiveWeapon(snapshotCopy, weapon, resolvedRoleId, targetConfig))
            {
                return false;
            }

            if (isWeaponOnRep)
            {
                source = "process-weapon-onrep";
            }
            else if (isWeaponInitComplete)
            {
                source = "process-weapon-init-complete";
            }
            else if (isWeaponNotifyEquipFinished)
            {
                source = "process-weapon-equip-finished";
            }
            else
            {
                source = "process-weapon-refresh-skin";
            }
        }
        else
        {
            APBWeapon* weapon = nullptr;
            if (isCharacterAddWeapon && parms)
            {
                weapon = static_cast<Params::PBCharacter_AddWeapon*>(parms)->Weapon;
                source = "process-character-add-weapon";
            }
            else if (isCharacterEquipPendingWeapon && parms)
            {
                weapon = static_cast<Params::PBCharacter_EquipPendingWeapon*>(parms)->NewWeapon;
                source = "process-character-equip-pending";
            }

            std::string resolvedRoleId;
            if (!weapon || !TryResolveSnapshotWeaponConfigForLiveWeapon(snapshotCopy, weapon, resolvedRoleId, targetConfig))
            {
                return false;
            }
        }

        return BeginScopedWeaponDefinitionOverride(targetConfig, source);
    }

    void EndProcessEventWeaponDefinitionOverride()
    {
        EndScopedWeaponDefinitionOverride();
    }

    UPBWeaponPartManager* ResolveWeaponPartManager(APBWeapon* weapon)
    {
        if (weapon)
        {
            if (UPBWeaponPartManager* partManager = weapon->GetWeaponPartMgr())
            {
                return partManager;
            }
        }

        UObject* object = GetLastOfType(UPBWeaponPartManager::StaticClass(), false);
        return object ? static_cast<UPBWeaponPartManager*>(object) : nullptr;
    }

    void RevealPartHolderMeshes(UPartDataHolderComponent* holder)
    {
        if (!holder)
        {
            return;
        }

        if (holder->PartMesh1P)
        {
            holder->PartMesh1P->SetHiddenInGame(false, false);
        }

        if (holder->PartMesh3P)
        {
            holder->PartMesh3P->SetHiddenInGame(false, false);
        }

        if (holder->DuplicatePartMesh1P)
        {
            holder->DuplicatePartMesh1P->SetHiddenInGame(false, false);
        }

        if (holder->DuplicatePartMesh3P)
        {
            holder->DuplicatePartMesh3P->SetHiddenInGame(false, false);
        }
    }

    std::vector<APBWeaponPart*> GetAttachedWeaponPartActors(APBWeapon* weapon)
    {
        std::vector<APBWeaponPart*> parts;
        if (!weapon)
        {
            return parts;
        }

        std::unordered_set<AActor*> visitedActors;
        std::unordered_set<APBWeaponPart*> seenParts;
        std::vector<AActor*> pendingActors;

        visitedActors.insert(weapon);
        pendingActors.push_back(weapon);

        auto queueActor = [&](AActor* actor)
        {
            if (actor && visitedActors.insert(actor).second)
            {
                pendingActors.push_back(actor);
            }
        };

        while (!pendingActors.empty())
        {
            AActor* current = pendingActors.back();
            pendingActors.pop_back();

            if (current != weapon && current->IsA(APBWeaponPart::StaticClass()))
            {
                APBWeaponPart* part = static_cast<APBWeaponPart*>(current);
                if (seenParts.insert(part).second)
                {
                    parts.push_back(part);
                }
            }

            TArray<AActor*> attachedActors{};
            current->GetAttachedActors(&attachedActors, true);
            for (AActor* actor : attachedActors)
            {
                queueActor(actor);
            }

            for (AActor* actor : current->Children)
            {
                queueActor(actor);
            }
        }

        return parts;
    }

    APBWeaponPart* FindAttachedWeaponPartActor(const std::vector<APBWeaponPart*>& parts, const std::string& preferredId, const std::string& fallbackId)
    {
        auto matchesId = [](APBWeaponPart* part, const std::string& id) -> bool
        {
            return part &&
                !id.empty() &&
                (NameToString(part->CurrentLoadout.ID) == id || NameToString(part->PresetLoadout.ID) == id);
        };

        for (APBWeaponPart* part : parts)
        {
            if (matchesId(part, preferredId))
            {
                return part;
            }
        }

        for (APBWeaponPart* part : parts)
        {
            if (matchesId(part, fallbackId))
            {
                return part;
            }
        }

        return nullptr;
    }

    std::string DescribeAttachedWeaponParts(APBWeapon* weapon)
    {
        std::ostringstream stream;
        bool first = true;
        for (APBWeaponPart* part : GetAttachedWeaponPartActors(weapon))
        {
            if (!first)
            {
                stream << " | ";
            }

            first = false;
            stream << "current=" << DescribeWeaponPartLoadout(part->CurrentLoadout)
                << ", preset=" << DescribeWeaponPartLoadout(part->PresetLoadout);
        }

        if (first)
        {
            return "<none>";
        }

        return stream.str();
    }

    bool ApplyNativeWeaponPartState(APBWeapon* weapon, const FPBWeaponNetworkConfig& targetConfig, const FPBWeaponNetworkConfig& nativeBaselineConfig, std::string* outMutationSummary = nullptr)
    {
        if (!weapon || !HasWeaponConfig(targetConfig))
        {
            return false;
        }

        bool mutated = false;
        std::ostringstream summary;
        bool firstSummary = true;

        auto appendSummary = [&](const std::string& entry)
        {
            if (entry.empty())
            {
                return;
            }

            if (!firstSummary)
            {
                summary << " | ";
            }

            firstSummary = false;
            summary << entry;
        };

        if (UPBWeaponPartManager* partManager = ResolveWeaponPartManager(weapon))
        {
            TMap<EPBPartSlotType, UPartDataHolderComponent*> slotMap = partManager->GetWeaponSlotPartMap(weapon);
            for (auto& pair : slotMap)
            {
                const EPBPartSlotType slotType = pair.Key();
                UPartDataHolderComponent* holder = pair.Value();
                if (!holder)
                {
                    continue;
                }

                FPBWeaponPartNetworkConfig targetPart{};
                if (!TryGetWeaponPartConfigForSlot(targetConfig, slotType, targetPart) || IsBlankName(targetPart.WeaponPartID))
                {
                    continue;
                }

                const std::string previousPartId = NameToString(holder->PartID);
                bool holderMutated = false;

                if (UClass* targetPartClass = partManager->GetPartClassByID(targetPart.WeaponPartID).Get())
                {
                    if (targetPartClass->DefaultObject && targetPartClass->DefaultObject->IsA(UPartDataHolderComponent::StaticClass()))
                    {
                        UPartDataHolderComponent* defaultHolder = static_cast<UPartDataHolderComponent*>(targetPartClass->DefaultObject);
                        holder->PartDisplayInfo = defaultHolder->PartDisplayInfo;
                        holder->PartConfigFromCSV = defaultHolder->PartConfigFromCSV;
                        holder->PartID = defaultHolder->PartID;
                        holderMutated = true;
                    }
                }

                if (NameToString(holder->PartID) != NameToString(targetPart.WeaponPartID))
                {
                    holder->PartID = targetPart.WeaponPartID;
                    holderMutated = true;
                }

                if (holderMutated)
                {
                    holder->ChangeAimingMesh(weapon->IsStartADS());
                    RevealPartHolderMeshes(holder);
                    mutated = true;
                    appendSummary("slot" + std::to_string(static_cast<int>(slotType)) + ":" + previousPartId + "->" + NameToString(holder->PartID));
                }
            }
        }

        const std::vector<APBWeaponPart*> attachedParts = GetAttachedWeaponPartActors(weapon);
        const int targetPartCount = (std::min)(targetConfig.WeaponPartSlotTypeArray.Num(), targetConfig.WeaponPartConfigs.Num());
        for (int index = 0; index < targetPartCount; ++index)
        {
            const EPBPartSlotType slotType = targetConfig.WeaponPartSlotTypeArray[index];
            const FPBWeaponPartNetworkConfig& targetPart = targetConfig.WeaponPartConfigs[index];
            if (IsBlankName(targetPart.WeaponPartID))
            {
                continue;
            }

            FPBWeaponPartNetworkConfig currentPart{};
            const bool hasCurrentPart = TryGetWeaponPartConfigForSlot(nativeBaselineConfig, slotType, currentPart);
            const std::string currentPartId = hasCurrentPart ? NameToString(currentPart.WeaponPartID) : std::string();
            const std::string targetPartId = NameToString(targetPart.WeaponPartID);
            APBWeaponPart* partActor = FindAttachedWeaponPartActor(attachedParts, currentPartId, targetPartId);
            if (!partActor)
            {
                continue;
            }

            const FPBWeaponPartLoadout targetLoadout = BuildWeaponPartLoadout(targetConfig, targetPart);
            const std::string previousCurrent = DescribeWeaponPartLoadout(partActor->CurrentLoadout);
            const std::string previousPreset = DescribeWeaponPartLoadout(partActor->PresetLoadout);

            if (!WeaponPartLoadoutEquals(partActor->CurrentLoadout, targetConfig, targetPart) ||
                !WeaponPartLoadoutEquals(partActor->PresetLoadout, targetConfig, targetPart))
            {
                partActor->CurrentLoadout = targetLoadout;
                partActor->PresetLoadout = targetLoadout;
                partActor->SetActorHiddenInGame(false);
                if (partActor->MeshComponent)
                {
                    partActor->MeshComponent->SetHiddenInGame(false, false);
                }
                partActor->ForceNetUpdate();
                MarkActorForReplication(partActor);
                mutated = true;
                appendSummary("part" + std::to_string(static_cast<int>(slotType)) + ":" + previousCurrent + "/" + previousPreset +
                    "->" + DescribeWeaponPartLoadout(targetLoadout));
            }
        }

        if (outMutationSummary)
        {
            *outMutationSummary = firstSummary ? std::string() : summary.str();
        }

        return mutated;
    }

    int ScoreWeaponSnapshotMatch(const FPBWeaponNetworkConfig& candidate, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon)
    {
        if (!HasWeaponConfig(candidate))
        {
            return 0;
        }

        if (WeaponConfigEquals(candidate, incoming))
        {
            return 5;
        }

        const std::string candidateWeaponId = NameToString(candidate.WeaponID);
        const std::string candidateClassId = NameToString(candidate.WeaponClassID);
        const std::string incomingWeaponId = NameToString(incoming.WeaponID);
        const std::string incomingClassId = NameToString(incoming.WeaponClassID);

        std::string liveWeaponId;
        std::string liveClassId;
        if (weapon)
        {
            liveWeaponId = NameToString(weapon->PartNetworkConfig.WeaponID);
            if (IsBlankText(liveWeaponId))
            {
                liveWeaponId = NameToString(weapon->GetWeaponId());
            }

            liveClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);
            if (IsBlankText(liveClassId))
            {
                liveClassId = NameToString(weapon->GetWeaponClassID());
            }
        }

        int score = 0;
        if (!IsBlankText(candidateWeaponId))
        {
            if (!IsBlankText(incomingWeaponId) && candidateWeaponId == incomingWeaponId)
            {
                score = (std::max)(score, 4);
            }

            if (!IsBlankText(liveWeaponId) && candidateWeaponId == liveWeaponId)
            {
                score = (std::max)(score, 3);
            }
        }

        if (!IsBlankText(candidateClassId))
        {
            if (!IsBlankText(incomingClassId) && candidateClassId == incomingClassId)
            {
                score = (std::max)(score, 2);
            }

            if (!IsBlankText(liveClassId) && candidateClassId == liveClassId)
            {
                score = (std::max)(score, 1);
            }
        }

        return score;
    }

    bool TryGetRoleWeaponConfigForInit(const FPBRoleNetworkConfig& roleConfig, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon, FPBWeaponNetworkConfig& outConfig)
    {
        int bestScore = 0;
        bool found = false;

        auto consider = [&](const FPBWeaponNetworkConfig& candidate)
        {
            const int score = ScoreWeaponSnapshotMatch(candidate, incoming, weapon);
            if (score > bestScore)
            {
                bestScore = score;
                outConfig = candidate;
                found = true;
            }
        };

        consider(roleConfig.FirstWeaponPartData);
        consider(roleConfig.SecondWeaponPartData);
        return found;
    }

    bool TryGetRoleWeaponConfigByWeaponId(const FPBRoleNetworkConfig& roleConfig, const std::string& desiredWeaponId, FPBWeaponNetworkConfig& outConfig)
    {
        if (desiredWeaponId.empty())
        {
            return false;
        }

        auto matches = [&](const FPBWeaponNetworkConfig& candidate) -> bool
        {
            return HasWeaponConfig(candidate) &&
                (NameToString(candidate.WeaponID) == desiredWeaponId || NameToString(candidate.WeaponClassID) == desiredWeaponId);
        };

        if (matches(roleConfig.FirstWeaponPartData))
        {
            outConfig = roleConfig.FirstWeaponPartData;
            return true;
        }

        if (matches(roleConfig.SecondWeaponPartData))
        {
            outConfig = roleConfig.SecondWeaponPartData;
            return true;
        }

        return false;
    }

    bool TryResolveSnapshotWeaponConfigByRoleAndWeaponId(const json& snapshot, const std::string& roleId, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig)
    {
        if (roleId.empty() || weaponId.empty() || !IsRoleAllowedBySnapshotSelection(snapshot, roleId))
        {
            return false;
        }

        FPBRoleNetworkConfig roleConfig{};
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            return false;
        }

        return TryGetRoleWeaponConfigByWeaponId(roleConfig, weaponId, outConfig);
    }

    bool TryResolveSnapshotWeaponConfigForInit(const json& snapshot, APBWeapon* weapon, const FPBWeaponNetworkConfig& incoming, std::string& outRoleId, FPBWeaponNetworkConfig& outConfig)
    {
        outRoleId.clear();
        const std::string selectedRoleId = GetSnapshotSelectedRoleId(snapshot);

        auto tryRole = [&](const std::string& roleId) -> bool
        {
            if (roleId.empty() || !IsRoleAllowedBySnapshotSelection(snapshot, roleId))
            {
                return false;
            }

            FPBRoleNetworkConfig roleConfig{};
            if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
            {
                return false;
            }

            if (!TryGetRoleWeaponConfigForInit(roleConfig, incoming, weapon, outConfig))
            {
                return false;
            }

            outRoleId = roleId;
            return true;
        };

        APBCharacter* ownerCharacter = nullptr;
        if (weapon)
        {
            try
            {
                ownerCharacter = weapon->GetPawnOwner();
            }
            catch (...)
            {
                ownerCharacter = nullptr;
            }
        }

        if (!amServer)
        {
            APBCharacter* localCharacter = GetLocalCharacter();
            if (localCharacter && ownerCharacter && ownerCharacter != localCharacter)
            {
                return false;
            }
        }

        if (tryRole(ResolveCharacterRoleId(ownerCharacter)))
        {
            return true;
        }

        if (!amServer)
        {
            if (tryRole(GetLocalPlayerPreferredRoleId()))
            {
                return true;
            }
        }

        // Avoid fighting the menu preview path. Selected-role fallback is only safe once
        // we are in a spawned match context or running on the authoritative server.
        if (!amServer && !GetLocalCharacter())
        {
            return false;
        }

        if (tryRole(selectedRoleId))
        {
            return true;
        }

        if (!amServer)
        {
            return false;
        }

        if (selectedRoleId.empty() && snapshot.contains("roles") && snapshot["roles"].is_array())
        {
            for (const auto& role : snapshot["roles"])
            {
                if (!role.is_object())
                {
                    continue;
                }

                if (tryRole(role.value("roleId", "")))
                {
                    return true;
                }
            }
        }

        return false;
    }

    bool ShouldLogInitWeaponOverride(APBWeapon* weapon, const FPBWeaponNetworkConfig& config)
    {
        if (!weapon)
        {
            return false;
        }

        const std::string signature = BuildWeaponConfigSignature(config);
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        auto existing = state.WeaponInitOverrideSignatures.find(weapon);
        if (existing != state.WeaponInitOverrideSignatures.end() && existing->second == signature)
        {
            return false;
        }

        state.WeaponInitOverrideSignatures[weapon] = signature;
        return true;
    }

    bool ShouldLogWeaponQueryOverride(const std::string& channel, const std::string& roleId, const std::string& weaponId, const FPBWeaponNetworkConfig& config)
    {
        const std::string signature = channel + "|" + roleId + "|" + weaponId + "|" + BuildWeaponConfigSignature(config);
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponQueryOverrideLogs.insert(signature).second;
    }

    void MaybeOverrideInitWeaponConfig(UObject* object, const std::string& functionName, void* parms)
    {
        if (!object || !parms || !object->IsA(APBWeapon::StaticClass()) || !functionName.contains("PBWeapon.InitWeapon"))
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

        FPBWeaponNetworkConfig targetConfig{};
        std::string resolvedRoleId;
        if (!TryResolveSnapshotWeaponConfigForInit(snapshotCopy, weapon, previousConfig, resolvedRoleId, targetConfig))
        {
            return;
        }

        if (!HasWeaponConfig(targetConfig) || WeaponConfigEquals(previousConfig, targetConfig))
        {
            return;
        }

        initParms->InPartSaved = targetConfig;
        weapon->PartNetworkConfig = targetConfig;

        if (ShouldLogInitWeaponOverride(weapon, targetConfig))
        {
            ClientLog("[LOADOUT] Overriding InitWeapon from snapshot for role " +
                (resolvedRoleId.empty() ? std::string("<unknown>") : resolvedRoleId) +
                ": incoming={" + DescribeWeaponConfig(previousConfig) +
                "} incomingParts=[" + DescribeWeaponParts(previousConfig) +
                "] target={" + DescribeWeaponConfig(targetConfig) +
                "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
        }
    }

    void MaybeOverrideProcessEventResult(UObject* object, const std::string& functionName, void* parms)
    {
        if (!object || !parms)
        {
            return;
        }

        const bool isFieldModGetWeapon = functionName.contains("PBFieldModManager.GetWeaponNetworkConfig");
        const bool isCustomizeGetWeapon = functionName.contains("PBCustomizeManager.GetWeaponNetworkConfig");
        const bool isFieldModSpawnWeapon = functionName.contains("PBFieldModManager.SpawnWeapon");
        if (!isFieldModGetWeapon && !isCustomizeGetWeapon && !isFieldModSpawnWeapon)
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

        if (isFieldModGetWeapon)
        {
            auto* getWeaponParms = static_cast<Params::PBFieldModManager_GetWeaponNetworkConfig*>(parms);
            const std::string roleId = NameToString(getWeaponParms->InRoleID);
            const std::string weaponId = NameToString(getWeaponParms->InWeaponID);

            FPBWeaponNetworkConfig targetConfig{};
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(snapshotCopy, roleId, weaponId, targetConfig) ||
                !HasWeaponConfig(targetConfig) ||
                WeaponConfigEquals(getWeaponParms->ReturnValue, targetConfig))
            {
                return;
            }

            getWeaponParms->ReturnValue = targetConfig;
            if (ShouldLogWeaponQueryOverride("fieldmod-get", roleId, weaponId, targetConfig))
            {
                ClientLog("[LOADOUT] Overrode PBFieldModManager.GetWeaponNetworkConfig for role " + roleId +
                    ", weapon " + weaponId + ": target={" + DescribeWeaponConfig(targetConfig) +
                    "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
            }
            return;
        }

        if (isCustomizeGetWeapon)
        {
            auto* getWeaponParms = static_cast<Params::PBCustomizeManager_GetWeaponNetworkConfig*>(parms);
            const std::string roleId = NameToString(getWeaponParms->InCharacterID);
            const std::string weaponId = NameToString(getWeaponParms->InWeaponID);

            FPBWeaponNetworkConfig targetConfig{};
            if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(snapshotCopy, roleId, weaponId, targetConfig) ||
                !HasWeaponConfig(targetConfig) ||
                WeaponConfigEquals(getWeaponParms->ReturnValue, targetConfig))
            {
                return;
            }

            getWeaponParms->ReturnValue = targetConfig;
            if (ShouldLogWeaponQueryOverride("customize-get", roleId, weaponId, targetConfig))
            {
                ClientLog("[LOADOUT] Overrode PBCustomizeManager.GetWeaponNetworkConfig for role " + roleId +
                    ", weapon " + weaponId + ": target={" + DescribeWeaponConfig(targetConfig) +
                    "} targetParts=[" + DescribeWeaponParts(targetConfig) + "]");
            }
            return;
        }

        auto* spawnWeaponParms = static_cast<Params::PBFieldModManager_SpawnWeapon*>(parms);
        APBWeapon* spawnedWeapon = spawnWeaponParms->ReturnValue;
        if (!spawnedWeapon)
        {
            return;
        }

        const std::string roleId = NameToString(spawnWeaponParms->InRoleID);
        const std::string weaponId = NameToString(spawnWeaponParms->InWeaponID);
        FPBWeaponNetworkConfig targetConfig{};
        if (!TryResolveSnapshotWeaponConfigByRoleAndWeaponId(snapshotCopy, roleId, weaponId, targetConfig) ||
            !HasWeaponConfig(targetConfig))
        {
            return;
        }

        const FPBWeaponNetworkConfig previousConfig = spawnedWeapon->GetPBWeaponPartSaveConfig();
        const bool definitionOverrideActive = BeginScopedWeaponDefinitionOverride(targetConfig, "spawned-reinit");

        spawnedWeapon->PartNetworkConfig = targetConfig;
        spawnedWeapon->InitWeapon(targetConfig, false);
        spawnedWeapon->PartNetworkConfig = targetConfig;
        spawnedWeapon->OnRep_PartNetworkConfig();
        spawnedWeapon->PartNetworkConfig = targetConfig;
        spawnedWeapon->InitWeapon(targetConfig, true);
        spawnedWeapon->PartNetworkConfig = targetConfig;
        std::string nativeMutationSummary;
        ApplyNativeWeaponPartState(spawnedWeapon, targetConfig, previousConfig, &nativeMutationSummary);
        RefreshWeaponRuntimeVisualsOnce(spawnedWeapon, targetConfig);

        if (ShouldLogWeaponQueryOverride("fieldmod-spawn", roleId, weaponId, targetConfig))
        {
            const FPBWeaponNetworkConfig nativeSaveConfig = spawnedWeapon->GetPBWeaponPartSaveConfig();
            ClientLog("[LOADOUT] Reinitialized spawned weapon from snapshot for role " + roleId +
                ", weapon " + weaponId + ": previousParts=[" + DescribeWeaponParts(previousConfig) +
                "] targetParts=[" + DescribeWeaponParts(targetConfig) +
                "] nativeSaveParts=[" + DescribeWeaponParts(nativeSaveConfig) +
                "] slotMap=[" + DescribeWeaponSlotMap(spawnedWeapon) +
                "] attachedParts=[" + DescribeAttachedWeaponParts(spawnedWeapon) +
                "] nativeMutations=[" + nativeMutationSummary + "]");
        }

        if (definitionOverrideActive)
        {
            EndScopedWeaponDefinitionOverride();
        }
    }

    std::string DescribeLiveWeapons(APBCharacter* character)
    {
        std::ostringstream stream;
        const std::vector<APBWeapon*> weapons = GetCharacterWeaponList(character);
        stream << "liveWeapons=" << weapons.size();
        for (size_t index = 0; index < weapons.size(); ++index)
        {
            APBWeapon* weapon = weapons[index];
            stream << " [" << index << ": "
                << DescribeWeaponConfig(weapon->PartNetworkConfig)
                << ", nativeSaveParts=[" << DescribeWeaponParts(weapon->GetPBWeaponPartSaveConfig()) << "]"
                << ", slotMap=[" << DescribeWeaponSlotMap(weapon) << "]"
                << ", attachedParts=[" << DescribeAttachedWeaponParts(weapon) << "]"
                << "]";
        }
        return stream.str();
    }

    std::string DescribeWeaponSlotMap(APBWeapon* weapon)
    {
        if (!weapon)
        {
            return "<null weapon>";
        }

        UPBWeaponPartManager* partManager = ResolveWeaponPartManager(weapon);
        if (!partManager)
        {
            return "<missing part manager>";
        }

        std::ostringstream stream;
        bool first = true;
        TMap<EPBPartSlotType, UPartDataHolderComponent*> slotMap = partManager->GetWeaponSlotPartMap(weapon);
        for (auto& pair : slotMap)
        {
            if (!first)
            {
                stream << " | ";
            }

            first = false;
            UPartDataHolderComponent* holder = pair.Value();
            stream << static_cast<int>(pair.Key()) << ":"
                << (holder ? NameToString(holder->GetPartID()) : "<null>");
        }

        if (first)
        {
            return "<empty>";
        }

        return stream.str();
    }

    bool DoesWeaponSlotMapMatchConfig(APBWeapon* weapon, const FPBWeaponNetworkConfig& config)
    {
        if (!weapon || !HasWeaponConfig(config))
        {
            return false;
        }

        UPBWeaponPartManager* partManager = ResolveWeaponPartManager(weapon);
        if (!partManager)
        {
            return false;
        }

        TMap<EPBPartSlotType, UPartDataHolderComponent*> slotMap = partManager->GetWeaponSlotPartMap(weapon);
        const int count = (std::min)(config.WeaponPartSlotTypeArray.Num(), config.WeaponPartConfigs.Num());
        if (count <= 0)
        {
            return false;
        }

        for (int index = 0; index < count; ++index)
        {
            const EPBPartSlotType slotType = config.WeaponPartSlotTypeArray[index];
            const FPBWeaponPartNetworkConfig& targetPart = config.WeaponPartConfigs[index];
            if (IsBlankName(targetPart.WeaponPartID))
            {
                continue;
            }

            UPartDataHolderComponent* holder = nullptr;
            for (auto& pair : slotMap)
            {
                if (pair.Key() == slotType)
                {
                    holder = pair.Value();
                    break;
                }
            }

            if (!holder)
            {
                return false;
            }

            if (NameToString(holder->GetPartID()) != NameToString(targetPart.WeaponPartID))
            {
                return false;
            }
        }

        return true;
    }

    APBWeapon* FindWeaponForConfig(APBCharacter* character, const FPBWeaponNetworkConfig& config, int preferredWeaponIndex)
    {
        if (!character || !HasWeaponConfig(config))
        {
            return nullptr;
        }

        std::vector<APBWeapon*> weapons = GetCharacterWeaponList(character);
        if (weapons.empty())
        {
            return nullptr;
        }

        APBWeapon* classMatchedWeapon = nullptr;
        const std::string desiredWeaponId = NameToString(config.WeaponID);
        const std::string desiredClassId = NameToString(config.WeaponClassID);
        bool sawReliableIdentity = false;

        for (APBWeapon* weapon : weapons)
        {
            std::string currentWeaponId = NameToString(weapon->PartNetworkConfig.WeaponID);
            if (IsBlankText(currentWeaponId))
            {
                currentWeaponId = NameToString(weapon->GetWeaponId());
            }

            std::string currentClassId = NameToString(weapon->PartNetworkConfig.WeaponClassID);
            if (IsBlankText(currentClassId))
            {
                currentClassId = NameToString(weapon->GetWeaponClassID());
            }

            if (!IsBlankText(currentWeaponId) || !IsBlankText(currentClassId))
            {
                sawReliableIdentity = true;
            }

            if (!IsBlankText(desiredWeaponId) && currentWeaponId == desiredWeaponId)
            {
                return weapon;
            }

            if (!classMatchedWeapon && !IsBlankText(desiredClassId) && currentClassId == desiredClassId)
            {
                classMatchedWeapon = weapon;
            }
        }

        if (classMatchedWeapon)
        {
            return classMatchedWeapon;
        }

        // During early deployment every live APBWeapon can briefly have blank identity.
        // Only then is slot-order fallback safe; once any weapon exposes IDs, a mismatch
        // should wait instead of accidentally writing AKM data onto the APS instance.
        if (!sawReliableIdentity && preferredWeaponIndex >= 0 && preferredWeaponIndex < static_cast<int>(weapons.size()))
        {
            return weapons[preferredWeaponIndex];
        }

        return nullptr;
    }

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
        weapon->RefreshMuzzleLocationAndDirection();

        MarkActorForReplication(weapon);
    }

    bool RefreshWeaponRuntimeVisualsOnce(APBWeapon* weapon, const FPBWeaponNetworkConfig& config)
    {
        if (!weapon)
        {
            return false;
        }

        bool shouldRefresh = false;
        {
            const std::string signature = BuildWeaponConfigSignature(config);
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            auto existing = state.WeaponVisualRefreshSignatures.find(weapon);
            if (existing == state.WeaponVisualRefreshSignatures.end() || existing->second != signature)
            {
                state.WeaponVisualRefreshSignatures[weapon] = signature;
                shouldRefresh = true;
            }
        }

        if (!shouldRefresh)
        {
            return false;
        }

        RefreshWeaponRuntimeVisuals(weapon);
        return true;
    }

    bool ApplyWeaponConfig(APBCharacter* character, const FPBWeaponNetworkConfig& config, int preferredWeaponIndex, bool& outChanged)
    {
        if (!HasWeaponConfig(config))
        {
            return true;
        }

        APBWeapon* targetWeapon = FindWeaponForConfig(character, config, preferredWeaponIndex);
        if (!targetWeapon)
        {
            return false;
        }

        const FPBWeaponNetworkConfig liveSaveConfig = targetWeapon->GetPBWeaponPartSaveConfig();
        const bool slotMapMatchesConfig = DoesWeaponSlotMapMatchConfig(targetWeapon, config);
        if (WeaponConfigEquals(targetWeapon->PartNetworkConfig, config) &&
            (WeaponConfigEquals(liveSaveConfig, config) || slotMapMatchesConfig))
        {
            RefreshWeaponRuntimeVisualsOnce(targetWeapon, config);
            return true;
        }

        const FPBWeaponNetworkConfig previousConfig = targetWeapon->PartNetworkConfig;
        const bool definitionOverrideActive = BeginScopedWeaponDefinitionOverride(config, "live-apply");

        targetWeapon->PartNetworkConfig = config;
        targetWeapon->InitWeapon(config, false);
        targetWeapon->PartNetworkConfig = config;
        targetWeapon->OnRep_PartNetworkConfig();
        targetWeapon->PartNetworkConfig = config;
        targetWeapon->InitWeapon(config, true);
        targetWeapon->PartNetworkConfig = config;
        std::string nativeMutationSummary;
        ApplyNativeWeaponPartState(targetWeapon, config, liveSaveConfig, &nativeMutationSummary);
        RefreshWeaponRuntimeVisualsOnce(targetWeapon, config);
        const FPBWeaponNetworkConfig nativeSaveConfig = targetWeapon->GetPBWeaponPartSaveConfig();
        outChanged = true;
        ClientLog("[LOADOUT] Applied weapon config: previous={" + DescribeWeaponConfig(previousConfig) +
            "} previousParts=[" + DescribeWeaponParts(previousConfig) +
            "] target={" + DescribeWeaponConfig(config) + "} targetParts=[" + DescribeWeaponParts(config) +
            "] nativeSaveParts=[" + DescribeWeaponParts(nativeSaveConfig) +
            "] slotMap=[" + DescribeWeaponSlotMap(targetWeapon) +
            "] attachedParts=[" + DescribeAttachedWeaponParts(targetWeapon) +
            "] nativeMutations=[" + nativeMutationSummary + "]");

        if (definitionOverrideActive)
        {
            EndScopedWeaponDefinitionOverride();
        }
        return true;
    }

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

        if (LauncherConfigEquals(launcher->SavedData, config))
        {
            return true;
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

        if (MeleeConfigEquals(meleeWeapon->MeleeNetworkConfig, config))
        {
            return true;
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

        if (MobilityConfigEquals(character->CurrentMobilityModule->SavedData, config))
        {
            return true;
        }

        character->CurrentMobilityModule->SavedData = config;
        character->OnRep_CurrentMobilityModule();
        MarkActorForReplication(character->CurrentMobilityModule);
        MarkActorForReplication(character);
        outChanged = true;
        return true;
    }

    std::string JoinMissingTargetNames(const std::vector<std::string>& missing)
    {
        if (missing.empty())
        {
            return "";
        }

        std::ostringstream stream;
        for (size_t index = 0; index < missing.size(); ++index)
        {
            if (index > 0)
            {
                stream << ",";
            }
            stream << missing[index];
        }

        return stream.str();
    }

    std::string GetMissingRequiredLiveTargets(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        std::vector<std::string> missing;

        if (!character)
        {
            return "character";
        }

        if (HasWeaponConfig(roleConfig.FirstWeaponPartData) && !FindWeaponForConfig(character, roleConfig.FirstWeaponPartData, 0))
        {
            missing.push_back("firstWeapon");
        }

        if (HasWeaponConfig(roleConfig.SecondWeaponPartData) && !FindWeaponForConfig(character, roleConfig.SecondWeaponPartData, 1))
        {
            missing.push_back("secondWeapon");
        }

        if (HasMeleeConfig(roleConfig.MeleeWeaponData) && !character->CurrentMeleeWeapon)
        {
            missing.push_back("meleeWeapon");
        }

        return JoinMissingTargetNames(missing);
    }

    std::string GetMissingOptionalLiveTargets(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        std::vector<std::string> missing;

        if (!character)
        {
            return "character";
        }

        if (HasLauncherConfig(roleConfig.LeftLauncherData) && !character->CurrentLeftLauncher)
        {
            missing.push_back("leftLauncher");
        }

        if (HasLauncherConfig(roleConfig.RightLauncherData) && !character->CurrentRightLauncher)
        {
            missing.push_back("rightLauncher");
        }

        if (HasMobilityConfig(roleConfig.MobilityModuleData) && !character->CurrentMobilityModule)
        {
            missing.push_back("mobilityModule");
        }

        return JoinMissingTargetNames(missing);
    }

    std::string GetMissingLiveTargets(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        const std::string requiredMissing = GetMissingRequiredLiveTargets(character, roleConfig);
        const std::string optionalMissing = GetMissingOptionalLiveTargets(character, roleConfig);
        if (requiredMissing.empty())
        {
            return optionalMissing;
        }

        if (optionalMissing.empty())
        {
            return requiredMissing;
        }

        return requiredMissing + "," + optionalMissing;
    }

    bool IsLiveCharacterReadyForConfig(APBCharacter* character, const FPBRoleNetworkConfig& roleConfig)
    {
        if (!character)
        {
            return false;
        }

        const bool wantsFirstWeapon = HasWeaponConfig(roleConfig.FirstWeaponPartData);
        const bool wantsSecondWeapon = HasWeaponConfig(roleConfig.SecondWeaponPartData);
        if (!wantsFirstWeapon && !wantsSecondWeapon)
        {
            return true;
        }

        if (wantsFirstWeapon && FindWeaponForConfig(character, roleConfig.FirstWeaponPartData, 0))
        {
            return true;
        }

        if (wantsSecondWeapon && FindWeaponForConfig(character, roleConfig.SecondWeaponPartData, 1))
        {
            return true;
        }

        return false;
    }

    std::string BuildLiveApplyDebugSummary(APBCharacter* character, const json& snapshot)
    {
        const std::string roleId = ResolveCharacterRoleId(character);
        std::ostringstream stream;
        stream << "character=" << (character ? NameToString(character->CharacterID) : "<null>")
            << ", resolvedRole=" << roleId;

        FPBRoleNetworkConfig roleConfig{};
        if (character && TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            stream << ", role=" << NameToString(roleConfig.CharacterID)
                << ", missing=" << GetMissingLiveTargets(character, roleConfig)
                << ", first={" << DescribeWeaponConfig(roleConfig.FirstWeaponPartData) << "}"
                << ", second={" << DescribeWeaponConfig(roleConfig.SecondWeaponPartData) << "}"
                << ", " << DescribeLiveWeapons(character);
        }
        else
        {
            stream << ", role=<unresolved>";
        }

        return stream.str();
    }

    bool ApplySnapshotToLiveCharacter(APBCharacter* character, const json& snapshot)
    {
        if (!character)
        {
            return false;
        }

        FPBRoleNetworkConfig roleConfig{};
        const std::string roleId = ResolveCharacterRoleId(character);
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            return false;
        }

        if (!IsLiveCharacterReadyForConfig(character, roleConfig))
        {
            ClientLog("[LOADOUT] Live snapshot waiting for weapon target. " + BuildLiveApplyDebugSummary(character, snapshot));
            return false;
        }

        bool changed = false;
        bool ready = true;

        if (!CharacterConfigEquals(character->CharacterSkinConfig, roleConfig.CharacterData))
        {
            character->CharacterSkinConfig = roleConfig.CharacterData;
            character->OnRep_CharacterSkinConfig();
            if (auto* skinManager = const_cast<UPBSkinManager*>(UPBSkinManager::GetPBSkinManager()))
            {
                skinManager->RefreshCharacterSkin(character, roleConfig.CharacterData);
            }
            MarkActorForReplication(character);
            changed = true;
        }

        ready = ApplyWeaponConfig(character, roleConfig.FirstWeaponPartData, 0, changed) && ready;
        ready = ApplyWeaponConfig(character, roleConfig.SecondWeaponPartData, 1, changed) && ready;
        ready = ApplyMeleeConfig(character->CurrentMeleeWeapon, roleConfig.MeleeWeaponData, changed) && ready;

        const bool leftLauncherReady = ApplyLauncherConfig(character->CurrentLeftLauncher, roleConfig.LeftLauncherData, changed);
        const bool rightLauncherReady = ApplyLauncherConfig(character->CurrentRightLauncher, roleConfig.RightLauncherData, changed);
        const bool mobilityReady = ApplyMobilityConfig(character, roleConfig.MobilityModuleData, changed);
        const std::string optionalMissing = GetMissingOptionalLiveTargets(character, roleConfig);

        if (ready)
        {
            if (!optionalMissing.empty() && (!leftLauncherReady || !rightLauncherReady || !mobilityReady))
            {
                ClientLog("[LOADOUT] Applied live snapshot core fields to role " + NameToString(roleConfig.CharacterID) +
                    "; optional targets pending " + optionalMissing);
            }
            else
            {
                ClientLog("[LOADOUT] Applied live snapshot to role " + NameToString(roleConfig.CharacterID));
            }
        }
        else if (changed)
        {
            ClientLog("[LOADOUT] Applied partial live snapshot to role " + NameToString(roleConfig.CharacterID) +
                "; waiting for " + GetMissingRequiredLiveTargets(character, roleConfig));
        }

        return ready;
    }

    bool IsRuntimeRoundReadyForLiveApply(APBCharacter* character)
    {
        if (!character || character->Inventory.Num() <= 0)
        {
            return false;
        }

        for (UObject* object : getObjectsOfClass(APBGameState::StaticClass(), false))
        {
            APBGameState* gameState = static_cast<APBGameState*>(object);
            if (!gameState)
            {
                continue;
            }

            const std::string roundState = NameToString(gameState->RoundState);
            if (roundState.empty() ||
                roundState == "None" ||
                roundState.contains("InvalidState") ||
                roundState.contains("RoleSelection") ||
                roundState.contains("CountdownToStart") ||
                roundState.contains("Waiting"))
            {
                continue;
            }

            return true;
        }

        return false;
    }

    void ServerTick()
    {
        if (!amServer)
        {
            return;
        }

        const auto now = std::chrono::steady_clock::now();
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (now < state.NextServerTickAt)
            {
                return;
            }

            state.NextServerTickAt = now + std::chrono::seconds(1);
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

        constexpr int maxServerApplyAttempts = 20;
        for (APBCharacter* character : GetServerPlayerCharacters())
        {
            if (!character || !IsRuntimeRoundReadyForLiveApply(character))
            {
                continue;
            }

            int attempt = 0;
            {
                State& state = GetState();
                std::scoped_lock lock(state.Mutex);
                if (state.ServerLiveApplyComplete.contains(character))
                {
                    continue;
                }

                attempt = state.ServerLiveApplyAttempts[character];
                if (attempt >= maxServerApplyAttempts)
                {
                    continue;
                }

                state.ServerLiveApplyAttempts[character] = attempt + 1;
            }

            const bool applied = ApplySnapshotToLiveCharacter(character, snapshotCopy);
            if (applied)
            {
                State& state = GetState();
                std::scoped_lock lock(state.Mutex);
                state.ServerLiveApplyComplete.insert(character);
                state.ServerLiveApplyAttempts.erase(character);
                ClientLog("[LOADOUT][SERVER] Authoritative snapshot applied. " + BuildLiveApplyDebugSummary(character, snapshotCopy));
            }
            else if (attempt + 1 >= maxServerApplyAttempts)
            {
                ClientLog("[LOADOUT][SERVER] Authoritative snapshot apply timed out. " + BuildLiveApplyDebugSummary(character, snapshotCopy));
            }
        }
    }

    void NotifyMenuConstructed()
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.MenuSeen = true;
        state.MenuSeenAt = std::chrono::steady_clock::now();
        state.InitialMenuCaptureComplete = false;
    }

    void TriggerAsyncExport(const std::string& reason, int delayMs)
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.ExportScheduled = true;
        state.PendingExportReason = reason;
        state.ExportDueAt = std::chrono::steady_clock::now() + std::chrono::milliseconds(delayMs);
        state.NextWorkerTickAt = {};
    }

    void WorkerTick()
    {
        const auto now = std::chrono::steady_clock::now();

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (state.InGameThreadTick)
            {
                return;
            }

            if (now < state.NextWorkerTickAt)
            {
                return;
            }

            state.InGameThreadTick = true;
            state.NextWorkerTickAt = now + std::chrono::milliseconds(250);
        }

        EnsureSnapshotLoaded();

        json snapshotCopy;
        bool snapshotAvailable = false;
        bool shouldTryMenuApply = false;
        bool shouldSkipMenuApply = false;
        bool shouldTryInitialCapture = false;
        bool shouldTryLiveApply = false;
        bool shouldTryScheduledExport = false;
        bool menuApplyGraceElapsed = false;
        int liveApplyAttempt = 0;
        constexpr int maxLiveApplyAttempts = 20;
        std::string exportReason;
        APBCharacter* localCharacter = GetLocalCharacter();
        const bool runtimeReadyForLiveApply = IsRuntimeRoundReadyForLiveApply(localCharacter);

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            snapshotAvailable = state.SnapshotAvailable;
            if (snapshotAvailable)
            {
                snapshotCopy = state.Snapshot;
            }

            if (state.MenuSeen && !state.InitialMenuCaptureComplete)
            {
                const auto elapsed = std::chrono::steady_clock::now() - state.MenuSeenAt;
                shouldTryInitialCapture = !state.SnapshotAvailable && elapsed >= std::chrono::seconds(2);
                menuApplyGraceElapsed = elapsed >= std::chrono::seconds(5);
            }

            shouldTryMenuApply = snapshotAvailable && !state.MenuApplyComplete && IsUnsafeMenuApplyEnabled();
            shouldSkipMenuApply = snapshotAvailable && !state.MenuApplyComplete && !IsUnsafeMenuApplyEnabled() && state.MenuSeen && menuApplyGraceElapsed;

            if (localCharacter != state.LastObservedCharacter)
            {
                state.LastObservedCharacter = localCharacter;
                state.PendingLiveApply = localCharacter != nullptr;
                state.LiveApplyAttempts = 0;
                state.NextLiveApplyAt = localCharacter ? now + std::chrono::seconds(8) : std::chrono::steady_clock::time_point{};
            }

            shouldTryLiveApply =
                snapshotAvailable &&
                state.PendingLiveApply &&
                localCharacter != nullptr &&
                runtimeReadyForLiveApply &&
                now >= state.NextLiveApplyAt &&
                state.LiveApplyAttempts < maxLiveApplyAttempts;
            if (shouldTryLiveApply)
            {
                state.LiveApplyAttempts++;
                liveApplyAttempt = state.LiveApplyAttempts;
                state.NextLiveApplyAt = now + std::chrono::seconds(5);
            }
            shouldTryScheduledExport = state.ExportScheduled && now >= state.ExportDueAt;
            exportReason = state.PendingExportReason;
        }

        if (shouldSkipMenuApply && HasArchiveLoaded())
        {
            ClientLog("[LOADOUT] Startup menu snapshot reapply is disabled by default to avoid unsafe UI recursion during login. Live battle apply remains enabled. Use -unsafeMenuLoadoutApply or PROJECTREBOUND_ENABLE_UNSAFE_MENU_APPLY=1 only for debugging.");
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.MenuApplyComplete = true;
        }

        if (shouldTryMenuApply && HasArchiveLoaded() && menuApplyGraceElapsed && IsMenuApplyReady() && ApplySnapshotToFieldModManager(snapshotCopy))
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.MenuApplyComplete = true;
            state.PendingLiveApply = true;
        }

        if (shouldTryInitialCapture && HasArchiveLoaded() && ExportSnapshotNow("initial-menu"))
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.InitialMenuCaptureComplete = true;
        }

        if (shouldTryLiveApply && ApplySnapshotToLiveCharacter(localCharacter, snapshotCopy))
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.PendingLiveApply = false;
            state.LiveApplyAttempts = 0;
        }
        else if (shouldTryLiveApply && liveApplyAttempt >= maxLiveApplyAttempts)
        {
            ClientLog("[LOADOUT] Live snapshot apply deferred too long; stopping retries to avoid network reliable buffer pressure. " +
                BuildLiveApplyDebugSummary(localCharacter, snapshotCopy));
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.PendingLiveApply = false;
        }

        if (shouldTryScheduledExport)
        {
            const bool exported = ExportSnapshotNow(exportReason.empty() ? "scheduled" : exportReason);
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            if (exported)
            {
                state.ExportScheduled = false;
                state.PendingExportReason.clear();
            }
            else
            {
                state.ExportDueAt = std::chrono::steady_clock::now() + std::chrono::milliseconds(1000);
            }
        }

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            state.InGameThreadTick = false;
        }
    }
}

// =====================================================================
//  Construction / lifetime
// =====================================================================

LoadoutManager::LoadoutManager()
    : impl_(std::make_unique<Impl>())
{
}

LoadoutManager::~LoadoutManager() = default;
LoadoutManager::LoadoutManager(LoadoutManager&&) noexcept = default;
LoadoutManager& LoadoutManager::operator=(LoadoutManager&&) noexcept = default;

// =====================================================================
//  Public facade - startup/menu signals
// =====================================================================

void LoadoutManager::PreloadSnapshot()
{
    LoadoutManagerDetail::PreloadSnapshot();
}

void LoadoutManager::NotifyMenuConstructed()
{
    LoadoutManagerDetail::NotifyMenuConstructed();
}

void LoadoutManager::RememberMenuSelectedRole(const FName& roleId)
{
    LoadoutManagerDetail::RememberMenuSelectedRole(roleId);
}

// =====================================================================
//  Public facade - ProcessEvent hook bridge
// =====================================================================

void LoadoutManager::OnClientProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    LoadoutManagerDetail::MaybeOverrideInitWeaponConfig(object, functionName, parms);

    // Keep the scoped definition override on the "pre" side so the original
    // ProcessEvent sees the temporary weapon-definition mutations in place.
    LoadoutManagerDetail::BeginProcessEventWeaponDefinitionOverride(object, functionName, parms);
}

void LoadoutManager::OnClientProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    LoadoutManagerDetail::MaybeOverrideProcessEventResult(object, functionName, parms);
    LoadoutManagerDetail::EndProcessEventWeaponDefinitionOverride();

    if (functionName.contains("PBPlayerController.SaveGameData"))
    {
        LoadoutManagerDetail::TriggerAsyncExport("player-save", 0);
    }
    else if (functionName.contains("PBFieldModManager.SavePreOrderGameSaved"))
    {
        LoadoutManagerDetail::TriggerAsyncExport("preorder-save", 0);
    }
    else if (functionName.contains("PBFieldModManager.SelectInventoryItem"))
    {
        LoadoutManagerDetail::TriggerAsyncExport("inventory-select", 0);
    }
    else if (functionName.contains("OnEquipComplete") ||
        functionName.contains("OnEquipPartCompleted") ||
        functionName.contains("OnEquipWeaponOrnamentComplete") ||
        functionName.contains("K2_OnEquipPartCompleted"))
    {
        LoadoutManagerDetail::TriggerAsyncExport("equip-complete", 0);
    }
    else if (functionName.contains("ExitEditWeaponPart") ||
        functionName.contains("ExitWeaponOrnamentPanel") ||
        functionName.contains("K2_ExitEditWeaponPart") ||
        functionName.contains("K2_CaptureSelectRoleWeapons"))
    {
        LoadoutManagerDetail::TriggerAsyncExport("customize-exit", 0);
    }
}

void LoadoutManager::OnServerProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    LoadoutManagerDetail::MaybeOverrideInitWeaponConfig(object, functionName, parms);

    // Hook callers must invoke the paired Post method after the original
    // ProcessEvent so the temporary definition override is restored promptly.
    LoadoutManagerDetail::BeginProcessEventWeaponDefinitionOverride(object, functionName, parms);
}

void LoadoutManager::OnServerProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    LoadoutManagerDetail::MaybeOverrideProcessEventResult(object, functionName, parms);
    LoadoutManagerDetail::EndProcessEventWeaponDefinitionOverride();
}

// =====================================================================
//  Public facade - worker/tick bridge
// =====================================================================

void LoadoutManager::TickClient()
{
    LoadoutManagerDetail::WorkerTick();
}

void LoadoutManager::TickServer()
{
    LoadoutManagerDetail::ServerTick();
}

