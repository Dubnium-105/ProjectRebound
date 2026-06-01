#pragma once

#include <vector>
#include <unordered_map>
#include "../SDK.hpp"
#include "../Server/Replication.h"

// ======================================================
//  ServerLogicHooks — tick-driven server hooks:
//  TickFlushHook, PostLogin, replication batching,
//  round state machine, role selection, late-join driver.
// ======================================================

struct FTickReplicationBatch
{
    std::vector<LibReplicate::FActorInfo> ActorInfos;
    std::vector<LibReplicate::FPlayerControllerInfo> PlayerControllerInfos;
    std::vector<void*> Connections;
    std::unordered_map<void*, void*> ConnectionByPlayerController;

    void Reset(int connectionCount)
    {
        ActorInfos.clear();
        PlayerControllerInfos.clear();
        Connections.clear();
        ConnectionByPlayerController.clear();

        const size_t connectionCapacity = connectionCount > 0 ? static_cast<size_t>(connectionCount) : 0;
        Connections.reserve(connectionCapacity);
        PlayerControllerInfos.reserve(connectionCapacity);
        ConnectionByPlayerController.reserve(connectionCapacity);
    }
};

SDK::FName* GetActorChannelName();
void SelectRoleForQueuedPlayers();
void CollectTickReplicationBatch(SDK::UNetDriver* NetDriver, SDK::UWorld* World, FTickReplicationBatch& Batch);
void ForceServerSuicideForAllPlayers();

// Detour entry points (called from HookCore::InitServerHooks)
void TickFlushHook(SDK::UNetDriver* NetDriver, float DeltaTime);
void* PostLogin(SDK::AGameMode* GameMode, SDK::APBPlayerController* PC);
