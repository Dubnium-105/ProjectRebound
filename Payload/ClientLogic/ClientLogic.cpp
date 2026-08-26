#include "ClientLogic.h"

#include "DirectMatchUiCleanupPolicy.h"
#include "SeamlessIntroCameraPolicy.h"

#include "../Communication/CommandProtocol.h"
#include "../Config/Config.h"
#include "../Config/CommandLinePolicy.h"
#include "../Debug/Debug.h"
#include "../Loadout/LoadoutApplication.h"
#include "../Loadout/LoadoutSerializer.h"
#include "../Loadout/MetaserverClient.h"
#include "../Loadout/WeaponArchivePolicy.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../Utility/Utility.h"

#include <Windows.h>

#include <array>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <iomanip>
#include <mutex>
#include <optional>
#include <sstream>
#include <string>
#include <thread>
#include <unordered_set>
#include <utility>
#include <vector>

using namespace SDK;

extern "C" void PayloadPushClientProcessEventSuppression();
extern "C" void PayloadPopClientProcessEventSuppression();
extern uintptr_t BaseAddress;

namespace
{
    using json = nlohmann::json;

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

        ScopedClientProcessEventSuppression(
            const ScopedClientProcessEventSuppression&) = delete;
        ScopedClientProcessEventSuppression& operator=(
            const ScopedClientProcessEventSuppression&) = delete;
    };

    class ScopedRoleInventoryStorage
    {
    public:
        explicit ScopedRoleInventoryStorage(
            FPBFieldModRoleGameSavedNetworkConfig& saved)
            : saved_(saved)
        {
        }

        ~ScopedRoleInventoryStorage()
        {
            // The generated TArray wrapper releases its outer allocation but
            // does not invoke destructors for nested inventory values.
            for (auto& inventory : saved_.RoleInventoryNetworkConfigArray)
            {
                inventory.CharacterSlots.Free();
                inventory.InventoryItems.Free();
            }
        }

        ScopedRoleInventoryStorage(const ScopedRoleInventoryStorage&) = delete;
        ScopedRoleInventoryStorage& operator=(
            const ScopedRoleInventoryStorage&) = delete;

    private:
        FPBFieldModRoleGameSavedNetworkConfig& saved_;
    };

    enum class ConnectStage
    {
        Idle,
        Queued,
        WaitingAfterLogin
    };

    std::mutex connectMutex;
    std::optional<std::string> pendingTarget;
    std::string currentTarget;
    ConnectStage connectStage = ConnectStage::Idle;
    std::chrono::steady_clock::time_point nextActionAt{};
    std::chrono::steady_clock::time_point frontendCleanupUntil{};
    std::chrono::steady_clock::time_point nextFrontendCleanupAt{};
    UWorld* directTravelSourceWorld = nullptr;
    bool directTravelUiFinalized = false;
    std::atomic<bool> ownedSeamlessDestinationUiCleanupPending{false};
    std::atomic<bool> ownedSeamlessDestinationUiCleanupWaitLogged{false};
    std::atomic<bool> ownedSeamlessIntroCameraRecoveryPending{false};
    std::atomic<bool> ownedSeamlessIntroCameraRecoveryWaitLogged{false};
    std::atomic<bool> ownedSeamlessIntroRoundBoundaryReached{false};
    std::atomic<bool> nativeRespawnUiCleanupPending{false};
    std::atomic<bool> loginCompleted{false};
    std::atomic<DWORD> gameThreadId{0};

    constexpr auto LoginSettleDelay = std::chrono::seconds(2);
    constexpr auto FrontendCleanupDuration = std::chrono::seconds(30);
    constexpr auto FrontendCleanupInterval = std::chrono::milliseconds(500);

    void HideDirectMatchFrontendLayers(bool logAllLayers)
    {
        // PBMainMenuManager is a persistent LocalPlayer subsystem. A raw
        // `open` changes the network world but does not pop its MenuStack, so
        // the frontend remains interactive over the match UI. Login creates
        // EnterGame -> LoginGate -> MainMenu layers. Deactivating only the top
        // MainMenu reveals the still-active "CONNECTING TO PLATFORM SERVER"
        // LoginGate. GetTopMenuWidget continues to report MainMenu after its
        // deactivation, so the lower login layers must be addressed by their
        // exact generated classes rather than inferred stack order.
        const std::array<UClass*, 3> frontendClasses = {
            UUMG_LoginGate_C::StaticClass(),
            UUMG_EnterGame_C::StaticClass(),
            UUMG_MainMenuBase_C::StaticClass(),
        };

        std::size_t cleanedCount = 0;
        for (UClass* frontendClass : frontendClasses)
        {
            const auto widgets = getObjectsOfClass(frontendClass, false);
            for (auto it = widgets.rbegin(); it != widgets.rend(); ++it)
            {
                if (cleanedCount >= DirectMatchUiCleanupPolicy::MaxFrontendWidgets)
                {
                    ClientLog("[CLIENT] Stopped direct-match frontend cleanup at its safety limit.");
                    return;
                }

                auto* const widget = reinterpret_cast<UCommonActivatableWidget*>(*it);
                if (!widget)
                    continue;

                const std::string widgetName = widget->GetFullName();
                if (!DirectMatchUiCleanupPolicy::IsDirectMatchFrontendWidget(widgetName))
                    continue;

                const bool wasActivated = widget->IsActivated();
                widget->SetVisibility(ESlateVisibility::Hidden);
                if (wasActivated)
                    widget->DeactivateWidget();
                ++cleanedCount;
                if (logAllLayers || wasActivated)
                {
                    ClientLog("[CLIENT] Hid direct-match frontend layer (activated=" +
                        std::string(wasActivated ? "true" : "false") + "): " + widgetName);
                }
            }
        }

        if (logAllLayers && cleanedCount == 0)
            ClientLog("[CLIENT] No direct-match frontend layer required cleanup before travel.");
    }

    void DetachDirectMatchAuthLayersAfterTravel()
    {
        const std::array<UClass*, 2> authClasses = {
            UUMG_LoginGate_C::StaticClass(),
            UUMG_Login_C::StaticClass(),
        };

        std::size_t detachedCount = 0;
        for (UClass* authClass : authClasses)
        {
            const auto widgets = getObjectsOfClass(authClass, false);
            for (auto it = widgets.rbegin(); it != widgets.rend(); ++it)
            {
                if (detachedCount >= DirectMatchUiCleanupPolicy::MaxFrontendWidgets)
                    return;

                auto* const widget = reinterpret_cast<UWidget*>(*it);
                if (!widget)
                    continue;

                const std::string widgetName = widget->GetFullName();
                widget->SetVisibility(ESlateVisibility::Collapsed);
                widget->RemoveFromParent();
                ++detachedCount;
                ClientLog("[CLIENT] Detached direct-travel auth layer: " + widgetName);
            }
        }
    }

    std::size_t DetachRetainedSourceMatchLayers()
    {
        std::size_t detachedCount = 0;
        const auto widgets = getObjectsOfClass(UUserWidget::StaticClass(), false);
        for (auto it = widgets.rbegin(); it != widgets.rend(); ++it)
        {
            if (detachedCount >=
                DirectMatchUiCleanupPolicy::MaxRetainedMatchWidgets)
            {
                ClientLog("[MULTIMATCH] Stopped retained match-layer cleanup "
                    "at its safety limit.");
                break;
            }

            auto* const widget = static_cast<UUserWidget*>(*it);
            if (!widget)
                continue;

            const std::string widgetName = widget->GetFullName();
            if (!DirectMatchUiCleanupPolicy::IsRetainedSourceMatchWidget(
                    widgetName) ||
                !widget->IsInViewport())
            {
                continue;
            }

            widget->SetVisibility(ESlateVisibility::Collapsed);
            ++detachedCount;
            // Seamless travel does not run the normal return-to-menu teardown
            // for these source-match roots. Use UMG's public detach path only;
            // do not mutate viewport slots, widget trees, Pawn state or input.
            widget->RemoveFromParent();
            ClientLog("[MULTIMATCH] Detached retained source-match layer: " +
                widgetName);
        }
        return detachedCount;
    }

    std::size_t StopRetainedPlayerHudMatchState(APBHUD* preferredHud)
    {
        std::unordered_set<APBHUD*> stopped;
        const auto stopOne = [&stopped](APBHUD* hud) {
            if (!hud || hud->IsDefaultObject() ||
                hud->bActorIsBeingDestroyed || !stopped.insert(hud).second)
            {
                return;
            }

            // PlayerHUD_BP owns and reuses HUD_QuickRespawnTips_C. Let the
            // owning native events hide the source death/result state; never
            // collapse or detach the reusable widget root itself.
            hud->K2_StopKillCamera();
            hud->K2_StopQuickRespawn();
            hud->K2_HiddenRoundResult();
            hud->K2_HiddenMatchResult();
            hud->K2_HiddenMatchResult_TDM();
            hud->K2_HiddenSummary();
        };

        stopOne(preferredHud);
        const auto retainedHuds =
            getObjectsOfClass(APlayerHUD_BP_C::StaticClass(), false);
        for (UObject* object : retainedHuds)
        {
            if (stopped.size() >= 4)
                break;
            if (!object || !object->IsA(APBHUD::StaticClass()))
                continue;
            stopOne(static_cast<APBHUD*>(object));
        }
        return stopped.size();
    }

    enum class NativeLoadoutStatus
    {
        Idle,
        Loading,
        Ready,
        Failed,
        Disabled,
    };

    struct NativeLoadoutState
    {
        std::uint64_t Generation = 0;
        NativeLoadoutStatus Status = NativeLoadoutStatus::Idle;
        ULocalPlayer* LocalPlayer = nullptr;
        UWorld* LastWorld = nullptr;
        AGameStateBase* LastGameState = nullptr;
        json Snapshot;
        std::string Detail;
        bool ResultLogged = false;
        std::unordered_set<UPBCustomizeManager*> AppliedCustomizeManagers;
        std::unordered_set<APBPlayerState*> AppliedPlayerStates;
        std::chrono::steady_clock::time_point NextCustomizeApplyAt{};
        std::chrono::steady_clock::time_point NextPlayerStateApplyAt{};
    };

    std::mutex nativeLoadoutMutex;
    NativeLoadoutState nativeLoadout;
    constexpr auto NativeLoadoutApplyInterval = std::chrono::milliseconds(100);

    bool IsNativeArchiveOnly()
    {
        static const bool enabled =
            CommandLinePolicy::HasExactSwitch(
                GetCommandLineA(), "-NativeArchiveOnly");
        return enabled;
    }

    APBPlayerState* ResolveLocalPlayerState(ULocalPlayer* localPlayer)
    {
        if (!localPlayer || !localPlayer->PlayerController ||
            !localPlayer->PlayerController->IsA(APBPlayerController::StaticClass()))
        {
            return nullptr;
        }

        return static_cast<APBPlayerController*>(
            localPlayer->PlayerController)->PBPlayerState;
    }

    UPBCustomizeManager* ResolveCustomizeManager(ULocalPlayer* localPlayer)
    {
        if (!localPlayer)
            return nullptr;

        ULocalPlayerSubsystem* const subsystem =
            USubsystemBlueprintLibrary::GetLocalPlayerSubsystem(
                localPlayer, UPBCustomizeManager::StaticClass());
        if (!subsystem || !subsystem->IsA(UPBCustomizeManager::StaticClass()))
            return nullptr;
        return static_cast<UPBCustomizeManager*>(subsystem);
    }

    std::uint32_t HashText(std::uint32_t hash, const std::string& value)
    {
        for (const unsigned char byte : value)
        {
            hash ^= byte;
            hash *= 16777619U;
        }
        hash ^= 0xFFU;
        hash *= 16777619U;
        return hash;
    }

    std::string HashHex(std::uint32_t hash)
    {
        std::ostringstream text;
        text << "0x" << std::hex << std::setw(8) << std::setfill('0') << hash;
        return text.str();
    }

    bool TryGetSnapshotRoleIds(
        const json& snapshot,
        std::vector<std::string>& outRoleIds,
        std::string& outDetail)
    {
        outRoleIds.clear();
        if (!snapshot.contains("roles") || !snapshot["roles"].is_array() ||
            snapshot["roles"].empty() || snapshot["roles"].size() > 64)
        {
            outDetail = "role collection is invalid";
            return false;
        }

        std::unordered_set<std::string> seenRoles;
        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object())
            {
                outDetail = "role entry is invalid";
                return false;
            }

            const std::string roleId = role.value("roleId", "");
            if (roleId.empty() || !seenRoles.insert(roleId).second)
            {
                outDetail = "role ID is empty or duplicated";
                return false;
            }
            outRoleIds.push_back(roleId);
        }
        return true;
    }

    void StartNativeLoadoutFetch(ULocalPlayer* localPlayer)
    {
        std::uint64_t generation = 0;
        std::string baseUrl;
        {
            std::lock_guard lock(nativeLoadoutMutex);
            UWorld* const currentWorld = UWorld::GetWorld();
            AGameStateBase* const currentGameState = currentWorld
                ? currentWorld->GameState
                : nullptr;
            if (nativeLoadout.LocalPlayer != localPlayer)
            {
                ++nativeLoadout.Generation;
                nativeLoadout.Status = NativeLoadoutStatus::Idle;
                nativeLoadout.LocalPlayer = localPlayer;
                nativeLoadout.LastWorld = currentWorld;
                nativeLoadout.LastGameState = currentGameState;
                nativeLoadout.Snapshot = json();
                nativeLoadout.Detail.clear();
                nativeLoadout.ResultLogged = false;
                nativeLoadout.AppliedCustomizeManagers.clear();
                nativeLoadout.AppliedPlayerStates.clear();
                nativeLoadout.NextCustomizeApplyAt = {};
                nativeLoadout.NextPlayerStateApplyAt = {};
            }
            else if (currentWorld && currentGameState &&
                (nativeLoadout.LastWorld != currentWorld ||
                 nativeLoadout.LastGameState != currentGameState))
            {
                nativeLoadout.LastWorld = currentWorld;
                nativeLoadout.LastGameState = currentGameState;
                // UPBCustomizeManager survives travel with LocalPlayer and the
                // GameInstance. Keep its one-shot archive completion applied;
                // only the match-scoped PlayerState needs a fresh FieldMod
                // initialization for this World/GameState generation.
                nativeLoadout.AppliedPlayerStates.clear();
                nativeLoadout.NextPlayerStateApplyAt = {};
                ClientLog("[LOADOUT] Native PlayerState consumer reset for a new match generation.");
            }
            if (nativeLoadout.Status != NativeLoadoutStatus::Idle)
                return;

            baseUrl = GetCmdValue("-LogicServerURL=");
            if (baseUrl.empty())
            {
                nativeLoadout.Status = NativeLoadoutStatus::Disabled;
                nativeLoadout.Detail = "LogicServerURL is missing";
                return;
            }

            generation = nativeLoadout.Generation;
            nativeLoadout.Status = NativeLoadoutStatus::Loading;
        }

        // This is one authenticated read per native ULocalPlayer lifecycle,
        // never a periodic archive mirror. Unreal objects remain game-thread-only.
        std::thread([generation, baseUrl = std::move(baseUrl)]()
        {
            LoadoutMetaserver::MetaserverClient client(baseUrl);
            LoadoutMetaserver::PlayerLoadoutsResult result =
                client.GetCurrentUserLoadouts();

            std::lock_guard lock(nativeLoadoutMutex);
            if (nativeLoadout.Generation != generation)
                return;

            nativeLoadout.ResultLogged = false;
            if (result.Succeeded())
            {
                nativeLoadout.Snapshot = result.Value->ToNormalizedSnapshot();
                nativeLoadout.Detail = "roles=" +
                    std::to_string(result.Value->Loadouts.size());
                nativeLoadout.Status = NativeLoadoutStatus::Ready;
                return;
            }

            nativeLoadout.Snapshot = json();
            nativeLoadout.Detail = result.Http.ErrorMessage.empty()
                ? "request failed"
                : result.Http.ErrorMessage;
            if (result.Http.StatusCode > 0)
            {
                nativeLoadout.Detail += ", http=" +
                    std::to_string(result.Http.StatusCode);
            }
            nativeLoadout.Status = NativeLoadoutStatus::Failed;
        }).detach();
    }

    void PublishNativeLoadoutResult()
    {
        NativeLoadoutStatus status = NativeLoadoutStatus::Idle;
        std::string detail;
        {
            std::lock_guard lock(nativeLoadoutMutex);
            if (nativeLoadout.ResultLogged ||
                (nativeLoadout.Status != NativeLoadoutStatus::Ready &&
                    nativeLoadout.Status != NativeLoadoutStatus::Failed &&
                    nativeLoadout.Status != NativeLoadoutStatus::Disabled))
            {
                return;
            }
            nativeLoadout.ResultLogged = true;
            status = nativeLoadout.Status;
            detail = nativeLoadout.Detail;
        }

        if (status == NativeLoadoutStatus::Ready)
            ClientLog("[LOADOUT] Native loadout snapshot ready: " + detail);
        else if (status == NativeLoadoutStatus::Failed)
            ClientLog("[LOADOUT] Native loadout snapshot fetch failed: " + detail);
        else
            ClientLog("[LOADOUT] Native loadout initialization disabled: " + detail);
    }

    bool TryApplyCustomizeSnapshot(
        UPBCustomizeManager* manager,
        const json& snapshot,
        int& outSlotCount,
        std::uint32_t& outSlotHash,
        int& outCharacterAppearanceCount,
        std::uint32_t& outCharacterAppearanceHash,
        int& outWeaponCount,
        int& outWeaponPartCount,
        std::uint32_t& outWeaponHash,
        std::string& outDetail)
    {
        using CompleteCharacterSlotFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, EPBCharacterSlotType);
        using CompleteCharacterAppearanceFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, EPBSkinClass);
        using CompleteCharacterSkinPaintingFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName);
        using CompleteWeaponSlotFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName, EPBPartSlotType);
        using CompleteWeaponSuiteFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName, FName);
        using CompleteWeaponPartSkinPaintingFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName, FName, FName);
        using CompleteWeaponOrnamentFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName);
        constexpr uintptr_t CompleteCharacterSlotRva = 0x16DD080;
        constexpr uintptr_t CompleteCharacterAppearanceRva = 0x16DCD80;
        constexpr uintptr_t CompleteCharacterSkinPaintingRva = 0x16DCEC0;
        constexpr uintptr_t CompleteWeaponSlotRva = 0x16DD5F0;
        constexpr uintptr_t CompleteWeaponSuiteRva = 0x16DD740;
        constexpr uintptr_t CompleteWeaponPartSkinPaintingRva = 0x16DD490;
        constexpr uintptr_t CompleteWeaponOrnamentRva = 0x16DD1D0;

        std::vector<std::string> roleIds;
        if (!manager || !TryGetSnapshotRoleIds(snapshot, roleIds, outDetail))
            return false;

        const auto findRole = [&](const std::string& roleId) -> const json*
        {
            for (const auto& candidate : snapshot["roles"])
            {
                if (candidate.is_object() && candidate.value("roleId", "") == roleId)
                    return &candidate;
            }
            return nullptr;
        };

        // Validate every role before changing the native cache so malformed
        // snapshots cannot be partially applied.
        for (const std::string& roleId : roleIds)
        {
            FPBInventoryNetworkConfig inventory{};
            if (!LoadoutApplication::TryBuildRoleInventory(
                snapshot, roleId, inventory, outDetail))
            {
                outDetail = roleId + ": " + outDetail;
                return false;
            }

            const json* const role = findRole(roleId);
            if (!role || !role->contains("characterData") ||
                !(*role)["characterData"].is_object())
            {
                outDetail = roleId + ": character appearance is missing";
                return false;
            }
            const auto& character = (*role)["characterData"];
            if (!character.contains("skinClassArray") ||
                !character["skinClassArray"].is_array() ||
                !character.contains("skinIdArray") ||
                !character["skinIdArray"].is_array() ||
                character["skinClassArray"].size() !=
                    character["skinIdArray"].size() ||
                character["skinIdArray"].size() > 8 ||
                !character.contains("skinPaintingId") ||
                !character["skinPaintingId"].is_string() ||
                character["skinPaintingId"].get_ref<const std::string&>().size() > 128)
            {
                outDetail = roleId + ": character appearance is invalid";
                return false;
            }
            std::unordered_set<int> appearanceClasses;
            bool hasSkin = false;
            for (std::size_t index = 0;
                index < character["skinIdArray"].size(); ++index)
            {
                const auto& appearanceClass = character["skinClassArray"][index];
                const auto& appearanceId = character["skinIdArray"][index];
                if (!appearanceClass.is_number_integer() ||
                    appearanceClass.get<int>() <= static_cast<int>(EPBSkinClass::None) ||
                    appearanceClass.get<int>() >= static_cast<int>(EPBSkinClass::EPBSkinClass_MAX) ||
                    !appearanceId.is_string() ||
                    appearanceId.get_ref<const std::string&>().empty() ||
                    appearanceId.get_ref<const std::string&>().size() > 128 ||
                    !appearanceClasses.insert(appearanceClass.get<int>()).second)
                {
                    outDetail = roleId + ": character appearance entry is invalid";
                    return false;
                }
                hasSkin = hasSkin || appearanceClass.get<int>() ==
                    static_cast<int>(EPBSkinClass::Skin);
            }
            const bool hasSkinPainting =
                !character["skinPaintingId"].get_ref<const std::string&>().empty();
            if (hasSkin != hasSkinPainting)
            {
                outDetail = roleId + ": character skin pair is incomplete";
                return false;
            }
        }

        auto* const completeCharacterSlot = reinterpret_cast<CompleteCharacterSlotFn>(
            BaseAddress + CompleteCharacterSlotRva);
        auto* const completeCharacterAppearance =
            reinterpret_cast<CompleteCharacterAppearanceFn>(
                BaseAddress + CompleteCharacterAppearanceRva);
        auto* const completeCharacterSkinPainting =
            reinterpret_cast<CompleteCharacterSkinPaintingFn>(
                BaseAddress + CompleteCharacterSkinPaintingRva);
        auto* const completeWeaponSlot = reinterpret_cast<CompleteWeaponSlotFn>(
            BaseAddress + CompleteWeaponSlotRva);
        auto* const completeWeaponSuite = reinterpret_cast<CompleteWeaponSuiteFn>(
            BaseAddress + CompleteWeaponSuiteRva);
        auto* const completeWeaponPartSkinPainting =
            reinterpret_cast<CompleteWeaponPartSkinPaintingFn>(
                BaseAddress + CompleteWeaponPartSkinPaintingRva);
        auto* const completeWeaponOrnament =
            reinterpret_cast<CompleteWeaponOrnamentFn>(
                BaseAddress + CompleteWeaponOrnamentRva);
        if (!completeCharacterSlot || !completeCharacterAppearance ||
            !completeCharacterSkinPainting || !completeWeaponSlot || !completeWeaponSuite ||
            !completeWeaponPartSkinPainting || !completeWeaponOrnament)
        {
            outDetail = "native completion entry is unavailable";
            return false;
        }

        outSlotCount = 0;
        outSlotHash = 2166136261U;
        outCharacterAppearanceCount = 0;
        outCharacterAppearanceHash = 2166136261U;
        outWeaponCount = 0;
        outWeaponPartCount = 0;
        outWeaponHash = 2166136261U;
        ScopedClientProcessEventSuppression suppressProcessEventHooks;
        for (const std::string& roleId : roleIds)
        {
            FPBInventoryNetworkConfig inventory{};
            if (!LoadoutApplication::TryBuildRoleInventory(
                snapshot, roleId, inventory, outDetail))
            {
                outDetail = roleId + ": " + outDetail;
                return false;
            }

            const FName roleName = LoadoutSerializer::NameFromString(roleId);
            outSlotHash = HashText(outSlotHash, roleId);
            for (int index = 0; index < inventory.CharacterSlots.Num(); ++index)
            {
                const EPBCharacterSlotType slot = inventory.CharacterSlots[index];
                const FName itemId = inventory.InventoryItems[index];
                completeCharacterSlot(manager, 0, itemId, roleName, slot);

                outSlotHash = HashText(
                    outSlotHash, std::to_string(static_cast<int>(slot)));
                outSlotHash = HashText(
                    outSlotHash, LoadoutSerializer::NameToString(itemId));
                ++outSlotCount;
            }

            const json* const role = findRole(roleId);
            const auto& character = (*role)["characterData"];
            std::string skinId;
            for (std::size_t index = 0;
                index < character["skinIdArray"].size(); ++index)
            {
                const int classValue = character["skinClassArray"][index].get<int>();
                const std::string appearanceId =
                    character["skinIdArray"][index].get<std::string>();
                if (classValue == static_cast<int>(EPBSkinClass::Skin))
                {
                    skinId = appearanceId;
                    continue;
                }
                completeCharacterAppearance(
                    manager, 0,
                    LoadoutSerializer::NameFromString(appearanceId), roleName,
                    static_cast<EPBSkinClass>(classValue));
                outCharacterAppearanceHash = HashText(
                    outCharacterAppearanceHash, roleId);
                outCharacterAppearanceHash = HashText(
                    outCharacterAppearanceHash, std::to_string(classValue));
                outCharacterAppearanceHash = HashText(
                    outCharacterAppearanceHash, appearanceId);
                ++outCharacterAppearanceCount;
            }
            if (!skinId.empty())
            {
                const std::string paintingId =
                    character["skinPaintingId"].get<std::string>();
                completeCharacterSkinPainting(
                    manager, 0,
                    LoadoutSerializer::NameFromString(skinId),
                    LoadoutSerializer::NameFromString(paintingId), roleName);
                outCharacterAppearanceHash = HashText(
                    outCharacterAppearanceHash, roleId);
                outCharacterAppearanceHash = HashText(
                    outCharacterAppearanceHash, "skin-painting");
                outCharacterAppearanceHash = HashText(
                    outCharacterAppearanceHash, skinId);
                outCharacterAppearanceHash = HashText(
                    outCharacterAppearanceHash, paintingId);
                ++outCharacterAppearanceCount;
            }
        }

        // This build receives GetPlayerArchiveV2 but does not dispatch field 8
        // into PBCustomizeManager. Reuse the manager's own success completions
        // to populate its weapon cache; these paths perform the native map
        // updates and delegate broadcasts without direct memory writes.
        for (const std::string& roleId : roleIds)
        {
            const json* role = nullptr;
            for (const auto& candidate : snapshot["roles"])
            {
                if (candidate.is_object() && candidate.value("roleId", "") == roleId)
                {
                    role = &candidate;
                    break;
                }
            }
            if (!role || !role->contains("weaponConfigs") ||
                !(*role)["weaponConfigs"].is_object())
            {
                outDetail = roleId + ": weapon config map is missing";
                return false;
            }

            const FName roleName = LoadoutSerializer::NameFromString(roleId);
            for (const auto& [mapWeaponId, weapon] : (*role)["weaponConfigs"].items())
            {
                if (!weapon.is_object())
                {
                    outDetail = roleId + ": weapon config is invalid";
                    return false;
                }
                const std::string weaponId = weapon.value("weaponId", "");
                if (weaponId.empty() || weaponId != mapWeaponId ||
                    !weapon.contains("parts") || !weapon["parts"].is_array())
                {
                    outDetail = roleId + ": weapon config identity is invalid";
                    return false;
                }

                // A definition-only config is the rolling-deployment fallback
                // used with older servers. It must not replace a native cache.
                if (weapon["parts"].empty())
                    continue;

                const FName weaponName = LoadoutSerializer::NameFromString(weaponId);
                outWeaponHash = HashText(outWeaponHash, roleId);
                outWeaponHash = HashText(outWeaponHash, weaponId);

                for (const auto& part : weapon["parts"])
                {
                    if (!part.is_object())
                    {
                        outDetail = roleId + ": weapon part is invalid";
                        return false;
                    }
                    const int slotValue = part.value("slotType", 0);
                    const std::string partId = part.value("weaponPartId", "");
                    if (slotValue <= static_cast<int>(EPBPartSlotType::UnexistedSlot) ||
                        slotValue >= static_cast<int>(EPBPartSlotType::Max) ||
                        slotValue == static_cast<int>(EPBPartSlotType::SlotTypeMax) ||
                        partId.empty())
                    {
                        outDetail = roleId + ": weapon part identity is invalid";
                        return false;
                    }
                    completeWeaponSlot(
                        manager, 0, LoadoutSerializer::NameFromString(partId),
                        roleName, weaponName,
                        static_cast<EPBPartSlotType>(slotValue));
                    outWeaponHash = HashText(outWeaponHash, std::to_string(slotValue));
                    outWeaponHash = HashText(outWeaponHash, partId);
                    ++outWeaponPartCount;
                }

                const std::string suiteId = weapon.value("weaponSuitId", "");
                const std::string suitePaintingId =
                    weapon.value("weaponSuitPaintingId", "");
                if (!suiteId.empty() || !suitePaintingId.empty())
                {
                    if (suiteId.empty() || suitePaintingId.empty())
                    {
                        outDetail = roleId + ": weapon suite pair is incomplete";
                        return false;
                    }
                    completeWeaponSuite(
                        manager, 0,
                        LoadoutSerializer::NameFromString(suiteId),
                        LoadoutSerializer::NameFromString(suitePaintingId),
                        roleName, weaponName);
                    outWeaponHash = HashText(outWeaponHash, suiteId);
                    outWeaponHash = HashText(outWeaponHash, suitePaintingId);
                }

                for (const auto& part : weapon["parts"])
                {
                    const std::string partId = part.value("weaponPartId", "");
                    const std::string skinId = part.value("weaponPartSkinId", "");
                    const std::string paintingId =
                        part.value("weaponPartSkinPaintingId", "");
                    if (skinId.empty() && paintingId.empty())
                        continue;
                    if (skinId.empty() || paintingId.empty())
                    {
                        // The pinned native client serializes two built-in
                        // reset sentinels as half-pairs: PartOri without an
                        // ID, and the receiver/fire-mode PTOriginal without a
                        // type.  They are not independent cosmetics.  The
                        // preceding weapon-slot completion plus the suite
                        // completion establish the effective original/base
                        // appearance, so do not reject the whole archive or
                        // dispatch an invalid half-pair to the appearance
                        // completion.
                        if (WeaponArchivePolicy::IsNativeOriginalPartAppearanceSentinel(
                            skinId, paintingId))
                            continue;
                        outDetail = roleId + ": weapon part appearance pair is incomplete";
                        return false;
                    }
                    completeWeaponPartSkinPainting(
                        manager, 0,
                        LoadoutSerializer::NameFromString(skinId),
                        LoadoutSerializer::NameFromString(paintingId),
                        roleName, weaponName,
                        LoadoutSerializer::NameFromString(partId));
                    outWeaponHash = HashText(outWeaponHash, skinId);
                    outWeaponHash = HashText(outWeaponHash, paintingId);
                }

                const std::string ornamentId = weapon.value("ornamentId", "");
                if (!ornamentId.empty())
                {
                    completeWeaponOrnament(
                        manager, 0,
                        LoadoutSerializer::NameFromString(ornamentId),
                        roleName, weaponName);
                    outWeaponHash = HashText(outWeaponHash, ornamentId);
                }
                ++outWeaponCount;
            }
        }
        outDetail = "native customize and weapon archive completions applied";
        return true;
    }

    bool TryApplyPlayerStateSnapshot(
        APBPlayerState* playerState,
        const json& snapshot,
        int& outSlotCount,
        std::uint32_t& outSlotHash,
        std::string& outDetail)
    {
        std::vector<std::string> snapshotRoleIds;
        if (!playerState ||
            !TryGetSnapshotRoleIds(snapshot, snapshotRoleIds, outDetail))
        {
            return false;
        }

        FPBFieldModRoleGameSavedNetworkConfig saved{};
        ScopedRoleInventoryStorage releaseNestedStorage(saved);
        TArray<FName> roleIds;
        TArray<int32> ownedQuotas;
        outSlotCount = 0;
        outSlotHash = 2166136261U;
        ScopedClientProcessEventSuppression suppressProcessEventHooks;
        for (const std::string& roleId : snapshotRoleIds)
        {
            FPBInventoryNetworkConfig inventory{};
            if (!LoadoutApplication::TryBuildRoleInventory(
                snapshot, roleId, inventory, outDetail))
            {
                outDetail = roleId + ": " + outDetail;
                return false;
            }

            int ownedQuota = 0;
            if (!LoadoutApplication::TryResolveRoleOwnedQuota(
                roleId, ownedQuota, outDetail))
            {
                outDetail = roleId + ": " + outDetail;
                return false;
            }

            const FName roleName = LoadoutSerializer::NameFromString(roleId);
            saved.RoleArray.Add(roleName);
            saved.RoleInventoryNetworkConfigArray.AddZeroed(inventory);
            roleIds.Add(roleName);
            ownedQuotas.Add(ownedQuota);

            outSlotHash = HashText(outSlotHash, roleId);
            for (int index = 0; index < inventory.CharacterSlots.Num(); ++index)
            {
                outSlotHash = HashText(outSlotHash,
                    std::to_string(static_cast<int>(inventory.CharacterSlots[index])));
                outSlotHash = HashText(outSlotHash,
                    LoadoutSerializer::NameToString(inventory.InventoryItems[index]));
            }
            outSlotCount += inventory.InventoryItems.Num();
        }

        if (saved.RoleArray.Num() != static_cast<int>(snapshotRoleIds.size()) ||
            saved.RoleArray.Num() != saved.RoleInventoryNetworkConfigArray.Num() ||
            saved.RoleArray.Num() != roleIds.Num() ||
            roleIds.Num() != ownedQuotas.Num())
        {
            outDetail = "native FieldMod array alignment failed";
            return false;
        }

        playerState->ClientInitFieldMod(saved, roleIds, ownedQuotas);
        outDetail = "native ClientInitFieldMod applied";
        return true;
    }

    void LogPendingInitialization(const std::string& target, const std::string& detail)
    {
        static std::string lastPendingMessage;
        const std::string message = target + ": " + detail;
        if (lastPendingMessage != message)
        {
            lastPendingMessage = message;
            ClientLog("[LOADOUT] Native initialization pending: " + message);
        }
    }

    void PumpNativeLoadoutInitialization(ULocalPlayer* localPlayer)
    {
        if (IsNativeArchiveOnly() || !localPlayer)
            return;

        StartNativeLoadoutFetch(localPlayer);
        PublishNativeLoadoutResult();

        json snapshot;
        UPBCustomizeManager* const customizeManager =
            ResolveCustomizeManager(localPlayer);
        APBPlayerState* const playerState = ResolveLocalPlayerState(localPlayer);
        bool applyCustomize = false;
        bool applyPlayerState = false;
        const auto now = std::chrono::steady_clock::now();
        {
            std::lock_guard lock(nativeLoadoutMutex);
            if (nativeLoadout.LocalPlayer != localPlayer ||
                nativeLoadout.Status != NativeLoadoutStatus::Ready)
            {
                return;
            }
            snapshot = nativeLoadout.Snapshot;
            applyCustomize = customizeManager &&
                !nativeLoadout.AppliedCustomizeManagers.contains(customizeManager) &&
                now >= nativeLoadout.NextCustomizeApplyAt;
            applyPlayerState = playerState &&
                !nativeLoadout.AppliedPlayerStates.contains(playerState) &&
                now >= nativeLoadout.NextPlayerStateApplyAt;
            if (applyCustomize)
                nativeLoadout.NextCustomizeApplyAt = now + NativeLoadoutApplyInterval;
            if (applyPlayerState)
                nativeLoadout.NextPlayerStateApplyAt = now + NativeLoadoutApplyInterval;
        }

        if (applyCustomize)
        {
            int slotCount = 0;
            std::uint32_t slotHash = 0;
            int characterAppearanceCount = 0;
            std::uint32_t characterAppearanceHash = 0;
            int weaponCount = 0;
            int weaponPartCount = 0;
            std::uint32_t weaponHash = 0;
            std::string detail;
            try
            {
                if (TryApplyCustomizeSnapshot(
                    customizeManager, snapshot, slotCount, slotHash,
                    characterAppearanceCount, characterAppearanceHash,
                    weaponCount, weaponPartCount, weaponHash, detail))
                {
                    {
                        std::lock_guard lock(nativeLoadoutMutex);
                        if (nativeLoadout.LocalPlayer == localPlayer)
                            nativeLoadout.AppliedCustomizeManagers.insert(customizeManager);
                    }
                    ClientLog("[LOADOUT] Native Customize completion applied: roles=" +
                        std::to_string(snapshot["roles"].size()) +
                        " slots=" + std::to_string(slotCount) +
                        " slot_hash=" + HashHex(slotHash) +
                        " character_appearances=" +
                            std::to_string(characterAppearanceCount) +
                        " character_appearance_hash=" +
                            HashHex(characterAppearanceHash) +
                        " weapons=" + std::to_string(weaponCount) +
                        " weapon_parts=" + std::to_string(weaponPartCount) +
                        " weapon_hash=" + HashHex(weaponHash));
                }
                else
                {
                    LogPendingInitialization("Customize", detail);
                }
            }
            catch (...)
            {
                LogPendingInitialization("Customize", "native completion exception");
            }
        }

        if (applyPlayerState)
        {
            int slotCount = 0;
            std::uint32_t slotHash = 0;
            std::string detail;
            try
            {
                if (TryApplyPlayerStateSnapshot(
                    playerState, snapshot, slotCount, slotHash, detail))
                {
                    {
                        std::lock_guard lock(nativeLoadoutMutex);
                        if (nativeLoadout.LocalPlayer == localPlayer)
                            nativeLoadout.AppliedPlayerStates.insert(playerState);
                    }
                    ClientLog("[LOADOUT] Native ClientInitFieldMod applied: roles=" +
                        std::to_string(snapshot["roles"].size()) +
                        " slots=" + std::to_string(slotCount) +
                        " slot_hash=" + HashHex(slotHash));
                }
                else
                {
                    LogPendingInitialization("FieldMod", detail);
                }
            }
            catch (...)
            {
                LogPendingInitialization("FieldMod", "native initialization exception");
            }
        }
    }
}

