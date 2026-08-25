// ServerLogic.cpp
#include "ServerLogic.h"
#include "../Config/Config.h"
#include "../Debug/Debug.h"
#include "../Network/NetDriverAccess.h"
#include "../ServerLogic/LateJoinManager.h"
#include "../ServerLogic/DedicatedMultiMatch.h"
#include "../ServerLogic/DedicatedMultiMatchPolicy.h"
#include "../Replication/libreplicate.h"
#include "../Loadout/LoadoutManager.h"
#include "../BattleLog/BattleLogExtractor.h"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include <Windows.h>
#include <iostream>
#include <thread>
#include <chrono>
#include <mutex>

using namespace SDK;

extern LibReplicate *libReplicate;
extern uintptr_t BaseAddress;
extern LoadoutManager* gLoadoutManager;
extern std::recursive_mutex gLoadoutManagerMutex;

// ======================================================
//  SECTION 6 — REPLICATION SYSTEM GLOBALS (moved to ServerLogic)
// ======================================================

std::vector<APlayerController *> playerControllersPossessed = std::vector<APlayerController *>();

int NumPlayersJoined = 0;
float PlayerJoinTimerSelectFuck = -1.0f;
bool DidProcFlow = false;
bool DidBroadcastRoleSelection = false;
float StartMatchTimer = -1.0f;
int NumPlayersSelectedRole = 0;
bool DidProcStartMatch = false;
bool canStartMatch = false;
int NumExpectedPlayers = -1;
float MatchStartCountdown = -1.0f;

std::unordered_map<APBPlayerController *, bool> PlayerRespawnAllowedMap{};
std::unordered_set<APBPlayerController *> PlayersConfirmedRole{};
std::unordered_set<APBPlayerController *> ConnectedPlayerControllers{};
std::unordered_set<APBPlayerController *> DisconnectedPlayerControllers{};
std::unordered_set<APBPlayerController *> PendingNameUpdatePlayers{};
std::unordered_set<APBPlayerController *> AppliedNameUpdatePlayers{};
float PendingNameApplyAccumulator = 0.0f;
float ReplicationFlushAccumulator = 0.0f;

// LateJoinManager instance (constructed later in MainThread after dependencies are ready)
LateJoinManager *gLateJoinManager = nullptr;

bool listening = false;
static UWorld *ObservedServerWorld = nullptr;
static std::uint64_t ServerMatchGeneration = 0;

// ======================================================
//  Helpers used by TickFlushHook and LateJoinManager
// ======================================================

APBGameState *GetPBGameState()
{
    UWorld *World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode || !World->AuthorityGameMode->GameState)
        return nullptr;

    return (APBGameState *)World->AuthorityGameMode->GameState;
}

APBGameMode *GetPBGameMode()
{
    UWorld *World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode)
        return nullptr;

    return (APBGameMode *)World->AuthorityGameMode;
}

bool IsRoundCurrentlyInProgress()
{
    APBGameState *GameState = GetPBGameState();
    return GameState && GameState->IsRoundInProgress();
}

// Get PlayerCount helper
int GetCurrentPlayerCount()
{
    UWorld *World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode)
        return -1;

    APBGameState *GS = (APBGameState *)World->AuthorityGameMode->GameState;
    if (!GS)
        return -1;

    return GS->PlayerArray.Num();
}

void ResetServerMatchStateForWorld(UWorld *world)
{
    ObservedServerWorld = world;
    ++ServerMatchGeneration;
    playerControllersPossessed.clear();
    ConnectedPlayerControllers.clear();
    DisconnectedPlayerControllers.clear();
    NumPlayersJoined = 0;
    PlayerJoinTimerSelectFuck = -1.0f;
    DidProcFlow = false;
    DidBroadcastRoleSelection = false;
    StartMatchTimer = -1.0f;
    NumPlayersSelectedRole = 0;
    DidProcStartMatch = false;
    canStartMatch = false;
    NumExpectedPlayers = -1;
    MatchStartCountdown = -1.0f;
    ReplicationFlushAccumulator = 0.0f;
    PendingNameApplyAccumulator = 0.0f;
    PlayerRespawnAllowedMap.clear();
    PlayersConfirmedRole.clear();
    PendingNameUpdatePlayers.clear();
    AppliedNameUpdatePlayers.clear();

    if (gLateJoinManager)
        gLateJoinManager->ResetForWorldChange();

    if (libReplicate)
        libReplicate->ResetForWorldChange();

    NetDriverAccess::ResetForWorldChange(world);

    {
        std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
        if (gLoadoutManager)
            gLoadoutManager->ResetForMatchGeneration(world);
    }

    BattleLog::ResetForMatchGeneration(world);

    Log("[SERVER] Reset match-scoped state for generation " +
        std::to_string(ServerMatchGeneration) + ".");
}

