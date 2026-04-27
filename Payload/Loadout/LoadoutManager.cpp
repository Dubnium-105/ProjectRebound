// ======================================================
//  LoadoutManager — 装备快照/导出/运行时实现
// ======================================================
//
//  装备流程概述：
//    1. 启动时从磁盘预加载 sidecar JSON 快照
//    2. 监听菜单/存档/自定义事件，导出最新快照
//    3. 监听运行时 ProcessEvent 流量（客户端与服务端）
//    4. 在安全时机将快照数据回写到活跃角色/武器
//    5. 仅在需要覆盖的原生调用前后使用作用域武器定义覆盖，
//       调用后立即恢复状态
//
//  与其他系统的关系：
//    - dllmain.cpp：拥有 Hook 注册和管理器生命周期，
//      本文件拥有装备管线细节、状态机和诊断逻辑
//    - LateJoinManager：通过 OnRoleSelectionConfirmed 回调
//      介入角色确认后的装备覆盖
//    - 外部 Hook 层：仅作为薄转发层调用本文件的公有门面
//
//  维护规则：
//    尽量将行为变更限制在本翻译单元内，
//    Hook 层应保持为薄转发器。

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

#include "SDK.hpp"
#include "SDK/Engine_parameters.hpp"
#include "SDK/ProjectBoundary_parameters.hpp"
#include "Libs/json.hpp"

using namespace SDK;

std::vector<UObject*> getObjectsOfClass(UClass* theClass, bool includeDefault);
UObject* GetLastOfType(UClass* theClass, bool includeDefault);
#include "Debug/Debug.h"

extern bool LoginCompleted;
extern bool amServer;

class LoadoutManager::Impl
{
};

// 内部实现命名空间：
//   旧的装备管线现在作为私有实现细节存在于此命名空间中。
//   下方的 LoadoutManager 公有方法是 Payload 其余部分的唯一支持入口。
namespace LoadoutManagerDetail
{
    using json = nlohmann::json;

    // =====================================================================
    //  内部类型 — 待处理角色选择上下文
    // =====================================================================

    struct PendingRoleSelectionContext
    {
        std::string RoleId;
        bool IsAuthoritative = false;
        std::chrono::steady_clock::time_point ConfirmedAt{};
    };