void ArmOwnedSeamlessDestinationUiCleanup()
{
    ownedSeamlessDestinationUiCleanupWaitLogged.store(
        false, std::memory_order_release);
    ownedSeamlessDestinationUiCleanupPending.store(
        true, std::memory_order_release);
    ClientLog("[MULTIMATCH] Armed one-shot destination HUD cleanup for owned seamless travel.");
}

void ArmOwnedSeamlessIntroCameraRecovery()
{
    ownedSeamlessIntroCameraRecoveryWaitLogged.store(
        false, std::memory_order_release);
    ownedSeamlessIntroRoundBoundaryReached.store(
        false, std::memory_order_release);
    ownedSeamlessIntroCameraRecoveryPending.store(
        true, std::memory_order_release);
    ClientLog("[CAMERA] Armed native post-round camera verification for owned seamless travel.");
}

void NotifyOwnedSeamlessIntroRoundBoundary()
{
    if (!ownedSeamlessIntroCameraRecoveryPending.load(
            std::memory_order_acquire))
    {
        return;
    }

    ownedSeamlessIntroRoundBoundaryReached.store(
        true, std::memory_order_release);
    ClientLog("[CAMERA] Native round-start boundary completed; awaiting the next PlayerCameraManager tick.");
}

bool TryFinalizeOwnedSeamlessIntroCamera()
{
    const bool pending = ownedSeamlessIntroCameraRecoveryPending.load(
        std::memory_order_acquire);
    const bool roundBoundaryReached =
        ownedSeamlessIntroRoundBoundaryReached.load(
            std::memory_order_acquire);
    if (!pending || !roundBoundaryReached)
        return false;

    try
    {
        UWorld* const world = UWorld::GetWorld();
        UGameInstance* const gameInstance = world
            ? world->OwningGameInstance
            : nullptr;
        ULocalPlayer* const localPlayer = gameInstance &&
            gameInstance->LocalPlayers.Num() > 0
            ? gameInstance->LocalPlayers[0]
            : nullptr;
        auto* const playerController = localPlayer &&
            localPlayer->PlayerController &&
            localPlayer->PlayerController->IsA(
                APBPlayerController::StaticClass())
            ? static_cast<APBPlayerController*>(
                localPlayer->PlayerController)
            : nullptr;
        APawn* const pawn = playerController
            ? playerController->Pawn
            : nullptr;
        APlayerCameraManager* const cameraManager = playerController
            ? playerController->PlayerCameraManager
            : nullptr;
        const bool isLocalPlayerController = playerController &&
            playerController->IsA(APBPlayerController::StaticClass()) &&
            cameraManager && cameraManager->PCOwner == playerController;
        const bool hasPlayablePawn = pawn &&
            pawn->IsA(APBCharacter::StaticClass()) &&
            !pawn->bActorIsBeingDestroyed;
        const bool acknowledgedPawnMatches = playerController &&
            playerController->AcknowledgedPawn == pawn;
        const bool pbCharacterMatches = playerController &&
            playerController->PBCharacter == pawn;
        const bool pawnIsAlive = hasPlayablePawn &&
            static_cast<APBCharacter*>(pawn)->CharacterLifeStatus ==
                EPBCharacterLifeStatus::Alive;
        constexpr float MaxSettledCameraDistance = 1000.0f;
        float cameraDistanceToPawn = -1.0f;
        bool cameraViewIsNearPawn = false;
        if (cameraManager && pawn && pawn->RootComponent)
        {
            cameraDistanceToPawn = cameraManager->CameraCachePrivate.POV.
                Location.GetDistanceTo(pawn->RootComponent->RelativeLocation);
            cameraViewIsNearPawn = std::isfinite(cameraDistanceToPawn) &&
                cameraDistanceToPawn <= MaxSettledCameraDistance;
        }
        const auto decision = SeamlessIntroCameraPolicy::Decide(
            pending,
            isLocalPlayerController,
            hasPlayablePawn,
            acknowledgedPawnMatches,
            pbCharacterMatches,
            pawnIsAlive,
            cameraViewIsNearPawn);

        if (decision ==
            SeamlessIntroCameraPolicy::ERecoveryDecision::Wait)
        {
            if (!ownedSeamlessIntroCameraRecoveryWaitLogged.exchange(
                    true, std::memory_order_acq_rel))
            {
                std::ostringstream wait;
                wait << "[CAMERA] Waiting for the opening camera POV to return near the local Pawn; distance="
                     << cameraDistanceToPawn;
                ClientLog(wait.str());
            }
            return false;
        }

        // Consume before entering the reflected native function: its K2 event
        // is synchronous and must never re-enter this one-shot generation.
        ownedSeamlessIntroCameraRecoveryPending.store(
            false, std::memory_order_release);
        AActor* const previousViewTarget = cameraManager->ViewTarget.Target;
        APBHUD* hud = playerController->MyPBHUD;
        if (!hud && playerController->MyHUD &&
            playerController->MyHUD->IsA(APBHUD::StaticClass()))
        {
            hud = static_cast<APBHUD*>(playerController->MyHUD);
        }
        std::size_t stoppedHudOwners = 0;
        {
            ScopedClientProcessEventSuppression suppressNestedHooks;
            // A retained PlayerController can carry the source death-camera
            // state into a living destination Pawn even after the HUD owner
            // received its K2 stop events. At this alive, post-intro boundary
            // any kill camera is necessarily stale, so use the controller's
            // native paired teardown before ending third-person view.
            playerController->StopKillCamera();
            playerController->StopThirdPersonCamera();
            // Destination start RPCs and the countdown can arrive before the
            // retained death HUD replays its source-world state. Repeat only
            // the native paired HUD teardown after K2_RoundHasStarted; do not
            // detach the reusable quick-respawn root or synthesize input.
            stoppedHudOwners = StopRetainedPlayerHudMatchState(hud);
        }
        const bool recovered =
            cameraManager->ViewTarget.Target == playerController->Pawn;
        std::ostringstream result;
        result << "[CAMERA] Post-round camera-manager/HUD settle boundary "
               << "camera_action="
               << "stop-kill-and-third-person"
               << " result=" << (recovered ? "pawn-target" : "mismatch")
               << " previous_view_target=" << previousViewTarget
               << " pawn=" << playerController->Pawn
               << " camera_distance=" << cameraDistanceToPawn
               << " hud_owners_stopped=" << stoppedHudOwners;
        ClientLog(result.str());
        return recovered;
    }
    catch (...)
    {
        ownedSeamlessIntroCameraRecoveryPending.store(
            true, std::memory_order_release);
        ClientLog("[CAMERA] Native post-round camera verification failed; one-shot remains pending.");
        return false;
    }
}