void EnsureServerMatchWorld(UWorld *world)
{
    if (DedicatedMultiMatch::OwnsWorldTransition())
        return;
    if (world != ObservedServerWorld && world && world->AuthorityGameMode &&
        world->GameState && world->NetDriver)
    {
        ResetServerMatchStateForWorld(world);
    }
}

bool BeginServerMatchGeneration(UWorld* world, UNetDriver* netDriver)
{
    if (!world || !netDriver || world->NetDriver != netDriver ||
        netDriver->World != world || !world->AuthorityGameMode || !world->GameState)
    {
        Log("[MULTIMATCH] Refused generation reset: world/NetDriver is not ready.");
        return false;
    }

    ResetServerMatchStateForWorld(world);
    NetDriverAccess::Observe(netDriver, world, NetDriverAccess::Source::World);

    AGameMode* gameMode = world->AuthorityGameMode->IsA(APBGameMode::StaticClass())
        ? static_cast<AGameMode*>(world->AuthorityGameMode)
        : nullptr;
    int reboundHumanConnections = 0;
    for (UNetConnection* connection : netDriver->ClientConnections)
    {
        if (!connection || !connection->PlayerController ||
            !connection->PlayerController->IsA(APBPlayerController::StaticClass()))
        {
            continue;
        }

        auto* const playerController =
            static_cast<APBPlayerController*>(connection->PlayerController);
        if (playerController->bActorIsBeingDestroyed)
            continue;

        ++reboundHumanConnections;

        DisconnectedPlayerControllers.erase(playerController);
        ConnectedPlayerControllers.insert(playerController);
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager)
                gLoadoutManager->OnPlayerConnected(playerController);
        }
        const bool hasPlayablePawn = playerController->Pawn &&
            !playerController->Pawn->bActorIsBeingDestroyed &&
            playerController->Pawn->IsA(APBCharacter::StaticClass());
        bool hasAuthoritativeRole = false;
        std::string authoritativeRole;
        try
        {
            if (playerController->PBPlayerState)
            {
                authoritativeRole =
                    playerController->PBPlayerState->SelectedCharacterID.ToString();
                if (authoritativeRole.empty() || authoritativeRole == "None")
                    authoritativeRole =
                        playerController->PBPlayerState->PossessedCharacterId.ToString();
                hasAuthoritativeRole = !authoritativeRole.empty() &&
                    authoritativeRole != "None";
            }
        }
        catch (...)
        {
            hasAuthoritativeRole = false;
            authoritativeRole.clear();
        }
        const bool preserved = gLateJoinManager && gameMode &&
            DedicatedMultiMatchPolicy::ShouldPreserveSeamlessReboundPlayer(
                DedicatedMultiMatch::OwnsWorldTransition(),
                true,
                !playerController->bActorIsBeingDestroyed,
                hasPlayablePawn,
                hasAuthoritativeRole) &&
            gLateJoinManager->RegisterSeamlessReboundPlayer(playerController);
        if (preserved)
        {
            // The role was already authoritatively accepted in the source
            // generation. Seed the new match gate without replaying the role
            // confirmation RPC or any first-join client UI.
            {
                std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                if (gLoadoutManager)
                {
                    gLoadoutManager->RebindSeamlessRoleForMatchGeneration(
                        playerController, authoritativeRole);
                }
            }
            PlayersConfirmedRole.insert(playerController);
        }
        else if (gLateJoinManager && gameMode)
        {
            if (DedicatedMultiMatch::OwnsWorldTransition() && !hasPlayablePawn)
            {
                std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
                if (gLoadoutManager)
                {
                    gLoadoutManager->
                        PrepareFreshSeamlessRoleSelectionForMatchGeneration(
                            playerController);
                }
            }
            gLateJoinManager->QueueInitialJoinPlayer(
                gameMode,
                playerController,
                DedicatedMultiMatch::OwnsWorldTransition() &&
                    !hasPlayablePawn);
        }
    }

    NumPlayersJoined = static_cast<int>(ConnectedPlayerControllers.size());
    NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
    if (gameMode && DedicatedMultiMatchPolicy::
            ShouldRepairSeamlessGameModePlayerCounts(
                DedicatedMultiMatch::OwnsWorldTransition(),
                reboundHumanConnections,
                gameMode->NumPlayers,
                gameMode->NumTravellingPlayers))
    {
        Log("[MULTIMATCH] Repaired destination native player counts: players=" +
            std::to_string(gameMode->NumPlayers) + "->" +
            std::to_string(reboundHumanConnections) + " travelling=" +
            std::to_string(gameMode->NumTravellingPlayers) + "->0.");
        gameMode->NumPlayers = reboundHumanConnections;
        gameMode->NumTravellingPlayers = 0;
    }
    Log("[MULTIMATCH] Rebound " + std::to_string(NumPlayersJoined) +
        " seamless connection(s) to generation " +
        std::to_string(ServerMatchGeneration) + ".");
    return true;
}

