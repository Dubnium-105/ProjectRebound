#include "ClientLogic.h"

#include "DirectMatchUiCleanupPolicy.h"
#include "NativeLoginGrantPolicy.h"
#include "SeamlessIntroCameraPolicy.h"

#include "../Communication/CommandProtocol.h"
#include "../Config/Config.h"
#include "../Config/CommandLinePolicy.h"
#include "../Debug/Debug.h"
#include "../Hooks/Hooks.h"
#include "../Hooks/StrictRosterSteamAuth.h"
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
// Defined by dllmain.cpp. This is a synchronized authority-world identity
// snapshot; ClientLogic never dereferences a UWorld from the pipe thread.
extern std::string ReadStrictAuthorityWorldInstanceId();

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
        WaitingAfterLogin,
        TravelRequested,
        WorldReady,
        LocalPawnReady,
        WaitingBackendConfirmation,
        LocalAuthorityPending,
        Playable,
        Failed,
        Cancelled
    };

    std::mutex connectMutex;
    std::optional<std::string> pendingTarget;
    // Retain the signed grant for the complete native travel operation.  The
    // fixed NMT_Login serializer can be re-entered for reliable retransmits;
    // a successful first serialization therefore marks the grant injected but
    // must not erase it until Playable, cancellation, or a bounded failure.
    std::string stagedNativeLoginGrant;
    // Base64url Steam ticket carrier for the same native NMT_Login attempt.
    // It is retained through reliable retransmits and cleared with the Grant;
    // raw ticket bytes stay inside StrictRosterSteamAuth.
    std::string stagedNativeSteamTicket;
    bool stagedNativeLoginGrantInjected = false;
    // This non-secret correlation scope is supplied by the guarded Toolbox
    // pipe alongside the opaque Grant. The client never treats unverified JWT
    // claims as an admission proof.
    std::optional<NativeMatchScope> stagedNativeMatchScope;
    std::optional<NativeMatchScope> confirmedNativeMatchScope;
    bool stagedNativeHostScope = false;
    std::string stagedNativeConnectionNonce;
    std::string confirmedNativeConnectionNonce;
    bool nativeBackendConnectionConfirmed = false;
    bool localNativePawnReady = false;
    bool localNativeNetReady = false;
    std::chrono::steady_clock::time_point nativeReadinessObservedAt{};
    bool localAuthorityClientScopePending = false;
    std::string currentTarget;
    ConnectStage connectStage = ConnectStage::Idle;
    std::chrono::steady_clock::time_point nextActionAt{};
    std::chrono::steady_clock::time_point travelDeadline{};
    // Steam's GetAuthSessionTicket callback is asynchronous.  Keep this
    // request on its own bounded deadline so a missing callback cannot leave
    // a staged Grant and ticket handle pending forever.
    std::chrono::steady_clock::time_point nativeTicketDeadline{};
    std::uint64_t connectSequence = 0;
    std::string lastConnectError;
    std::chrono::steady_clock::time_point frontendCleanupUntil{};
    std::chrono::steady_clock::time_point nextFrontendCleanupAt{};
    UWorld* directTravelSourceWorld = nullptr;
    bool directTravelUiFinalized = false;
    UWorld* localWorldIdentity = nullptr;
    std::uint64_t localWorldSequence = 0;
    std::string localWorldInstanceId;
    UWorld* playableWorldIdentity = nullptr;
    std::string playableLocalWorldInstanceId;
    std::atomic<bool> ownedSeamlessDestinationUiCleanupPending{false};
    std::atomic<bool> ownedSeamlessDestinationUiCleanupWaitLogged{false};
    std::atomic<bool> ownedSeamlessIntroCameraRecoveryPending{false};
    std::atomic<bool> ownedSeamlessIntroCameraRecoveryWaitLogged{false};
    std::atomic<bool> ownedSeamlessIntroRoundBoundaryReached{false};
    std::atomic<bool> nativeRespawnUiCleanupPending{false};
    std::atomic<bool> loginCompleted{false};
    std::atomic<ULONGLONG> loginTravelReadyAtTick{0};
    std::atomic<DWORD> gameThreadId{0};

    constexpr auto LoginSettleDelay = std::chrono::seconds(2);
    constexpr auto TravelTimeout = std::chrono::seconds(90);
    constexpr auto NativeTicketTimeout = std::chrono::seconds(10);
    constexpr ULONGLONG LoginTravelSettleMilliseconds = 2000;
    constexpr auto FrontendCleanupDuration = std::chrono::seconds(30);
    constexpr auto FrontendCleanupInterval = std::chrono::milliseconds(500);

    void SecureClearNativeGrant(std::string& value) noexcept
    {
        volatile char* data = value.empty() ? nullptr : value.data();
        for (std::size_t index = 0; data && index < value.size(); ++index)
            data[index] = 0;
        value.clear();
    }

    void ClearStagedNativeLoginGrantLocked() noexcept
    {
        SecureClearNativeGrant(stagedNativeLoginGrant);
        SecureClearNativeGrant(stagedNativeSteamTicket);
        stagedNativeLoginGrantInjected = false;
    }

    void ClearNativeMatchScopeLocked() noexcept
    {
        stagedNativeMatchScope.reset();
        confirmedNativeMatchScope.reset();
        stagedNativeHostScope = false;
        SecureClearNativeGrant(stagedNativeConnectionNonce);
        SecureClearNativeGrant(confirmedNativeConnectionNonce);
        nativeBackendConnectionConfirmed = false;
        localNativePawnReady = false;
        localNativeNetReady = false;
        nativeReadinessObservedAt = {};
        localAuthorityClientScopePending = false;
        localWorldIdentity = nullptr;
        localWorldInstanceId.clear();
        playableWorldIdentity = nullptr;
        playableLocalWorldInstanceId.clear();
    }

    std::string ObserveLocalWorldLocked(UWorld* const world)
    {
        if (!world)
            return {};
        if (localWorldIdentity != world || localWorldInstanceId.empty())
        {
            localNativePawnReady = false;
            localNativeNetReady = false;
            nativeReadinessObservedAt = {};
            localWorldIdentity = world;
            ++localWorldSequence;
            std::ostringstream id;
            id << "client_world_" << std::hex << localWorldSequence << "_"
               << reinterpret_cast<std::uintptr_t>(world);
            localWorldInstanceId = id.str();
        }
        return localWorldInstanceId;
    }

    bool TryPromoteClientPlayableLocked()
    {
        if (!stagedNativeMatchScope || !confirmedNativeMatchScope ||
            !nativeBackendConnectionConfirmed || !localNativePawnReady ||
            !localNativeNetReady ||
            !stagedNativeMatchScope->Matches(*confirmedNativeMatchScope) ||
            (stagedNativeHostScope &&
                (stagedNativeConnectionNonce.empty() ||
                    stagedNativeConnectionNonce != confirmedNativeConnectionNonce)))
        {
            return false;
        }
        if (!localWorldIdentity || localWorldInstanceId.empty() ||
            (playableWorldIdentity && (playableWorldIdentity != localWorldIdentity ||
                playableLocalWorldInstanceId != localWorldInstanceId)))
            return false;
        playableWorldIdentity = localWorldIdentity;
        playableLocalWorldInstanceId = localWorldInstanceId;
        connectStage = ConnectStage::Playable;
        pendingTarget.reset();
        localAuthorityClientScopePending = false;
        nativeTicketDeadline = {};
        ClearStagedNativeLoginGrantLocked();
        return true;
    }

    bool NativeReadinessSnapshotCurrentLocked()
    {
        return nativeReadinessObservedAt != std::chrono::steady_clock::time_point{} &&
            (!playableWorldIdentity || (playableWorldIdentity == localWorldIdentity &&
                playableLocalWorldInstanceId == localWorldInstanceId)) &&
            std::chrono::steady_clock::now() - nativeReadinessObservedAt <=
                std::chrono::seconds(2);
    }

    const char* ConnectStageName(const ConnectStage stage) noexcept
    {
        switch (stage)
        {
        case ConnectStage::Idle: return "idle";
        case ConnectStage::Queued: return "queued";
        case ConnectStage::WaitingAfterLogin: return "waiting_game_login";
        case ConnectStage::TravelRequested: return "travel_requested";
        case ConnectStage::WorldReady: return "world_ready";
        case ConnectStage::LocalPawnReady: return "local_pawn_ready";
        case ConnectStage::WaitingBackendConfirmation:
            return "waiting_backend_confirmation";
        case ConnectStage::LocalAuthorityPending:
            return "local_authority_pending";
        case ConnectStage::Playable: return "playable";
        case ConnectStage::Failed: return "failed";
        case ConnectStage::Cancelled: return "cancelled";
        }
        return "unknown";
    }

    bool IsOfflinePveClient() noexcept
    {
        const std::string commandLine = GetCommandLineA();
        return CommandLinePolicy::HasExactSwitch(commandLine, "-pve");
    }

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
    if (!IsOfflinePveClient())
    {
        ClientLog("[STRICT-ROSTER] Refused an online direct-open request; "
                  "a verified native Join Grant is required.");
        return false;
    }
    std::string validationError;
    if (!CommandProtocol::ValidateMatchTarget(target, &validationError))
    {
        ClientLog("[CLIENT] Rejected match target: " + validationError);
        return false;
    }

    {
        std::unique_lock<std::mutex> lock(connectMutex);
        if (pendingTarget.has_value())
            return false;

        ClearStagedNativeLoginGrantLocked();
        ClearNativeMatchScopeLocked();
        pendingTarget = target;
        connectStage = ConnectStage::Queued;
        travelDeadline = {};
        nativeTicketDeadline = {};
        ++connectSequence;
        lastConnectError.clear();
        frontendCleanupUntil = {};
        nextFrontendCleanupAt = {};
        directTravelSourceWorld = nullptr;
        directTravelUiFinalized = false;
    }
    ClientLog("[CLIENT] Match transition queued: " + target);
    return true;
}