bool TryFinalizeOwnedSeamlessDestinationUi(
    APBPlayerController* playerController)
{
    if (!ownedSeamlessDestinationUiCleanupPending.load(
            std::memory_order_acquire))
    {
        return false;
    }

    APBHUD* hud = nullptr;
    try
    {
        if (!playerController ||
            !playerController->IsA(APBPlayerController::StaticClass()))
        {
            return false;
        }

        hud = playerController->MyPBHUD;
        if (!hud && playerController->MyHUD &&
            playerController->MyHUD->IsA(APBHUD::StaticClass()))
        {
            hud = static_cast<APBHUD*>(playerController->MyHUD);
        }
        if (!hud || !hud->IsA(APBHUD::StaticClass()))
        {
            if (!ownedSeamlessDestinationUiCleanupWaitLogged.exchange(
                    true, std::memory_order_acq_rel))
            {
                ClientLog("[MULTIMATCH] Destination HUD cleanup is waiting for APBHUD replication.");
            }
            return false;
        }

        // Use the game's own paired teardown events. The retained HUD can
        // otherwise carry death/quick-respawn and result widgets into the next
        // world. The normal return-to-menu path also retires several top-level
        // UMG roots, but seamless travel intentionally skips that path; detach
        // only the exact source-match roots observed in the destination
        // viewport. No camera pointers, Pawn state or input flags are written.
        ScopedClientProcessEventSuppression suppressNestedHooks;
        const std::size_t stoppedHudOwners =
            StopRetainedPlayerHudMatchState(hud);
        const std::size_t retiredMatchLayers =
            DetachRetainedSourceMatchLayers();

        ClientLog("[MULTIMATCH] Destination source-match HUD owners stopped=" +
            std::to_string(stoppedHudOwners) + " layers retired=" +
            std::to_string(retiredMatchLayers) + ".");
    }
    catch (...)
    {
        ClientLog("[MULTIMATCH] Destination HUD cleanup failed; retrying at the next start RPC.");
        return false;
    }

    ownedSeamlessDestinationUiCleanupPending.store(
        false, std::memory_order_release);
    nativeRespawnUiCleanupPending.store(false, std::memory_order_release);
    ClientLog("[MULTIMATCH] Finalized retained HUD state at destination startup.");
    return true;
}

