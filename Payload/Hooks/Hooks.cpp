// Hooks.cpp
#include "Hooks.h"
#include <Windows.h>
#include <atomic>
#include <chrono>
#include <iostream>
#include <mutex>
#include <string>
#include <thread>
#include <vector>
#include "ArchiveCompletionPolicy.h"
#include "../SDK.hpp"
#include "../Network/NetDriverAccess.h"
#include "../SDK/Engine_parameters.hpp"
#include "../SDK/ProjectBoundary_parameters.hpp"
#include "../safetyhook/safetyhook.hpp"
#include "../Libs/json.hpp"
#include "../Replication/libreplicate.h"
#include "../ServerLogic/LateJoinManager.h"
#include "../Loadout/LoadoutManager.h"
#include "../Config/Config.h"
#include "../Debug/Debug.h"
#include "../Debug/DebugTool.h"
#include "../ServerLogic/ServerLogic.h"
#include "../ClientLogic/ClientLogic.h"
#include "../Utility/Utility.h"
#include "../BattleLog/BattleLogExtractor.h"

extern uintptr_t BaseAddress;
extern LibReplicate* libReplicate;
extern DebugTool* gDebugTool;
extern LoadoutManager* gLoadoutManager;
extern std::recursive_mutex gLoadoutManagerMutex;

using namespace SDK;

// Retained for generated ProcessEvent calls made by server-side loadout
// serialization/application helpers. The production client never constructs
// LoadoutManager; this guard does not maintain a client archive mirror.
static thread_local unsigned int gClientProcessEventSuppressionDepth = 0;

extern "C" void PayloadPushClientProcessEventSuppression()
{
    ++gClientProcessEventSuppressionDepth;
}

extern "C" void PayloadPopClientProcessEventSuppression()
{
    if (gClientProcessEventSuppressionDepth > 0)
        --gClientProcessEventSuppressionDepth;
}

// NumExpectedPlayers can be established or changed after one or more role
// confirmations have already arrived. Keep the start gate derived from the
// authoritative confirmation set whenever either side of the quorum changes.
// Before StartMatch this may also move true -> false when a new initial player
// joins and raises the expected count.
static void RecomputeMatchStartGate(const char* reason)
{
    NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
    if (DidProcStartMatch)
        return;

    const bool hasQuorum = NumExpectedPlayers > 0 &&
        NumPlayersSelectedRole >= NumExpectedPlayers;
    if (canStartMatch != hasQuorum)
    {
        std::cout << "[MATCH] Start gate " << (hasQuorum ? "ready" : "waiting")
                  << " after " << (reason ? reason : "state update")
                  << " (" << NumPlayersSelectedRole << "/" << NumExpectedPlayers << ")"
                  << std::endl;
    }
    canStartMatch = hasQuorum;
    if (hasQuorum)
        StartMatchTimer = -1.0f;
}

static void CleanupDisconnectedPlayer(APBPlayerController* playerController, const char* reason)
{
    if (!playerController)
        return;

    // Tombstone first: teardown can re-enter PostLogin/role hooks before the
    // native destroy/logout call has fully unwound.
    DisconnectedPlayerControllers.insert(playerController);

    {
        std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
        if (gLoadoutManager)
            gLoadoutManager->OnPlayerDisconnected(playerController);
    }
    if (gLateJoinManager)
        gLateJoinManager->OnPlayerDisconnected(playerController);

    PlayerRespawnAllowedMap.erase(playerController);
    PlayersConfirmedRole.erase(playerController);
    PendingNameUpdatePlayers.erase(playerController);
    AppliedNameUpdatePlayers.erase(playerController);
    ConnectedPlayerControllers.erase(playerController);
    NumPlayersJoined = static_cast<int>(ConnectedPlayerControllers.size());
    NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
    if (!DidProcStartMatch && NumExpectedPlayers > NumPlayersJoined)
        NumExpectedPlayers = NumPlayersJoined;
    RecomputeMatchStartGate(reason);
}

static bool IsCurrentConnectedController(APBPlayerController* playerController)
{
    return playerController &&
        ConnectedPlayerControllers.contains(playerController) &&
        !DisconnectedPlayerControllers.contains(playerController) &&
        !playerController->bActorIsBeingDestroyed;
}

static bool IsCurrentSelectedRole(
    APBPlayerController* playerController,
    const FName& roleId)
{
    if (!IsCurrentConnectedController(playerController) ||
        !playerController->PBPlayerState)
    {
        return false;
    }

    APBPlayerState* const expectedPlayerState = playerController->PBPlayerState;
    bool hasSelectedRole = false;
    try { hasSelectedRole = expectedPlayerState->HasSelectedRole(); }
    catch (...) { return false; }

    if (!IsCurrentConnectedController(playerController) ||
        playerController->PBPlayerState != expectedPlayerState ||
        !hasSelectedRole)
    {
        return false;
    }

    const std::string selected = expectedPlayerState->SelectedCharacterID.ToString();
    const std::string submitted = roleId.ToString();
    return !selected.empty() && selected != "None" &&
        !submitted.empty() && submitted != "None" && selected == submitted;
}

// ======================================================
//  SECTION 7 — HOOK DETOURS (ENGINE HOOKS)
// ======================================================

static SafetyHookInline TickFlush = {};

