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
    void StopServer();

    void OnPlayerConnected(SDK::APBPlayerController* playerController);
    void OnPlayerDisconnected(SDK::APBPlayerController* playerController);
    void OnActorDestroyed(SDK::AActor* actor);

    // A Deferred decision means the hook must not invoke the original RPC.
    // TickServer replays it when the fetch finishes or the one-second grace
    // expires. The replay re-enters this method and returns Ready/Fallback.
    LoadoutRoleConfirmDecision BeginRoleConfirmation(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId);
    void CommitRoleConfirmationAfterOriginal(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId);

    // Called by the ServerPreOrderInventory hook after the native function has
    // accepted the inventory. Manager-originated calls are ignored through an
    // internal re-entry guard.
    bool OnExternalPreOrderInventory(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId,
        const SDK::FPBInventoryNetworkConfig& inventory);

    // Called before the native external RPC. Returns true when another
    // connection owns the role's in-flight spawn lease; the manager copies
    // and replays the latest submission after that lease is released.
    bool DeferExternalPreOrderInventoryIfLeaseConflict(
        SDK::APBPlayerController* playerController,
        const SDK::FName& roleId,
        const SDK::FPBInventoryNetworkConfig& inventory);
    bool IsInternalPreOrderInProgress() const;

    // Called after PBCharacter.K2_InventorySpawned.
    bool IsCharacterTombstoned(SDK::APBCharacter* character) const;
    void OnInventorySpawned(SDK::APBCharacter* character);

    // LateJoin calls this immediately before creating a playable Pawn. A
    // pending FieldMod cache verification holds the spawn for at most the
    // same one-second grace used by role confirmation.
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