void ArmNativeRespawnUiCleanup()
{
    nativeRespawnUiCleanupPending.store(true, std::memory_order_release);
}

bool TryFinalizeNativeRespawnUi(APBPlayerController* playerController)
{
    if (!nativeRespawnUiCleanupPending.load(std::memory_order_acquire))
        return false;

    try
    {
        if (!playerController || !playerController->Pawn ||
            !playerController->IsA(APBPlayerController::StaticClass()))
        {
            return false;
        }

        APBHUD* hud = playerController->MyPBHUD;
        if (!hud && playerController->MyHUD &&
            playerController->MyHUD->IsA(APBHUD::StaticClass()))
        {
            hud = static_cast<APBHUD*>(playerController->MyHUD);
        }
        if (!hud || !hud->IsA(APBHUD::StaticClass()))
            return false;

        ScopedClientProcessEventSuppression suppressNestedHooks;
        hud->K2_StopKillCamera();
        hud->K2_StopQuickRespawn();
    }
    catch (...)
    {
        ClientLog("[RESPAWN] Native restart HUD cleanup failed; keeping the one-shot pending.");
        return false;
    }

    nativeRespawnUiCleanupPending.store(false, std::memory_order_release);
    ClientLog("[RESPAWN] Finalized death HUD after native ClientRestart possession.");
    return true;
}