void TickFlushHook(UNetDriver *NetDriver, float DeltaTime)
{
    if (listening && NetDriver && UWorld::GetWorld())
    {
        EnsureServerMatchWorld(UWorld::GetWorld());
        NetDriverAccess::Observe(NetDriver, UWorld::GetWorld(), NetDriverAccess::Source::HookArgument);

        if (PlayerJoinTimerSelectFuck > 0.0f)
        {
            PlayerJoinTimerSelectFuck -= DeltaTime;

            if (PlayerJoinTimerSelectFuck <= 0.0f)
            {
                DidBroadcastRoleSelection = true;

                std::vector<APBPlayerController*> rolePromptControllers(
                    ConnectedPlayerControllers.begin(),
                    ConnectedPlayerControllers.end());
                for (APBPlayerController* playerController : rolePromptControllers)
                {
                    if (!playerController ||
                        !ConnectedPlayerControllers.contains(playerController) ||
                        DisconnectedPlayerControllers.contains(playerController) ||
                        playerController->bActorIsBeingDestroyed)
                    {
                        continue;
                    }

                    const bool canSelectRole = playerController->CanSelectRole();
                    if (!ConnectedPlayerControllers.contains(playerController) ||
                        DisconnectedPlayerControllers.contains(playerController) ||
                        playerController->bActorIsBeingDestroyed)
                    {
                        continue;
                    }
                    if (canSelectRole)
                    {
                        std::cout << "Selecting role..." << std::endl;
                        playerController->ClientSelectRole();
                        if (gLateJoinManager &&
                            ConnectedPlayerControllers.contains(playerController) &&
                            !DisconnectedPlayerControllers.contains(playerController))
                        {
                            gLateJoinManager->OnRoleSelectionPromptSent(playerController);
                        }
                    }
                    else
                    {
                        std::cout << "CANT SELECT ROLE WEE WOO WEE WOO" << std::endl;
                    }
                }
            }
        }

        std::vector<LibReplicate::FActorInfo> ActorInfos = std::vector<LibReplicate::FActorInfo>();
        std::vector<UNetConnection *> Connections = std::vector<UNetConnection *>();
        std::vector<void *> PlayerControllers = std::vector<void *>();

        for (UNetConnection *Connection : NetDriver->ClientConnections)
        {
            if (Connection->OwningActor)
            {
                Connection->ViewTarget = Connection->PlayerController ? Connection->PlayerController->GetViewTarget() : Connection->OwningActor;
                Connections.push_back(Connection);
            }
        }

        for (int i = 0; i < UWorld::GetWorld()->Levels.Num(); i++)
        {
            ULevel *Level = UWorld::GetWorld()->Levels[i];

            if (Level)
            {
                for (int j = 0; j < Level->Actors.Num(); j++)
                {
                    AActor *actor = Level->Actors[j];

                    if (!actor)
                        continue;

                    if (actor->RemoteRole == ENetRole::ROLE_None)
                        continue;

                    if (!actor->bReplicates)
                        continue;

                    if (actor->bActorIsBeingDestroyed)
                        continue;

                    if (actor->Class == APlayerController_BP_C::StaticClass())
                    {
                        PlayerControllers.push_back((void *)actor);
                        if (((APlayerController *)actor)->Character && ((APlayerController *)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass()))
                        {
                            ((UCharacterMovementComponent *)(((APlayerController *)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass())))->bIgnoreClientMovementErrorChecksAndCorrection = true;
                            ((UCharacterMovementComponent *)(((APlayerController *)actor)->Character->GetComponentByClass(UCharacterMovementComponent::StaticClass())))->bServerAcceptClientAuthoritativePosition = true;
                        }
                        continue;
                    }

                    ActorInfos.push_back(LibReplicate::FActorInfo(actor, actor->bNetTemporary));
                }
            }
        }

        std::vector<LibReplicate::FPlayerControllerInfo> PlayerControllerInfos = std::vector<LibReplicate::FPlayerControllerInfo>();

        for (void *PlayerController : PlayerControllers)
        {
            for (UNetConnection *Connection : Connections)
            {
                if (Connection->PlayerController == PlayerController)
                {
                    PlayerControllerInfos.push_back(LibReplicate::FPlayerControllerInfo(Connection, PlayerController));
                    break;
                }
            }
        }

        std::vector<void *> CastConnections = std::vector<void *>();

        for (UNetConnection *Connection : Connections)
        {
            CastConnections.push_back((void *)Connection);
        }

        static FName *ActorName = nullptr;

        if (!ActorName)
        {
            ActorName = new FName();
            ActorName->ComparisonIndex = UKismetStringLibrary::Conv_StringToName(L"Actor").ComparisonIndex;
            ActorName->Number = UKismetStringLibrary::Conv_StringToName(L"Actor").Number;
        }

        if (ActorInfos.size() > 0 && CastConnections.size() > 0)
        {
            if (NetDriver)
            {
                libReplicate->CallFromTickFlushHook(ActorInfos, PlayerControllerInfos, CastConnections, ActorName, NetDriver);

                int *counter = reinterpret_cast<int *>(reinterpret_cast<char *>(NetDriver) + 0x420);
                *counter = *counter + 1;
            }
        }

        // Consume completed HTTP work and replay any role confirmation whose
        // bounded loadout grace period has completed before LateJoin attempts
        // to create a Pawn this frame.
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager)
                gLoadoutManager->TickServer(DeltaTime);
        }

        // Drive LateJoin state machine
        if (gLateJoinManager)
            gLateJoinManager->Tick(DeltaTime);
    }

    APBGameState *CurrentGameState = GetPBGameState();
    if (CurrentGameState && !CurrentGameState->IsRoundInProgress())
    {
        if (CurrentGameState->RoundState.ToString().contains("InvalidState"))
        {

            if (NumPlayersJoined >= Config.MinPlayersToStart)
            {
                if (!DidProcFlow)
                {
                    if (MatchStartCountdown == -1.0f)
                    {
                        MatchStartCountdown = 30.0f;

                        NumExpectedPlayers = NumPlayersJoined;
                        RecomputeMatchStartGate("countdown initialized");
                    }
                    else
                    {
                        MatchStartCountdown -= DeltaTime;

                        if (NumExpectedPlayers > NumPlayersJoined)
                        {
                            NumExpectedPlayers = NumPlayersJoined;
                            RecomputeMatchStartGate("player count decreased");

                            MatchStartCountdown += 15.0f;
                        }

                        if (MatchStartCountdown <= 0.0f)
                        {
                            DidProcFlow = true;

                            std::cout << "All players connected, beginning role selection flow!" << std::endl;

                            PlayerJoinTimerSelectFuck = 5.0f;

                            NumExpectedPlayers = NumPlayersJoined;
                            RecomputeMatchStartGate("role selection opened");
                        }
                    }
                }
            }
        }

        if (CurrentGameState->RoundState.ToString().contains("CountdownToStart"))
        {

            for (UNetConnection *pc : NetDriver->ClientConnections)
            {
                if (pc->PlayerController && pc->PlayerController->Pawn)
                    pc->PlayerController->Possess(pc->PlayerController->Pawn);
            }
        }
    }

    if (canStartMatch && !DidProcStartMatch &&
        (!gLateJoinManager || gLateJoinManager->AreInitialPlayersReadyForStart()))
    {
        DidProcStartMatch = true;

        ((APBGameMode *)UWorld::GetWorld()->AuthorityGameMode)->StartMatch();
    }

    if (GetAsyncKeyState(VK_F8) && amServer)
    {
        for (int i = SDK::UObject::GObjects->Num() - 1; i >= 0; i--)
        {
            SDK::UObject *Obj = SDK::UObject::GObjects->GetByIndex(i);

            if (!Obj)
                continue;

            if (Obj->IsDefaultObject())
                continue;

            if (Obj->IsA(APBPlayerController::StaticClass()))
            {
                ((APBPlayerController *)Obj)->ServerSuicide(0);
            }
        }

        while (GetAsyncKeyState(VK_F8))
        {
        }
    }

    return TickFlush.call(NetDriver, DeltaTime);
}