bool CopyStagedNativeLoginGrant(std::string& grant)
{
    SecureClearNativeGrant(grant);
    std::lock_guard<std::mutex> lock(connectMutex);
    if (!pendingTarget.has_value() || stagedNativeLoginGrant.empty() ||
        (connectStage != ConnectStage::Queued &&
            connectStage != ConnectStage::WaitingAfterLogin &&
            connectStage != ConnectStage::TravelRequested &&
            connectStage != ConnectStage::WorldReady &&
            connectStage != ConnectStage::LocalPawnReady &&
            connectStage != ConnectStage::WaitingBackendConfirmation))
    {
        return false;
    }
    grant = stagedNativeLoginGrant;
    return true;
}

bool CopyStagedNativeSteamTicket(std::string& encodedTicket)
{
    SecureClearNativeGrant(encodedTicket);
    std::lock_guard<std::mutex> lock(connectMutex);
    if (!pendingTarget.has_value() || stagedNativeLoginGrant.empty() ||
        (connectStage != ConnectStage::Queued &&
            connectStage != ConnectStage::WaitingAfterLogin &&
            connectStage != ConnectStage::TravelRequested &&
            connectStage != ConnectStage::WorldReady &&
            connectStage != ConnectStage::LocalPawnReady &&
            connectStage != ConnectStage::WaitingBackendConfirmation))
    {
        return false;
    }
    if (!stagedNativeSteamTicket.empty())
    {
        encodedTicket = stagedNativeSteamTicket;
        return true;
    }
    if (!StrictRosterSteamAuth::CopyClientAuthTicket(encodedTicket))
        return false;
    stagedNativeSteamTicket = encodedTicket;
    return true;
}

