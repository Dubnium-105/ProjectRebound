#include "ClientLoadoutSync.h"

#include "../Config/Config.h"
#include "../Debug/Debug.h"
#include "../Loadout/LoadoutApplication.h"
#include "../Loadout/LoadoutSerializer.h"
#include "../Loadout/MetaserverClient.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"

#include <Windows.h>

#include <chrono>
#include <cstdint>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <utility>

using namespace SDK;

extern "C" void PayloadPushClientProcessEventSuppression();
extern "C" void PayloadPopClientProcessEventSuppression();

namespace
{
    using json = nlohmann::json;
    using Clock = std::chrono::steady_clock;

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

    enum class FetchStatus
    {
        Idle,
        Loading,
        Ready,
        Failed,
        Disabled,
    };

    struct FetchState
    {
        std::uint64_t Generation = 0;
        std::uint64_t ResultSerial = 0;
        std::uint64_t LoggedSerial = 0;
        FetchStatus Status = FetchStatus::Idle;
        json Snapshot;
        std::string PlayerId;
        std::string Detail;
        Clock::time_point RetryAt{};
    };

    std::mutex gFetchMutex;
    FetchState gFetch;
    Clock::time_point gNextApplyAt{};
    UWorld* gLastApplyWorld = nullptr;
    UPBFieldModManager* gLastApplyManager = nullptr;
    int gLastMissingRoles = -1;

    constexpr auto ApplyInterval = std::chrono::milliseconds(500);
    constexpr auto RetryDelay = std::chrono::seconds(3);

    bool SameName(const FName& left, const FName& right)
    {
        return left.ComparisonIndex == right.ComparisonIndex &&
            left.Number == right.Number;
    }

    bool SameInventory(
        const FPBInventoryNetworkConfig& left,
        const FPBInventoryNetworkConfig& right)
    {
        if (left.CharacterSlots.Num() != right.CharacterSlots.Num() ||
            left.InventoryItems.Num() != right.InventoryItems.Num() ||
            left.CharacterSlots.Num() != left.InventoryItems.Num())
        {
            return false;
        }
        for (int index = 0; index < left.CharacterSlots.Num(); ++index)
        {
            if (left.CharacterSlots[index] != right.CharacterSlots[index] ||
                !SameName(left.InventoryItems[index], right.InventoryItems[index]))
            {
                return false;
            }
        }
        return true;
    }

    FPBInventoryNetworkConfig* FindRoleInventory(
        UPBFieldModManager* manager,
        const FName& roleId)
    {
        if (!manager ||
            !manager->CharacterPreOrderingInventoryConfigs.IsValid())
        {
            return nullptr;
        }

        for (auto& entry : manager->CharacterPreOrderingInventoryConfigs)
        {
            if (SameName(entry.Key(), roleId)) return &entry.Value();
        }
        return nullptr;
    }

    UPBFieldModManager* ResolveFieldModManager(UWorld* world)
    {
        if (!world) return nullptr;
        try
        {
            ScopedClientProcessEventSuppression suppressProcessEventHooks;
            UWorldSubsystem* subsystem =
                USubsystemBlueprintLibrary::GetWorldSubsystem(
                    world, UPBFieldModManager::StaticClass());
            return subsystem && subsystem->IsA(UPBFieldModManager::StaticClass())
                ? static_cast<UPBFieldModManager*>(subsystem)
                : nullptr;
        }
        catch (...)
        {
            return nullptr;
        }
    }

    APBPlayerState* ResolveLocalPlayerState(UWorld* world)
    {
        if (!world || !world->OwningGameInstance ||
            world->OwningGameInstance->LocalPlayers.Num() <= 0)
        {
            return nullptr;
        }
        ULocalPlayer* localPlayer = world->OwningGameInstance->LocalPlayers[0];
        if (!localPlayer || !localPlayer->PlayerController ||
            !localPlayer->PlayerController->IsA(APBPlayerController::StaticClass()))
        {
            return nullptr;
        }
        auto* controller = static_cast<APBPlayerController*>(
            localPlayer->PlayerController);
        return controller->PBPlayerState;
    }