// ======================================================
//  SECTION 8 — HOOK DETOURS (GAMEPLAY HOOKS)
// ======================================================

static SafetyHookInline NotifyActorDestroyed = {};

bool NotifyActorDestroyedHook(UWorld *World, AActor *Actor, bool SomeShit, bool SomeShit2)
{
    if (listening && Actor)
    {
        if (Actor->IsA(APBPlayerController::StaticClass()))
        {
            CleanupDisconnectedPlayer(
                static_cast<APBPlayerController*>(Actor), "controller destroyed");
        }
        else
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager)
                gLoadoutManager->OnActorDestroyed(Actor);
        }
    }

    bool ret = NotifyActorDestroyed.call<bool>(World, Actor, SomeShit, SomeShit2);

    if (listening && Actor && libReplicate)
    {
        LibReplicate::FActorInfo ActorInfo = LibReplicate::FActorInfo((void *)Actor, Actor->bNetTemporary);

        libReplicate->CallWhenActorDestroyed(ActorInfo);
    }

    return ret;
}

static SafetyHookInline NotifyAcceptingConnection = {};

__int64 NotifyAcceptingConnectionHook(UObject *obj)
{
    return 1;
}

static SafetyHookInline NotifyControlMessage = {};

char NotifyControlMessageHook(unsigned __int64 ScuffedShit, __int64 a2, uint8_t a3, __int64 a4)
{
    if (UWorld *World = UWorld::GetWorld())
    {
        if (UNetDriver *ActiveNetDriver = NetDriverAccess::Resolve())
        {
            NetDriverAccess::Observe(ActiveNetDriver, World, NetDriverAccess::Source::Cached);
        }
    }

    return NotifyControlMessage.call<char>(ScuffedShit, a2, a3, a4);
}

static SafetyHookInline ProcessEvent;