void MarkStagedNativeLoginGrantInjected()
{
    std::lock_guard<std::mutex> lock(connectMutex);
    if (pendingTarget.has_value() && !stagedNativeLoginGrant.empty() &&
        !stagedNativeSteamTicket.empty())
    {
        stagedNativeLoginGrantInjected = true;
    }
}

void ClearStagedNativeLoginGrant()
{
    std::lock_guard<std::mutex> lock(connectMutex);
    ClearStagedNativeLoginGrantLocked();
}

namespace
{
    bool IsNativeClientTransitionBusyLocked() noexcept
    {
        return pendingTarget.has_value() ||
            connectStage == ConnectStage::Queued ||
            connectStage == ConnectStage::WaitingAfterLogin ||
            connectStage == ConnectStage::TravelRequested ||
            connectStage == ConnectStage::WorldReady ||
            connectStage == ConnectStage::LocalPawnReady ||
            connectStage == ConnectStage::WaitingBackendConfirmation ||
            connectStage == ConnectStage::LocalAuthorityPending ||
            connectStage == ConnectStage::Playable;
    }

    AuthorizedJoinResult QueueConnectToMatchAuthorizedWithScope(
    const std::string& target,
        const std::string_view joinGrant,
        const NativeMatchScope& expectedScope)
    {
        std::string scopeError;
        if (!expectedScope.IsValid(&scopeError))
        {
            ClientLog("[STRICT-ROSTER] Rejected strict join: " + scopeError);
            return AuthorizedJoinResult{
                false, "invalid_scope", scopeError};
        }

    std::string validationError;
    if (!CommandProtocol::ValidateMatchTarget(target, &validationError))
    {
        ClientLog("[STRICT-ROSTER] Rejected match target: " + validationError);
        return AuthorizedJoinResult{
            false, "invalid_target", validationError};
    }
    if (joinGrant.empty() || joinGrant.size() > CommandProtocol::MaxTokenBytes)
    {
        ClientLog("[STRICT-ROSTER] Rejected invalid join grant length.");
        return AuthorizedJoinResult{
            false, "invalid_join_grant", "join grant is missing or too large"};
    }

    if (!IsStrictRosterNativeClientGrantInjectionReady())
    {
        ClientLog("[STRICT-ROSTER] Refused strict join: native Grant injection is unverified.");
        return AuthorizedJoinResult{
            false,
            "native_client_grant_injection_unverified",
            "native NMT_Login Grant injection is not verified for this build"};
    }
    if (!NativeLoginGrantPolicy::ValidateGrantTokenShape(joinGrant))
    {
        ClientLog("[STRICT-ROSTER] Rejected strict join: Grant transport shape is invalid.");
        return AuthorizedJoinResult{
            false, "invalid_join_grant", "join grant transport shape is invalid"};
    }

    bool queueBusyAfterTicket = false;
    std::uint64_t operationSequence = 0;
    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (IsNativeClientTransitionBusyLocked())
        {
            return AuthorizedJoinResult{
                false, "busy", "another native match transition is pending"};
        }
    }

    // Obtain the ticket on the same Steam client that will carry this Grant
    // through NMT_Login.  The callback is asynchronous; PumpPendingClientCommands
    // keeps the travel in the waiting state until the exact handle/result is
    // confirmed by Steam's ordinary dispatcher.
    const auto ticketState = StrictRosterSteamAuth::RequestClientAuthTicket();
    if (ticketState == StrictRosterSteamAuth::ClientTicketState::Unavailable)
    {
        ClientLog("[STRICT-ROSTER] Refused strict join: Steam auth ticket is unavailable.");
        return AuthorizedJoinResult{
            false,
            "native_platform_ticket_unavailable",
            "Steam platform auth ticket could not be acquired"};
    }

    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (IsNativeClientTransitionBusyLocked())
        {
            // The ticket belongs to the request that raced this queue.  Do
            // not leave an unscoped Steam handle alive after rejecting it.
            // CancelClientAuthTicket takes its own mutex after this scope is
            // released below.
            queueBusyAfterTicket = true;
        }
        else
        {
            ClearStagedNativeLoginGrantLocked();
            ClearNativeMatchScopeLocked();
            pendingTarget = target;
            stagedNativeLoginGrant = std::string(joinGrant);
            stagedNativeMatchScope = expectedScope;
            stagedNativeLoginGrantInjected = false;
            connectStage = ConnectStage::Queued;
            travelDeadline = {};
            nativeTicketDeadline = ticketState ==
                    StrictRosterSteamAuth::ClientTicketState::Pending
                ? std::chrono::steady_clock::now() + NativeTicketTimeout
                : std::chrono::steady_clock::time_point{};
            operationSequence = ++connectSequence;
            lastConnectError.clear();
            frontendCleanupUntil = {};
            nextFrontendCleanupAt = {};
            directTravelSourceWorld = nullptr;
            directTravelUiFinalized = false;
        }
    }
    if (queueBusyAfterTicket)
    {
        StrictRosterSteamAuth::CancelClientAuthTicket();
        return AuthorizedJoinResult{
            false, "busy", "another native match transition is pending"};
    }
    ClientLog(ticketState == StrictRosterSteamAuth::ClientTicketState::Ready
        ? "[STRICT-ROSTER] Signed native join queued with a verified Steam ticket carrier."
        : "[STRICT-ROSTER] Signed native join queued; waiting for the Steam ticket callback.");
    return AuthorizedJoinResult{
        true, "accepted", "native join queued", operationSequence};
    }
}