    void StartFetchIfNeeded()
    {
        const Clock::time_point now = Clock::now();
        std::uint64_t generation = 0;
        std::string baseUrl;

        {
            std::lock_guard lock(gFetchMutex);
            if (gFetch.Status == FetchStatus::Loading ||
                gFetch.Status == FetchStatus::Ready ||
                gFetch.Status == FetchStatus::Disabled ||
                (gFetch.Status == FetchStatus::Failed && now < gFetch.RetryAt))
            {
                return;
            }

            baseUrl = GetCmdValue("-LogicServerURL=");
            if (baseUrl.empty())
            {
                gFetch.Status = FetchStatus::Disabled;
                gFetch.Detail = "-LogicServerURL is missing";
                ++gFetch.ResultSerial;
                return;
            }

            generation = gFetch.Generation;
            gFetch.Status = FetchStatus::Loading;
            gFetch.Detail.clear();
        }

        std::thread([generation, baseUrl = std::move(baseUrl)]()
        {
            LoadoutMetaserver::MetaserverClient client(baseUrl);
            LoadoutMetaserver::PlayerLoadoutsResult result =
                client.GetCurrentUserLoadouts();

            std::lock_guard lock(gFetchMutex);
            if (gFetch.Generation != generation) return;

            ++gFetch.ResultSerial;
            if (result.Succeeded())
            {
                gFetch.Snapshot = result.Value->ToNormalizedSnapshot();
                gFetch.PlayerId = result.Value->PlayerId;
                gFetch.Detail = "roles=" +
                    std::to_string(result.Value->Loadouts.size());
                gFetch.Status = FetchStatus::Ready;
                return;
            }

            gFetch.Snapshot = json();
            gFetch.PlayerId.clear();
            gFetch.Detail = result.Http.ErrorMessage.empty()
                ? "request failed"
                : result.Http.ErrorMessage;
            if (result.Http.StatusCode > 0)
                gFetch.Detail += ", http=" +
                    std::to_string(result.Http.StatusCode);
            gFetch.Status = FetchStatus::Failed;
            gFetch.RetryAt = Clock::now() + RetryDelay;
        }).detach();
    }

    void PublishFetchResult()
    {
        FetchStatus status = FetchStatus::Idle;
        std::string playerId;
        std::string detail;
        {
            std::lock_guard lock(gFetchMutex);
            if (gFetch.LoggedSerial == gFetch.ResultSerial) return;
            gFetch.LoggedSerial = gFetch.ResultSerial;
            status = gFetch.Status;
            playerId = gFetch.PlayerId;
            detail = gFetch.Detail;
        }

        if (status == FetchStatus::Ready)
        {
            ClientLog("[LOADOUT] MetaTunnel baseline ready: player=" +
                playerId + " " + detail);
        }
        else if (status == FetchStatus::Disabled)
        {
            ClientLog("[LOADOUT] Client baseline disabled: " + detail);
        }
        else if (status == FetchStatus::Failed)
        {
            ClientLog("[LOADOUT] Client baseline fetch failed; retrying: " + detail);
        }
    }

    std::optional<json> SnapshotCopy()
    {
        std::lock_guard lock(gFetchMutex);
        if (gFetch.Status != FetchStatus::Ready ||
            !gFetch.Snapshot.is_object())
        {
            return std::nullopt;
        }
        return gFetch.Snapshot;
    }