bool QueueConnectToMatch(const std::string& target)
{
    std::string validationError;
    if (!CommandProtocol::ValidateMatchTarget(target, &validationError))
    {
        ClientLog("[CLIENT] Rejected match target: " + validationError);
        return false;
    }

    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (pendingTarget.has_value())
            return false;

        pendingTarget = target;
        connectStage = ConnectStage::Queued;
        frontendCleanupUntil = {};
        nextFrontendCleanupAt = {};
        directTravelSourceWorld = nullptr;
        directTravelUiFinalized = false;
    }
    ClientLog("[CLIENT] Match transition queued: " + target);
    return true;
}

bool QueueConnectToMatchAuthorized(
    const std::string& target,
    const std::string_view joinGrant)
{
    std::string validationError;
    if (!CommandProtocol::ValidateMatchTarget(target, &validationError))
    {
        ClientLog("[STRICT-ROSTER] Rejected match target: " + validationError);
        return false;
    }
    if (joinGrant.empty() || joinGrant.size() > CommandProtocol::MaxTokenBytes)
    {
        ClientLog("[STRICT-ROSTER] Rejected invalid join grant length.");
        return false;
    }

    // A direct travel here would bypass NMT_Login admission and lose the
    // signed roster claim. Keep strict joins closed until the locked build's
    // client login-message injection site is proven dynamically.
    ClientLog("[STRICT-ROSTER] Join rejected: pinned NMT_Login injection path is unverified.");
    return false;
}