AuthorizedJoinResult QueueConnectToMatchAuthorizedDetailed(
    const std::string& target,
    const std::string_view joinGrant,
    const nlohmann::json& expectedScope)
{
    const auto role = expectedScope.find("room_role");
    if (role != expectedScope.end() &&
        (!role->is_string() || role->get<std::string>() != "MEMBER"))
    {
        return AuthorizedJoinResult{
            false,
            "invalid_scope",
            "remote native joins require room_role MEMBER"};
    }
    std::string scopeError;
    const auto scope = NativeMatchScope::FromJson(expectedScope, &scopeError);
    if (!scope)
    {
        return AuthorizedJoinResult{
            false,
            "invalid_scope",
            scopeError.empty() ? "strict admission scope is invalid" : scopeError};
    }
    return QueueConnectToMatchAuthorizedWithScope(target, joinGrant, *scope);
}

nlohmann::json StageLocalAuthorityClientScope(
    const nlohmann::json& hostScope,
    const std::string_view nativeConnectionNonce)
{
    std::string scopeError;
    const auto scope = NativeMatchScope::FromHostJson(hostScope, &scopeError);
    if (!scope)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "invalid_host_scope"},
            {"message", scopeError.empty()
                ? "P2P HOST scope is invalid" : scopeError}};
    }
    if (!NativeMatchScopeDetail::IsSafeNonce(std::string(nativeConnectionNonce)))
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "native_connection_nonce_required"},
            {"message", "a fresh native HOST connection nonce is required"}};
    }

    const std::string currentAuthorityWorldId =
        ReadStrictAuthorityWorldInstanceId();
    if (currentAuthorityWorldId.empty() ||
        currentAuthorityWorldId != scope->worldInstanceId)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "authority_world_mismatch"},
            {"message", "HOST scope must match the observed native authority world"}};
    }

    std::uint64_t operationSequence = 0;
    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (stagedNativeHostScope && stagedNativeMatchScope &&
            stagedNativeMatchScope->Matches(*scope) &&
            stagedNativeConnectionNonce == nativeConnectionNonce &&
            (localAuthorityClientScopePending ||
                connectStage == ConnectStage::LocalPawnReady ||
                connectStage == ConnectStage::Playable))
        {
            nlohmann::json already{
                {"accepted", true},
                {"code", "already_staged"},
                {"status", ConnectStageName(connectStage)},
                {"operation_sequence", connectSequence},
                {"native_connection_nonce", stagedNativeConnectionNonce},
                {"scope", scope->ToJson()},
                {"room_role", "HOST"}};
            already["scope"]["room_role"] = "HOST";
            return already;
        }
        if (IsNativeClientTransitionBusyLocked() || localAuthorityClientScopePending)
        {
            return nlohmann::json{
                {"accepted", false},
                {"code", "busy"},
                {"message", "another native client transition is pending"},
                {"operation_sequence", connectSequence}};
        }

        ClearStagedNativeLoginGrantLocked();
        ClearNativeMatchScopeLocked();
        stagedNativeMatchScope = *scope;
        stagedNativeHostScope = true;
        stagedNativeConnectionNonce = std::string(nativeConnectionNonce);
        localAuthorityClientScopePending = true;
        localNativePawnReady = false;
        localNativeNetReady = false;
        nativeBackendConnectionConfirmed = false;
        confirmedNativeMatchScope.reset();
        SecureClearNativeGrant(confirmedNativeConnectionNonce);
        pendingTarget.reset();
        connectStage = ConnectStage::LocalAuthorityPending;
        travelDeadline = std::chrono::steady_clock::now() + TravelTimeout;
        nativeTicketDeadline = {};
        operationSequence = ++connectSequence;
        lastConnectError.clear();
        frontendCleanupUntil = {};
        nextFrontendCleanupAt = {};
        directTravelSourceWorld = nullptr;
        directTravelUiFinalized = false;
    }

    nlohmann::json result{
        {"accepted", true},
        {"code", "staged"},
        {"status", "local_authority_pending"},
        {"operation_sequence", operationSequence},
        {"native_connection_nonce", std::string(nativeConnectionNonce)},
        {"scope", scope->ToJson()},
        {"room_role", "HOST"}};
    result["scope"]["room_role"] = "HOST";
    return result;
}