std::uint64_t GetServerMatchGeneration()
{
    return ServerMatchGeneration;
}

void BeginGracefulDedicatedExit(APBGameMode* gameMode, const char* reason)
{
    if (!gameMode)
    {
        using RequestExitFn = void(__fastcall*)(bool);
        const auto requestExit = reinterpret_cast<RequestExitFn>(BaseAddress + 0x19EFEE0);
        Log("[MATCH] Requesting process exit without GameMode after travel failure: " +
            std::string(reason ? reason : "unknown"));
        requestExit(false);
        return;
    }

    using NotifyAllClientsReturnToMainMenuFn = __int64(__fastcall*)(APBGameMode*);
    const auto notifyAllClientsReturnToMainMenu =
        reinterpret_cast<NotifyAllClientsReturnToMainMenuFn>(BaseAddress + 0x1633990);
    notifyAllClientsReturnToMainMenu(gameMode);

    constexpr uintptr_t WaitingToCleanUpOffset = 0x404;
    constexpr uintptr_t FinalCleanupStartedOffset = 0x4C4;
    using BeginFinalCleanupFn = void(__fastcall*)(APBGameMode*, float);
    const auto beginFinalCleanup =
        reinterpret_cast<BeginFinalCleanupFn>(BaseAddress + 0x163EFD0);
    const float cleanupWait = *reinterpret_cast<float*>(
        reinterpret_cast<uintptr_t>(gameMode) + WaitingToCleanUpOffset);
    *reinterpret_cast<uint8_t*>(
        reinterpret_cast<uintptr_t>(gameMode) + FinalCleanupStartedOffset) = 0;
    beginFinalCleanup(gameMode, cleanupWait);

    Log("[MATCH] Notified clients and continued native dedicated cleanup: " +
        std::string(reason ? reason : "process-per-match"));
}

void QueuePendingPlayerNameUpdate(APBPlayerController *PlayerController)
{
    if (!PlayerController)
        return;

    if (AppliedNameUpdatePlayers.find(PlayerController) == AppliedNameUpdatePlayers.end())
    {
        PendingNameUpdatePlayers.insert(PlayerController);
    }
}

