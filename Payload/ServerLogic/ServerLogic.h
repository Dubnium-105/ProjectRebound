#pragma once
#include <atomic>
#include <vector>
#include <cstdint>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include "../SDK.hpp"

class LateJoinManager;

// Global server state
extern std::atomic_bool listening;
extern std::vector<SDK::APlayerController *> playerControllersPossessed;
extern int NumPlayersJoined;
extern float PlayerJoinTimerSelectFuck;
extern bool DidProcFlow;
extern bool DidBroadcastRoleSelection;
extern float StartMatchTimer;
extern int NumPlayersSelectedRole;
extern bool DidProcStartMatch;
extern bool canStartMatch;
extern int NumExpectedPlayers;
extern float MatchStartCountdown;
extern float ReplicationFlushAccumulator;
extern std::unordered_map<SDK::APBPlayerController *, bool> PlayerRespawnAllowedMap;
extern std::unordered_set<SDK::APBPlayerController *> PlayersConfirmedRole;
extern std::unordered_set<SDK::APBPlayerController *> ConnectedPlayerControllers;
extern std::unordered_set<SDK::APBPlayerController *> DisconnectedPlayerControllers;
extern std::unordered_set<SDK::APBPlayerController *> PendingNameUpdatePlayers;
extern std::unordered_set<SDK::APBPlayerController *> AppliedNameUpdatePlayers;
extern float PendingNameApplyAccumulator;
extern LateJoinManager *gLateJoinManager;

// Game state helpers
SDK::APBGameState *GetPBGameState();
SDK::APBGameMode *GetPBGameMode();
bool IsRoundCurrentlyInProgress();
int GetCurrentPlayerCount();

// Player name update helpers
void QueuePendingPlayerNameUpdate(SDK::APBPlayerController *PlayerController);
void ApplyPendingPlayerNameUpdates(const char *reason);

// Match-scoped globals must be cleared when the injected process switches
// UWorlds. Seed this before Listen and observe it from game-thread hooks.
void ResetServerMatchStateForWorld(SDK::UWorld *world);
void EnsureServerMatchWorld(SDK::UWorld *world);
bool BeginServerMatchGeneration(
    SDK::UWorld* world,
    SDK::UNetDriver* netDriver);
std::uint64_t GetServerMatchGeneration();

// Preserves the pinned build's native return-to-menu and cleanup sequence.
// A null GameMode can only occur after a failed travel and falls back to the
// engine's RequestExit(false) entry.
void BeginGracefulDedicatedExit(
    SDK::APBGameMode* gameMode,
    const char* reason);

// Match lifecycle receipts are emitted only from the authoritative native
// result/return path.  The bridge functions below contain no pipe or disk I/O;
// they hand a scoped event to the lock-only native outbox.
void MatchLifecycleCaptureResultWorld(SDK::APBGameMode* gameMode);
void MatchLifecycleOnResultFrozen(SDK::APBGameMode* gameMode);
void MatchLifecycleOnResultConfirmed(SDK::APBGameMode* gameMode);
void MatchLifecycleArmReturnToMenu(SDK::APBGameMode* gameMode);
void MatchLifecycleReturnToMenuNotified(SDK::APBGameMode* gameMode);
void MatchLifecycleOnNetworkFlush(
    SDK::UWorld* world,
    SDK::UNetDriver* netDriver);
float MatchLifecycleFinalCleanupWait(
    SDK::APBGameMode* gameMode,
    float requestedSeconds);
void MatchLifecycleCancelProductionAllocationScope(
    const std::string& attemptId,
    const std::string& authoritySessionId,
    const std::string& worldInstanceId,
    std::int64_t rosterRevision,
    int routeGeneration);

// Server startup
// Online authority startup invokes these phases on successive engine ticks.
// No phase waits for a later tick or authorizes a player connection.
bool BeginServerMapTravel();
void RequestServerStreamingLevels(SDK::UWorld* world);
bool IsServerMapReady(SDK::UWorld* world, const SDK::UWorld* previousWorld);
bool AreServerStreamingLevelsReady(SDK::UWorld* world);
bool CompleteServerListen(SDK::UWorld* world);
void StartServer();