void ProcessEventHook(UObject *Object, UFunction *Function, void *Parms)
{
    const std::string functionName = Function ? std::string(Function->GetFullName()) : "";

    // 热键检测（游戏线程安全）— F6=dump, F7=reapply snapshot
    if (gDebugTool)
    {
        static auto nextHotkeyCheck = std::chrono::steady_clock::now();
        const auto now = std::chrono::steady_clock::now();
        if (now >= nextHotkeyCheck)
        {
            nextHotkeyCheck = now + std::chrono::milliseconds(500);
            try
            {
                if (GetAsyncKeyState(VK_F6) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F6);
                if (GetAsyncKeyState(VK_F7) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F7);
            }
            catch (...)
            {
                // 吞掉所有异常 — 调试工具不应导致游戏崩溃
            }
        }
    }

    // ServerSay：拦截调试命令（__DBG__ 前缀）
    if (functionName.contains("ServerSay"))
    {
        APBPlayerController *PBPlayerController = Object && Object->IsA(APBPlayerController::StaticClass())
                                                      ? (APBPlayerController *)Object
                                                      : nullptr;
        if (PBPlayerController)
        {
            auto *SayParms = static_cast<Params::PBPlayerController_ServerSay *>(Parms);
            if (SayParms)
            {
                const std::string msg = SayParms->Msg.ToString();

                // __DBG__ 前缀：运行时调试命令
                if (msg.rfind("__DBG__", 0) == 0)
                {
                    const std::string payload = msg.substr(7);
                    if (gDebugTool)
                        gDebugTool->ExecuteChat(payload);
                    // 抑制此聊天消息
                    return;
                }
            }
        }
    }

    // PBGameMode can restart a whole round in one batch. Split authoritative
    // player controllers out of that batch and queue them through the same
    // per-role lease/JIT seed path; AI and untracked controllers retain the
    // native call. A matching managed permit is installed only by
    // LateJoinManager immediately around its singleton replay.
    if (functionName.contains("PBGameMode.RestartPlayers"))
    {
        auto* restartParms = static_cast<Params::PBGameMode_RestartPlayers*>(Parms);
        if (restartParms && gLateJoinManager)
        {
            TArray<AController*> nativeControllers{};
            bool interceptedManagedPlayer = false;
            for (AController* controller : restartParms->InControllers)
            {
                APBPlayerController* playerController =
                    controller && controller->IsA(APBPlayerController::StaticClass())
                        ? static_cast<APBPlayerController*>(controller)
                        : nullptr;
                if (!playerController ||
                    !gLateJoinManager->IsManagedPlayer(playerController) ||
                    gLateJoinManager->HasManagedRestartPermit(playerController))
                {
                    nativeControllers.Add(controller);
                    continue;
                }

                interceptedManagedPlayer = true;
                const auto allowed = PlayerRespawnAllowedMap.find(playerController);
                if (allowed == PlayerRespawnAllowedMap.end() || allowed->second)
                    gLateJoinManager->QueueManagedRespawn(playerController);
            }

            if (interceptedManagedPlayer)
            {
                if (nativeControllers.Num() > 0)
                {
                    restartParms->InControllers = nativeControllers;
                    ProcessEvent.call(Object, Function, Parms);
                    BattleLog::OnProcessEventPost(
                        BattleLog::ProcessSide::Server, Object, functionName, Parms);
                }
                return;
            }
        }
    }

    // Backstop direct GameModeBase restart entry points that do not use the
    // PBGameMode batch wrapper. Their first parameter is always NewPlayer.
    if (functionName.contains("GameModeBase.RestartPlayer"))
    {
        AController* controller = nullptr;
        if (functionName.contains("RestartPlayerAtPlayerStart"))
        {
            auto* restartParms =
                static_cast<Params::GameModeBase_RestartPlayerAtPlayerStart*>(Parms);
            controller = restartParms ? restartParms->NewPlayer : nullptr;
        }
        else if (functionName.contains("RestartPlayerAtTransform"))
        {
            auto* restartParms =
                static_cast<Params::GameModeBase_RestartPlayerAtTransform*>(Parms);
            controller = restartParms ? restartParms->NewPlayer : nullptr;
        }
        else
        {
            auto* restartParms = static_cast<Params::GameModeBase_RestartPlayer*>(Parms);
            controller = restartParms ? restartParms->NewPlayer : nullptr;
        }

        APBPlayerController* playerController =
            controller && controller->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(controller)
                : nullptr;
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController) &&
            !gLateJoinManager->HasManagedRestartPermit(playerController))
        {
            const auto allowed = PlayerRespawnAllowedMap.find(playerController);
            if (allowed == PlayerRespawnAllowedMap.end() || allowed->second)
                gLateJoinManager->QueueManagedRespawn(playerController);
            return;
        }
    }

    // Last ProcessEvent-level backstop before a pawn is allocated. Native
    // engine code can bypass the public restart wrappers, but any reflected
    // SpawnDefaultPawn call for a tracked controller still requires the JIT
    // seed permit.
    if (functionName.contains("GameModeBase.SpawnDefaultPawn"))
    {
        AController* controller = nullptr;
        APawn** returnValue = nullptr;
        if (functionName.contains("SpawnDefaultPawnAtTransform"))
        {
            auto* spawnParms =
                static_cast<Params::GameModeBase_SpawnDefaultPawnAtTransform*>(Parms);
            if (spawnParms)
            {
                controller = spawnParms->NewPlayer;
                returnValue = &spawnParms->ReturnValue;
            }
        }
        else
        {
            auto* spawnParms = static_cast<Params::GameModeBase_SpawnDefaultPawnFor*>(Parms);
            if (spawnParms)
            {
                controller = spawnParms->NewPlayer;
                returnValue = &spawnParms->ReturnValue;
            }
        }

        APBPlayerController* playerController =
            controller && controller->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(controller)
                : nullptr;
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController) &&
            !gLateJoinManager->HasManagedRestartPermit(playerController))
        {
            const auto allowed = PlayerRespawnAllowedMap.find(playerController);
            if (allowed == PlayerRespawnAllowedMap.end() || allowed->second)
                gLateJoinManager->QueueManagedRespawn(playerController);
            if (returnValue) *returnValue = nullptr;
            return;
        }
    }

    if (functionName.contains("PBPlayerController.ServerQuickRespawn"))
    {
        APBPlayerController *PBPlayerController = (APBPlayerController *)Object;

        if (PBPlayerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(PBPlayerController) &&
            !gLateJoinManager->HasManagedRestartPermit(PBPlayerController))
        {
            const auto allowed = PlayerRespawnAllowedMap.find(PBPlayerController);
            if (allowed == PlayerRespawnAllowedMap.end() || allowed->second)
                gLateJoinManager->QueueManagedRespawn(PBPlayerController);
            return;
        }

        if (PlayerRespawnAllowedMap.contains(PBPlayerController) &&
            PlayerRespawnAllowedMap[PBPlayerController] == false)
        {
            std::cout << "Denied quick respawn until role/loadout confirmation!" << std::endl;
            return;
        }
    }

    if (functionName.contains("PlayerController.ServerRestartPlayer"))
    {
        APBPlayerController *PBPlayerController = (APBPlayerController *)Object;

        if (PBPlayerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(PBPlayerController) &&
            !gLateJoinManager->HasManagedRestartPermit(PBPlayerController))
        {
            const auto allowed = PlayerRespawnAllowedMap.find(PBPlayerController);
            if (allowed == PlayerRespawnAllowedMap.end() || allowed->second)
                gLateJoinManager->QueueManagedRespawn(PBPlayerController);
            return;
        }

        if (PlayerRespawnAllowedMap.contains(PBPlayerController) && PlayerRespawnAllowedMap[PBPlayerController] == false)
        {
            std::cout << "Denied restart!" << std::endl;
            return;
        }
    }

    // LateJoin: role-selection interception (CanPlayerSelectRole / CanSelectRole)
    if (gLateJoinManager && gLateJoinManager->OnProcessEvent(Object, functionName, Parms))
    {
        // Already handled by LateJoinManager
        return;
    }

    // Capture a real in-match FieldMod submission only after the native RPC
    // has accepted it. LoadoutManager uses an internal guard around its own
    // baseline writes, so those calls are ignored here.
    if (functionName.contains("PBPlayerController.ServerPreOrderInventory"))
    {
        APBPlayerController* playerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;
        auto* preOrderParms =
            static_cast<Params::PBPlayerController_ServerPreOrderInventory*>(Parms);

        bool internalManagerWrite = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            internalManagerWrite = gLoadoutManager &&
                gLoadoutManager->IsInternalPreOrderInProgress();
        }
        if (internalManagerWrite)
        {
            ProcessEvent.call(Object, Function, Parms);
            BattleLog::OnProcessEventPost(
                BattleLog::ProcessSide::Server, Object, functionName, Parms);
            return;
        }

        bool deferredForLease = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager && playerController && preOrderParms &&
                ConnectedPlayerControllers.contains(playerController) &&
                !DisconnectedPlayerControllers.contains(playerController) &&
                !playerController->bActorIsBeingDestroyed)
            {
                deferredForLease =
                    gLoadoutManager->DeferExternalPreOrderInventoryIfLeaseConflict(
                        playerController,
                        preOrderParms->InRoleID,
                        preOrderParms->InPreOrderingInventory);
            }
        }
        if (deferredForLease)
        {
            if (gLateJoinManager && playerController && preOrderParms &&
                IsCurrentSelectedRole(playerController, preOrderParms->InRoleID))
            {
                const auto allowed = PlayerRespawnAllowedMap.find(playerController);
                if (allowed != PlayerRespawnAllowedMap.end() && !allowed->second)
                    gLateJoinManager->QueueManagedRespawn(playerController);
            }
            return;
        }

        ProcessEvent.call(Object, Function, Parms);
        bool recordedRuntimeOverride = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager && playerController && preOrderParms &&
                ConnectedPlayerControllers.contains(playerController) &&
                !DisconnectedPlayerControllers.contains(playerController) &&
                !playerController->bActorIsBeingDestroyed)
            {
                recordedRuntimeOverride = gLoadoutManager->OnExternalPreOrderInventory(
                    playerController,
                    preOrderParms->InRoleID,
                    preOrderParms->InPreOrderingInventory);
            }
        }
        // The native RPC has returned. Even when the loadout bridge is not
        // running, the canonical p_ id is unavailable, or this controller is
        // not bound to LoadoutManager, an already-spawned player waiting after
        // death must be allowed into the serialized respawn path. The manager
        // API itself rejects PendingRoleSelection and every first-spawn state.
        if (gLateJoinManager && playerController && preOrderParms &&
            IsCurrentSelectedRole(playerController, preOrderParms->InRoleID))
        {
            const auto allowed = PlayerRespawnAllowedMap.find(playerController);
            if (allowed != PlayerRespawnAllowedMap.end() && !allowed->second)
            {
                const bool queued =
                    gLateJoinManager->QueueManagedRespawn(playerController);
                if (queued && !recordedRuntimeOverride)
                {
                    std::cout << "[LATEJOIN] Native inventory accepted; queued "
                        "managed respawn without a recorded bridge override."
                        << std::endl;
                }
            }
        }
        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    // Loadout/LateJoin: role confirmation can be deferred for at most one
    // second without blocking the game thread. A deferred confirmation is
    // replayed by LoadoutManager with copied parameters and a re-entry guard.
    if (functionName.contains("PBPlayerController.ServerConfirmRoleSelection"))
    {
        APBPlayerController* playerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;
        auto* confirmParms =
            static_cast<Params::PBPlayerController_ServerConfirmRoleSelection*>(Parms);

        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager && playerController && confirmParms &&
                IsCurrentConnectedController(playerController))
            {
                const LoadoutRoleConfirmDecision decision =
                    gLoadoutManager->BeginRoleConfirmation(
                        playerController, confirmParms->InRoleID);
                if (decision == LoadoutRoleConfirmDecision::Deferred)
                    return;
            }
        }

        QueuePendingPlayerNameUpdate(playerController);
        const bool isLateJoin = gLateJoinManager &&
            gLateJoinManager->IsLateJoinPlayer(playerController);
        const bool isDeferredInitialJoin = gLateJoinManager &&
            gLateJoinManager->IsInitialJoinPlayer(playerController);

        ProcessEvent.call(Object, Function, Parms);

        const bool connectionStillCurrent =
            IsCurrentConnectedController(playerController);
        bool roleWasAccepted = connectionStillCurrent;
        std::string committedRoleId;
        if (roleWasAccepted && (!playerController->PBPlayerState || !confirmParms))
            roleWasAccepted = false;
        if (roleWasAccepted && playerController->PBPlayerState && confirmParms)
        {
            auto* playerState = playerController->PBPlayerState;
            bool queriedSelectionState = false;
            bool hasSelectedRole = false;
            try
            {
                hasSelectedRole = playerState->HasSelectedRole();
                queriedSelectionState = true;
            }
            catch (...)
            {
                // Keep compatibility with SDK revisions where this helper is
                // unavailable; the concrete SelectedCharacterID check below
                // can still reject a mismatched selection.
            }

            if (!IsCurrentConnectedController(playerController) ||
                playerController->PBPlayerState != playerState)
            {
                roleWasAccepted = false;
            }

            const std::string acceptedRole = roleWasAccepted
                ? playerState->SelectedCharacterID.ToString()
                : std::string{};
            const std::string requestedRole = confirmParms->InRoleID.ToString();
            const bool acceptedRoleIsConcrete =
                !acceptedRole.empty() && acceptedRole != "None";
            const bool requestedRoleIsConcrete =
                !requestedRole.empty() && requestedRole != "None";

            // ServerConfirmRoleSelection is synchronous on the authoritative
            // player state. Advance neither spawn nor quorum unless the state
            // reports a selected role and it is exactly the requested one.
            if ((queriedSelectionState && !hasSelectedRole) ||
                !acceptedRoleIsConcrete || !requestedRoleIsConcrete ||
                acceptedRole != requestedRole)
            {
                roleWasAccepted = false;
            }
            else
            {
                committedRoleId = acceptedRole;
            }
        }
        if (!roleWasAccepted)
        {
            ClientLog("[LOADOUT] Role confirmation did not commit to the current connection");
            BattleLog::OnProcessEventPost(
                BattleLog::ProcessSide::Server, Object, functionName, Parms);
            return;
        }

        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            if (gLoadoutManager && playerController && confirmParms &&
                IsCurrentConnectedController(playerController))
            {
                gLoadoutManager->CommitRoleConfirmationAfterOriginal(
                    playerController, confirmParms->InRoleID);
            }
        }

        if (gLateJoinManager && (isLateJoin || isDeferredInitialJoin))
            gLateJoinManager->OnRoleConfirmed(
                playerController, committedRoleId);

        if (!IsCurrentConnectedController(playerController))
        {
            BattleLog::OnProcessEventPost(
                BattleLog::ProcessSide::Server, Object, functionName, Parms);
            return;
        }

        // A player joining an already-running match must not alter the
        // original match-start quorum. Initial joins still count normally.
        if (!isLateJoin && playerController)
        {
            const auto [_, inserted] = PlayersConfirmedRole.insert(playerController);
            if (inserted)
            {
                NumPlayersSelectedRole = static_cast<int>(PlayersConfirmedRole.size());
                std::cout << "[MATCH] Role confirmed ("
                    << NumPlayersSelectedRole << "/" << NumExpectedPlayers << ")"
                    << std::endl;
            }
            else
            {
                std::cout << "[MATCH] Ignoring duplicate role confirmation."
                    << std::endl;
            }

            RecomputeMatchStartGate(inserted ? "role confirmed" : "duplicate confirmation");
        }

        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    // Inventory actors now exist; detailed configs may safely be applied, but
    // only when their live identities match the effective inventory.
    if (functionName.contains("K2_InventorySpawned"))
    {
        APBCharacter* character =
            Object && Object->IsA(APBCharacter::StaticClass())
                ? static_cast<APBCharacter*>(Object)
                : nullptr;
        bool tombstonedBeforeOriginal = false;
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            tombstonedBeforeOriginal = gLoadoutManager && character &&
                gLoadoutManager->IsCharacterTombstoned(character);
        }
        ProcessEvent.call(Object, Function, Parms);
        {
            std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
            const bool destroyedDuringOriginal =
                gLoadoutManager && character && !tombstonedBeforeOriginal &&
                gLoadoutManager->IsCharacterTombstoned(character);
            if (gLoadoutManager && character && !destroyedDuringOriginal)
                gLoadoutManager->OnInventorySpawned(character);
        }
        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    if (functionName.contains("K2_OnLogout"))
    {
        auto* logoutParms = static_cast<Params::GameModeBase_K2_OnLogout*>(Parms);
        APBPlayerController* playerController =
            logoutParms && logoutParms->ExitingController &&
                    logoutParms->ExitingController->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(logoutParms->ExitingController)
                : nullptr;

        if (playerController)
            CleanupDisconnectedPlayer(playerController, "player disconnected");
        ProcessEvent.call(Object, Function, Parms);
        BattleLog::OnProcessEventPost(
            BattleLog::ProcessSide::Server, Object, functionName, Parms);
        return;
    }

    if (functionName.contains("ReadyToMatchIntro_WaitingToStart"))
    {
        ApplyPendingPlayerNameUpdates("ReadyToMatchIntro_WaitingToStart");
        if (!canStartMatch)
        {
            return;
        }
    }

    if (functionName.contains("PBPlayerController.ClientBeKilled"))
    {
        std::cout << "Intercepted Player Kill!" << std::endl;

        APBPlayerController* PBPlayerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;

        if (PBPlayerController)
            PlayerRespawnAllowedMap[PBPlayerController] = false;
        if (gLateJoinManager && PBPlayerController)
            gLateJoinManager->OnPlayerKilled(PBPlayerController);
        // The FieldMod deploy/preorder (or a new role confirmation) reopens
        // the managed respawn. Queuing here would immediately respawn the old
        // role before the player's post-death selection arrives.
    }

    if (functionName.contains("PlayerController.CanRestartPlayer"))
    {
        APBPlayerController* playerController =
            Object && Object->IsA(APBPlayerController::StaticClass())
                ? static_cast<APBPlayerController*>(Object)
                : nullptr;
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController) &&
            !gLateJoinManager->HasManagedRestartPermit(playerController))
        {
            auto* restartParms =
                static_cast<Params::PlayerController_CanRestartPlayer*>(Parms);
            if (restartParms) restartParms->ReturnValue = false;
            return;
        }
    }

    if (functionName.contains("GameModeBase.PlayerCanRestart"))
    {
        auto* restartParms = (Params::GameModeBase_PlayerCanRestart *)Parms;
        APBPlayerController* playerController = restartParms && restartParms->Player &&
                restartParms->Player->IsA(APBPlayerController::StaticClass())
            ? static_cast<APBPlayerController*>(restartParms->Player)
            : nullptr;
        if (playerController && gLateJoinManager &&
            gLateJoinManager->IsManagedPlayer(playerController))
        {
            if (gLateJoinManager->HasManagedRestartPermit(playerController))
            {
                restartParms->ReturnValue = true;
                return;
            }

            restartParms->ReturnValue = false;
            return;
        }

        restartParms->ReturnValue = ((AGameModeBase *)Object)->HasMatchStarted() ||
            (gLateJoinManager && gLateJoinManager->CanRestartBeforeMatch(playerController));
        return;
    }

    ProcessEvent.call(Object, Function, Parms);
    BattleLog::OnProcessEventPost(
        BattleLog::ProcessSide::Server,
        Object,
        functionName,
        Parms);
}