void ApplyPendingPlayerNameUpdates(const char *reason)
{
    if (PendingNameUpdatePlayers.empty())
        return;

    UWorld *World = UWorld::GetWorld();
    if (!World || !World->AuthorityGameMode)
        return;

    AGameMode *GameMode = (AGameMode *)World->AuthorityGameMode;
    if (!GameMode)
        return;

    std::vector<APBPlayerController *> toApply;
    toApply.reserve(PendingNameUpdatePlayers.size());
    for (APBPlayerController *playerController : PendingNameUpdatePlayers)
    {
        if (playerController)
            toApply.push_back(playerController);
    }

    for (APBPlayerController *playerController : toApply)
    {
        if (!playerController || !playerController->PlayerState)
            continue;

        FString playerName = playerController->PlayerState->GetPlayerName();
        std::string nameStr = playerName.ToString();

        if (nameStr.empty() || nameStr == "UserName")
            continue;

        GameMode->ChangeName(playerController, playerName, true);

        if (playerController->PBPlayerState)
        {
            playerController->PBPlayerState->OnCustomPlayerNameChanged();
        }

        PendingNameUpdatePlayers.erase(playerController);
        AppliedNameUpdatePlayers.insert(playerController);

        std::cout << "[NAME] Applied delayed name update(" << reason << "): " << nameStr << std::endl;
    }
}

// ======================================================
//  SECTION 13 — SERVER STARTUP AND COMMAND RELATED LOGIC
// ======================================================

void StartServer()
{
    Log("[SERVER] Starting server...");

    LoadConfig();

    Log("[SERVER] Map loaded: " + std::string(Config.MapName.begin(), Config.MapName.end()));
    Log("[SERVER] Mode: " + std::string(Config.FullModePath.begin(), Config.FullModePath.end()));
    Log("[SERVER] Port: " + std::to_string(Config.Port));

    std::wstring openCmd = L"open " + Config.MapName + L"?game=" + Config.FullModePath;
    Log("[SERVER] Executing open command");

    UKismetSystemLibrary::ExecuteConsoleCommand(UWorld::GetWorld(), openCmd.c_str(), nullptr);

    Log("[SERVER] Waiting for world to load...");
    Sleep(8000);

    UEngine *Engine = UEngine::GetEngine();
    UWorld *World = UWorld::GetWorld();

    if (!World)
    {
        Log("[ERROR] World is NULL after map load!");
        return;
    }

    Log("[SERVER] Forcing streaming levels to load...");

    for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; i--)
    {
        SDK::UObject *Obj = SDK::UObject::GObjects->GetByIndex(i);

        if (!Obj)
            continue;

        if (Obj->IsDefaultObject())
            continue;

        if (Obj->IsA(ULevelStreaming::StaticClass()))
        {
            ULevelStreaming *LS = (ULevelStreaming *)Obj;

            LS->SetShouldBeLoaded(true);
            LS->SetShouldBeVisible(true);

            Log("[SERVER] Streaming level loaded: " + std::string(Obj->GetFullName()));
        }
    }

    if (!libReplicate)
    {
        Log("[ERROR] libReplicate is null before CreateNetDriver!");
        return;
    }

    Log("[SERVER] Creating NetDriver...");
    FName name = UKismetStringLibrary::Conv_StringToName(L"GameNetDriver");
    const std::vector<UNetDriver*> existingNetDrivers =
        NetDriverAccess::SnapshotNetDrivers();
    libReplicate->CreateNetDriver(Engine, World, &name);

    UIpNetDriver *NetDriver = reinterpret_cast<UIpNetDriver *>(
        NetDriverAccess::ResolveNamedUnboundForBootstrap(
            World, name, existingNetDrivers));

    if (!NetDriver)
    {
        Log("[ERROR] NetDriver not found after CreateNetDriver!");
        return;
    }

    NetDriverAccess::Observe(NetDriver, World, NetDriverAccess::Source::ObjectScan);
    Log("[SERVER] NetDriver created successfully.");

    // Establish the map generation before accepting PostLogin. This also
    // clears state left by a previous world in a long-lived injected process.
    ResetServerMatchStateForWorld(World);

    Log("[SERVER] Calling Listen()...");
    libReplicate->Listen(NetDriver, World, LibReplicate::EJoinMode::Open, Config.Port);
    NetDriverAccess::Observe(NetDriver, World, NetDriverAccess::Source::World);

    NetDriverAccess::Snapshot snapshot{};
    if (NetDriverAccess::TryGetSnapshot(snapshot, false))
    {
        Log("[SERVER] NetDriver exposed via source: " + std::string(NetDriverAccess::ToString(snapshot.LastSource)));
    }

    listening = true;

    Log("[SERVER] Server is now listening.");
}