    void ApplySnapshot(const json& snapshot, const char* reason)
    {
        UWorld* world = UWorld::GetWorld();
        if (!world) return;

        UPBFieldModManager* manager = ResolveFieldModManager(world);
        if (!manager || !snapshot.contains("roles") ||
            !snapshot["roles"].is_array())
        {
            return;
        }

        APBPlayerState* playerState = ResolveLocalPlayerState(world);
        int desiredRoles = 0;
        int nativeApplied = 0;
        int directApplied = 0;
        int alreadyCurrent = 0;
        int missingRoles = 0;
        int invalidRoles = 0;

        for (const auto& role : snapshot["roles"])
        {
            if (!role.is_object())
            {
                ++invalidRoles;
                continue;
            }
            const std::string roleId = role.value("roleId", "");
            FPBInventoryNetworkConfig desired{};
            std::string detail;
            if (!LoadoutApplication::TryBuildRoleInventory(
                snapshot, roleId, desired, detail))
            {
                ++invalidRoles;
                continue;
            }
            ++desiredRoles;

            const FName roleName = LoadoutSerializer::NameFromString(roleId);
            FPBInventoryNetworkConfig* current =
                FindRoleInventory(manager, roleName);
            if (current && SameInventory(*current, desired))
            {
                ++alreadyCurrent;
                continue;
            }

            // This is the same native client RPC the authoritative server uses
            // after role archive initialization. On a local client it updates
            // both equipping and pre-ordering FieldMod state and broadcasts the
            // original game notifications.
            if (playerState)
            {
                try
                {
                    ScopedClientProcessEventSuppression suppressProcessEventHooks;
                    playerState->ClientRefreshRoleEquippingInventory(
                        roleName, desired);
                    playerState->ClientRefreshRolePreOrderingInventory(
                        roleName, desired);
                    ++nativeApplied;
                }
                catch (...) {}
            }

            current = FindRoleInventory(manager, roleName);
            if (current && !SameInventory(*current, desired))
            {
                // Armory/showroom worlds may not own an APBPlayerState. The
                // map entry is nevertheless the same native cache consumed by
                // SelectCharacter, so replace only its existing value.
                *current = desired;
                ++directApplied;
            }

            current = FindRoleInventory(manager, roleName);
            if (!current || !SameInventory(*current, desired)) ++missingRoles;
        }

        const bool worldChanged = world != gLastApplyWorld ||
            manager != gLastApplyManager;
        if (nativeApplied > 0 || directApplied > 0 ||
            invalidRoles > 0 || worldChanged ||
            missingRoles != gLastMissingRoles)
        {
            ClientLog("[LOADOUT] Native client baseline synchronized: reason=" +
                std::string(reason ? reason : "pump") +
                " desired=" + std::to_string(desiredRoles) +
                " current=" + std::to_string(alreadyCurrent) +
                " native=" + std::to_string(nativeApplied) +
                " direct=" + std::to_string(directApplied) +
                " missing=" + std::to_string(missingRoles) +
                " invalid=" + std::to_string(invalidRoles));
        }

        gLastApplyWorld = world;
        gLastApplyManager = manager;
        gLastMissingRoles = missingRoles;
    }

    void PumpOrPrepare(bool force, const char* reason)
    {
        StartFetchIfNeeded();
        PublishFetchResult();

        const Clock::time_point now = Clock::now();
        if (!force && now < gNextApplyAt) return;
        gNextApplyAt = now + ApplyInterval;

        std::optional<json> snapshot = SnapshotCopy();
        if (snapshot.has_value()) ApplySnapshot(*snapshot, reason);
    }
}

void ResetClientLoadoutSync()
{
    {
        std::lock_guard lock(gFetchMutex);
        ++gFetch.Generation;
        gFetch.Status = FetchStatus::Idle;
        gFetch.Snapshot = json();
        gFetch.PlayerId.clear();
        gFetch.Detail.clear();
        gFetch.RetryAt = {};
    }
    gNextApplyAt = {};
    gLastApplyWorld = nullptr;
    gLastApplyManager = nullptr;
    gLastMissingRoles = -1;
}

void PumpClientLoadoutSync()
{
    PumpOrPrepare(false, "pump");
}

void PrepareClientLoadoutConsumer(const char* reason)
{
    PumpOrPrepare(true, reason ? reason : "consumer");
}