static SafetyHookInline PostLoginHook;

void *PostLogin(AGameMode *GameMode, APBPlayerController *PC)
{
    EnsureServerMatchWorld(UWorld::GetWorld());
    if (PC)
        DisconnectedPlayerControllers.erase(PC);
    void *Ret = PostLoginHook.call<void *>(GameMode, PC);

    if (!PC || DisconnectedPlayerControllers.contains(PC) ||
        PC->bActorIsBeingDestroyed)
    {
        std::cout << "[SERVER] Ignored PostLogin result for a disconnected controller."
            << std::endl;
        return Ret;
    }
    try
    {
        if (!PC->HasAuthority()) return Ret;
    }
    catch (...)
    {
        return Ret;
    }

    ConnectedPlayerControllers.insert(PC);
    NumPlayersJoined = static_cast<int>(ConnectedPlayerControllers.size());

    std::cout << "Player Connected!" << std::endl;

    {
        std::lock_guard<std::recursive_mutex> lock(gLoadoutManagerMutex);
        if (gLoadoutManager)
            gLoadoutManager->OnPlayerConnected(PC);
    }

    // LateJoin detection
    if (gLateJoinManager && gLateJoinManager->OnPostLogin(GameMode, PC))
    {
        // Handled as LateJoin player; skip normal first-life flow
        return Ret;
    }

    // This is a genuine pre-match participant. If the expected quorum was
    // already established, include the new player and re-close a previously
    // ready gate until this connection confirms a role.
    if (!DidProcStartMatch && NumExpectedPlayers > 0)
    {
        NumExpectedPlayers = NumPlayersJoined;
        RecomputeMatchStartGate("initial player connected");
    }

    // Initial joins use the same role-confirmation gate as mid-match joins so
    // authoritative inventory can be seeded before the first playable Pawn is
    // created.
    if (gLateJoinManager)
    {
        gLateJoinManager->QueueInitialJoinPlayer(GameMode, PC);
        return Ret;
    }

    // Preserve the native fallback when the deferred-spawn manager is not
    // available.
    if (PC && PC->Pawn)
    {
        PC->ServerSuicide(0);   // triggers respawn
    }

    return Ret;
}