nlohmann::json ConfirmClientMatchConnection(
    const nlohmann::json& arguments)
{
    std::string parseError;
    const auto confirmation =
        NativeClientMatchConfirmation::FromJson(arguments, &parseError);
    if (!confirmation)
    {
        return nlohmann::json{
            {"accepted", false},
            {"code", "invalid_confirmation"},
            {"message", parseError.empty()
                ? "native client confirmation is invalid" : parseError}};
    }

    std::lock_guard<std::mutex> lock(connectMutex);
    const auto reject = [&](const char* code, const char* message) {
        return nlohmann::json{
            {"accepted", false},
            {"code", code},
            {"message", message},
            {"status", ConnectStageName(connectStage)},
            {"operation_sequence", connectSequence}};
    };

    const bool sameConfirmedConnection =
        nativeBackendConnectionConfirmed && confirmedNativeMatchScope &&
        stagedNativeHostScope == confirmation->hostScope &&
        confirmedNativeMatchScope->Matches(confirmation->scope) &&
        confirmedNativeConnectionNonce == confirmation->nativeConnectionNonce &&
        confirmation->operationSequence == connectSequence;
    if (sameConfirmedConnection)
    {
        nlohmann::json result{
            {"accepted", true},
            {"code", "already_confirmed"},
            {"message", "native client connection confirmation already applied"},
            {"status", ConnectStageName(connectStage)},
            {"operation_sequence", connectSequence},
            {"scope_verified", connectStage == ConnectStage::Playable &&
                NativeReadinessSnapshotCurrentLocked() && localNativePawnReady && localNativeNetReady},
            {"local_pawn_ready", localNativePawnReady},
            {"native_net_ready", localNativeNetReady},
            {"native_connection_nonce", confirmedNativeConnectionNonce},
            {"local_world_instance_id", localWorldInstanceId}};
        result["scope"] = confirmation->scope.ToJson();
        if (confirmation->hostScope)
            result["scope"]["room_role"] = "HOST";
        return result;
    }

    if (!stagedNativeMatchScope ||
        !stagedNativeMatchScope->Matches(confirmation->scope))
    {
        return reject(
            "scope_mismatch",
            "native client confirmation does not match the staged admission scope");
    }
    if (confirmation->operationSequence != connectSequence)
    {
        return reject(
            "stale_operation",
            "native client confirmation operation sequence is stale");
    }
    if (stagedNativeHostScope != confirmation->hostScope)
    {
        return reject(
            "scope_role_mismatch",
            "native client confirmation role does not match the staged operation");
    }
    if (stagedNativeHostScope &&
        (stagedNativeConnectionNonce.empty() ||
            stagedNativeConnectionNonce != confirmation->nativeConnectionNonce))
    {
        return reject(
            "native_connection_nonce_mismatch",
            "native HOST confirmation nonce does not match the staged authority nonce");
    }
    if (!pendingTarget.has_value() && !localAuthorityClientScopePending &&
        connectStage != ConnectStage::Playable)
    {
        return reject(
            "transition_not_pending",
            "native client confirmation arrived outside the active transition");
    }
    if (connectStage == ConnectStage::Failed ||
        connectStage == ConnectStage::Cancelled ||
        connectStage == ConnectStage::Idle)
    {
        return reject(
            "transition_not_pending",
            "native client confirmation arrived after transition termination");
    }
    if (nativeBackendConnectionConfirmed &&
        (!confirmedNativeMatchScope ||
            !confirmedNativeMatchScope->Matches(confirmation->scope) ||
            confirmedNativeConnectionNonce != confirmation->nativeConnectionNonce))
    {
        return reject(
            "confirmation_conflict",
            "a different native connection is already confirmed for this operation");
    }

    confirmedNativeMatchScope = confirmation->scope;
    confirmedNativeConnectionNonce = confirmation->nativeConnectionNonce;
    nativeBackendConnectionConfirmed = true;
    // A pipe confirmation records backend proof only. Promotion is performed
    // by the next game-thread observation of the current world and socket.
    const bool promoted = false;
    if (!promoted && localNativePawnReady)
        connectStage = ConnectStage::LocalPawnReady;

    nlohmann::json result{
        {"accepted", true},
        {"code", promoted ? "playable" : "accepted"},
        {"message", promoted
            ? "native client connection confirmed and local readiness is complete"
            : "native client connection confirmed; awaiting local native readiness"},
        {"status", ConnectStageName(connectStage)},
        {"operation_sequence", connectSequence},
        {"scope_verified", promoted},
        {"local_pawn_ready", localNativePawnReady},
        {"native_net_ready", localNativeNetReady},
        {"native_connection_nonce", confirmedNativeConnectionNonce},
        {"local_world_instance_id", localWorldInstanceId}};
    result["scope"] = confirmation->scope.ToJson();
    if (confirmation->hostScope)
        result["scope"]["room_role"] = "HOST";
    return result;
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
    bool expected = false;
    if (!loginCompleted.compare_exchange_strong(
            expected, true, std::memory_order_acq_rel))
    {
        return;
    }
    gameThreadId.store(GetCurrentThreadId(), std::memory_order_release);
    loginTravelReadyAtTick.store(
        GetTickCount64() + LoginTravelSettleMilliseconds,
        std::memory_order_release);
    ClientLog("[ARCHIVE] Native QueryAssets/GetPlayerArchiveV2 ownership enabled; "
              "client archive mirrors are read-only.");
    if (IsNativeArchiveOnly())
    {
        ClientLog("[LOADOUT] NativeArchiveOnly active; client FieldMod initialization is read-only.");
    }
}

bool IsClientLoginCompleted()
{
    return loginCompleted.load(std::memory_order_acquire);
}