    // =====================================================================
    //  内部类型 — 全局单例状态
    // =====================================================================

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
        bool PendingPreSpawnFieldModApply = false;
        int LiveApplyAttempts = 0;
        std::chrono::steady_clock::time_point MenuSeenAt{};
        std::chrono::steady_clock::time_point NextWorkerTickAt{};
        std::chrono::steady_clock::time_point ExportDueAt{};
        std::chrono::steady_clock::time_point NextLiveApplyAt{};
        std::chrono::steady_clock::time_point NextServerTickAt{};
        std::chrono::steady_clock::time_point NextPreSpawnFieldModApplyAt{};
        std::chrono::steady_clock::time_point LastPreSpawnFieldModApplyAt{};
        std::string PendingExportReason;
        std::string PendingPreSpawnFieldModApplyReason;
        std::string LastPreSpawnFieldModApplyFailure;
        APBPlayerController* PendingPreSpawnFieldModApplyController = nullptr;
        APBCharacter* LastObservedCharacter = nullptr;
        std::unordered_map<APBCharacter*, int> ServerLiveApplyAttempts;
        std::unordered_set<APBCharacter*> ServerLiveApplyComplete;
        std::unordered_map<APBPlayerController*, PendingRoleSelectionContext> PendingRoleSelections;
        std::unordered_map<APBWeapon*, std::string> WeaponVisualRefreshSignatures;
        std::unordered_map<APBWeapon*, std::string> WeaponInitOverrideSignatures;
        std::unordered_set<std::string> WeaponLiveMismatchLogs;
        std::unordered_set<std::string> WeaponQueryOverrideLogs;
        std::unordered_set<std::string> WeaponDefinitionOverrideLogs;
        std::unordered_set<std::string> WeaponDefinitionOverrideFailureLogs;
        std::unordered_set<std::string> WeaponProcessEventProbeLogs;
        std::string LastMenuSelectedRoleId;
    };

    // @brief 供外部调用的全局单例状态引用
    State& GetState()
    {
        static State StateInstance;
        return StateInstance;
    }

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

        // 只过滤明显损坏的 FName。运行时合法名字的索引可能远高于 60000，
        // 之前的阈值会把快照里解析出的角色/武器/皮肤名全部误判成 None。
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

    bool CanSafelyResolveLoadedObjectName(const FName& value)
    {
        return value.ComparisonIndex > 0 && value.ComparisonIndex < 10000000 && value.Number >= 0 && value.Number < 1000000;
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
    //  角色选择上下文 — 菜单记住 / 待处理 / 清理
    // =====================================================================

    // @brief 记住菜单中最后选择的角色（字符串版本）
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

    // @brief 记住菜单中最后选择的角色（FName 版本）
    void RememberMenuSelectedRole(const FName& roleId)
    {
        if (IsBlankName(roleId))
        {
            return;
        }

        RememberMenuSelectedRole(NameToString(roleId));
    }

    // @brief 获取已记住的菜单角色 ID
    std::string GetRememberedMenuSelectedRoleId()
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.LastMenuSelectedRoleId;
    }

    bool TryResolveRoleConfig(const json& snapshot, const std::string& roleId, FPBRoleNetworkConfig& outConfig);
    bool TryResolveSnapshotWeaponConfigByRoleAndWeaponId(const json& snapshot, const std::string& roleId, const std::string& weaponId, FPBWeaponNetworkConfig& outConfig);
    bool TryResolveSnapshotWeaponConfigForInit(const json& snapshot, APBWeapon* weapon, const FPBWeaponNetworkConfig& incoming, std::string& outResolvedRoleId, FPBWeaponNetworkConfig& outConfig);
    bool TryGetRoleWeaponConfigForInit(const FPBRoleNetworkConfig& roleConfig, const FPBWeaponNetworkConfig& incoming, APBWeapon* weapon, FPBWeaponNetworkConfig& outConfig);
    bool HasWeaponConfig(const FPBWeaponNetworkConfig& config);
    std::string ResolveCharacterRoleId(APBCharacter* character);
    std::string ResolveLiveCharacterRoleId(APBCharacter* character);

    // @brief 获取本地 AppData 根目录（ProjectReboundBrowser）
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

    // @brief 获取装备导出快照路径
    std::filesystem::path GetExportSnapshotPath()
    {
        return GetAppDataRoot() / "loadout-export-v1.json";
    }

    // @brief 获取启动器注入的装备快照路径
    std::filesystem::path GetLaunchSnapshotPath()
    {
        return GetAppDataRoot() / "launchers" / "loadout-launch-v1.json";
    }

    // =====================================================================
    //  运行时查询 — 对象查找 / 玩家角色 / 存档状态
    // =====================================================================

    // @brief 生成 UTC 时间戳字符串（ISO 8601）
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

    // @brief 获取运行时 FieldModManager 对象
    UPBFieldModManager* GetFieldModManager()
    {
        UObject* object = GetLastOfType(UPBFieldModManager::StaticClass(), false);
        return object ? static_cast<UPBFieldModManager*>(object) : nullptr;
    }

    // @brief 获取本地玩家控制器
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

    // @brief 获取本地玩家角色
    APBCharacter* GetLocalCharacter()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (playerController && playerController->PBCharacter)
        {
            return playerController->PBCharacter;
        }

        return nullptr;
    }

    // @brief 解析本地玩家优先角色 ID（按多来源回退）
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

    bool IsCharacterInSpawnAssemblyWindow(APBCharacter* character)
    {
        if (!character)
        {
            return false;
        }

        const auto* raw = reinterpret_cast<const std::uint8_t*>(character);
        const auto* readyPtr = reinterpret_cast<const bool*>(raw + offsetof(APBCharacter, bReadyInStartSpot));
        return !*readyPtr;
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

    // @brief 通过角色实例反查对应的 PlayerController
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

    // @brief 记录待处理的角色确认上下文（用于首生武器匹配）
    void RememberPendingRoleSelection(APBPlayerController* playerController, const std::string& roleId, bool isAuthoritative)
    {
        const std::string normalizedRoleId = NormalizeRoleId(roleId);
        if (!playerController || normalizedRoleId.empty())
        {
            return;
        }

        bool changed = false;
        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            PendingRoleSelectionContext& context = state.PendingRoleSelections[playerController];
            changed =
                context.RoleId != normalizedRoleId ||
                context.IsAuthoritative != isAuthoritative;
            context.RoleId = normalizedRoleId;
            context.IsAuthoritative = isAuthoritative;
            context.ConfirmedAt = std::chrono::steady_clock::now();
        }

        if (changed)
        {
            ClientLog("[LOADOUT] Armed pending spawn role context (" +
                std::string(isAuthoritative ? "server" : "client") + ") for " +
                playerController->GetFullName() + ": role=" + normalizedRoleId);
        }
    }

    // @brief 查询指定控制器当前待处理的角色 ID
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

    // @brief 清理失效/已完成的待处理角色上下文
    void PrunePendingRoleSelections()
    {
        std::unordered_set<APBPlayerController*> liveControllers;
        std::vector<std::pair<APBPlayerController*, std::string>> stabilizedControllers;
        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            if (!playerController)
            {
                continue;
            }

            liveControllers.insert(playerController);
        }

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            for (auto it = state.PendingRoleSelections.begin(); it != state.PendingRoleSelections.end();)
            {
                APBPlayerController* playerController = it->first;
                if (!playerController || liveControllers.find(it->first) == liveControllers.end())
                {
                    it = state.PendingRoleSelections.erase(it);
                    continue;
                }

                APBCharacter* character = GetControllerCharacter(playerController);
                if (character && IsCharacterAlive(character) && !IsCharacterInSpawnAssemblyWindow(character))
                {
                    stabilizedControllers.push_back({ playerController, ResolveLiveCharacterRoleId(character) });
                    it = state.PendingRoleSelections.erase(it);
                    continue;
                }

                ++it;
            }
        }

        for (const auto& [playerController, actualRoleId] : stabilizedControllers)
        {
            ClientLog("[LOADOUT] Cleared pending spawn role context after live character stabilized: " +
                playerController->GetFullName() + " -> " +
                (actualRoleId.empty() ? std::string("<unknown>") : actualRoleId));
        }
    }

    // @brief 获取服务端当前玩家角色集合（去重）
    std::vector<APBCharacter*> GetServerPlayerCharacters()
    {
        std::vector<APBCharacter*> characters;
        std::unordered_set<APBCharacter*> seen;

        for (UObject* object : getObjectsOfClass(APBPlayerController::StaticClass(), false))
        {
            APBPlayerController* playerController = static_cast<APBPlayerController*>(object);
            APBCharacter* character = GetControllerCharacter(playerController);
            if (!character || seen.find(character) != seen.end())
            {
                continue;
            }

            seen.insert(character);
            characters.push_back(character);
        }

        return characters;
    }

    // @brief 判断本地存档/归档是否已完成加载
    bool HasArchiveLoaded()
    {
        APBPlayerController* playerController = GetLocalPlayerController();
        if (playerController)
        {
            return playerController->bHasLoadedArchive;
        }

        return LoginCompleted && GetFieldModManager() != nullptr;
    }

    // @brief 判断是否启用不安全菜单应用开关（调试用途）
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

    // @brief 判断菜单展示角色是否已就绪
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

    // @brief 判断菜单侧应用快照的前置条件是否满足
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

    // =====================================================================
    //  快照捕获与导出 — 菜单侧采集 / 磁盘写入 / 延迟加载
    // =====================================================================

    // @brief 从菜单当前状态捕获完整装备快照
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

    // @brief 立即导出快照到磁盘并更新内存态
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
        }

        ClientLog("[LOADOUT] Exported local snapshot (" + reason + ") -> " + exportPath.string());
        return true;
    }

    // @brief 确保快照已从启动器或本地导出文件加载
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

        loadedSnapshot.erase("selectedRoleId");

        {
            std::scoped_lock lock(state.Mutex);
            state.Snapshot = loadedSnapshot;
            state.SnapshotAvailable = true;
            state.PendingLiveApply = true;
        }

        ClientLog("[LOADOUT] Loaded snapshot from " + source);
    }

    // @brief 启动阶段调用的快照预加载入口
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

    // @brief 将快照中的背包/武器配置应用到 FieldModManager
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

        std::string targetRoleId = NormalizeRoleId(originalRoleId);
        if (targetRoleId.empty())
        {
            // 优先使用局内已确认的待处理角色（PendingRoleSelections），
            // 避免复活时回退到菜单中旧的选择覆盖玩家当前角色。
            targetRoleId = GetPendingRoleIdForController(playerController);
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

    bool ApplySnapshotToPreSpawnInventoryState(const json& snapshot, APBPlayerController* preferredController, std::string& outDetail)
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

    bool TryApplySnapshotToFieldModManagerForPreSpawn(const char* reason, APBPlayerController* preferredController = nullptr)
    {
        const auto now = std::chrono::steady_clock::now();
        const std::string queuedReason = (reason && *reason) ? reason : "pre-spawn";
        bool shouldLogQueued = false;
        State& state = GetState();
        {
            std::scoped_lock lock(state.Mutex);

            if (state.LastPreSpawnFieldModApplyAt.time_since_epoch().count() != 0 &&
                now - state.LastPreSpawnFieldModApplyAt < std::chrono::milliseconds(500))
            {
                return false;
            }

            shouldLogQueued =
                !state.PendingPreSpawnFieldModApply ||
                state.PendingPreSpawnFieldModApplyReason != queuedReason;

            state.PendingPreSpawnFieldModApply = true;
            state.NextPreSpawnFieldModApplyAt = now;
            state.NextWorkerTickAt = {};
            state.PendingPreSpawnFieldModApplyReason = queuedReason;
            if (preferredController)
            {
                state.PendingPreSpawnFieldModApplyController = preferredController;
            }
        }

        if (shouldLogQueued)
        {
            ClientLog("[LOADOUT] Queued pre-spawn snapshot inventory apply: " + queuedReason +
                (preferredController ? " controller=" + preferredController->GetFullName() : ""));
        }

        return true;
    }

    void DrainPendingPreSpawnInventoryApply(const json& snapshot)
    {
        const auto now = std::chrono::steady_clock::now();
        bool shouldTryApply = false;
        std::string reason;
        APBPlayerController* targetController = nullptr;

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            shouldTryApply =
                state.PendingPreSpawnFieldModApply &&
                now >= state.NextPreSpawnFieldModApplyAt;
            reason = state.PendingPreSpawnFieldModApplyReason;
            targetController = state.PendingPreSpawnFieldModApplyController;
        }

        if (!shouldTryApply)
        {
            return;
        }

        std::string detail;
        const bool applied = ApplySnapshotToPreSpawnInventoryState(snapshot, targetController, detail);

        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        if (applied)
        {
            state.PendingPreSpawnFieldModApply = false;
            state.PendingPreSpawnFieldModApplyReason.clear();
            state.PendingPreSpawnFieldModApplyController = nullptr;
            state.LastPreSpawnFieldModApplyFailure.clear();
            state.NextPreSpawnFieldModApplyAt = {};
            state.LastPreSpawnFieldModApplyAt = std::chrono::steady_clock::now();

            ClientLog("[LOADOUT] Applied snapshot inventory to pre-spawn state: " +
                (reason.empty() ? std::string("pre-spawn") : reason) +
                " (" + detail + ")");
            return;
        }

        if (detail.empty())
        {
            detail = "unknown";
        }

        if (state.LastPreSpawnFieldModApplyFailure != detail)
        {
            ClientLog("[LOADOUT] Pre-spawn snapshot inventory apply waiting (" +
                (reason.empty() ? std::string("pre-spawn") : reason) +
                "): " + detail);
            state.LastPreSpawnFieldModApplyFailure = detail;
        }

        state.NextPreSpawnFieldModApplyAt = std::chrono::steady_clock::now() + std::chrono::milliseconds(250);
    }

    // =====================================================================
    //  快照应用 — FieldModManager / 实时角色 / 武器配置解析
    // =====================================================================

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

    // @brief 从快照中解析角色配置（支持 roleId / selectedRole / 首项回退）
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
                if (roleId.empty() && snapshot["roles"].size() == 1 && snapshot["roles"][0].is_object())
                {
                    roleJson = snapshot["roles"][0];
                }
            }

            if (!roleJson.is_object() && roleId.empty() && snapshot["roles"].size() == 1 && snapshot["roles"][0].is_object())
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

    // =====================================================================
    //  配置比较 — 各网络配置类型的判等
    // =====================================================================

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

    // =====================================================================
    //  运行时辅助 — 角色 ID 解析 / 武器列表 / 描述 / 签名
    // =====================================================================

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

        APBPlayerState* playerState = character->PBPlayerState;
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

        APBPlayerState* playerState = character->PBPlayerState;
        if (playerState)
        {
            if (!IsBlankName(playerState->PossessedCharacterId))
            {
                return NameToString(playerState->PossessedCharacterId);
            }

            if (!IsBlankName(playerState->UsageCharacterID))
            {
                return NameToString(playerState->UsageCharacterID);
            }

            if (!IsBlankName(playerState->SelectedCharacterID))
            {
                return NameToString(playerState->SelectedCharacterID);
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

    // =====================================================================
    //  武器定义覆盖 — 数据表查找 / PartSlot 读写 / 作用域上下文
    // =====================================================================

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

        APBCharacter* ownerCharacter = nullptr;
        try
        {
            ownerCharacter = weapon->GetPawnOwner();
        }
        catch (...)
        {
            ownerCharacter = nullptr;
        }

        auto tryLiveRole = [&](const FPBWeaponNetworkConfig& candidateIncoming) -> bool
        {
            const std::string liveRoleId = ResolveLiveCharacterRoleId(ownerCharacter);
            if (liveRoleId.empty())
            {
                return false;
            }

            FPBRoleNetworkConfig roleConfig{};
            if (!TryResolveRoleConfig(snapshot, liveRoleId, roleConfig))
            {
                return false;
            }

            if (!TryGetRoleWeaponConfigForInit(roleConfig, candidateIncoming, weapon, outConfig))
            {
                return false;
            }

            outRoleId = liveRoleId;
            return true;
        };

        if (tryLiveRole(weapon->PartNetworkConfig))
        {
            return true;
        }

        const FPBWeaponNetworkConfig nativeSaveConfig = weapon->GetPBWeaponPartSaveConfig();
        if (tryLiveRole(nativeSaveConfig))
        {
            return true;
        }

        if (TryResolveSnapshotWeaponConfigForInit(snapshot, weapon, weapon->PartNetworkConfig, outRoleId, outConfig))
        {
            return true;
        }

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

    // =====================================================================
    //  武器定义覆盖 — 作用域 Begin/End / ProcessEvent Hook 驱动
    // =====================================================================

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

    // @brief 在 ProcessEvent 前按函数类型开启作用域武器定义覆盖
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
        const bool isCharacterAddWeapon = functionName.contains("PBCharacter.AddWeapon");
        const bool isCharacterEquipPendingWeapon = functionName.contains("PBCharacter.EquipPendingWeapon");
        if (!isFieldModGetWeapon && !isCustomizeGetWeapon && !isFieldModSpawnWeapon && !isWeaponInit &&
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
        else if (isCharacterAddWeapon || isCharacterEquipPendingWeapon)
        {
            APBCharacter* character = object->IsA(APBCharacter::StaticClass()) ? static_cast<APBCharacter*>(object) : nullptr;
            if (!character || !IsCharacterInSpawnAssemblyWindow(character))
            {
                return false;
            }

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
        else
        {
            return false;
        }

        return BeginScopedWeaponDefinitionOverride(targetConfig, source);
    }

    // @brief 结束并恢复 ProcessEvent 作用域武器定义覆盖
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
        if (roleId.empty() || weaponId.empty())
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

    bool TryResolveSnapshotWeaponConfigFromPendingRoleSelections(
        const json& snapshot,
        APBWeapon* weapon,
        const FPBWeaponNetworkConfig& incoming,
        std::string& outRoleId,
        FPBWeaponNetworkConfig& outConfig)
    {
        outRoleId.clear();

        std::vector<std::string> roleCandidates;
        auto pushUniqueRole = [&](const std::string& roleId)
        {
            const std::string normalizedRoleId = NormalizeRoleId(roleId);
            if (normalizedRoleId.empty())
            {
                return;
            }

            if (std::find(roleCandidates.begin(), roleCandidates.end(), normalizedRoleId) == roleCandidates.end())
            {
                roleCandidates.push_back(normalizedRoleId);
            }
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

        if (APBPlayerController* ownerController = FindPlayerControllerForCharacter(ownerCharacter))
        {
            pushUniqueRole(GetPendingRoleIdForController(ownerController));
        }

        if (!amServer)
        {
            pushUniqueRole(GetPendingRoleIdForController(GetLocalPlayerController()));
        }

        {
            State& state = GetState();
            std::scoped_lock lock(state.Mutex);
            for (const auto& [_, context] : state.PendingRoleSelections)
            {
                pushUniqueRole(context.RoleId);
            }
        }

        int bestScore = 0;
        bool found = false;
        std::string bestRoleId;
        FPBWeaponNetworkConfig bestConfig{};

        for (const std::string& roleId : roleCandidates)
        {
            FPBRoleNetworkConfig roleConfig{};
            if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
            {
                continue;
            }

            FPBWeaponNetworkConfig candidateConfig{};
            if (!TryGetRoleWeaponConfigForInit(roleConfig, incoming, weapon, candidateConfig))
            {
                continue;
            }

            const int score = ScoreWeaponSnapshotMatch(candidateConfig, incoming, weapon);
            if (score <= bestScore)
            {
                continue;
            }

            bestScore = score;
            bestRoleId = roleId;
            bestConfig = candidateConfig;
            found = true;
        }

        if (!found)
        {
            return false;
        }

        outRoleId = bestRoleId;
        outConfig = bestConfig;
        return true;
    }

    bool TryResolveSnapshotWeaponConfigForInit(const json& snapshot, APBWeapon* weapon, const FPBWeaponNetworkConfig& incoming, std::string& outRoleId, FPBWeaponNetworkConfig& outConfig)
    {
        outRoleId.clear();

        auto tryRole = [&](const std::string& roleId) -> bool
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

        if (TryResolveSnapshotWeaponConfigFromPendingRoleSelections(snapshot, weapon, incoming, outRoleId, outConfig))
        {
            return true;
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

    // =====================================================================
    //  实时武器应用 — InitWeapon 覆盖 / ProcessEvent 结果覆盖
    // =====================================================================

    // @brief 按快照覆写 PBWeapon.InitWeapon 输入配置
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

    // @brief 按快照覆写武器查询/生成相关 ProcessEvent 返回值
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

        if (ShouldLogWeaponQueryOverride("fieldmod-spawn", roleId, weaponId, targetConfig))
        {
            const FPBWeaponNetworkConfig liveConfig = spawnedWeapon->PartNetworkConfig;
            const FPBWeaponNetworkConfig nativeSaveConfig = spawnedWeapon->GetPBWeaponPartSaveConfig();
            ClientLog("[LOADOUT] SpawnWeapon completed under pre-spawn override for role " + roleId +
                ", weapon " + weaponId + ": liveParts=[" + DescribeWeaponParts(liveConfig) +
                "] targetParts=[" + DescribeWeaponParts(targetConfig) +
                "] nativeSaveParts=[" + DescribeWeaponParts(nativeSaveConfig) +
                "] slotMap=[" + DescribeWeaponSlotMap(spawnedWeapon) +
                "] attachedParts=[" + DescribeAttachedWeaponParts(spawnedWeapon) +
                "] liveMatchesTarget=" + (WeaponConfigEquals(liveConfig, targetConfig) ? "true" : "false") +
                "] nativeMatchesTarget=" + (WeaponConfigEquals(nativeSaveConfig, targetConfig) ? "true" : "false") + "]");
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

    APBWeapon* GetWeaponByPreferredIndex(APBCharacter* character, int preferredWeaponIndex)
    {
        if (!character || preferredWeaponIndex < 0)
        {
            return nullptr;
        }

        const std::vector<APBWeapon*> weapons = GetCharacterWeaponList(character);
        if (preferredWeaponIndex >= static_cast<int>(weapons.size()))
        {
            return nullptr;
        }

        return weapons[preferredWeaponIndex];
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

    bool ShouldLogLiveWeaponMismatch(
        APBWeapon* weapon,
        const std::string& label,
        const FPBWeaponNetworkConfig& expectedConfig,
        const FPBWeaponNetworkConfig& liveConfig,
        const FPBWeaponNetworkConfig& nativeSaveConfig,
        bool matchedByIdentity)
    {
        if (!weapon)
        {
            return true;
        }

        std::ostringstream key;
        key << reinterpret_cast<std::uintptr_t>(weapon)
            << "|" << label
            << "|" << BuildWeaponConfigSignature(expectedConfig)
            << "|" << BuildWeaponConfigSignature(liveConfig)
            << "|" << BuildWeaponConfigSignature(nativeSaveConfig)
            << "|" << (matchedByIdentity ? "identity" : "slot");

        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        return state.WeaponLiveMismatchLogs.insert(key.str()).second;
    }

    // =====================================================================
    //  实时角色应用 — 快照 → 活跃角色/武器/配件/机动模块
    // =====================================================================

    bool InspectLiveWeaponConfig(APBCharacter* character, const FPBWeaponNetworkConfig& config, int preferredWeaponIndex, const char* label)
    {
        if (!HasWeaponConfig(config))
        {
            return true;
        }

        APBWeapon* targetWeapon = FindWeaponForConfig(character, config, preferredWeaponIndex);
        const bool matchedByIdentity = targetWeapon != nullptr;
        if (!targetWeapon)
        {
            targetWeapon = GetWeaponByPreferredIndex(character, preferredWeaponIndex);
        }

        if (!targetWeapon)
        {
            return false;
        }

        const FPBWeaponNetworkConfig liveConfig = targetWeapon->PartNetworkConfig;
        const FPBWeaponNetworkConfig liveSaveConfig = targetWeapon->GetPBWeaponPartSaveConfig();
        const bool slotMapMatchesConfig = DoesWeaponSlotMapMatchConfig(targetWeapon, config);
        if (WeaponConfigEquals(liveConfig, config) &&
            (WeaponConfigEquals(liveSaveConfig, config) || slotMapMatchesConfig))
        {
            return true;
        }

        if (ShouldLogLiveWeaponMismatch(targetWeapon, label, config, liveConfig, liveSaveConfig, matchedByIdentity))
        {
            ClientLog("[LOADOUT] Spawned weapon differs from snapshot, but runtime weapon correction is disabled (" + std::string(label) +
                "): expected={" + DescribeWeaponConfig(config) +
                "} expectedParts=[" + DescribeWeaponParts(config) +
                "] live={" + DescribeWeaponConfig(liveConfig) +
                "} liveParts=[" + DescribeWeaponParts(liveConfig) +
                "] nativeSaveParts=[" + DescribeWeaponParts(liveSaveConfig) +
                "] slotMap=[" + DescribeWeaponSlotMap(targetWeapon) +
                "] attachedParts=[" + DescribeAttachedWeaponParts(targetWeapon) +
                "] matchedByIdentity=" + (matchedByIdentity ? "true" : "false") + "]");
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

        if (HasWeaponConfig(roleConfig.FirstWeaponPartData) && !GetWeaponByPreferredIndex(character, 0))
        {
            missing.push_back("firstWeapon");
        }

        if (HasWeaponConfig(roleConfig.SecondWeaponPartData) && !GetWeaponByPreferredIndex(character, 1))
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

        int requiredWeaponCount = 0;
        if (HasWeaponConfig(roleConfig.FirstWeaponPartData))
        {
            requiredWeaponCount++;
        }

        if (HasWeaponConfig(roleConfig.SecondWeaponPartData))
        {
            requiredWeaponCount++;
        }

        return static_cast<int>(GetCharacterWeaponList(character).size()) >= requiredWeaponCount;
    }

    std::string BuildLiveApplyDebugSummary(APBCharacter* character, const json& snapshot)
    {
        const std::string roleId = ResolveLiveCharacterRoleId(character);
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

    // @brief 将快照应用到已出生的实时角色对象
    bool ApplySnapshotToLiveCharacter(APBCharacter* character, const json& snapshot)
    {
        if (!character)
        {
            return false;
        }

        FPBRoleNetworkConfig roleConfig{};
        const std::string roleId = ResolveLiveCharacterRoleId(character);
        if (!TryResolveRoleConfig(snapshot, roleId, roleConfig))
        {
            return false;
        }

        if (!IsLiveCharacterReadyForConfig(character, roleConfig))
        {
            ClientLog("[LOADOUT] Live snapshot waiting for spawned weapon actors. " + BuildLiveApplyDebugSummary(character, snapshot));
            return false;
        }

        bool changed = false;
        bool ready = true;
        const bool firstWeaponMatches = InspectLiveWeaponConfig(character, roleConfig.FirstWeaponPartData, 0, "firstWeapon");
        const bool secondWeaponMatches = InspectLiveWeaponConfig(character, roleConfig.SecondWeaponPartData, 1, "secondWeapon");
        (void)firstWeaponMatches;
        (void)secondWeaponMatches;

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

        ready = ApplyMeleeConfig(character->CurrentMeleeWeapon, roleConfig.MeleeWeaponData, changed) && ready;

        const bool leftLauncherReady = ApplyLauncherConfig(character->CurrentLeftLauncher, roleConfig.LeftLauncherData, changed);
        const bool rightLauncherReady = ApplyLauncherConfig(character->CurrentRightLauncher, roleConfig.RightLauncherData, changed);
        const bool mobilityReady = ApplyMobilityConfig(character, roleConfig.MobilityModuleData, changed);
        const std::string optionalMissing = GetMissingOptionalLiveTargets(character, roleConfig);
        const bool weaponMismatchRemains = !firstWeaponMatches || !secondWeaponMatches;

        if (ready)
        {
            if (weaponMismatchRemains)
            {
                ClientLog("[LOADOUT] Applied live snapshot non-weapon fields to role " + NameToString(roleConfig.CharacterID) +
                    "; weapon mismatch remains on spawned actors.");
            }
            else if (!optionalMissing.empty() && (!leftLauncherReady || !rightLauncherReady || !mobilityReady))
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

    // @brief 判断当前回合状态是否允许执行实时快照应用
    bool IsRuntimeRoundReadyForLiveApply(APBCharacter* character)
    {
        if (!character || character->Inventory.Num() <= 0 || !IsCharacterAlive(character))
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

    // =====================================================================
    //  服务端 Tick — 权威快照应用驱动
    // =====================================================================

    // @brief 服务端周期驱动：尝试对所有玩家角色执行权威快照应用
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

            // If a pre-spawn apply is pending, bypass the 1-second throttle
            // to ensure ServerPreOrderInventory is applied before Pawn creation.
            // This is critical for initial-join deferred-spawn: role confirmation
            // queues a pre-spawn apply, and the LateJoinManager may attempt to
            // spawn the Pawn within the same second.
            if (state.PendingPreSpawnFieldModApply)
            {
                state.NextServerTickAt = now;
            }

            if (now < state.NextServerTickAt)
            {
                return;
            }

            state.NextServerTickAt = now + std::chrono::seconds(1);
        }

        PrunePendingRoleSelections();
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

        DrainPendingPreSpawnInventoryApply(snapshotCopy);

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
                if (state.ServerLiveApplyComplete.find(character) != state.ServerLiveApplyComplete.end())
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

    // =====================================================================
    //  客户端 Worker Tick — 异步导出 / 菜单应用 / 实时应用驱动
    // =====================================================================

    // @brief 通知菜单构建完成，启动初始捕获/应用窗口
    void NotifyMenuConstructed()
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.MenuSeen = true;
        state.MenuSeenAt = std::chrono::steady_clock::now();
        state.InitialMenuCaptureComplete = false;
    }

    // @brief 调度一次延迟异步快照导出
    void TriggerAsyncExport(const std::string& reason, int delayMs)
    {
        State& state = GetState();
        std::scoped_lock lock(state.Mutex);
        state.ExportScheduled = true;
        state.PendingExportReason = reason;
        state.ExportDueAt = std::chrono::steady_clock::now() + std::chrono::milliseconds(delayMs);
        state.NextWorkerTickAt = {};
    }

    // @brief 客户端周期驱动：菜单应用、实时应用与导出调度
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

        PrunePendingRoleSelections();
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

        if (snapshotAvailable)
        {
            DrainPendingPreSpawnInventoryApply(snapshotCopy);
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

// @brief 从磁盘预加载持久化的装备快照
void LoadoutManager::PreloadSnapshot()
{
    LoadoutManagerDetail::PreloadSnapshot();
}

// @brief 通知管理器主菜单已构建完成
void LoadoutManager::NotifyMenuConstructed()
{
    LoadoutManagerDetail::NotifyMenuConstructed();
}

// @brief 记住菜单中最后显式选择的角色
void LoadoutManager::RememberMenuSelectedRole(const FName& roleId)
{
    LoadoutManagerDetail::RememberMenuSelectedRole(roleId);
}

// @brief 从已确认的运行时角色选择中设置待处理的出生角色上下文
void LoadoutManager::OnRoleSelectionConfirmed(APBPlayerController* playerController, const FName& roleId, bool isAuthoritative)
{
    if (!playerController || LoadoutManagerDetail::IsBlankName(roleId))
    {
        return;
    }

    // 同步更新菜单记住的角色，使 ApplySnapshotToFieldModManager 的回退逻辑
    // 在复活后也能拿到玩家局内最新确认的角色，而非停留在菜单旧值。
    LoadoutManagerDetail::RememberMenuSelectedRole(roleId);

    LoadoutManagerDetail::RememberPendingRoleSelection(
        playerController,
        LoadoutManagerDetail::NameToString(roleId),
        isAuthoritative);

    LoadoutManagerDetail::TryApplySnapshotToFieldModManagerForPreSpawn(
        isAuthoritative ? "role-confirmed-server" : "role-confirmed-client",
        playerController);
}

// =====================================================================
//  公有接口 — ProcessEvent Hook 桥接
// =====================================================================

// @brief 客户端 ProcessEvent pre-hook 入口。
//  1. 覆盖 InitWeapon 参数中的武器配置
//  2. 开启作用域武器定义覆盖，使原始 ProcessEvent 看到临时修改
void LoadoutManager::OnClientProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    if (functionName.contains("OnRestartInStartSpot"))
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

        LoadoutManagerDetail::TryApplySnapshotToFieldModManagerForPreSpawn("restart-in-start-spot-client", playerController);
    }

    LoadoutManagerDetail::MaybeOverrideInitWeaponConfig(object, functionName, parms);

    // 在 "pre" 侧保持作用域定义覆盖，使原始 ProcessEvent 执行时
    // 能看到临时的武器定义修改
    LoadoutManagerDetail::BeginProcessEventWeaponDefinitionOverride(object, functionName, parms);
}

// @brief 客户端 ProcessEvent post-hook 入口。
//  1. 覆盖 GetWeaponNetworkConfig/SpawnWeapon 的返回值
//  2. 恢复作用域武器定义覆盖
//  3. 根据事件类型调度异步快照导出
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

// @brief 服务端 ProcessEvent pre-hook 入口。与客户端路径对称。
void LoadoutManager::OnServerProcessEventPre(UObject* object, const std::string& functionName, void* parms)
{
    if (functionName.contains("OnRestartInStartSpot"))
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

        LoadoutManagerDetail::TryApplySnapshotToFieldModManagerForPreSpawn("restart-in-start-spot-server", playerController);
    }

    LoadoutManagerDetail::MaybeOverrideInitWeaponConfig(object, functionName, parms);

    // Hook 调用方必须在原始 ProcessEvent 之后调用配对的 Post 方法，
    // 以便及时恢复临时的武器定义覆盖
    LoadoutManagerDetail::BeginProcessEventWeaponDefinitionOverride(object, functionName, parms);
}

// @brief 服务端 ProcessEvent post-hook 入口。恢复作用域覆盖。
void LoadoutManager::OnServerProcessEventPost(UObject* object, const std::string& functionName, void* parms)
{
    LoadoutManagerDetail::MaybeOverrideProcessEventResult(object, functionName, parms);
    LoadoutManagerDetail::EndProcessEventWeaponDefinitionOverride();
}

// =====================================================================
//  公有接口 — Worker/Tick 桥接
// =====================================================================

// @brief 推进客户端侧异步导出/应用工作
void LoadoutManager::TickClient()
{
    LoadoutManagerDetail::WorkerTick();
}

// @brief 推进服务端侧权威应用工作
void LoadoutManager::TickServer()
{
    LoadoutManagerDetail::ServerTick();
}