static SafetyHookInline OnFireWeaponHook;

void *OnFireWeapon(APBWeapon *Weapon)
{
    if ((uintptr_t)_ReturnAddress() - BaseAddress != 0x1608B31)
    {
        return nullptr;
    }
    else
    {
        return OnFireWeaponHook.call<void *>(Weapon);
    }
}

// ======================================================
//  SECTION 9 — HOOK DETOURS (CLIENT HOOKS)
// ======================================================

static SafetyHookInline ProcessEventClient;
static SafetyHookInline FixEquipErrorHook;
static SafetyHookInline FixCharacterSkinPaintingErrorHook;
static SafetyHookInline FixCharacterAppearanceErrorHook;

static void LogArchiveCompletionTranslation(
    const char* completionKind,
    int completionCode,
    int normalizedCode,
    std::atomic<unsigned long long>& translationCount)
{
    const auto count = translationCount.fetch_add(1, std::memory_order_relaxed) + 1;
    ClientLog("[ARCHIVE] " + std::string(completionKind) +
        " completion translated " + std::to_string(completionCode) + "->" +
        std::to_string(normalizedCode) + " after persisted update; count=" +
        std::to_string(count));
}

void __fastcall FixEquipErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4, int a5)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizeEquipmentCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "equipment_archive", completionCode, normalized, translationCount);
    FixEquipErrorHook.call<void>(a1, normalized, a3, a4, a5);
}

void __fastcall FixCharacterSkinPaintingErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4, __int64 a5)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizePersistedCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "character_skin_painting", completionCode, normalized, translationCount);
    FixCharacterSkinPaintingErrorHook.call<void>(a1, normalized, a3, a4, a5);
}

void __fastcall FixCharacterAppearanceErrorHookFn(
    __int64 a1, int completionCode, __int64 a3, __int64 a4, int a5)
{
    static std::atomic<unsigned long long> translationCount{0};
    const int normalized =
        ArchiveCompletionPolicy::NormalizePersistedCompletion(completionCode);
    if (normalized != completionCode)
        LogArchiveCompletionTranslation(
            "character_appearance", completionCode, normalized, translationCount);
    FixCharacterAppearanceErrorHook.call<void>(a1, normalized, a3, a4, a5);
}

void ProcessEventHookClient(UObject *Object, UFunction *Function, void *Parms)
{
    if (gClientProcessEventSuppressionDepth > 0)
    {
        ProcessEventClient.call(Object, Function, Parms);
        return;
    }

    // 热键检测（游戏线程安全）— F6=dump, F7=reapply snapshot
    if (gDebugTool)
    {
        static auto nextHotkeyCheck = std::chrono::steady_clock::now();
        const auto now = std::chrono::steady_clock::now();
        if (now >= nextHotkeyCheck)
        {
            nextHotkeyCheck = now + std::chrono::milliseconds(500);
            try
            {
                if (GetAsyncKeyState(VK_F6) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F6);
                if (GetAsyncKeyState(VK_F7) & 0x8000)
                    gDebugTool->ExecuteHotkey(VK_F7);
            }
            catch (...)
            {
                // 吞掉所有异常 — 调试工具不应导致游戏崩溃
            }
        }
    }

    const std::string functionName = Function ? std::string(Function->GetFullName()) : "";

    // TEMP LOGIN DEBUG DUMP (GameInstance only)
    // if (Object && Object->IsA(UPBGameInstance::StaticClass()))
    //{
    //    std::string fn = Function->GetFullName();
    //        std::cout << "[LOGIN-DUMP] GI :: " << fn << std::endl;
    //}
    // Froce space to login
    if (functionName.contains("UMG_EnterGame_C.Construct"))
    {
        ClientLog("[LOGIN] EnterGame Construct forcing SPACE");

        std::thread([]()
                    {
                Sleep(1000); // small delay so widget is fully active
                PressSpace(); })
            .detach();
    }
    if (functionName.contains("UMG_EnterGame_C.BP_OnActivated"))
    {
        ClientLog("[LOGIN] EnterGame Activated forcing SPACE");

        std::thread([]()
                    {
                Sleep(1000);
                PressSpace(); })
            .detach();
    }
    // Detect login complete via MainMenuBase Construct
    if (functionName.contains("UMG_MainMenuBase_C.Construct"))
    {
        NotifyClientLoginCompleted();
    }
    if (functionName.contains("OnConnectMatchServerTimeOut"))
    {
        ClientLog("[PE] " + std::string(Object->GetFullName()) + " - " + functionName);

        ConnectToMatch();
    }

    // 先执行原始 ProcessEvent，确保游戏状态已更新
    ProcessEventClient.call(Object, Function, Parms);

    // Pipe and command-line match transitions are consumed only on this game thread.
    PumpPendingClientCommands();

    BattleLog::OnProcessEventPost(
        BattleLog::ProcessSide::Client,
        Object,
        functionName,
        Parms);
}