bool IsClientLoginReadyForTravel()
{
    if (!IsClientLoginCompleted())
        return false;
    const ULONGLONG readyAt =
        loginTravelReadyAtTick.load(std::memory_order_acquire);
    return readyAt != 0 && GetTickCount64() >= readyAt;
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
        std::lock_guard<std::mutex> lock(connectMutex);
        localNativePawnReady = false;
        localNativeNetReady = false;
        nativeReadinessObservedAt = {};
        return;
    }

    auto* const localPlayer = static_cast<UPBLocalPlayer*>(
        world->OwningGameInstance->LocalPlayers[0]);
    if (localPlayer == nullptr)
        return;

    bool cancelTicketForWorldReplacement = false;
    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (!localAuthorityClientScopePending && localWorldIdentity != world)
        {
            if (connectStage == ConnectStage::Playable && stagedNativeMatchScope)
            {
                ClearNativeMatchScopeLocked();
                connectStage = ConnectStage::Failed;
                lastConnectError = "confirmed_native_world_replaced";
                cancelTicketForWorldReplacement = true;
            }
            (void)ObserveLocalWorldLocked(world);
        }
    }
    if (cancelTicketForWorldReplacement)
        StrictRosterSteamAuth::CancelClientAuthTicket();

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

    // ExecuteConsoleCommand only proves that Unreal accepted the console
    // command. Keep the operation alive until the destination world and the
    // local Pawn/PlayerState become native-playable, or until the bounded
    // travel deadline expires.
    bool travelTimedOut = false;
    bool cancelSteamTicketAfterTravelFailure = false;
    bool destinationWorldReady = false;
    bool destinationLocalPawnReady = false;
    bool destinationNativeNetReady = false;
    APBPlayerController* localPlayerController = nullptr;
    if (world->OwningGameInstance &&
        world->OwningGameInstance->LocalPlayers.Num() > 0)
    {
        auto* const candidate = world->OwningGameInstance->LocalPlayers[0]
            ? world->OwningGameInstance->LocalPlayers[0]->PlayerController
            : nullptr;
        if (candidate && candidate->IsA(APBPlayerController::StaticClass()))
            localPlayerController = static_cast<APBPlayerController*>(candidate);
    }
    {
        std::lock_guard<std::mutex> lock(connectMutex);
        const bool watchingTravel = localAuthorityClientScopePending ||
            connectStage == ConnectStage::TravelRequested ||
            connectStage == ConnectStage::WorldReady ||
            connectStage == ConnectStage::LocalPawnReady ||
            connectStage == ConnectStage::WaitingBackendConfirmation ||
            connectStage == ConnectStage::Playable;
        if (watchingTravel)
        {
            if (stagedNativeHostScope)
            {
                destinationWorldReady = world->GameState != nullptr &&
                    world->NetDriver != nullptr &&
                    world->NetDriver->World == world &&
                    stagedNativeHostScope && stagedNativeMatchScope &&
                    ReadStrictAuthorityWorldInstanceId() ==
                        stagedNativeMatchScope->worldInstanceId;
            }
            else
            {
                destinationWorldReady = directTravelSourceWorld != nullptr &&
                    world != directTravelSourceWorld && world->GameState != nullptr;
            }
            const APawn* const pawn = localPlayerController
                ? localPlayerController->Pawn : nullptr;
            destinationLocalPawnReady = destinationWorldReady && pawn &&
                pawn->IsA(APBCharacter::StaticClass()) &&
                !pawn->bActorIsBeingDestroyed &&
                localPlayerController->AcknowledgedPawn == pawn &&
                localPlayerController->PBCharacter == pawn &&
                static_cast<const APBCharacter*>(pawn)->CharacterLifeStatus ==
                    EPBCharacterLifeStatus::Alive;
            if (stagedNativeHostScope)
            {
                // A listen HOST owns the authority NetDriver and its local
                // PlayerController does not have a remote NetConnection.
                // Require the local player, authority world/driver, and
                // AuthorityGameMode instead of inventing a client socket.
                destinationNativeNetReady = destinationLocalPawnReady &&
                    localPlayer != nullptr &&
                    localPlayerController != nullptr &&
                    localPlayerController->Player != nullptr &&
                    world->NetDriver != nullptr &&
                    world->NetDriver->World == world &&
                    world->AuthorityGameMode != nullptr;
            }
            else
            {
                const auto* const serverConnection = world->NetDriver
                    ? world->NetDriver->ServerConnection : nullptr;
                destinationNativeNetReady = destinationLocalPawnReady &&
                    world->NetDriver != nullptr &&
                    world->NetDriver->World == world &&
                    localPlayerController != nullptr &&
                    serverConnection != nullptr &&
                    serverConnection->Driver == world->NetDriver &&
                    serverConnection->PlayerController == localPlayerController;
            }
            if (destinationWorldReady)
            {
                ObserveLocalWorldLocked(world);
                localNativePawnReady = destinationLocalPawnReady;
                localNativeNetReady = destinationNativeNetReady;
                nativeReadinessObservedAt = now;

                // Explicit offline PvE is the only path that may complete
                // without an authority scope.  Online Playable requires the
                // exact backend/native confirmation and the same-world
                // network objects below.
                if (connectStage == ConnectStage::Playable)
                {
                    // Continue refreshing readiness through respawn and
                    // disconnect; a historical Pawn is never a fresh proof.
                }
                else if (destinationLocalPawnReady && destinationNativeNetReady &&
                    IsOfflinePveClient())
                {
                    connectStage = ConnectStage::Playable;
                    pendingTarget.reset();
                    nativeTicketDeadline = {};
                    ClearStagedNativeLoginGrantLocked();
                    ClientLog("[CLIENT] Offline PvE travel reached Playable; transition completed.");
                }
                else if (TryPromoteClientPlayableLocked())
                {
                    ClientLog("[CLIENT] Strict native travel reached Playable after exact backend scope confirmation.");
                }
                else if (destinationLocalPawnReady)
                {
                    connectStage = localAuthorityClientScopePending
                        ? ConnectStage::LocalPawnReady
                        : (nativeBackendConnectionConfirmed
                            ? ConnectStage::LocalPawnReady
                            : ConnectStage::WaitingBackendConfirmation);
                    ClientLog(nativeBackendConnectionConfirmed
                        ? "[CLIENT] Native Pawn is ready; awaiting same-world NetConnection readiness."
                        : "[CLIENT] Native Pawn/NetDriver are ready; awaiting exact backend connection confirmation.");
                }
                else if (connectStage == ConnectStage::TravelRequested ||
                    connectStage == ConnectStage::LocalPawnReady ||
                    connectStage == ConnectStage::WaitingBackendConfirmation ||
                    localAuthorityClientScopePending)
                {
                    connectStage = localAuthorityClientScopePending
                        ? ConnectStage::LocalAuthorityPending
                        : ConnectStage::WorldReady;
                    ClientLog(localAuthorityClientScopePending
                        ? "[CLIENT] Native HOST world is listening; awaiting local Pawn/NetConnection."
                        : "[CLIENT] Native travel reached WorldReady; awaiting native Pawn/NetConnection.");
                }
            }
            else
            {
                localNativePawnReady = false;
                localNativeNetReady = false;
                nativeReadinessObservedAt = now;
            }
            if (connectStage != ConnectStage::Playable &&
                travelDeadline != std::chrono::steady_clock::time_point{} &&
                now >= travelDeadline)
            {
                connectStage = ConnectStage::Failed;
                lastConnectError = destinationWorldReady
                    ? "playable_pawn_timeout" : "world_ready_timeout";
                pendingTarget.reset();
                nativeTicketDeadline = {};
                ClearStagedNativeLoginGrantLocked();
                ClearNativeMatchScopeLocked();
                frontendCleanupUntil = {};
                nextFrontendCleanupAt = {};
                travelTimedOut = true;
                cancelSteamTicketAfterTravelFailure = true;
            }
        }
    }
    if (travelTimedOut)
    {
        if (cancelSteamTicketAfterTravelFailure)
            StrictRosterSteamAuth::CancelClientAuthTicket();
        ClientLog("[CLIENT] Native travel failed: bounded readiness timeout (" +
            lastConnectError + ").");
        return;
    }

    {
        std::unique_lock<std::mutex> lock(connectMutex);
        const bool localAuthorityPending = localAuthorityClientScopePending;
        if (!pendingTarget.has_value() && !localAuthorityPending)
            return;

        // A staged P2P HOST is already on the local authority travel path;
        // it has no remote Grant or client Steam ticket to inject. Its only
        // completion path is the local world/Pawn/NetConnection observation
        // above plus the exact HOST CONNECTED confirmation.
        if (localAuthorityPending)
            return;

        // A strict transition may reach this pump only with the signed grant
        // still staged.  Keep the local-PVE direct-open path separate; an
        // online request whose staged bearer was cleared or never installed
        // must fail before ExecuteConsoleCommand can be reached.
        if (!IsOfflinePveClient() && stagedNativeLoginGrant.empty())
        {
            connectStage = ConnectStage::Failed;
            lastConnectError = "native_grant_missing";
            pendingTarget.reset();
            nativeTicketDeadline = {};
            ClearStagedNativeLoginGrantLocked();
            ClearNativeMatchScopeLocked();
            frontendCleanupUntil = {};
            nextFrontendCleanupAt = {};
            lock.unlock();
            ClientLog("[STRICT-ROSTER] Refused native travel: staged Grant is missing.");
            return;
        }

        if (!IsOfflinePveClient())
        {
            const auto ticketState = StrictRosterSteamAuth::GetClientTicketState();
            if (ticketState == StrictRosterSteamAuth::ClientTicketState::Unavailable)
            {
                connectStage = ConnectStage::Failed;
                lastConnectError = "native_platform_ticket_unavailable";
                pendingTarget.reset();
                nativeTicketDeadline = {};
                ClearStagedNativeLoginGrantLocked();
                ClearNativeMatchScopeLocked();
                frontendCleanupUntil = {};
                nextFrontendCleanupAt = {};
                lock.unlock();
                StrictRosterSteamAuth::CancelClientAuthTicket();
                ClientLog("[STRICT-ROSTER] Refused native travel: Steam ticket "
                          "callback failed or the ticket carrier is unavailable.");
                return;
            }
            if (ticketState != StrictRosterSteamAuth::ClientTicketState::Ready &&
                nativeTicketDeadline != std::chrono::steady_clock::time_point{} &&
                now >= nativeTicketDeadline)
            {
                connectStage = ConnectStage::Failed;
                lastConnectError = "native_platform_ticket_timeout";
                pendingTarget.reset();
                nativeTicketDeadline = {};
                ClearStagedNativeLoginGrantLocked();
                ClearNativeMatchScopeLocked();
                frontendCleanupUntil = {};
                nextFrontendCleanupAt = {};
                lock.unlock();
                StrictRosterSteamAuth::CancelClientAuthTicket();
                ClientLog("[STRICT-ROSTER] Refused native travel: Steam ticket "
                          "callback did not arrive before the bounded request deadline.");
                return;
            }
            if (ticketState != StrictRosterSteamAuth::ClientTicketState::Ready)
            {
                nextActionAt = now + std::chrono::milliseconds(100);
                return;
            }
        }

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

    // Once travel has been dispatched, later ticks only observe the world and
    // admission. Having no new command on such a tick is not a travel failure;
    // preserve the staged Grant/ticket until NMT_Login and confirmation finish.
    if (!connectTarget.has_value())
        return;

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
                currentTarget = *connectTarget;
                connectStage = ConnectStage::TravelRequested;
                travelDeadline = std::chrono::steady_clock::now() + TravelTimeout;
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
    {
        std::unique_lock<std::mutex> lock(connectMutex);
        connectStage = ConnectStage::Failed;
        lastConnectError = "travel_command_rejected";
        pendingTarget.reset();
        nativeTicketDeadline = {};
        ClearStagedNativeLoginGrantLocked();
        ClearNativeMatchScopeLocked();
        frontendCleanupUntil = {};
        nextFrontendCleanupAt = {};
        lock.unlock();
        StrictRosterSteamAuth::CancelClientAuthTicket();
        return;
    }

    std::lock_guard<std::mutex> lock(connectMutex);
    if (connectTarget.has_value() && connectStage == ConnectStage::TravelRequested &&
        pendingTarget == connectTarget)
    {
        const auto cleanupStart = std::chrono::steady_clock::now();
        frontendCleanupUntil = cleanupStart + FrontendCleanupDuration;
        nextFrontendCleanupAt = cleanupStart;
    }
}