void ConnectToMatch()
{
    std::string target;
    {
        std::lock_guard<std::mutex> lock(connectMutex);
        target = currentTarget;
    }

    if (target.empty())
    {
        ClientLog("[CLIENT] Reconnect requested without a current match target.");
        return;
    }
    if (!QueueConnectToMatch(target))
        ClientLog("[CLIENT] Reconnect ignored because another transition is pending.");
}

void AutoConnectToMatchFromCmdline()
{
    if (!MatchIP.empty() && !QueueConnectToMatch(MatchIP))
        ClientLog("[CLIENT] Initial match target could not be queued.");
}

void NotifyClientLoginCompleted()
{
    gameThreadId.store(GetCurrentThreadId());
    loginCompleted.store(true);
    ClientLog("[ARCHIVE] Native QueryAssets/GetPlayerArchiveV2 ownership enabled; "
              "client archive mirrors are read-only.");
    if (IsNativeArchiveOnly())
    {
        ClientLog("[LOADOUT] NativeArchiveOnly active; client FieldMod initialization is read-only.");
    }
}

void PumpPendingClientCommands()
{
    static thread_local bool pumping = false;
    if (pumping || !loginCompleted.load() ||
        gameThreadId.load() != GetCurrentThreadId())
        return;

    class ScopedPumpingFlag
    {
    public:
        explicit ScopedPumpingFlag(bool& flag) : flag_(flag) { flag_ = true; }
        ~ScopedPumpingFlag() { flag_ = false; }
    private:
        bool& flag_;
    } pumpingGuard(pumping);

    UWorld* const world = UWorld::GetWorld();
    if (world == nullptr || world->OwningGameInstance == nullptr ||
        world->OwningGameInstance->LocalPlayers.Num() == 0)
    {
        return;
    }

    auto* const localPlayer = static_cast<UPBLocalPlayer*>(
        world->OwningGameInstance->LocalPlayers[0]);
    if (localPlayer == nullptr)
        return;

    PumpNativeLoadoutInitialization(localPlayer);

    const auto now = std::chrono::steady_clock::now();
    std::optional<std::string> connectTarget;
    bool maintainFrontendCleanup = false;
    bool finalizeTravelUi = false;

    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (frontendCleanupUntil != std::chrono::steady_clock::time_point{} &&
            now < frontendCleanupUntil && now >= nextFrontendCleanupAt)
        {
            nextFrontendCleanupAt = now + FrontendCleanupInterval;
            maintainFrontendCleanup = true;
            if (!directTravelUiFinalized &&
                directTravelSourceWorld != nullptr &&
                world != directTravelSourceWorld)
            {
                directTravelUiFinalized = true;
                finalizeTravelUi = true;
            }
        }
        else if (frontendCleanupUntil != std::chrono::steady_clock::time_point{} &&
            now >= frontendCleanupUntil)
        {
            frontendCleanupUntil = {};
            nextFrontendCleanupAt = {};
        }
    }

    if (maintainFrontendCleanup)
        HideDirectMatchFrontendLayers(false);
    if (finalizeTravelUi)
    {
        try
        {
            static_cast<UPBGameInstance*>(world->OwningGameInstance)->HideLoadingScreen();
            DetachDirectMatchAuthLayersAfterTravel();
            ClientLog("[CLIENT] Finalized direct-travel loading/auth UI after match world activation.");
        }
        catch (...)
        {
            std::lock_guard<std::mutex> lock(connectMutex);
            directTravelUiFinalized = false;
            ClientLog("[CLIENT] Failed to finalize direct-travel UI; retrying.");
        }
    }

    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (!pendingTarget.has_value())
            return;

        if (connectStage == ConnectStage::Queued)
        {
            connectStage = ConnectStage::WaitingAfterLogin;
            nextActionAt = now + LoginSettleDelay;
            return;
        }
        if (now < nextActionAt)
            return;

        if (connectStage == ConnectStage::WaitingAfterLogin)
        {
            connectTarget = pendingTarget;
        }
    }

    bool actionSucceeded = false;
    try
    {
        if (connectTarget.has_value())
        {
            const std::wstring command = L"open " +
                std::wstring(connectTarget->begin(), connectTarget->end());
            HideDirectMatchFrontendLayers(true);
            {
                std::lock_guard<std::mutex> lock(connectMutex);
                directTravelSourceWorld = world;
                directTravelUiFinalized = false;
            }
            ClientLog("[CLIENT] Connecting directly to match: " + *connectTarget);
            UKismetSystemLibrary::ExecuteConsoleCommand(world, command.c_str(), nullptr);
            actionSucceeded = true;
        }
    }
    catch (...)
    {
        ClientLog("[CLIENT] Match transition failed on the game thread.");
    }
    if (!actionSucceeded)
        return;

    std::lock_guard<std::mutex> lock(connectMutex);
    if (connectTarget.has_value() &&
        connectStage == ConnectStage::WaitingAfterLogin &&
        pendingTarget == connectTarget)
    {
        currentTarget = *connectTarget;
        pendingTarget.reset();
        connectStage = ConnectStage::Idle;
        const auto cleanupStart = std::chrono::steady_clock::now();
        frontendCleanupUntil = cleanupStart + FrontendCleanupDuration;
        nextFrontendCleanupAt = cleanupStart;
    }
}