static SafetyHookInline ClientDeathCrash;

__int64 ClientDeathCrashHook(__int64 a1)
{
    return 0;
}

// ======================================================
//  SECTION 10 — HOOK DETOURS (MISC HOOKS)
// ======================================================

static SafetyHookInline ObjectNeedsLoad;

char ObjectNeedsLoadHook(UObject *a1)
{
    return 1;
}

static SafetyHookInline ActorNeedsLoad;

char ActorNeedsLoadHook(UObject *a1)
{
    return 1;
}

static SafetyHookInline MessageBoxWHook;

int WINAPI MessageBoxW_Detour(HWND hWnd, LPCWSTR lpText, LPCWSTR lpCaption, UINT uType)
{
    if (lpText && wcsstr(lpText, L"Roboto"))
    {
        return IDOK;
    }
    return MessageBoxWHook.call<int>(hWnd, lpText, lpCaption, uType);
}

static SafetyHookInline HudFunctionThatCrashesTheGame;

__int64 HudFunctionThatCrashesTheGameHook(__int64 a1, __int64 a2)
{
    return 0;
}

static SafetyHookInline GameEngineTick;

__int64 GameEngineTickHook(APlayerController *a1,
                           float a2,
                           __int64 a3,
                           __int64 a4)
{

    static bool flip = true;

    flip = !flip;

    if (flip)
    {
        std::cout << "NO TICKY" << std::endl;
        return 0;
    }

    return GameEngineTick.call<__int64>(a1, a2, a3, a4);
}

static SafetyHookInline IsDedicatedServerHook;

bool IsDedicatedServer(void *WorldContextOrSomething)
{
    return true;
}

static SafetyHookInline IsServerHook;

bool IsServer(void *WorldContextOrSomething)
{
    return true;
}

static SafetyHookInline IsStandaloneHook;

bool IsStandalone(void *WorldContextOrSomething)
{
    return false;
}

// ======================================================
//  SECTION 11 — HOOK INITIALIZATION
// ======================================================

extern uintptr_t BaseAddress;
extern LibReplicate *libReplicate;

void InitMessageBoxHook()
{
    HMODULE user32 = GetModuleHandleA("user32.dll");
    if (!user32)
        return;

    void *addr = GetProcAddress(user32, "MessageBoxW");
    if (!addr)
        return;

    MessageBoxWHook = safetyhook::create_inline(addr, MessageBoxW_Detour);
}

void InitServerHooks()
{
    NotifyActorDestroyed = safetyhook::create_inline((void *)(BaseAddress + 0x33403E0), NotifyActorDestroyedHook);
    NotifyAcceptingConnection = safetyhook::create_inline((void *)(BaseAddress + 0x36CDC90), NotifyAcceptingConnectionHook);
    NotifyControlMessage = safetyhook::create_inline((void *)(BaseAddress + 0x36CDCE0), NotifyControlMessageHook);
    TickFlush = safetyhook::create_inline((void *)(BaseAddress + 0x33E05F0), TickFlushHook);
    ProcessEvent = safetyhook::create_inline((void *)(BaseAddress + 0x1BCBE40), ProcessEventHook);
    ObjectNeedsLoad = safetyhook::create_inline((void *)(BaseAddress + 0x1B7B710), ObjectNeedsLoadHook);
    ActorNeedsLoad = safetyhook::create_inline((void *)(BaseAddress + 0x3124E70), ActorNeedsLoadHook);
    OnFireWeaponHook = safetyhook::create_inline((void *)(BaseAddress + 0x1610500), OnFireWeapon);
    PostLoginHook = safetyhook::create_inline((void *)(BaseAddress + 0x32903B0), PostLogin);
    IsDedicatedServerHook = safetyhook::create_inline((void *)(BaseAddress + 0x33266F0), IsDedicatedServer);
    IsServerHook = safetyhook::create_inline((void *)(BaseAddress + 0x3326C60), IsServer);
    IsStandaloneHook = safetyhook::create_inline((void *)(BaseAddress + 0x3326CE0), IsStandalone);
}

void InitClientHook()
{
    ProcessEventClient = safetyhook::create_inline((void *)(BaseAddress + 0x1BCBE40), ProcessEventHookClient);
    ClientDeathCrash = safetyhook::create_inline((void *)(BaseAddress + 0x16abe10), ClientDeathCrashHook);
    FixEquipErrorHook = safetyhook::create_inline((void *)(BaseAddress + 0x16DD080), FixEquipErrorHookFn);
    FixCharacterSkinPaintingErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DCEC0), FixCharacterSkinPaintingErrorHookFn);
    FixCharacterAppearanceErrorHook = safetyhook::create_inline(
        (void *)(BaseAddress + 0x16DCD80), FixCharacterAppearanceErrorHookFn);
    ClientLog("[ARCHIVE] Installed pinned-build completion compatibility hooks "
        "(generic 404->0; equipment 404/9002->0).");
}