nlohmann::json GetClientMatchStatus()
{
    std::lock_guard<std::mutex> lock(connectMutex);
    const bool pending = pendingTarget.has_value();
    const bool scopeVerified = connectStage == ConnectStage::Playable &&
        nativeBackendConnectionConfirmed && NativeReadinessSnapshotCurrentLocked() && localNativePawnReady &&
        localNativeNetReady && stagedNativeMatchScope &&
        confirmedNativeMatchScope &&
        stagedNativeMatchScope->Matches(*confirmedNativeMatchScope);
    nlohmann::json scope = nlohmann::json::object();
    if (stagedNativeMatchScope)
    {
        scope = stagedNativeMatchScope->ToJson();
        if (stagedNativeHostScope)
            scope["room_role"] = "HOST";
    }
    nlohmann::json playableScope = nlohmann::json::object();
    if (confirmedNativeMatchScope)
    {
        playableScope = confirmedNativeMatchScope->ToJson();
        if (stagedNativeHostScope)
            playableScope["room_role"] = "HOST";
    }
    return nlohmann::json{
        {"state", ConnectStageName(connectStage)},
        {"operation_sequence", connectSequence},
        {"pending", pending},
        {"login_completed", IsClientLoginCompleted()},
        {"login_ready", IsClientLoginReadyForTravel()},
        {"native_grant_staged", pending && !stagedNativeLoginGrant.empty()},
        {"native_grant_injected", pending && stagedNativeLoginGrantInjected},
        {"scope", std::move(scope)},
        {"playable_scope", std::move(playableScope)},
        {"scope_verified", scopeVerified},
        {"local_pawn_ready", localNativePawnReady},
        {"native_net_ready", localNativeNetReady},
        {"local_authority_scope_pending", localAuthorityClientScopePending},
        {"native_connection_nonce", nativeBackendConnectionConfirmed
            ? confirmedNativeConnectionNonce
            : (stagedNativeHostScope ? stagedNativeConnectionNonce : std::string{})},
        {"local_world_instance_id", localWorldInstanceId},
        {"last_error", lastConnectError}
    };
}

