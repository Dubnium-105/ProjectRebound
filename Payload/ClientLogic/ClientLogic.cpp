#include "ClientLogic.h"

#include "../Communication/CommandProtocol.h"
#include "../Config/Config.h"
#include "../Debug/Debug.h"
#include "../Loadout/LoadoutApplication.h"
#include "../Loadout/LoadoutSerializer.h"
#include "../Loadout/MetaserverClient.h"
#include "../SDK.hpp"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"

#include <Windows.h>

#include <atomic>
#include <chrono>
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
        WaitingAfterLogin,
        WaitingAfterRange
    };

    std::mutex connectMutex;
    std::optional<std::string> pendingTarget;
    std::string currentTarget;
    ConnectStage connectStage = ConnectStage::Idle;
    std::chrono::steady_clock::time_point nextActionAt{};
    std::atomic<bool> loginCompleted{false};
    std::atomic<DWORD> gameThreadId{0};

    constexpr auto LoginSettleDelay = std::chrono::seconds(2);
    constexpr auto RangeSettleDelay = std::chrono::seconds(1);

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
            std::string(GetCommandLineA()).find("-NativeArchiveOnly") != std::string::npos;
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
            if (nativeLoadout.LocalPlayer != localPlayer)
            {
                ++nativeLoadout.Generation;
                nativeLoadout.Status = NativeLoadoutStatus::Idle;
                nativeLoadout.LocalPlayer = localPlayer;
                nativeLoadout.Snapshot = json();
                nativeLoadout.Detail.clear();
                nativeLoadout.ResultLogged = false;
                nativeLoadout.AppliedCustomizeManagers.clear();
                nativeLoadout.AppliedPlayerStates.clear();
                nativeLoadout.NextCustomizeApplyAt = {};
                nativeLoadout.NextPlayerStateApplyAt = {};
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
        int& outWeaponCount,
        int& outWeaponPartCount,
        std::uint32_t& outWeaponHash,
        std::string& outDetail)
    {
        using CompleteCharacterSlotFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, EPBCharacterSlotType);
        using CompleteWeaponSlotFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName, EPBPartSlotType);
        using CompleteWeaponSuiteFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName, FName);
        using CompleteWeaponPartSkinPaintingFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName, FName, FName);
        using CompleteWeaponOrnamentFn = void(__fastcall*)(
            UPBCustomizeManager*, int32, FName, FName, FName);
        constexpr uintptr_t CompleteCharacterSlotRva = 0x16DD080;
        constexpr uintptr_t CompleteWeaponSlotRva = 0x16DD5F0;
        constexpr uintptr_t CompleteWeaponSuiteRva = 0x16DD740;
        constexpr uintptr_t CompleteWeaponPartSkinPaintingRva = 0x16DD490;
        constexpr uintptr_t CompleteWeaponOrnamentRva = 0x16DD1D0;

        std::vector<std::string> roleIds;
        if (!manager || !TryGetSnapshotRoleIds(snapshot, roleIds, outDetail))
            return false;

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
        }

        auto* const completeCharacterSlot = reinterpret_cast<CompleteCharacterSlotFn>(
            BaseAddress + CompleteCharacterSlotRva);
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
        if (!completeCharacterSlot || !completeWeaponSlot || !completeWeaponSuite ||
            !completeWeaponPartSkinPainting || !completeWeaponOrnament)
        {
            outDetail = "native completion entry is unavailable";
            return false;
        }

        outSlotCount = 0;
        outSlotHash = 2166136261U;
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
            int weaponCount = 0;
            int weaponPartCount = 0;
            std::uint32_t weaponHash = 0;
            std::string detail;
            try
            {
                if (TryApplyCustomizeSnapshot(
                    customizeManager, snapshot, slotCount, slotHash,
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
    }
    ClientLog("[CLIENT] Match transition queued: " + target);
    return true;
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
    bool enterRange = false;
    std::optional<std::string> connectTarget;

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
            enterRange = true;
        }
        else if (connectStage == ConnectStage::WaitingAfterRange)
        {
            connectTarget = pendingTarget;
        }
    }

    bool actionSucceeded = false;
    try
    {
        if (enterRange)
        {
            ClientLog("[CLIENT] Entering Shooting Range before match transition...");
            localPlayer->GoToRange(0.0f);
            actionSucceeded = true;
        }
        else if (connectTarget.has_value())
        {
            const std::wstring command = L"open " +
                std::wstring(connectTarget->begin(), connectTarget->end());
            ClientLog("[CLIENT] Connecting to match: " + *connectTarget);
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
    if (enterRange && connectStage == ConnectStage::WaitingAfterLogin)
    {
        connectStage = ConnectStage::WaitingAfterRange;
        nextActionAt = std::chrono::steady_clock::now() + RangeSettleDelay;
    }
    else if (connectTarget.has_value() &&
        connectStage == ConnectStage::WaitingAfterRange &&
        pendingTarget == connectTarget)
    {
        currentTarget = *connectTarget;
        pendingTarget.reset();
        connectStage = ConnectStage::Idle;
    }
}
