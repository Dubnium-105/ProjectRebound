#pragma once

// Server-authoritative bridge between the community room metaserver loadout
// and the in-match FieldMod/spawn flow. Client inventory/UI entry points stay
// intentionally inert: the native QueryAssets/GetPlayerArchiveV2 flow owns
// client archive state.

#include <memory>
#include <string>

#include "../SDK.hpp"
#include "LoadoutStatePolicy.h"

struct LoadoutBridgeOptions
{
    bool BaselineOverride = true;
    bool PreOrderIntercept = true;
    bool ConfirmDeferral = true;
    bool SpawnApplication = true;
};

class LoadoutManager
{
public:
    LoadoutManager();
    ~LoadoutManager();

    LoadoutManager(const LoadoutManager&) = delete;
    LoadoutManager& operator=(const LoadoutManager&) = delete;
    LoadoutManager(LoadoutManager&&) noexcept;
    LoadoutManager& operator=(LoadoutManager&&) noexcept;

    // Starts the server-only bridge. The URL must point at a loopback
    // MetaTunnel and roomId must be the room represented by this listen host.
    bool StartServer(
        std::string baseUrl,
        std::string roomId,
        LoadoutBridgeOptions options = {});

    // Starts the dedicated, loopback-only PVE bridge for the user currently
    // authenticated by MetaTunnel. The caller must gate this behind exact
    // -server, -pve and -LocalPveLoadout command-line switches.
    bool StartLocalPveServer(
        std::string baseUrl,
        LoadoutBridgeOptions options = {});
    void StopServer();

    void OnPlayerConnected(SDK::APBPlayerController* playerController);
    // Rebinds the authoritative source-match role into the fresh match
    // generation without replaying ServerConfirmRoleSelection or client UI.
    // Spawn is held only until the fresh baseline fetch settles or the normal
    // bounded fallback deadline expires.
    void RebindSeamlessRoleForMatchGeneration(
        SDK::APBPlayerController* playerController,
        const std::string& roleId);
    // A seamless destination can retain PlayerController/PlayerState while
    // intentionally discarding the old Pawn and asking the player to select a
    // role again. Source-world cleanup has already destructed the retained
    // PlayerState FieldMod containers, so rebuild them before allowing either
    // native pre-order or role-confirm RPCs to consume them.
    void PrepareFreshSeamlessRoleSelectionForMatchGeneration(
        SDK::APBPlayerController* playerController);
    void OnPlayerDisconnected(SDK::APBPlayerController* playerController);
    void OnActorDestroyed(SDK::AActor* actor);
    // A seamless travel may recycle the same UWorld address. Advance the
    // server epoch explicitly so pointer-keyed requests from the previous
    // match cannot be accepted in the next generation.
    void ResetForMatchGeneration(SDK::UWorld* currentWorld);

    // A Deferred decision means the hook must not invoke the original RPC.
    // TickServer replays it when the fetch finishes or the one-second grace
    // expires. A Ready decision first verifies the effective inventory through
    // the original ServerPreOrderInventory path.
    LoadoutRoleConfirmDecision BeginRoleConfirmation(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId);
    void CommitRoleConfirmationAfterOriginal(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId);

    // Called after the original ServerPreOrderInventory returns. The request is
    // recorded only if the per-player native pre-ordering state matches.
    bool OnExternalPreOrderInventory(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId,
        const SDK::FPBInventoryNetworkConfig& inventory);
    bool ShouldHoldExternalPreOrderForSeamlessSeed(
        SDK::APBPlayerController* playerController);

    // Compatibility no-op retained for older hook callers. Native per-player
    // state no longer requires a shared role lease and this always returns false.
    bool DeferExternalPreOrderInventoryIfLeaseConflict(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId,
        const SDK::FPBInventoryNetworkConfig& inventory);
    bool IsInternalPreOrderInProgress() const;
    // True only while CanReleaseRoleSpawn is synchronously replaying the
    // preserved role through the native ServerConfirmRoleSelection path.
    bool IsInternalSeamlessRoleReconfirmInProgress(
        SDK::APBPlayerController* playerController) const;
    // The native reliable-RPC validator may be bypassed only for the exact
    // guarded destination-role recovery after canonical pre-order validation.
    bool ShouldBypassSeamlessRoleValidator(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId) const;

    // Called after PBCharacter.K2_InventorySpawned.
    bool IsCharacterTombstoned(SDK::APBCharacter* character) const;
    void OnInventorySpawned(SDK::APBCharacter* character);

    // Compatibility hooks retained for LateJoinManager. Only a seamless
    // rebound role is gated while its fresh-generation baseline is settling;
    // ordinary spawn dispatches remain un-gated and notifications are no-ops.
    bool CanReleaseRoleSpawn(SDK::APBPlayerController* playerController);

    // Brackets the concrete synchronous RestartPlayers/QuickRespawn dispatch.
    // The request generation and pre-dispatch Pawn prevent an old same-role
    // InventorySpawned event from releasing a newer role-cache lease.
    void BeginSpawnDispatch(SDK::APBPlayerController* playerController);
    void CompleteSpawnDispatch(SDK::APBPlayerController* playerController);
    void FinalizeSpawnRequest(SDK::APBPlayerController* playerController);
    void AbandonSpawnRequest(SDK::APBPlayerController* playerController);
    void TickServer(float deltaSeconds = 0.0f);

private:
    class Impl;
    std::unique_ptr<Impl> impl_;
};