nlohmann::json CancelPendingClientTransition()
{
    bool shouldCancelSteamTicket = false;
    bool wasTerminal = false;
    std::string terminalStatus;
    std::string terminalCode;
    std::uint64_t terminalSequence = 0;
    {
        std::lock_guard<std::mutex> lock(connectMutex);
        if (!pendingTarget.has_value() && !localAuthorityClientScopePending &&
            connectStage != ConnectStage::TravelRequested &&
            connectStage != ConnectStage::WorldReady)
        {
            wasTerminal = true;
            // A Playable connection keeps its auth-session ticket alive until
            // the explicit disconnect/return-to-menu cleanup.  The terminal
            // command is that cleanup boundary; never cancel it merely when
            // Playable was first reached.  The helper is idempotent, so every
            // terminal cleanup also closes a ticket left by a failed/expired
            // transition that raced one of the bounded failure paths.
            shouldCancelSteamTicket = true;
            nativeTicketDeadline = {};
            ClearStagedNativeLoginGrantLocked();
            const bool wasPlayable = connectStage == ConnectStage::Playable;
            ClearNativeMatchScopeLocked();
            if (wasPlayable)
            {
                connectStage = ConnectStage::Cancelled;
                lastConnectError = "cancelled";
                ++connectSequence;
            }
            terminalStatus = ConnectStageName(connectStage);
            terminalCode = wasPlayable ? "cancelled" : "already_terminal";
            terminalSequence = connectSequence;
        }
        else
        {
            pendingTarget.reset();
            ClearStagedNativeLoginGrantLocked();
            ClearNativeMatchScopeLocked();
            connectStage = ConnectStage::Cancelled;
            lastConnectError = "cancelled";
            frontendCleanupUntil = {};
            nextFrontendCleanupAt = {};
            travelDeadline = {};
            nativeTicketDeadline = {};
            ++connectSequence;
            shouldCancelSteamTicket = true;
        }
    }
    if (shouldCancelSteamTicket)
        StrictRosterSteamAuth::CancelClientAuthTicket();
    if (wasTerminal)
        return nlohmann::json{
            {"accepted", true},
            {"status", terminalStatus},
            {"code", terminalCode},
            {"operation_sequence", terminalSequence}
        };
    ClientLog("[CLIENT] Cancelled pending native travel operation.");
    return nlohmann::json{
        {"accepted", true},
        {"status", "cancelled"},
        {"code", "cancelled"}
    };
}
